// Package discoverycommand implements local operator discovery commands. It
// exposes no public HTTP mutation surface and receives an already-authorized
// storage repository from the executable adapter.
package discoverycommand

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	ExitSuccess     = 0
	ExitOperational = 1
	ExitUsage       = 2
	ExitCanceled    = 4

	MaximumImportBytes        = 256 * 1024
	MaximumCandidateLineBytes = 2048
	MaximumImportCandidates   = 100
	MaximumConcurrentProbes   = 8

	OutputHuman = OutputFormat("human")
	OutputJSON  = OutputFormat("json")

	outputSchemaV1 = "activity-relay-directory.discovery-admin.v1"
	outputSchemaV2 = "activity-relay-directory.discovery-admin.v2"
)

var (
	ErrInvalidCommand = errors.New("local discovery command is invalid")
	ErrImportFile     = errors.New("local discovery import file is invalid")
	ErrPreparation    = errors.New("local discovery preparation failed")
	ErrConfirmation   = errors.New("local discovery confirmation failed")
)

type Action string

const (
	ActionAdd    Action = "add"
	ActionRemove Action = "remove"
	ActionImport Action = "import"
)

func (action Action) valid() bool {
	return action == ActionAdd || action == ActionRemove || action == ActionImport
}

type OutputFormat string

func (format OutputFormat) valid() bool {
	return format == OutputHuman || format == OutputJSON
}

type Request struct {
	Action        Action
	Candidate     string
	RelayActor    string
	FilePath      string
	OperatorID    string
	ReasonCode    string
	SourceLabel   string
	AddDeadRelays bool
	InputFormat   InputFormat
	AssumeYes     bool
	Format        OutputFormat
}

// Prober is the safe network capability needed by discovery preparation.
type Prober interface {
	ProbeActor(context.Context, string) (actorresolver.ActorProbeResult, error)
	ProbeInbox(context.Context, string) (actorresolver.InboxProbeResult, error)
}

// Repository is the private local mutation capability used only after the
// prospective network work has completed and the operator has confirmed it.
type Repository interface {
	storage.DiscoveryRepository
	storage.ObservationRepository
}

type CandidateRepository interface {
	storage.DiscoveryCandidateRepository
}

// KnownStateRepository is the read-only state needed to distinguish a new
// imported relay from one already active through lifecycle or discovery state.
type KnownStateRepository interface {
	storage.DiscoveryRepository
	storage.ModerationReadRepository
}

// CSVProfilePreviewRepository exposes the retained CSV assertion set for
// read-only import preview.
type CSVProfilePreviewRepository interface {
	ProfileSourceProfile(context.Context, string, storage.ProfileSourceKind) (storage.RelayProfile, error)
}

type Candidate struct {
	Line int
	URL  string
	CSV  *CSVProfileRow
}

type PreparedRelay struct {
	Line            int
	RelayActor      string
	PublicBaseURL   string
	InboxURL        string
	InboxProbeState storage.InboxProbeState
}

type DuplicateCandidate struct {
	Line       int
	RelayActor string
}

type AlreadyKnownSource string

const (
	AlreadyKnownLifecycle          AlreadyKnownSource = "lifecycle"
	AlreadyKnownDiscovery          AlreadyKnownSource = "discovery"
	AlreadyKnownLifecycleDiscovery AlreadyKnownSource = "lifecycle+discovery"
)

type AlreadyKnownCandidate struct {
	Line       int
	RelayActor string
	Source     AlreadyKnownSource
}

type RetainedCandidate struct {
	Line              int
	CandidateActorURL string
	PublicBaseURL     string
	State             storage.DiscoveryCandidateState
	Failure           storage.DiscoveryCandidateFailure
}

type FailedCandidate struct {
	Line       int
	RelayActor string
	Code       string
}

type Plan struct {
	CandidateCount int
	Ready          []PreparedRelay
	Retained       []RetainedCandidate
	AlreadyKnown   []AlreadyKnownCandidate
	Duplicates     []DuplicateCandidate
	Failed         []FailedCandidate
	CSVRows        map[int]CSVProfileRow
	ProfileChanges map[string][]storage.ProfileField
}

type probeWork struct {
	index    int
	line     int
	actorURL string
}

type probeResult struct {
	index    int
	line     int
	actorURL string
	ready    PreparedRelay
	code     string
}

