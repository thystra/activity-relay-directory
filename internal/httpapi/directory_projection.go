package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	directoryProjectionPath          = "/v2/relays"
	directoryProjectionSchemaVersion = 6
	directoryProjectionCursorVersion = 2
)

func (handler *PublicListingHandler) serveDirectoryProjection(response http.ResponseWriter, request *http.Request) {
	if !allowReadMethod(response, request) {
		return
	}
	result, failure := handler.loadDirectoryProjection(request)
	if failure != nil {
		if failure.retryAfter != "" {
			response.Header().Set("Retry-After", failure.retryAfter)
		}
		writeDirectoryProjectionError(response, request, failure.status, failure.code, failure.message)
		return
	}
	writeCacheableDirectoryProjectionJSON(response, request, result)
}

func (handler *PublicListingHandler) loadDirectoryProjection(request *http.Request) (directoryProjectionResponse, *publicListingFailure) {
	return handler.loadDirectoryProjectionWithParser(request, handler.parseDirectoryProjectionQuery)
}

func (handler *PublicListingHandler) loadHumanDirectoryProjection(request *http.Request) (directoryProjectionResponse, *publicListingFailure) {
	return handler.loadDirectoryProjectionWithParser(request, handler.parseHumanDirectoryProjectionQuery)
}

