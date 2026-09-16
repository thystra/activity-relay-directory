package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	directoryProjectionSchemaVersion = 2
	directoryProjectionCursorVersion = 1
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
		Relays:        make([]directoryProjectionRelay, 0, len(page.Relays)),
		Pagination: directoryProjectionPagination{
			Limit:         parsed.limit,
			CurrentCursor: parsed.currentCursor,
		},
	}
	observedUnix := parsed.observedAt.Unix()
	previousActor := ""
	for _, relay := range page.Relays {
		if err := storage.ValidateDirectoryProjectionRelay(relay, observedUnix); err != nil ||
			(previousActor != "" && relay.RelayActor <= previousActor) ||
			(parsed.after.RelayActor != "" && relay.RelayActor <= parsed.after.RelayActor) ||
			(parsed.before.RelayActor != "" && relay.RelayActor >= parsed.before.RelayActor) {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		previousActor = relay.RelayActor
		result.Relays = append(result.Relays, presentDirectoryProjectionRelay(relay))
	}

	if page.Previous != (storage.DirectoryProjectionCursor{}) {
		if !page.Previous.Valid() ||
			(parsed.after.RelayActor != "" && page.Previous.RelayActor <= parsed.after.RelayActor) ||
			(parsed.before.RelayActor != "" && page.Previous.RelayActor >= parsed.before.RelayActor) ||
			(len(page.Relays) != 0 && page.Previous.RelayActor > page.Relays[0].RelayActor) {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		cursor, err := handler.encodeDirectoryProjectionCursor(directoryProjectionCursor{
			Version:    directoryProjectionCursorVersion,
			IssuedUnix: parsed.cursorIssuedUnix,
			RelayActor: page.Previous.RelayActor,
		})
		if err != nil {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		result.Pagination.PreviousCursor = cursor
	}

	if page.Next != (storage.DirectoryProjectionCursor{}) {
		if !page.Next.Valid() ||
			(parsed.after.RelayActor != "" && page.Next.RelayActor <= parsed.after.RelayActor) ||
			(parsed.before.RelayActor != "" && page.Next.RelayActor >= parsed.before.RelayActor) ||
			(len(page.Relays) != 0 && page.Next.RelayActor < page.Relays[len(page.Relays)-1].RelayActor) {
			return directoryProjectionResponse{}, directoryProjectionUnavailable()
		}
		cursor, err := handler.encodeDirectoryProjectionCursor(directoryProjectionCursor{
			Version:    directoryProjectionCursorVersion,
			IssuedUnix: parsed.cursorIssuedUnix,
			RelayActor: page.Next.RelayActor,
		})
		if err != nil {
			return directoryProjectionResponse{}, &publicListingFailure{
				status:  http.StatusServiceUnavailable,
				code:    "temporarily_unavailable",
				message: "directory projection temporarily unavailable",
			}
		}
		result.Pagination.NextCursor = cursor
	}
	return result, nil
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
	after            storage.DirectoryProjectionCursor
	before           storage.DirectoryProjectionCursor
	observedAt       time.Time
	cursorIssuedUnix int64
	currentCursor    string
}

type directoryProjectionCursor struct {
	Version    int    `json:"v"`
	IssuedUnix int64  `json:"i"`
	RelayActor string `json:"a"`
}

type directoryProjectionResponse struct {
	SchemaVersion int                           `json:"schema_version"`
	Relays        []directoryProjectionRelay    `json:"relays"`
	Pagination    directoryProjectionPagination `json:"pagination"`
}

type directoryProjectionRelay struct {
	RelayActor    string                          `json:"relay_actor"`
	PublicBaseURL string                          `json:"public_base_url"`
	Heartbeat     directoryProjectionHeartbeat    `json:"heartbeat"`
	Reachability  directoryProjectionReachability `json:"reachability"`
	Inbox         directoryProjectionInbox        `json:"inbox"`
	RFC9421       directoryProjectionRFC9421      `json:"rfc9421"`
}

type directoryProjectionHeartbeat struct {
	State      storage.PublicHeartbeatState `json:"state"`
	LastSeenAt *string                      `json:"last_seen_at"`
}

type directoryProjectionReachability struct {
	State         storage.ReachabilityState `json:"state"`
	LastCheckedAt *string                   `json:"last_checked_at"`
	LastSuccessAt *string                   `json:"last_success_at"`
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

func (reachability directoryProjectionReachability) DisplayState() string {
	return string(reachability.State)
}

type directoryProjectionInbox struct {
	URL           *string                 `json:"url"`
	DeclaredAt    *string                 `json:"declared_at"`
	ProbeState    storage.InboxProbeState `json:"probe_state"`
	LastCheckedAt *string                 `json:"last_checked_at"`
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
	presented := directoryProjectionRelay{
		RelayActor:    relay.RelayActor,
		PublicBaseURL: relay.PublicBaseURL,
		Heartbeat: directoryProjectionHeartbeat{
			State:      relay.HeartbeatState,
			LastSeenAt: formatProjectionUnix(relay.LastSeenUnix),
		},
		Reachability: directoryProjectionReachability{
			State:         relay.ActorState,
			LastCheckedAt: formatProjectionUnix(relay.ActorLastCheckedUnix),
			LastSuccessAt: formatProjectionUnix(relay.ActorLastSuccessUnix),
		},
		Inbox: directoryProjectionInbox{
			DeclaredAt:    formatProjectionUnix(relay.InboxDeclaredUnix),
			ProbeState:    relay.InboxProbeState,
			LastCheckedAt: formatProjectionUnix(relay.InboxLastCheckedUnix),
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
	return handler.parseDirectoryProjectionQueryMode(rawQuery, now, true)
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
		allowed := key == "limit" || key == "cursor" || (allowBefore && key == "before")
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
		result.after = storage.DirectoryProjectionCursor{RelayActor: cursor.RelayActor}
	}
	if entries, exists := values["before"]; exists {
		cursor, err := handler.decodeDirectoryProjectionCursor(entries[0])
		if err != nil || cursor.IssuedUnix > current.Unix() ||
			current.Unix()-cursor.IssuedUnix > int64(publicListingCursorMaxAge/time.Second) {
			return directoryProjectionQuery{}, errors.New("invalid directory projection cursor")
		}
		result.cursorIssuedUnix = cursor.IssuedUnix
		result.before = storage.DirectoryProjectionCursor{RelayActor: cursor.RelayActor}
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
	if cursor.Version != directoryProjectionCursorVersion || cursor.IssuedUnix < 0 || cursor.RelayActor == "" {
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