func Parse(arguments []string) (Request, error) {
	if len(arguments) == 0 {
		return Request{}, ErrInvalidCommand
	}
	action := Action(arguments[0])
	if !action.valid() {
		return Request{}, ErrInvalidCommand
	}
	request := Request{Action: action, Format: OutputHuman}
	flags := flag.NewFlagSet("discovery "+string(action), flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	candidate := uniqueString{}
	actor := uniqueString{}
	filePath := uniqueString{}
	operator := uniqueString{}
	reason := uniqueString{}
	sourceLabel := uniqueString{}
	format := uniqueString{value: string(OutputHuman)}
	inputFormat := uniqueString{value: string(InputLines)}
	assumeYes := uniqueTrue{}
	addDeadRelays := uniqueTrue{}

	switch action {
	case ActionAdd:
		flags.Var(&candidate, "url", "HTTPS relay candidate")
	case ActionRemove:
		flags.Var(&actor, "actor", "canonical relay actor")
	case ActionImport:
		flags.Var(&filePath, "file", "bounded local candidate file")
		flags.Var(&addDeadRelays, "add-dead-relays", "retain unreachable or incompatible relay candidates")
		flags.Var(&inputFormat, "input-format", "lines or csv")
	}
	flags.Var(&operator, "operator", "private operator identifier")
	flags.Var(&reason, "reason", "private reason code")
	flags.Var(&sourceLabel, "source-label", "private bounded source label")
	flags.Var(&assumeYes, "yes", "confirm noninteractively")
	flags.Var(&format, "format", "human or json")

	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 ||
		!operator.set || !reason.set || !storage.ValidOperatorID(operator.value) ||
		!storage.ValidModerationReasonCode(reason.value) ||
		!storage.ValidDiscoverySourceLabel(sourceLabel.value) {
		return Request{}, ErrInvalidCommand
	}
	request.OperatorID = operator.value
	request.ReasonCode = reason.value
	request.SourceLabel = sourceLabel.value
	request.AddDeadRelays = addDeadRelays.value
	request.AssumeYes = assumeYes.value
	request.Format = OutputFormat(format.value)
	if !request.Format.valid() {
		return Request{}, ErrInvalidCommand
	}

	switch action {
	case ActionAdd:
		if !candidate.set || candidate.value == "" || actor.set || filePath.set {
			return Request{}, ErrInvalidCommand
		}
		request.Candidate = candidate.value
	case ActionRemove:
		if !actor.set || candidate.set || filePath.set {
			return Request{}, ErrInvalidCommand
		}
		canonical, err := v1.NormalizeRelayActorURL(actor.value)
		if err != nil || canonical != actor.value {
			return Request{}, ErrInvalidCommand
		}
		request.RelayActor = actor.value
	case ActionImport:
		if !filePath.set || filePath.value == "" || candidate.set || actor.set ||
			!sourceLabel.set || sourceLabel.value == "" {
			return Request{}, ErrInvalidCommand
		}
		request.FilePath = filePath.value
		request.InputFormat = InputFormat(inputFormat.value)
		if !request.InputFormat.valid() {
			return Request{}, ErrInvalidCommand
		}
	}
	return request, nil
}

// LoadCandidates reads only a regular local UTF-8 file. The path is never
// persisted; only Request.SourceLabel crosses the storage boundary.
func LoadCandidates(path string) ([]Candidate, error) {
	if path == "" {
		return nil, ErrImportFile
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaximumImportBytes {
		return nil, ErrImportFile
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrImportFile
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) ||
		openedInfo.Size() < 0 || openedInfo.Size() > MaximumImportBytes {
		return nil, ErrImportFile
	}

	body, err := io.ReadAll(io.LimitReader(file, MaximumImportBytes+1))
	if err != nil || len(body) > MaximumImportBytes || !utf8.Valid(body) {
		return nil, ErrImportFile
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 1024), MaximumCandidateLineBytes+1)
	candidates := make([]Candidate, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if len(line) > MaximumCandidateLineBytes || !utf8.ValidString(line) {
			return nil, ErrImportFile
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(candidates) >= MaximumImportCandidates {
			return nil, ErrImportFile
		}
		candidates = append(candidates, Candidate{Line: lineNumber, URL: trimmed})
	}
	if err := scanner.Err(); err != nil || len(candidates) == 0 {
		return nil, ErrImportFile
	}
	return candidates, nil
}

func Prepare(ctx context.Context, request Request, prober Prober) (Plan, error) {
	if ctx == nil || prober == nil || !request.Action.valid() {
		return Plan{}, ErrPreparation
	}
	if request.Action == ActionRemove {
		return Plan{}, nil
	}
	var candidates []Candidate
	if request.Action == ActionAdd {
		candidates = []Candidate{{Line: 1, URL: request.Candidate}}
	} else {
		var loaded []Candidate
		var err error
		switch normalizeInputFormat(request.InputFormat) {
		case InputLines:
			loaded, err = LoadCandidates(request.FilePath)
		case InputCSV:
			loaded, err = LoadCSVCandidates(request.FilePath, request.SourceLabel)
		default:
			err = ErrImportFile
		}
		if err != nil {
			return Plan{}, err
		}
		candidates = loaded
	}
	return prepareCandidatesWithPolicy(
		ctx,
		candidates,
		prober,
		request.Action == ActionImport && request.AddDeadRelays,
	)
}

func prepareCandidates(ctx context.Context, candidates []Candidate, prober Prober) (Plan, error) {
	return prepareCandidatesWithPolicy(ctx, candidates, prober, false)
}

func prepareCandidatesWithPolicy(
	ctx context.Context,
	candidates []Candidate,
	prober Prober,
	retainDead bool,
) (Plan, error) {
	plan := Plan{CandidateCount: len(candidates)}
	for _, candidate := range candidates {
		if candidate.CSV == nil {
			continue
		}
		if plan.CSVRows == nil {
			plan.CSVRows = make(map[int]CSVProfileRow)
		}
		plan.CSVRows[candidate.Line] = *candidate.CSV
	}
	if len(candidates) == 0 || len(candidates) > MaximumImportCandidates {
		return Plan{}, ErrPreparation
	}

	works := make([]probeWork, 0, len(candidates))
	results := make([]probeResult, len(candidates))
	for index, candidate := range candidates {
		actorURL, err := candidateActorURL(candidate.URL)
		if err != nil {
			results[index] = probeResult{index: index, line: candidate.Line, code: "invalid_candidate"}
			continue
		}
		works = append(works, probeWork{index: index, line: candidate.Line, actorURL: actorURL})
	}

	workers := MaximumConcurrentProbes
	if workers > len(works) {
		workers = len(works)
	}
	if workers > 0 {
		jobs := make(chan probeWork)
		completed := make(chan probeResult, len(works))
		var group sync.WaitGroup
		for worker := 0; worker < workers; worker++ {
			group.Add(1)
			go func() {
				defer group.Done()
				for item := range jobs {
					completed <- probeCandidate(ctx, item, prober)
				}
			}()
		}
		go func() {
			defer close(jobs)
			for _, item := range works {
				select {
				case jobs <- item:
				case <-ctx.Done():
					return
				}
			}
		}()
		go func() {
			group.Wait()
			close(completed)
		}()
		for completedResult := range completed {
			results[completedResult.index] = completedResult
		}
		if err := ctx.Err(); err != nil {
			return Plan{}, errors.Join(ErrPreparation, err)
		}
	}

	seen := make(map[string]struct{})
	for _, prepared := range results {
		if prepared.code != "" {
			if retainDead && prepared.actorURL != "" &&
				(prepared.code == string(storage.DiscoveryCandidateActorUnreachable) ||
					prepared.code == string(storage.DiscoveryCandidateActorInvalid)) {
				if _, duplicate := seen[prepared.actorURL]; duplicate {
					plan.Duplicates = append(plan.Duplicates, DuplicateCandidate{
						Line: prepared.line, RelayActor: prepared.actorURL,
					})
					continue
				}
				base, err := publicBaseURL(prepared.actorURL)
				if err != nil {
					plan.Failed = append(plan.Failed, FailedCandidate{
						Line: prepared.line, Code: "invalid_candidate",
					})
					continue
				}
				state := storage.DiscoveryCandidateUnreachable
				failure := storage.DiscoveryCandidateActorUnreachable
				if prepared.code == string(storage.DiscoveryCandidateActorInvalid) {
					state = storage.DiscoveryCandidateIncompatible
					failure = storage.DiscoveryCandidateActorInvalid
				}
				seen[prepared.actorURL] = struct{}{}
				plan.Retained = append(plan.Retained, RetainedCandidate{
					Line: prepared.line, CandidateActorURL: prepared.actorURL,
					PublicBaseURL: base, State: state, Failure: failure,
				})
				continue
			}
			plan.Failed = append(plan.Failed, FailedCandidate{
				Line: prepared.line, RelayActor: prepared.actorURL, Code: prepared.code,
			})
			continue
		}
		if prepared.ready.RelayActor == "" {
			return Plan{}, ErrPreparation
		}
		if _, duplicate := seen[prepared.ready.RelayActor]; duplicate {
			plan.Duplicates = append(plan.Duplicates, DuplicateCandidate{
				Line: prepared.line, RelayActor: prepared.ready.RelayActor,
			})
			continue
		}
		seen[prepared.ready.RelayActor] = struct{}{}
		plan.Ready = append(plan.Ready, prepared.ready)
	}
	return plan, nil
}

// ClassifyKnown removes already-active identities from an import's mutation
// set. A removed discovery or inactive lifecycle history remains eligible for a
// fresh operator discovery; only currently active state is classified as known.
func ClassifyKnown(
	ctx context.Context,
	request Request,
	plan Plan,
	repository KnownStateRepository,
) (Plan, error) {
	if request.Action != ActionImport {
		return plan, nil
	}
	if ctx == nil || repository == nil {
		return Plan{}, ErrPreparation
	}

	ready := make([]PreparedRelay, 0, len(plan.Ready))
	for _, candidate := range plan.Ready {
		identity := storage.IdentityIntent{RelayActor: candidate.RelayActor}
		discovery, found, err := repository.GetDiscovery(ctx, identity)
		if err != nil {
			return Plan{}, errors.Join(ErrPreparation, err)
		}
		discoveryActive := found && discovery.State == storage.DiscoveryActive

		lifecycleActive := false
		lifecycle, err := repository.ModerationState(ctx, candidate.RelayActor)
		switch {
		case err == nil:
			lifecycleActive = lifecycle.LifecycleState == storage.LifecycleRegistered
		case errors.Is(err, storage.ErrRelayAbsent):
		default:
			return Plan{}, errors.Join(ErrPreparation, err)
		}

		if !discoveryActive && !lifecycleActive {
			ready = append(ready, candidate)
			continue
		}

		source := AlreadyKnownDiscovery
		switch {
		case lifecycleActive && discoveryActive:
			source = AlreadyKnownLifecycleDiscovery
		case lifecycleActive:
			source = AlreadyKnownLifecycle
		}
		plan.AlreadyKnown = append(plan.AlreadyKnown, AlreadyKnownCandidate{
			Line:       candidate.Line,
			RelayActor: candidate.RelayActor,
			Source:     source,
		})
	}

	retained := make([]RetainedCandidate, 0, len(plan.Retained))
	for _, candidate := range plan.Retained {
		identity := storage.IdentityIntent{RelayActor: candidate.CandidateActorURL}
		discovery, found, err := repository.GetDiscovery(ctx, identity)
		if err != nil {
			return Plan{}, errors.Join(ErrPreparation, err)
		}
		discoveryActive := found && discovery.State == storage.DiscoveryActive

		lifecycleActive := false
		lifecycle, err := repository.ModerationState(ctx, candidate.CandidateActorURL)
		switch {
		case err == nil:
			lifecycleActive = lifecycle.LifecycleState == storage.LifecycleRegistered
		case errors.Is(err, storage.ErrRelayAbsent):
		default:
			return Plan{}, errors.Join(ErrPreparation, err)
		}

		if !discoveryActive && !lifecycleActive {
			retained = append(retained, candidate)
			continue
		}

		source := AlreadyKnownDiscovery
		switch {
		case lifecycleActive && discoveryActive:
			source = AlreadyKnownLifecycleDiscovery
		case lifecycleActive:
			source = AlreadyKnownLifecycle
		}
		plan.AlreadyKnown = append(plan.AlreadyKnown, AlreadyKnownCandidate{
			Line:       candidate.Line,
			RelayActor: candidate.CandidateActorURL,
			Source:     source,
		})
	}
	failed := plan.Failed
	if normalizeInputFormat(request.InputFormat) == InputCSV {
		failed = make([]FailedCandidate, 0, len(plan.Failed))
		for _, candidate := range plan.Failed {
			if candidate.RelayActor == "" {
				failed = append(failed, candidate)
				continue
			}

			identity := storage.IdentityIntent{RelayActor: candidate.RelayActor}
			discovery, found, err := repository.GetDiscovery(ctx, identity)
			if err != nil {
				return Plan{}, errors.Join(ErrPreparation, err)
			}
			discoveryActive := found && discovery.State == storage.DiscoveryActive

			lifecycleActive := false
			lifecycle, err := repository.ModerationState(ctx, candidate.RelayActor)
			switch {
			case err == nil:
				lifecycleActive = lifecycle.LifecycleState == storage.LifecycleRegistered
			case errors.Is(err, storage.ErrRelayAbsent):
			default:
				return Plan{}, errors.Join(ErrPreparation, err)
			}

			if !discoveryActive && !lifecycleActive {
				failed = append(failed, candidate)
				continue
			}

			source := AlreadyKnownDiscovery
			switch {
			case lifecycleActive && discoveryActive:
				source = AlreadyKnownLifecycleDiscovery
			case lifecycleActive:
				source = AlreadyKnownLifecycle
			}
			plan.AlreadyKnown = append(plan.AlreadyKnown, AlreadyKnownCandidate{
				Line:       candidate.Line,
				RelayActor: candidate.RelayActor,
				Source:     source,
			})
		}
	}

	plan.Ready = ready
	plan.Retained = retained
	plan.Failed = failed
	return plan, nil
}

func PreviewCSVProfileChanges(
	ctx context.Context,
	request Request,
	plan Plan,
	repository CSVProfilePreviewRepository,
) (Plan, error) {
	if normalizeInputFormat(request.InputFormat) != InputCSV || request.Action != ActionImport {
		return plan, nil
	}
	if ctx == nil || repository == nil {
		return Plan{}, ErrPreparation
	}
	if plan.ProfileChanges == nil {
		plan.ProfileChanges = make(map[string][]storage.ProfileField)
	}
	actors := make(map[int]string, len(plan.Ready)+len(plan.AlreadyKnown))
	for _, candidate := range plan.Ready {
		actors[candidate.Line] = candidate.RelayActor
	}
	for _, candidate := range plan.AlreadyKnown {
		actors[candidate.Line] = candidate.RelayActor
	}
	for line, actor := range actors {
		row, ok := plan.CSVRows[line]
		if !ok {
			return Plan{}, ErrPreparation
		}
		current, err := repository.ProfileSourceProfile(ctx, actor, storage.ProfileSourceCSV)
		if err != nil {
			return Plan{}, errors.Join(ErrPreparation, err)
		}
		fields := changedProfileFields(current, row.Profile)
		if len(fields) > 0 {
			plan.ProfileChanges[actor] = fields
		}
	}
	return plan, nil
}

func changedProfileFields(before, after storage.RelayProfile) []storage.ProfileField {
	changed := make([]storage.ProfileField, 0, len(storage.ProfileFields()))
	add := func(field storage.ProfileField, differs bool) {
		if differs {
			changed = append(changed, field)
		}
	}
	add(storage.ProfileFieldParticipationMode, before.ParticipationMode != after.ParticipationMode)
	add(storage.ProfileFieldAvailability, before.Availability != after.Availability)
	add(storage.ProfileFieldRelayType, before.RelayType != after.RelayType)
	add(storage.ProfileFieldLanguages, !slices.Equal(before.Languages, after.Languages))
	add(storage.ProfileFieldCountries, !slices.Equal(before.Countries, after.Countries))
	add(storage.ProfileFieldRegions, !slices.Equal(before.Regions, after.Regions))
	add(storage.ProfileFieldTopics, !slices.Equal(before.Topics, after.Topics))
	add(storage.ProfileFieldContactFediverse, before.ContactFediverse != after.ContactFediverse)
	add(storage.ProfileFieldContactEmail, before.ContactEmail != after.ContactEmail)
	add(storage.ProfileFieldContactURL, before.ContactURL != after.ContactURL)
	add(storage.ProfileFieldParticipationURL, before.ParticipationURL != after.ParticipationURL)
	add(storage.ProfileFieldNotes, before.Notes != after.Notes)
	return changed
}

func profileChangeCount(plan Plan) int {
	count := 0
	for _, fields := range plan.ProfileChanges {
		count += len(fields)
	}
	return count
}

func probeCandidate(ctx context.Context, item probeWork, prober Prober) probeResult {
	actor, err := prober.ProbeActor(ctx, item.actorURL)
	if err != nil {
		code := "actor_unreachable"
		switch {
		case errors.Is(err, actorresolver.ErrNetworkTarget):
			code = "network_target_prohibited"
		case errors.Is(err, actorresolver.ErrActorDocument), errors.Is(err, actorresolver.ErrPublicKey):
			code = "actor_invalid"
		}
		return probeResult{
			index: item.index, line: item.line, actorURL: item.actorURL, code: code,
		}
	}
	if actor.ActorID != item.actorURL {
		return probeResult{
			index: item.index, line: item.line, actorURL: item.actorURL, code: "actor_invalid",
		}
	}
	base, err := publicBaseURL(actor.ActorID)
	if err != nil {
		return probeResult{
			index: item.index, line: item.line, actorURL: item.actorURL, code: "actor_invalid",
		}
	}
	prepared := PreparedRelay{
		Line:            item.line,
		RelayActor:      actor.ActorID,
		PublicBaseURL:   base,
		InboxURL:        actor.InboxURL,
		InboxProbeState: storage.InboxNotChecked,
	}
	if actor.InboxURL != "" {
		inboxResult, err := prober.ProbeInbox(ctx, actor.InboxURL)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return probeResult{index: item.index, line: item.line, code: "canceled"}
			}
			prepared.InboxProbeState = storage.InboxUnreachable
		} else {
			switch inboxResult {
			case actorresolver.InboxProbeResponsive:
				prepared.InboxProbeState = storage.InboxResponsive
			case actorresolver.InboxProbeMethodRejected:
				prepared.InboxProbeState = storage.InboxMethodRejected
			case actorresolver.InboxProbeUnreachable:
				prepared.InboxProbeState = storage.InboxUnreachable
			default:
				return probeResult{index: item.index, line: item.line, code: "inbox_diagnostic_invalid"}
			}
		}
	}
	return probeResult{index: item.index, line: item.line, ready: prepared}
}

func candidateActorURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if _, err := url.Parse(raw); err != nil {
		return "", ErrInvalidCommand
	}

	canonical, err := v1.NormalizeRelayActorURL(raw)
	if err != nil {
		return "", ErrInvalidCommand
	}
	parsed, err := url.Parse(canonical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	switch parsed.EscapedPath() {
	case "/actor":
		return canonical, nil
	case "/", "/inbox":
		base, err := publicBaseURL(canonical)
		if err != nil {
			return "", ErrInvalidCommand
		}
		return base + "/actor", nil
	default:
		return "", ErrInvalidCommand
	}
}

func publicBaseURL(actor string) (string, error) {
	parsed, err := url.Parse(actor)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrInvalidCommand
	}
	return v1.NormalizePublicBaseURL(parsed.Scheme + "://" + parsed.Host)
}

// RenderPlan writes the prospective, non-secret summary before confirmation.
// Failed inputs are identified only by line number and a closed reason code.
func RenderPlan(output io.Writer, request Request, plan Plan) error {
	if output == nil || request.Action == ActionRemove {
		return nil
	}
	if _, err := fmt.Fprintf(output,
		"discovery prospective: candidates=%d ready=%d retained=%d already_known=%d duplicate_input=%d failed=%d\n",
		plan.CandidateCount, len(plan.Ready), len(plan.Retained), len(plan.AlreadyKnown),
		len(plan.Duplicates), len(plan.Failed)); err != nil {
		return err
	}
	for _, ready := range plan.Ready {
		if _, err := fmt.Fprintf(output,
			"ready line=%d actor=%s inbox=%s inbox_probe=%s\n",
			ready.Line, ready.RelayActor, printableOptional(ready.InboxURL), ready.InboxProbeState); err != nil {
			return err
		}
	}
	for _, retained := range plan.Retained {
		if _, err := fmt.Fprintf(output,
			"retained line=%d candidate=%s code=%s\n",
			retained.Line, retained.CandidateActorURL, retained.Failure); err != nil {
			return err
		}
	}
	for _, known := range plan.AlreadyKnown {
		if _, err := fmt.Fprintf(output,
			"already_known line=%d actor=%s source=%s\n",
			known.Line, known.RelayActor, known.Source); err != nil {
			return err
		}
	}
	for _, duplicate := range plan.Duplicates {
		if _, err := fmt.Fprintf(output, "duplicate_input line=%d actor=%s\n", duplicate.Line, duplicate.RelayActor); err != nil {
			return err
		}
	}
	for _, failed := range plan.Failed {
		if _, err := fmt.Fprintf(output, "failed line=%d code=%s\n", failed.Line, failed.Code); err != nil {
			return err
		}
	}
	if normalizeInputFormat(request.InputFormat) == InputCSV {
		actors := make([]string, 0, len(plan.ProfileChanges))
		for actor := range plan.ProfileChanges {
			actors = append(actors, actor)
		}
		slices.Sort(actors)
		if _, err := fmt.Fprintf(output, "profile_changes=%d affected_actors=%d\n", profileChangeCount(plan), len(actors)); err != nil {
			return err
		}
		for _, actor := range actors {
			fields := plan.ProfileChanges[actor]
			names := make([]string, len(fields))
			for i, field := range fields {
				names[i] = string(field)
			}
			if _, err := fmt.Fprintf(output, "profile_change actor=%s fields=%s\n", actor, strings.Join(names, ",")); err != nil {
				return err
			}
		}
	}
	return nil
}

