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

	outputSchema = "activity-relay-directory.discovery-admin.v1"
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

type Candidate struct {
	Line int
	URL  string
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
	Line int
	Code string
}

type Plan struct {
	CandidateCount int
	Ready          []PreparedRelay
	Retained       []RetainedCandidate
	AlreadyKnown   []AlreadyKnownCandidate
	Duplicates     []DuplicateCandidate
	Failed         []FailedCandidate
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
		loaded, err := LoadCandidates(request.FilePath)
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
			plan.Failed = append(plan.Failed, FailedCandidate{Line: prepared.line, Code: prepared.code})
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
	plan.Ready = ready
	plan.Retained = retained
	return plan, nil
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
	return nil
}

func Confirm(request Request, plan Plan, input io.Reader, errorOutput io.Writer) error {
	if request.AssumeYes {
		return nil
	}
	if request.Action == ActionImport && len(plan.Ready) == 0 &&
		len(plan.Retained) == 0 && len(plan.AlreadyKnown) > 0 {
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
		if mutationCount == 0 {
			return ErrConfirmation
		}
		expected = "IMPORT " + strconv.Itoa(mutationCount)
		prompt = "confirmation required: type " + expected + " to apply relay discoveries: "
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
		results = append(results, mutationResult{
			Line: known.Line, RelayActor: known.RelayActor, Status: "already_known",
		})
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
		results = append(results, mutationResult{
			Line: ready.Line, RelayActor: ready.RelayActor, Status: "ok", Outcome: outcome,
			InboxURL: ready.InboxURL, InboxProbeState: ready.InboxProbeState,
		})
	}
	return renderResults(request, results, standardOutput, errorOutput, hadFailure)
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
		encoder := json.NewEncoder(standardOutput)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(resultDocument{
			Schema: outputSchema, Kind: "discovery_result", Action: request.Action, Results: results,
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
