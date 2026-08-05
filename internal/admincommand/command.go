// Package admincommand implements the local moderation command contract. It
// contains no database opener, HTTP route, or operating-system authorization
// mechanism; the executable adapter supplies an already authorized repository.
package admincommand

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	ExitSuccess     = 0
	ExitOperational = 1
	ExitUsage       = 2
	ExitAbsent      = 3
	ExitCanceled    = 4

	DefaultAuditLimit = 50
	OutputHuman       = OutputFormat("human")
	OutputJSON        = OutputFormat("json")

	outputSchema = "activity-relay-directory.admin.v1"
)

var (
	ErrInvalidCommand = errors.New("local moderation command is invalid")
	ErrConfirmation   = errors.New("local moderation confirmation failed")
)

// Action is the closed local moderation command vocabulary.
type Action string

const (
	ActionSuspend Action = "suspend"
	ActionRestore Action = "restore"
	ActionShow    Action = "show"
	ActionAudit   Action = "audit"
)

func (action Action) valid() bool {
	switch action {
	case ActionSuspend, ActionRestore, ActionShow, ActionAudit:
		return true
	default:
		return false
	}
}

func (action Action) changesState() bool {
	return action == ActionSuspend || action == ActionRestore
}

// OutputFormat selects a stable local human or JSON representation.
type OutputFormat string

func (format OutputFormat) valid() bool {
	return format == OutputHuman || format == OutputJSON
}

// Request is a fully parsed and canonical local moderation request.
type Request struct {
	Action      Action
	RelayActor  string
	ModeratorID string
	ReasonCode  string
	AssumeYes   bool
	Format      OutputFormat
	AuditLimit  int
	AuditAfter  storage.ModerationAuditCursor
}

// Repository is the private local moderation capability required by this
// adapter. Public HTTP code must not receive this combined interface.
type Repository interface {
	storage.ModerationRepository
	storage.ModerationReadRepository
}

// Parse validates one action and its exact action-specific flags. It rejects
// duplicate flags, positional values, noncanonical actors, and alternate cursor
// encodings.
func Parse(arguments []string) (Request, error) {
	if len(arguments) == 0 {
		return Request{}, ErrInvalidCommand
	}
	action := Action(arguments[0])
	if !action.valid() {
		return Request{}, ErrInvalidCommand
	}

	request := Request{
		Action:     action,
		Format:     OutputHuman,
		AuditLimit: DefaultAuditLimit,
	}
	flags := flag.NewFlagSet(string(action), flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	actor := uniqueString{}
	format := uniqueString{value: string(OutputHuman)}
	flags.Var(&actor, "actor", "canonical relay actor")
	flags.Var(&format, "format", "human or json")

	var moderator, reason, after uniqueString
	var assumeYes uniqueTrue
	var limit uniqueInt
	limit.value = DefaultAuditLimit

	switch action {
	case ActionSuspend, ActionRestore:
		flags.Var(&moderator, "moderator", "private moderator identifier")
		flags.Var(&reason, "reason", "private reason code")
		flags.Var(&assumeYes, "yes", "confirm noninteractively")
	case ActionAudit:
		flags.Var(&after, "after", "keyset cursor")
		flags.Var(&limit, "limit", "page size")
	case ActionShow:
	}

	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return Request{}, ErrInvalidCommand
	}
	if !actor.set || !canonicalActor(actor.value) {
		return Request{}, ErrInvalidCommand
	}
	request.RelayActor = actor.value
	request.Format = OutputFormat(format.value)
	if !request.Format.valid() {
		return Request{}, ErrInvalidCommand
	}

	switch action {
	case ActionSuspend, ActionRestore:
		if !moderator.set || !reason.set ||
			!storage.ValidOperatorID(moderator.value) ||
			!storage.ValidModerationReasonCode(reason.value) {
			return Request{}, ErrInvalidCommand
		}
		request.ModeratorID = moderator.value
		request.ReasonCode = reason.value
		request.AssumeYes = assumeYes.value
	case ActionAudit:
		if limit.value <= 0 || limit.value > storage.MaximumModerationAuditPage {
			return Request{}, ErrInvalidCommand
		}
		request.AuditLimit = limit.value
		if after.set {
			cursor, err := ParseCursor(after.value)
			if err != nil {
				return Request{}, ErrInvalidCommand
			}
			request.AuditAfter = cursor
		}
	}
	return request, nil
}