func Confirm(request Request, plan Plan, input io.Reader, errorOutput io.Writer) error {
	if request.AssumeYes {
		return nil
	}
	if request.Action == ActionImport && len(plan.Ready) == 0 &&
		len(plan.Retained) == 0 && len(plan.AlreadyKnown) > 0 &&
		normalizeInputFormat(request.InputFormat) != InputCSV {
		return nil
	}
	if input == nil || errorOutput == nil {
		return ErrConfirmation
	}
	var expected, prompt string
	switch request.Action {
	case ActionAdd:
		if len(plan.Ready) != 1 {
			return ErrConfirmation
		}
		expected = plan.Ready[0].RelayActor
		prompt = "confirmation required: type " + expected + " to add discovery: "
	case ActionRemove:
		expected = request.RelayActor
		prompt = "confirmation required: type " + expected + " to remove discovery: "
	case ActionImport:
		mutationCount := len(plan.Ready) + len(plan.Retained)
		if normalizeInputFormat(request.InputFormat) == InputCSV {
			for _, known := range plan.AlreadyKnown {
				if len(plan.ProfileChanges[known.RelayActor]) > 0 {
					mutationCount++
				}
			}
		}
		if mutationCount == 0 {
			return nil
		}
		expected = "IMPORT " + strconv.Itoa(mutationCount)
		if normalizeInputFormat(request.InputFormat) == InputCSV {
			prompt = "confirmation required: type " + expected + " to apply relay discoveries and profile changes: "
		} else {
			prompt = "confirmation required: type " + expected + " to apply relay discoveries: "
		}
	default:
		return ErrConfirmation
	}
	if _, err := fmt.Fprint(errorOutput, prompt); err != nil {
		return ErrConfirmation
	}
	reader := bufio.NewReader(io.LimitReader(input, 4098))
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrConfirmation
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line != expected {
		return ErrConfirmation
	}
	return nil
}