func (handler *PublicListingHandler) loadDirectoryProjectionWithParser(
	request *http.Request,
	parse func(string, time.Time) (directoryProjectionQuery, error),
) (directoryProjectionResponse, *publicListingFailure) {
	if handler == nil || handler.directoryRepository == nil || handler.now == nil || handler.semaphore == nil ||
		len(handler.cursorKey) != publicListingCursorKeySize || parse == nil {
		return directoryProjectionResponse{}, &publicListingFailure{
			status:  http.StatusServiceUnavailable,
			code:    "temporarily_unavailable",
			message: "directory projection temporarily unavailable",
		}
	}

	select {
	case handler.semaphore <- struct{}{}:
		defer func() { <-handler.semaphore }()
	default:
		return directoryProjectionResponse{}, &publicListingFailure{
			status:     http.StatusTooManyRequests,
			code:       "rate_limited",
			message:    "directory projection request limit exceeded",
			retryAfter: "1",
		}
	}

	parsed, err := parse(request.URL.RawQuery, handler.now())
	if err != nil {
		return directoryProjectionResponse{}, &publicListingFailure{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "invalid directory projection request",
		}
	}

	ctx, cancel := context.WithTimeout(request.Context(), publicListingReadTimeout)
	defer cancel()
	page, err := handler.directoryRepository.ListDirectoryRelays(ctx, storage.DirectoryProjectionQuery{
		After:      parsed.after,
		Before:     parsed.before,
		Limit:      parsed.limit,
		ObservedAt: parsed.observedAt,
	})
	if err != nil {
		return directoryProjectionResponse{}, &publicListingFailure{
			status:  http.StatusServiceUnavailable,
			code:    "temporarily_unavailable",
			message: "directory projection temporarily unavailable",
		}
	}

	if len(page.Relays) > parsed.limit {
		return directoryProjectionResponse{}, directoryProjectionUnavailable()
	}

	result := directoryProjectionResponse{
		SchemaVersion: directoryProjectionSchemaVersion,
		observedAt:    parsed.observedAt,
		Relays:        make([]directoryProjectionRelay, 0, len(page.Relays)),
		Pagination: directoryProjectionPagination{
			Limit:         parsed.limit,
			CurrentCursor: parsed.currentCursor,
		},
	}
	observedUnix := parsed.observedAt.Unix()
	previous := storage.DirectoryProjectionCursor{}
	for _, relay := range page.Relays {
		current := storage.DirectoryProjectionCursor{Tier: relay.Tier, RelayActor: relay.RelayActor}
		if err := storage.ValidateDirectoryProjectionRelay(relay, observedUnix); err != nil ||
			(previous != (storage.DirectoryProjectionCursor{}) && !directoryProjectionCursorLess(previous, current)) ||
			(parsed.after != (storage.DirectoryProjectionCursor{}) && !directoryProjectionCursorLess(parsed.after, current)) ||
			(parsed.before != (storage.DirectoryProjectionCursor{}) && !directoryProjectionCursorLess(current, parsed.before)) {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		previous = current
		result.Relays = append(result.Relays, presentDirectoryProjectionRelay(relay))
	}

	var first, last storage.DirectoryProjectionCursor
	if len(page.Relays) != 0 {
		first = storage.DirectoryProjectionCursor{Tier: page.Relays[0].Tier, RelayActor: page.Relays[0].RelayActor}
		lastRelay := page.Relays[len(page.Relays)-1]
		last = storage.DirectoryProjectionCursor{Tier: lastRelay.Tier, RelayActor: lastRelay.RelayActor}
	}
	if page.Previous != (storage.DirectoryProjectionCursor{}) {
		if !page.Previous.Valid() ||
			(parsed.after == (storage.DirectoryProjectionCursor{}) && parsed.before == (storage.DirectoryProjectionCursor{})) ||
			(parsed.after != (storage.DirectoryProjectionCursor{}) && directoryProjectionCursorLess(page.Previous, parsed.after)) ||
			(parsed.before != (storage.DirectoryProjectionCursor{}) && !directoryProjectionCursorLess(page.Previous, parsed.before)) ||
			(len(page.Relays) != 0 && directoryProjectionCursorLess(first, page.Previous)) {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		cursor, err := handler.encodeDirectoryProjectionCursor(directoryProjectionCursor{
			Version:    directoryProjectionCursorVersion,
			IssuedUnix: parsed.cursorIssuedUnix,
			Tier:       page.Previous.Tier,
			RelayActor: page.Previous.RelayActor,
		})
		if err != nil {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		result.Pagination.PreviousCursor = cursor
	}

	if page.Next != (storage.DirectoryProjectionCursor{}) {
		if !page.Next.Valid() ||
			(parsed.after != (storage.DirectoryProjectionCursor{}) && !directoryProjectionCursorLess(parsed.after, page.Next)) ||
			(parsed.before != (storage.DirectoryProjectionCursor{}) && !directoryProjectionCursorLess(page.Next, parsed.before)) ||
			(len(page.Relays) != 0 && directoryProjectionCursorLess(page.Next, last)) ||
			(page.Previous != (storage.DirectoryProjectionCursor{}) && directoryProjectionCursorLess(page.Next, page.Previous)) {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		cursor, err := handler.encodeDirectoryProjectionCursor(directoryProjectionCursor{
			Version:    directoryProjectionCursorVersion,
			IssuedUnix: parsed.cursorIssuedUnix,
			Tier:       page.Next.Tier,
			RelayActor: page.Next.RelayActor,
		})
		if err != nil {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		result.Pagination.NextCursor = cursor
	}
	return result, nil
}

func directoryProjectionCursorLess(left, right storage.DirectoryProjectionCursor) bool {
	if left.Tier != right.Tier {
		return left.Tier < right.Tier
	}
	return left.RelayActor < right.RelayActor
}

func directoryProjectionUnavailable() *publicListingFailure {
	return &publicListingFailure{
		status:  http.StatusServiceUnavailable,
		code:    "temporarily_unavailable",
		message: "directory projection temporarily unavailable",
	}
}

type directoryProjectionQuery struct {
	limit            int
	registration     string
	after            storage.DirectoryProjectionCursor
	before           storage.DirectoryProjectionCursor
	observedAt       time.Time
	cursorIssuedUnix int64
	currentCursor    string
}

type directoryProjectionCursor struct {
	Version    int                   `json:"v"`
	IssuedUnix int64                 `json:"i"`
	Tier       storage.DirectoryTier `json:"t"`
	RelayActor string                `json:"a"`
}

type directoryProjectionResponse struct {
	SchemaVersion int                           `json:"schema_version"`
	Relays        []directoryProjectionRelay    `json:"relays"`
	Pagination    directoryProjectionPagination `json:"pagination"`
	observedAt    time.Time
}

type directoryProjectionRelay struct {
	RelayActor    string                          `json:"relay_actor"`
	PublicBaseURL string                          `json:"public_base_url"`
	Tier          storage.DirectoryTier           `json:"tier"`
	Profile       directoryProjectionProfile      `json:"profile"`
	Telemetry     directoryProjectionTelemetry    `json:"telemetry"`
	Heartbeat     directoryProjectionHeartbeat    `json:"heartbeat"`
	Reachability  directoryProjectionReachability `json:"reachability"`
	Inbox         directoryProjectionInbox        `json:"inbox"`
	RFC9421       directoryProjectionRFC9421      `json:"rfc9421"`
}

type directoryProjectionProfile struct {
	ParticipationMode string   `json:"participation_mode"`
	Availability      string   `json:"availability"`
	RelayType         string   `json:"relay_type"`
	Languages         []string `json:"languages"`
	Countries         []string `json:"countries"`
	Regions           []string `json:"regions"`
	Topics            []string `json:"topics"`
	ContactFediverse  string   `json:"contact_fediverse"`
	ContactEmail      string   `json:"contact_email"`
	ContactURL        string   `json:"contact_url"`
	ParticipationURL  string   `json:"participation_url"`
	Notes             string   `json:"notes"`
}

func (profile directoryProjectionProfile) Empty() bool {
	return profile.ParticipationMode == "" && profile.Availability == "" &&
		profile.RelayType == "" && len(profile.Languages) == 0 &&
		len(profile.Countries) == 0 && len(profile.Regions) == 0 &&
		len(profile.Topics) == 0 && profile.ContactFediverse == "" &&
		profile.ContactEmail == "" && profile.ContactURL == "" &&
		profile.ParticipationURL == "" && profile.Notes == ""
}

type directoryProjectionTelemetry struct {
	ReceivingInstanceCount *int    `json:"receiving_instance_count"`
	ReportedAt             *string `json:"reported_at"`
	SitesCount             *int    `json:"-"`
	SitesReportedAt        *string `json:"-"`
}

type directoryProjectionHeartbeat struct {
	State      storage.PublicHeartbeatState `json:"state"`
	LastSeenAt *string                      `json:"last_seen_at"`
}

type directoryProjectionReachability struct {
	State          storage.ReachabilityState `json:"state"`
	LastCheckedAt  *string                   `json:"last_checked_at"`
	LastSuccessAt  *string                   `json:"last_success_at"`
	Diagnostic     *directoryProbeDiagnostic `json:"diagnostic,omitempty"`
	NextEligibleAt *string                   `json:"next_eligible_at,omitempty"`
}

func (heartbeat directoryProjectionHeartbeat) DisplayState() string {
	switch heartbeat.State {
	case storage.HeartbeatNotObserved:
		return "not observed"
	case storage.HeartbeatPrune:
		return "prune boundary"
	default:
		return string(heartbeat.State)
	}
}

func (diagnostic *directoryProbeDiagnostic) Summary() string {
	if diagnostic == nil {
		return "Not recorded"
	}
	switch {
	case diagnostic.Stage == "dns" && diagnostic.Code == "nxdomain":
		return "DNS name does not exist (possibly removed)"
	case diagnostic.Stage == "dns" && diagnostic.Code == "no_address":
		return "DNS has no usable address (possibly removed)"
	case diagnostic.Stage == "actor" && diagnostic.HTTPStatus == 404:
		return "Actor returned HTTP 404 (possibly removed)"
	case diagnostic.Stage == "actor" && diagnostic.HTTPStatus == 410:
		return "Actor returned HTTP 410 Gone (possibly removed)"
	case diagnostic.Stage == "inbox" && diagnostic.HTTPStatus == 404:
		return "Inbox returned HTTP 404 (missing)"
	case diagnostic.Stage == "inbox" && diagnostic.HTTPStatus == 410:
		return "Inbox returned HTTP 410 (gone)"
	case diagnostic.Stage == "inbox" && diagnostic.HTTPStatus == 405:
		return "OPTIONS not permitted; delivery capability unknown"
	case diagnostic.Stage == "inbox" && diagnostic.HTTPStatus == 501:
		return "OPTIONS not implemented; delivery capability unknown"
	case diagnostic.Stage == "inbox" && diagnostic.HTTPStatus >= 200 && diagnostic.HTTPStatus < 400:
		return "OPTIONS returned a successful response"
	case diagnostic.Stage == "actor" && diagnostic.HTTPStatus >= 500:
		return fmt.Sprintf("Actor returned HTTP %d (service degraded or temporarily unavailable)", diagnostic.HTTPStatus)
	case diagnostic.Stage == "inbox" && diagnostic.HTTPStatus >= 500:
		return fmt.Sprintf("Inbox returned HTTP %d (service degraded or temporarily unavailable)", diagnostic.HTTPStatus)
	case diagnostic.Stage == "tls" && diagnostic.Code == "certificate":
		return "TLS certificate verification failed (degraded)"
	case diagnostic.Stage == "tls" && diagnostic.Code == "handshake":
		return "TLS handshake failed (degraded)"
	case diagnostic.Stage == "dns" && diagnostic.Code == "temporary":
		return "Temporary DNS lookup failure"
	case diagnostic.Stage == "connect" && diagnostic.Code == "refused":
		return "Connection refused (degraded or unavailable)"
	case diagnostic.HTTPStatus != 0:
		return fmt.Sprintf("%s returned HTTP %d", diagnostic.Stage, diagnostic.HTTPStatus)
	default:
		return strings.ReplaceAll(diagnostic.Stage, "_", " ") + ": " + strings.ReplaceAll(diagnostic.Code, "_", " ")
	}
}

func (reachability directoryProjectionReachability) DisplayState() string {
	return string(reachability.State)
}

type directoryProjectionInbox struct {
	URL           *string                   `json:"url"`
	DeclaredAt    *string                   `json:"declared_at"`
	ProbeState    storage.InboxProbeState   `json:"probe_state"`
	LastCheckedAt *string                   `json:"last_checked_at"`
	Diagnostic    *directoryProbeDiagnostic `json:"diagnostic,omitempty"`
}

type directoryProbeDiagnostic struct {
	Stage      string `json:"stage"`
	Code       string `json:"code"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Assessment string `json:"assessment"`
}

// A missing diagnostic means no classified observation is retained yet.
func presentProbeDiagnostic(stage, code string, status int) *directoryProbeDiagnostic {
	if stage == "" {
		return nil
	}
	return &directoryProbeDiagnostic{Stage: stage, Code: code, HTTPStatus: status,
		Assessment: diagnosticAssessment(stage, code, status)}
}

// Assessment is an interpretation of the evidence, not a lifecycle state or
// an authorization for deletion, pruning, or changes to public tiering.
func diagnosticAssessment(stage, code string, status int) string {
	switch {
	case stage == "dns" && (code == "nxdomain" || code == "no_address"):
		return "possibly_removed"
	case stage == "actor" && (status == 404 || status == 410):
		return "possibly_removed"
	case stage == "inbox" && (status == 404 || status == 410):
		return "inbox_missing"
	case stage == "inbox" && (status == 405 || status == 501):
		return "method_rejected"
	case stage == "inbox" && (status == 401 || status == 403):
		return "restricted"
	case stage == "inbox" && status >= 200 && status < 400:
		return "responsive"
	case stage == "actor" && (code == "invalid_document" || code == "content_type"):
		return "invalid_actor"
	case stage == "actor" && status >= 500, stage == "inbox" && status >= 500,
		stage == "tls", stage == "connect", stage == "network",
		stage == "dns" && (code == "timeout" || code == "temporary"):
		return "degraded"
	default:
		return "unavailable"
	}
}

type directoryProjectionRFC9421 struct {
	State      string  `json:"state"`
	VerifiedAt *string `json:"verified_at"`
}

func (inbox directoryProjectionInbox) DisplayState() string {
	switch inbox.ProbeState {
	case storage.InboxNotChecked:
		return "not checked"
	case storage.InboxMethodRejected:
		return "method rejected"
	default:
		return string(inbox.ProbeState)
	}
}

type directoryProjectionPagination struct {
	Limit          int    `json:"limit"`
	NextCursor     string `json:"next_cursor"`
	CurrentCursor  string `json:"-"`
	PreviousCursor string `json:"-"`
}

type directoryProjectionErrorEnvelope struct {
	SchemaVersion int                          `json:"schema_version"`
	Error         directoryProjectionErrorBody `json:"error"`
}

type directoryProjectionErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func presentDirectoryProjectionRelay(relay storage.DirectoryProjectionRelay) directoryProjectionRelay {
	sitesCount := relay.ReceivingInstanceCount
	sitesReportedUnix := relay.TelemetryReportedUnix
	if relay.ParticipatingInstanceCount != nil {
		sitesCount = relay.ParticipatingInstanceCount
		sitesReportedUnix = relay.ParticipatingReportedUnix
	}
	presented := directoryProjectionRelay{
		RelayActor:    relay.RelayActor,
		PublicBaseURL: relay.PublicBaseURL,
		Tier:          relay.Tier,
		Profile:       presentDirectoryProjectionProfile(relay.Profile),
		Telemetry: directoryProjectionTelemetry{
			ReceivingInstanceCount: relay.ReceivingInstanceCount,
			ReportedAt:             formatProjectionUnix(relay.TelemetryReportedUnix),
			SitesCount:             sitesCount,
			SitesReportedAt:        formatProjectionUnix(sitesReportedUnix),
		},
		Heartbeat: directoryProjectionHeartbeat{
			State:      relay.HeartbeatState,
			LastSeenAt: formatProjectionUnix(relay.LastHeartbeatUnix),
		},
		Reachability: directoryProjectionReachability{
			State:          relay.ActorState,
			LastCheckedAt:  formatProjectionUnix(relay.ActorLastCheckedUnix),
			LastSuccessAt:  formatProjectionUnix(relay.ActorLastSuccessUnix),
			Diagnostic:     presentProbeDiagnostic(relay.ActorDiagnostic.Stage, relay.ActorDiagnostic.Code, relay.ActorDiagnostic.HTTPStatus),
			NextEligibleAt: formatProjectionUnix(relay.NextReachabilityCheckUnix),
		},
		Inbox: directoryProjectionInbox{
			DeclaredAt:    formatProjectionUnix(relay.InboxDeclaredUnix),
			ProbeState:    relay.InboxProbeState,
			LastCheckedAt: formatProjectionUnix(relay.InboxLastCheckedUnix),
			Diagnostic:    presentProbeDiagnostic(relay.InboxDiagnostic.Stage, relay.InboxDiagnostic.Code, relay.InboxDiagnostic.HTTPStatus),
		},
		RFC9421: directoryProjectionRFC9421{
			State:      "not verified",
			VerifiedAt: formatProjectionUnix(relay.RFC9421VerifiedUnix),
		},
	}
	if relay.InboxURL != "" {
		urlValue := relay.InboxURL
		presented.Inbox.URL = &urlValue
	}
	if relay.RFC9421VerifiedUnix != nil {
		presented.RFC9421.State = "verified"
	}
	return presented
}

func presentDirectoryProjectionProfile(profile storage.RelayProfile) directoryProjectionProfile {
	return directoryProjectionProfile{
		ParticipationMode: profile.ParticipationMode,
		Availability:      profile.Availability,
		RelayType:         profile.RelayType,
		Languages:         append([]string{}, profile.Languages...),
		Countries:         append([]string{}, profile.Countries...),
		Regions:           append([]string{}, profile.Regions...),
		Topics:            append([]string{}, profile.Topics...),
		ContactFediverse:  profile.ContactFediverse,
		ContactEmail:      profile.ContactEmail,
		ContactURL:        profile.ContactURL,
		ParticipationURL:  profile.ParticipationURL,
		Notes:             profile.Notes,
	}
}

func formatProjectionUnix(value *int64) *string {
	if value == nil {
		return nil
	}
	formatted := time.Unix(*value, 0).UTC().Format(time.RFC3339)
	return &formatted
}

func (handler *PublicListingHandler) parseDirectoryProjectionQuery(rawQuery string, now time.Time) (directoryProjectionQuery, error) {
	return handler.parseDirectoryProjectionQueryMode(rawQuery, now, false)
}

func (handler *PublicListingHandler) parseHumanDirectoryProjectionQuery(rawQuery string, now time.Time) (directoryProjectionQuery, error) {
	result, err := handler.parseDirectoryProjectionQueryMode(rawQuery, now, true)
	if err != nil {
		return directoryProjectionQuery{}, err
	}
	values, _ := url.ParseQuery(rawQuery)
	if _, exists := values["limit"]; !exists {
		result.limit = storage.MaximumDirectoryProjectionPage
	}
	return result, nil
}

func (handler *PublicListingHandler) parseDirectoryProjectionQueryMode(
	rawQuery string,
	now time.Time,
	allowBefore bool,
) (directoryProjectionQuery, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return directoryProjectionQuery{}, err
	}
	for key, entries := range values {
		allowed := key == "limit" || key == "cursor" || (allowBefore && (key == "before" || key == "registration"))
		if !allowed || len(entries) != 1 {
			return directoryProjectionQuery{}, errors.New("invalid directory projection query")
		}
	}
	if _, hasCursor := values["cursor"]; hasCursor {
		if _, hasBefore := values["before"]; hasBefore {
			return directoryProjectionQuery{}, errors.New("invalid directory projection query")
		}
	}

	result := directoryProjectionQuery{limit: storage.DefaultDirectoryProjectionPage}
	if entries, exists := values["registration"]; exists {
		switch entries[0] {
		case "open", "restricted", "closed":
			result.registration = entries[0]
		default:
			return directoryProjectionQuery{}, errors.New("invalid registration filter")
		}
	}
	if entries, exists := values["limit"]; exists {
		raw := entries[0]
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > storage.MaximumDirectoryProjectionPage || strconv.Itoa(limit) != raw {
			return directoryProjectionQuery{}, errors.New("invalid directory projection limit")
		}
		result.limit = limit
	}

	current := now.UTC()
	if current.Unix() < 0 {
		return directoryProjectionQuery{}, errors.New("invalid directory projection time")
	}
	result.observedAt = current
	result.cursorIssuedUnix = current.Unix()

	if entries, exists := values["cursor"]; exists {
		cursor, err := handler.decodeDirectoryProjectionCursor(entries[0])
		if err != nil || cursor.IssuedUnix > current.Unix() ||
			current.Unix()-cursor.IssuedUnix > int64(publicListingCursorMaxAge/time.Second) {
			return directoryProjectionQuery{}, errors.New("invalid directory projection cursor")
		}
		result.cursorIssuedUnix = cursor.IssuedUnix
		result.currentCursor = entries[0]
		result.after = storage.DirectoryProjectionCursor{Tier: cursor.Tier, RelayActor: cursor.RelayActor}
	}
	if entries, exists := values["before"]; exists {
		cursor, err := handler.decodeDirectoryProjectionCursor(entries[0])
		if err != nil || cursor.IssuedUnix > current.Unix() ||
			current.Unix()-cursor.IssuedUnix > int64(publicListingCursorMaxAge/time.Second) {
			return directoryProjectionQuery{}, errors.New("invalid directory projection cursor")
		}
		result.cursorIssuedUnix = cursor.IssuedUnix
		result.before = storage.DirectoryProjectionCursor{Tier: cursor.Tier, RelayActor: cursor.RelayActor}
	}
	return result, nil
}

func (handler *PublicListingHandler) encodeDirectoryProjectionCursor(cursor directoryProjectionCursor) (string, error) {
	if handler == nil || len(handler.cursorKey) != publicListingCursorKeySize || !validDirectoryProjectionCursor(cursor) {
		return "", errors.New("invalid directory projection cursor")
	}
	raw, err := json.Marshal(cursor)
	if err != nil || len(raw) > publicListingMaxCursorSize {
		return "", errors.New("invalid directory projection cursor")
	}
	mac := hmac.New(sha256.New, handler.cursorKey)
	_, _ = mac.Write(raw)
	signature := mac.Sum(nil)
	encoded := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(encoded) > publicListingMaxCursorSize {
		return "", errors.New("invalid directory projection cursor")
	}
	return encoded, nil
}

func (handler *PublicListingHandler) decodeDirectoryProjectionCursor(value string) (directoryProjectionCursor, error) {
	if handler == nil || len(handler.cursorKey) != publicListingCursorKeySize || value == "" || len(value) > publicListingMaxCursorSize {
		return directoryProjectionCursor{}, errors.New("invalid directory projection cursor")
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return directoryProjectionCursor{}, errors.New("invalid directory projection cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(raw) == 0 || len(raw) > publicListingMaxCursorSize {
		return directoryProjectionCursor{}, errors.New("invalid directory projection cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return directoryProjectionCursor{}, errors.New("invalid directory projection cursor")
	}
	mac := hmac.New(sha256.New, handler.cursorKey)
	_, _ = mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return directoryProjectionCursor{}, errors.New("invalid directory projection cursor")
	}

	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var cursor directoryProjectionCursor
	if err := decoder.Decode(&cursor); err != nil {
		return directoryProjectionCursor{}, errors.New("invalid directory projection cursor")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validDirectoryProjectionCursor(cursor) {
		return directoryProjectionCursor{}, errors.New("invalid directory projection cursor")
	}
	canonical, err := handler.encodeDirectoryProjectionCursor(cursor)
	if err != nil || canonical != value {
		return directoryProjectionCursor{}, errors.New("invalid directory projection cursor")
	}
	return cursor, nil
}

func validDirectoryProjectionCursor(cursor directoryProjectionCursor) bool {
	if cursor.Version != directoryProjectionCursorVersion || cursor.IssuedUnix < 0 ||
		!cursor.Tier.Valid() || cursor.RelayActor == "" {
		return false
	}
	canonical, err := v1.NormalizeRelayActorURL(cursor.RelayActor)
	return err == nil && canonical == cursor.RelayActor
}

func writeCacheableDirectoryProjectionJSON(
	response http.ResponseWriter,
	request *http.Request,
	value directoryProjectionResponse,
) {
	body, err := json.Marshal(value)
	if err != nil {
		writeDirectoryProjectionError(response, request, http.StatusServiceUnavailable, "temporarily_unavailable", "directory projection temporarily unavailable")
		return
	}
	body = append(body, '\n')
	writeCacheablePublicRepresentation(response, request, "application/json", body)
}

func writeDirectoryProjectionError(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	code string,
	message string,
) {
	writeJSON(response, request, status, directoryProjectionErrorEnvelope{
		SchemaVersion: directoryProjectionSchemaVersion,
		Error:         directoryProjectionErrorBody{Code: code, Message: message},
	})
}