// Confirm requires the exact canonical actor on stdin for a state-changing
// request unless the explicit automation acknowledgement was supplied.
func Confirm(request Request, input io.Reader, errorOutput io.Writer) error {
	if !request.Action.changesState() || request.AssumeYes {
		return nil
	}
	if input == nil || errorOutput == nil {
		return ErrConfirmation
	}
	if _, err := fmt.Fprintf(
		errorOutput,
		"confirmation required: type %s to %s: ",
		request.RelayActor,
		request.Action,
	); err != nil {
		return ErrConfirmation
	}

	reader := bufio.NewReader(io.LimitReader(input, 4098))
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrConfirmation
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if line != request.RelayActor {
		return ErrConfirmation
	}
	return nil
}

// Execute applies one parsed request with fixed exit classes and bounded,
// redacted errors. Local audit output intentionally contains private tokens and
// must remain protected by operating-system database access.
func Execute(
	ctx context.Context,
	request Request,
	repository Repository,
	standardOutput io.Writer,
	errorOutput io.Writer,
	now func() time.Time,
) int {
	if ctx == nil || repository == nil || standardOutput == nil ||
		errorOutput == nil || now == nil || !request.Action.valid() ||
		!request.Format.valid() {
		return writeFailure(errorOutput, ExitOperational, "moderation command unavailable")
	}

	switch request.Action {
	case ActionSuspend, ActionRestore:
		return executeTransition(ctx, request, repository, standardOutput, errorOutput, now)
	case ActionShow:
		return executeShow(ctx, request, repository, standardOutput, errorOutput)
	case ActionAudit:
		return executeAudit(ctx, request, repository, standardOutput, errorOutput)
	default:
		return writeFailure(errorOutput, ExitOperational, "moderation command unavailable")
	}
}

// ParseCursor decodes the canonical local keyset cursor form recordedUnix:eventID.
func ParseCursor(value string) (storage.ModerationAuditCursor, error) {
	if len(value) == 0 || len(value) > 64 || strings.Count(value, ":") != 1 {
		return storage.ModerationAuditCursor{}, ErrInvalidCommand
	}
	parts := strings.SplitN(value, ":", 2)
	recorded, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return storage.ModerationAuditCursor{}, ErrInvalidCommand
	}
	eventID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return storage.ModerationAuditCursor{}, ErrInvalidCommand
	}
	cursor := storage.ModerationAuditCursor{
		RecordedUnix: recorded,
		EventID:      eventID,
	}
	if !cursor.Valid() || cursor == (storage.ModerationAuditCursor{}) ||
		FormatCursor(cursor) != value {
		return storage.ModerationAuditCursor{}, ErrInvalidCommand
	}
	return cursor, nil
}

// FormatCursor returns the canonical local keyset cursor or an empty string for
// the zero end/start marker.
func FormatCursor(cursor storage.ModerationAuditCursor) string {
	if cursor == (storage.ModerationAuditCursor{}) {
		return ""
	}
	return strconv.FormatInt(cursor.RecordedUnix, 10) + ":" +
		strconv.FormatInt(cursor.EventID, 10)
}