func Execute(
	ctx context.Context,
	request Request,
	plan Plan,
	repository Repository,
	standardOutput, errorOutput io.Writer,
	now func() time.Time,
) int {
	if ctx == nil || repository == nil || standardOutput == nil || errorOutput == nil ||
		now == nil || !request.Action.valid() || !request.Format.valid() {
		return writeFailure(errorOutput, ExitOperational, "discovery command unavailable")
	}
	if request.Action == ActionRemove {
		return executeRemove(ctx, request, repository, standardOutput, errorOutput, now)
	}
	return executeAddPlan(ctx, request, plan, repository, standardOutput, errorOutput, now)
}

func executeRemove(
	ctx context.Context,
	request Request,
	repository Repository,
	standardOutput, errorOutput io.Writer,
	now func() time.Time,
) int {
	outcome, err := repository.RemoveDiscovery(ctx, storage.DiscoveryRemoveIntent{
		RelayActor:  request.RelayActor,
		OperatorID:  request.OperatorID,
		ReasonCode:  request.ReasonCode,
		SourceKind:  storage.DiscoverySourceManual,
		SourceLabel: request.SourceLabel,
	}, now())
	if err != nil {
		return classifyStorageError(errorOutput, err, "discovery removal")
	}
	result := mutationResult{RelayActor: request.RelayActor, Status: "ok", Outcome: outcome}
	return renderResults(request, []mutationResult{result}, standardOutput, errorOutput, false)
}

func executeAddPlan(
	ctx context.Context,
	request Request,
	plan Plan,
	repository Repository,
	standardOutput, errorOutput io.Writer,
	now func() time.Time,
) int {
	if plan.CandidateCount <= 0 ||
		len(plan.Ready)+len(plan.Retained)+len(plan.AlreadyKnown)+
			len(plan.Duplicates)+len(plan.Failed) == 0 {
		return writeFailure(errorOutput, ExitOperational, "discovery plan is empty")
	}
	profileRepository, err := csvProfileRepository(request, plan, repository)
	if err != nil {
		return writeFailure(errorOutput, ExitOperational, "CSV profile persistence unavailable")
	}
	sourceKind := storage.DiscoverySourceManual
	if request.Action == ActionImport {
		sourceKind = storage.DiscoverySourceFile
	}
	results := make([]mutationResult, 0,
		len(plan.Ready)+len(plan.Retained)+len(plan.AlreadyKnown)+
			len(plan.Duplicates)+len(plan.Failed))
	hadFailure := len(plan.Failed) > 0
	for _, failed := range plan.Failed {
		results = append(results, mutationResult{Line: failed.Line, Status: "failed", Code: failed.Code})
	}
	if len(plan.Retained) > 0 {
		candidateRepository, ok := repository.(CandidateRepository)
		if !ok {
			return writeFailure(errorOutput, ExitOperational, "discovery candidate retention unavailable")
		}
		for _, retained := range plan.Retained {
			acceptedAt := now()
			outcome, err := candidateRepository.RetainDiscoveryCandidate(
				ctx,
				storage.DiscoveryCandidateIntent{
					CandidateActorURL: retained.CandidateActorURL,
					PublicBaseURL:     retained.PublicBaseURL,
					State:             retained.State,
					Failure:           retained.Failure,
					OperatorID:        request.OperatorID,
					ReasonCode:        request.ReasonCode,
					SourceKind:        sourceKind,
					SourceLabel:       request.SourceLabel,
				},
				acceptedAt,
			)
			if err != nil {
				hadFailure = true
				results = append(results, mutationResult{
					Line: retained.Line, CandidateActorURL: retained.CandidateActorURL,
					Status: "failed", Code: "candidate_write_failed",
				})
				continue
			}
			results = append(results, mutationResult{
				Line: retained.Line, CandidateActorURL: retained.CandidateActorURL,
				Status: "retained", CandidateOutcome: outcome, Code: string(retained.Failure),
			})
		}
	}
	for _, known := range plan.AlreadyKnown {
		result := mutationResult{
			Line: known.Line, RelayActor: known.RelayActor, Status: "already_known",
		}
		if profileRepository != nil {
			profile, err := applyCSVProfile(
				ctx, request, plan, known.Line, known.RelayActor,
				profileRepository, now(),
			)
			if err != nil {
				hadFailure = true
				result.Status = "failed"
				result.Code = "profile_write_failed"
				results = append(results, result)
				continue
			}
			result.Profile = profile
		}
		results = append(results, result)
	}
	for _, duplicate := range plan.Duplicates {
		results = append(results, mutationResult{
			Line: duplicate.Line, RelayActor: duplicate.RelayActor, Status: "duplicate_input",
		})
	}
	for _, ready := range plan.Ready {
		acceptedAt := now()
		outcome, err := repository.AddDiscovery(ctx, storage.DiscoveryAddIntent{
			RelayActor: ready.RelayActor, PublicBaseURL: ready.PublicBaseURL,
			OperatorID: request.OperatorID, ReasonCode: request.ReasonCode,
			SourceKind: sourceKind, SourceLabel: request.SourceLabel,
		}, acceptedAt)
		if err != nil {
			hadFailure = true
			results = append(results, mutationResult{
				Line: ready.Line, RelayActor: ready.RelayActor, Status: "failed", Code: "discovery_write_failed",
			})
			continue
		}
		if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
			RelayActor: ready.RelayActor, State: storage.ReachabilityReachable, InboxURL: ready.InboxURL,
		}, acceptedAt); err != nil {
			hadFailure = true
			results = append(results, mutationResult{
				Line: ready.Line, RelayActor: ready.RelayActor, Status: "failed",
				Outcome: outcome, Code: "actor_observation_write_failed",
			})
			continue
		}
		if ready.InboxURL != "" && ready.InboxProbeState != storage.InboxNotChecked {
			if err := repository.RecordInboxObservation(ctx, storage.InboxObservationIntent{
				RelayActor: ready.RelayActor, InboxURL: ready.InboxURL, State: ready.InboxProbeState,
			}, acceptedAt); err != nil {
				hadFailure = true
				results = append(results, mutationResult{
					Line: ready.Line, RelayActor: ready.RelayActor, Status: "failed",
					Outcome: outcome, Code: "inbox_observation_write_failed",
				})
				continue
			}
		}
		profile, err := applyCSVProfile(
			ctx, request, plan, ready.Line, ready.RelayActor,
			profileRepository, acceptedAt,
		)
		if err != nil {
			hadFailure = true
			results = append(results, mutationResult{
				Line: ready.Line, RelayActor: ready.RelayActor, Status: "failed",
				Outcome: outcome, Code: "profile_write_failed",
			})
			continue
		}
		results = append(results, mutationResult{
			Line: ready.Line, RelayActor: ready.RelayActor, Status: "ok", Outcome: outcome,
			InboxURL: ready.InboxURL, InboxProbeState: ready.InboxProbeState, Profile: profile,
		})
	}
	return renderResults(request, results, standardOutput, errorOutput, hadFailure)
}