func executeTransition(
	ctx context.Context,
	request Request,
	repository Repository,
	standardOutput io.Writer,
	errorOutput io.Writer,
	now func() time.Time,
) int {
	intent := storage.ModerationIntent{
		RelayActor:  request.RelayActor,
		ModeratorID: request.ModeratorID,
		ReasonCode:  request.ReasonCode,
	}
	var (
		outcome storage.ModerationOutcome
		err     error
	)
	if request.Action == ActionSuspend {
		outcome, err = repository.Suspend(ctx, intent, now())
	} else {
		outcome, err = repository.Restore(ctx, intent, now())
	}
	if err != nil {
		return classifyError(errorOutput, err, "moderation decision")
	}
	if !outcome.Valid() {
		return writeFailure(errorOutput, ExitOperational, "moderation decision failed")
	}
	if request.Format == OutputJSON {
		return writeJSON(errorOutput, standardOutput, transitionDocument{
			Schema:     outputSchema,
			Kind:       "moderation_decision",
			Action:     request.Action,
			RelayActor: request.RelayActor,
			Outcome:    outcome,
		})
	}
	if _, err := fmt.Fprintf(
		standardOutput,
		"action=%s relay_actor=%s outcome=%s\n",
		request.Action,
		request.RelayActor,
		outcome,
	); err != nil {
		return writeFailure(errorOutput, ExitOperational, "moderation output failed")
	}
	return ExitSuccess
}

func executeShow(
	ctx context.Context,
	request Request,
	repository Repository,
	standardOutput io.Writer,
	errorOutput io.Writer,
) int {
	state, err := repository.ModerationState(ctx, request.RelayActor)
	if err != nil {
		return classifyError(errorOutput, err, "moderation state")
	}
	if request.Format == OutputJSON {
		return writeJSON(errorOutput, standardOutput, stateDocument{
			Schema:              outputSchema,
			Kind:                "moderation_state",
			RelayActor:          state.RelayActor,
			PublicBaseURL:       state.PublicBaseURL,
			LifecycleState:      state.LifecycleState,
			AdministrativeState: state.AdministrativeState,
			FirstRegisteredUnix: state.FirstRegisteredUnix,
			UpdatedUnix:         state.UpdatedUnix,
			LastHeartbeatUnix:   state.LastHeartbeatUnix,
			UnregisteredUnix:    state.UnregisteredUnix,
			SuspendedUnix:       state.SuspendedUnix,
		})
	}
	lines := []struct {
		name  string
		value string
	}{
		{"relay_actor", state.RelayActor},
		{"public_base_url", state.PublicBaseURL},
		{"lifecycle_state", string(state.LifecycleState)},
		{"administrative_state", string(state.AdministrativeState)},
		{"first_registered_at_unix", strconv.FormatInt(state.FirstRegisteredUnix, 10)},
		{"updated_at_unix", strconv.FormatInt(state.UpdatedUnix, 10)},
		{"last_heartbeat_at_unix", optionalUnix(state.LastHeartbeatUnix)},
		{"unregistered_at_unix", optionalUnix(state.UnregisteredUnix)},
		{"suspended_at_unix", optionalUnix(state.SuspendedUnix)},
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(standardOutput, "%s=%s\n", line.name, line.value); err != nil {
			return writeFailure(errorOutput, ExitOperational, "moderation output failed")
		}
	}
	return ExitSuccess
}

func executeAudit(
	ctx context.Context,
	request Request,
	repository Repository,
	standardOutput io.Writer,
	errorOutput io.Writer,
) int {
	page, err := repository.ModerationAudit(ctx, storage.ModerationAuditQuery{
		RelayActor: request.RelayActor,
		After:      request.AuditAfter,
		Limit:      request.AuditLimit,
	})
	if err != nil {
		return classifyError(errorOutput, err, "moderation audit")
	}
	if request.Format == OutputJSON {
		events := make([]auditEventDocument, 0, len(page.Events))
		for _, event := range page.Events {
			events = append(events, auditEventDocument{
				EventID:      event.EventID,
				RelayActor:   event.RelayActor,
				Action:       event.Action,
				ModeratorID:  event.ModeratorID,
				ReasonCode:   event.ReasonCode,
				RecordedUnix: event.RecordedUnix,
			})
		}
		return writeJSON(errorOutput, standardOutput, auditDocument{
			Schema:     outputSchema,
			Kind:       "moderation_audit",
			RelayActor: request.RelayActor,
			Events:     events,
			NextCursor: FormatCursor(page.Next),
		})
	}
	if _, err := fmt.Fprintf(
		standardOutput,
		"relay_actor=%s\ncount=%d\n",
		request.RelayActor,
		len(page.Events),
	); err != nil {
		return writeFailure(errorOutput, ExitOperational, "moderation output failed")
	}
	for _, event := range page.Events {
		if _, err := fmt.Fprintf(
			standardOutput,
			"event_id=%d recorded_at_unix=%d action=%s moderator_id=%s reason_code=%s\n",
			event.EventID,
			event.RecordedUnix,
			event.Action,
			event.ModeratorID,
			event.ReasonCode,
		); err != nil {
			return writeFailure(errorOutput, ExitOperational, "moderation output failed")
		}
	}
	if _, err := fmt.Fprintf(standardOutput, "next_cursor=%s\n", FormatCursor(page.Next)); err != nil {
		return writeFailure(errorOutput, ExitOperational, "moderation output failed")
	}
	return ExitSuccess
}