type profileMutationResult struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Cleared   int `json:"cleared"`
	Unchanged int `json:"unchanged"`
}

type mutationResult struct {
	Line              int                               `json:"line,omitempty"`
	RelayActor        string                            `json:"relay_actor,omitempty"`
	CandidateActorURL string                            `json:"candidate_actor_url,omitempty"`
	Status            string                            `json:"status"`
	Outcome           storage.DiscoveryOutcome          `json:"outcome,omitempty"`
	CandidateOutcome  storage.DiscoveryCandidateOutcome `json:"candidate_outcome,omitempty"`
	Code              string                            `json:"code,omitempty"`
	InboxURL          string                            `json:"inbox_url,omitempty"`
	InboxProbeState   storage.InboxProbeState           `json:"inbox_probe,omitempty"`
	Profile           *profileMutationResult            `json:"profile,omitempty"`
}

type resultDocument struct {
	Schema  string           `json:"schema"`
	Kind    string           `json:"kind"`
	Action  Action           `json:"action"`
	Results []mutationResult `json:"results"`
}

func renderResults(
	request Request,
	results []mutationResult,
	standardOutput, errorOutput io.Writer,
	hadFailure bool,
) int {
	if request.Format == OutputJSON {
		schema := outputSchemaV1
		if request.Action == ActionImport && normalizeInputFormat(request.InputFormat) == InputCSV {
			schema = outputSchemaV2
		}
		encoder := json.NewEncoder(standardOutput)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(resultDocument{
			Schema: schema, Kind: "discovery_result", Action: request.Action, Results: results,
		}); err != nil {
			return writeFailure(errorOutput, ExitOperational, "discovery output failed")
		}
	} else {
		for _, result := range results {
			if result.Status == "failed" {
				if result.CandidateActorURL != "" {
					if _, err := fmt.Fprintf(standardOutput,
						"line=%d status=failed code=%s candidate=%s\n",
						result.Line, result.Code, result.CandidateActorURL); err != nil {
						return writeFailure(errorOutput, ExitOperational, "discovery output failed")
					}
					continue
				}
				if _, err := fmt.Fprintf(standardOutput, "line=%d status=failed code=%s actor=%s\n",
					result.Line, result.Code, printableOptional(result.RelayActor)); err != nil {
					return writeFailure(errorOutput, ExitOperational, "discovery output failed")
				}
				continue
			}
			if result.Status == "retained" {
				if _, err := fmt.Fprintf(standardOutput,
					"line=%d status=retained outcome=%s candidate=%s code=%s\n",
					result.Line, result.CandidateOutcome, result.CandidateActorURL, result.Code); err != nil {
					return writeFailure(errorOutput, ExitOperational, "discovery output failed")
				}
				continue
			}
			if result.Status == "already_known" {
				if result.Profile != nil {
					if _, err := fmt.Fprintf(standardOutput,
						"line=%d status=already_known actor=%s profile_changes=%s\n",
						result.Line, printableOptional(result.RelayActor), printableProfileChanges(result.Profile)); err != nil {
						return writeFailure(errorOutput, ExitOperational, "discovery output failed")
					}
					continue
				}
				if _, err := fmt.Fprintf(standardOutput,
					"line=%d status=already_known actor=%s\n",
					result.Line, printableOptional(result.RelayActor)); err != nil {
					return writeFailure(errorOutput, ExitOperational, "discovery output failed")
				}
				continue
			}
			if result.Status == "duplicate_input" {
				if _, err := fmt.Fprintf(standardOutput,
					"line=%d status=duplicate_input actor=%s\n",
					result.Line, printableOptional(result.RelayActor)); err != nil {
					return writeFailure(errorOutput, ExitOperational, "discovery output failed")
				}
				continue
			}
			if result.Profile != nil {
				if _, err := fmt.Fprintf(standardOutput,
					"line=%d status=%s outcome=%s actor=%s inbox=%s inbox_probe=%s profile_changes=%s\n",
					result.Line, result.Status, result.Outcome, printableOptional(result.RelayActor),
					printableOptional(result.InboxURL), printableInboxState(result.InboxProbeState),
					printableProfileChanges(result.Profile)); err != nil {
					return writeFailure(errorOutput, ExitOperational, "discovery output failed")
				}
				continue
			}
			if _, err := fmt.Fprintf(standardOutput,
				"line=%d status=%s outcome=%s actor=%s inbox=%s inbox_probe=%s\n",
				result.Line, result.Status, result.Outcome, printableOptional(result.RelayActor),
				printableOptional(result.InboxURL), printableInboxState(result.InboxProbeState)); err != nil {
				return writeFailure(errorOutput, ExitOperational, "discovery output failed")
			}
		}
	}
	if hadFailure {
		return ExitOperational
	}
	return ExitSuccess
}