func classifyError(errorOutput io.Writer, err error, subject string) int {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return writeFailure(errorOutput, ExitCanceled, subject+" canceled")
	case errors.Is(err, storage.ErrRelayAbsent):
		return writeFailure(errorOutput, ExitAbsent, "relay is not retained")
	case errors.Is(err, storage.ErrTransitionInput),
		errors.Is(err, storage.ErrTransitionTime),
		errors.Is(err, storage.ErrAdministrativeReadInput):
		return writeFailure(errorOutput, ExitUsage, subject+" is invalid")
	default:
		return writeFailure(errorOutput, ExitOperational, subject+" failed")
	}
}

func writeFailure(output io.Writer, code int, message string) int {
	if output != nil {
		_, _ = fmt.Fprintln(output, message)
	}
	return code
}

func writeJSON(errorOutput, standardOutput io.Writer, document any) int {
	encoder := json.NewEncoder(standardOutput)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return writeFailure(errorOutput, ExitOperational, "moderation output failed")
	}
	return ExitSuccess
}

func canonicalActor(value string) bool {
	canonical, err := v1.NormalizeRelayActorURL(value)
	return err == nil && canonical == value
}

func optionalUnix(value *int64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatInt(*value, 10)
}

type transitionDocument struct {
	Schema     string                    `json:"schema"`
	Kind       string                    `json:"kind"`
	Action     Action                    `json:"action"`
	RelayActor string                    `json:"relay_actor"`
	Outcome    storage.ModerationOutcome `json:"outcome"`
}

type stateDocument struct {
	Schema              string                           `json:"schema"`
	Kind                string                           `json:"kind"`
	RelayActor          string                           `json:"relay_actor"`
	PublicBaseURL       string                           `json:"public_base_url"`
	LifecycleState      storage.RelayLifecycleState      `json:"lifecycle_state"`
	AdministrativeState storage.RelayAdministrativeState `json:"administrative_state"`
	FirstRegisteredUnix int64                            `json:"first_registered_at_unix"`
	UpdatedUnix         int64                            `json:"updated_at_unix"`
	LastHeartbeatUnix   *int64                           `json:"last_heartbeat_at_unix"`
	UnregisteredUnix    *int64                           `json:"unregistered_at_unix"`
	SuspendedUnix       *int64                           `json:"suspended_at_unix"`
}

type auditEventDocument struct {
	EventID      int64                    `json:"event_id"`
	RelayActor   string                   `json:"relay_actor"`
	Action       storage.ModerationAction `json:"action"`
	ModeratorID  string                   `json:"moderator_id"`
	ReasonCode   string                   `json:"reason_code"`
	RecordedUnix int64                    `json:"recorded_at_unix"`
}

type auditDocument struct {
	Schema     string               `json:"schema"`
	Kind       string               `json:"kind"`
	RelayActor string               `json:"relay_actor"`
	Events     []auditEventDocument `json:"events"`
	NextCursor string               `json:"next_cursor"`
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

type uniqueInt struct {
	set   bool
	value int
}

func (value *uniqueInt) String() string { return strconv.Itoa(value.value) }
func (value *uniqueInt) Set(raw string) error {
	if value.set {
		return ErrInvalidCommand
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(parsed) != raw {
		return ErrInvalidCommand
	}
	value.set = true
	value.value = parsed
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