func printableProfileChanges(profile *profileMutationResult) string {
	if profile == nil {
		return "none"
	}
	count := profile.Created + profile.Updated + profile.Cleared
	if count == 0 {
		return "none"
	}
	return strconv.Itoa(count)
}

func classifyStorageError(output io.Writer, err error, operation string) int {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return writeFailure(output, ExitCanceled, operation+" canceled")
	case errors.Is(err, storage.ErrTransitionInput), errors.Is(err, storage.ErrTransitionTime),
		errors.Is(err, storage.ErrObservationInput), errors.Is(err, storage.ErrObservationTime),
		errors.Is(err, storage.ErrObservationConflict),
		errors.Is(err, storage.ErrDiscoveryCandidateInput),
		errors.Is(err, storage.ErrDiscoveryCandidateTime):
		return writeFailure(output, ExitUsage, operation+" is invalid")
	default:
		return writeFailure(output, ExitOperational, operation+" failed")
	}
}

func writeFailure(output io.Writer, code int, message string) int {
	if output != nil {
		_, _ = fmt.Fprintln(output, message)
	}
	return code
}

func printableOptional(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func printableInboxState(value storage.InboxProbeState) string {
	if value == "" {
		return "-"
	}
	return string(value)
}

type uniqueString struct {
	set   bool
	value string
}

func (value *uniqueString) String() string { return value.value }
func (value *uniqueString) Set(raw string) error {
	if value.set {
		return ErrInvalidCommand
	}
	value.set = true
	value.value = raw
	return nil
}

type uniqueTrue struct {
	set   bool
	value bool
}

func (value *uniqueTrue) String() string   { return strconv.FormatBool(value.value) }
func (value *uniqueTrue) IsBoolFlag() bool { return true }
func (value *uniqueTrue) Set(raw string) error {
	if value.set || raw != "true" {
		return ErrInvalidCommand
	}
	value.set = true
	value.value = true
	return nil
}
