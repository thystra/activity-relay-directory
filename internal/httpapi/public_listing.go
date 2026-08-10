package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
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
	publicListingSchemaVersion = 1
	publicListingCursorVersion = 1
	publicListingReadTimeout   = 2 * time.Second
	publicListingCacheControl  = "public, max-age=60, must-revalidate"
	publicListingMaxCursorSize = 8192
	publicListingCursorKeySize = 32
	publicListingCursorMaxAge  = 5 * time.Minute
	publicListingConcurrency   = 16
)

var ErrPublicListingConfiguration = errors.New("public listing configuration is invalid")

// PublicListingHandler serves the separately gated public directory projection.
// Its concurrency budget is independent of signed lifecycle admission.
type PublicListingHandler struct {
	repository storage.PublicListingRepository
	now        func() time.Time
	semaphore  chan struct{}
	cursorKey  []byte
}

// NewPublicListingHandler validates the production public-listing dependency graph.
func NewPublicListingHandler(
	repository storage.PublicListingRepository,
	now func() time.Time,
) (*PublicListingHandler, error) {
	return newPublicListingHandler(repository, now, publicListingConcurrency)
}

func newPublicListingHandler(
	repository storage.PublicListingRepository,
	now func() time.Time,
	maximumConcurrent int,
) (*PublicListingHandler, error) {
	if repository == nil || now == nil || maximumConcurrent <= 0 || maximumConcurrent > 1024 {
		return nil, ErrPublicListingConfiguration
	}
	cursorKey := make([]byte, publicListingCursorKeySize)
	if _, err := rand.Read(cursorKey); err != nil {
		return nil, ErrPublicListingConfiguration
	}
	return &PublicListingHandler{
		repository: repository,
		now:        now,
		semaphore:  make(chan struct{}, maximumConcurrent),
		cursorKey:  cursorKey,
	}, nil
}

func (handler *PublicListingHandler) serve(response http.ResponseWriter, request *http.Request) {
	if !allowReadMethod(response, request) {
		return
	}
	if handler == nil || handler.repository == nil || handler.now == nil || handler.semaphore == nil ||
		len(handler.cursorKey) != publicListingCursorKeySize {
		writePublicListingError(response, request, http.StatusServiceUnavailable, "temporarily_unavailable", "public listing temporarily unavailable")
		return
	}

	select {
	case handler.semaphore <- struct{}{}:
		defer func() { <-handler.semaphore }()
	default:
		response.Header().Set("Retry-After", "1")
		writePublicListingError(response, request, http.StatusTooManyRequests, "rate_limited", "public listing request limit exceeded")
		return
	}

	parsed, err := handler.parsePublicListingQuery(request.URL.RawQuery, handler.now())
	if err != nil {
		writePublicListingError(response, request, http.StatusBadRequest, "invalid_request", "invalid public listing request")
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), publicListingReadTimeout)
	defer cancel()
	page, err := handler.repository.ListPublicRelays(ctx, storage.HealthProjectionQuery{
		After:      parsed.after,
		Limit:      parsed.limit,
		ObservedAt: parsed.observedAt,
	})
	if err != nil {
		writePublicListingError(response, request, http.StatusServiceUnavailable, "temporarily_unavailable", "public listing temporarily unavailable")
		return
	}

	result := publicListingResponse{
		SchemaVersion: publicListingSchemaVersion,
		Relays:        make([]publicListingRelay, 0, len(page.Relays)),
		Pagination: publicListingPagination{
			Limit: parsed.limit,
		},
	}
	for _, relay := range page.Relays {
		identity, identityErr := v1.NormalizeRelayIdentity(relay.RelayActor, relay.PublicBaseURL)
		expectedHealth, healthErr := storage.ClassifyHealth(relay.LastSeenUnix, parsed.observedAt.Unix())
		if identityErr != nil || identity.RelayActor != relay.RelayActor ||
			identity.PublicBaseURL != relay.PublicBaseURL || healthErr != nil ||
			expectedHealth != relay.HealthState || !relay.PublicEligible() {
			writePublicListingError(response, request, http.StatusServiceUnavailable, "temporarily_unavailable", "public listing temporarily unavailable")
			return
		}
		result.Relays = append(result.Relays, publicListingRelay{
			RelayActor:    relay.RelayActor,
			PublicBaseURL: relay.PublicBaseURL,
			Health:        relay.HealthState,
			LastSeenAt:    time.Unix(relay.LastSeenUnix, 0).UTC().Format(time.RFC3339),
		})
	}
	if page.Next != (storage.HealthProjectionCursor{}) {
		cursor, err := handler.encodePublicListingCursor(publicListingCursor{
			Version:      publicListingCursorVersion,
			ObservedUnix: parsed.observedAt.Unix(),
			LastSeenUnix: page.Next.LastSeenUnix,
			RelayActor:   page.Next.RelayActor,
		})
		if err != nil {
			writePublicListingError(response, request, http.StatusServiceUnavailable, "temporarily_unavailable", "public listing temporarily unavailable")
			return
		}
		result.Pagination.NextCursor = cursor
	}

	writeCacheablePublicListingJSON(response, request, result)
}

type publicListingQuery struct {
	limit      int
	after      storage.HealthProjectionCursor
	observedAt time.Time
}

type publicListingCursor struct {
	Version      int    `json:"v"`
	ObservedUnix int64  `json:"o"`
	LastSeenUnix int64  `json:"s"`
	RelayActor   string `json:"a"`
}

type publicListingResponse struct {
	SchemaVersion int                     `json:"schema_version"`
	Relays        []publicListingRelay    `json:"relays"`
	Pagination    publicListingPagination `json:"pagination"`
}

type publicListingRelay struct {
	RelayActor    string         `json:"relay_actor"`
	PublicBaseURL string         `json:"public_base_url"`
	Health        v1.HealthState `json:"health"`
	LastSeenAt    string         `json:"last_seen_at"`
}

type publicListingPagination struct {
	Limit      int    `json:"limit"`
	NextCursor string `json:"next_cursor"`
}

type publicListingErrorEnvelope struct {
	SchemaVersion int                    `json:"schema_version"`
	Error         publicListingErrorBody `json:"error"`
}

type publicListingErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (handler *PublicListingHandler) parsePublicListingQuery(rawQuery string, now time.Time) (publicListingQuery, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return publicListingQuery{}, err
	}
	for key, entries := range values {
		if (key != "limit" && key != "cursor") || len(entries) != 1 {
			return publicListingQuery{}, errors.New("invalid public listing query")
		}
	}

	result := publicListingQuery{limit: storage.DefaultPublicListingPage}
	if entries, exists := values["limit"]; exists {
		raw := entries[0]
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > storage.MaximumPublicListingPage || strconv.Itoa(limit) != raw {
			return publicListingQuery{}, errors.New("invalid public listing limit")
		}
		result.limit = limit
	}

	current := now.UTC()
	if current.Unix() < 0 {
		return publicListingQuery{}, errors.New("invalid public listing time")
	}
	result.observedAt = current
	if entries, exists := values["cursor"]; exists {
		cursor, err := handler.decodePublicListingCursor(entries[0])
		if err != nil || cursor.ObservedUnix > current.Unix() ||
			current.Unix()-cursor.ObservedUnix > int64(publicListingCursorMaxAge/time.Second) {
			return publicListingQuery{}, errors.New("invalid public listing cursor")
		}
		result.observedAt = time.Unix(cursor.ObservedUnix, 0).UTC()
		result.after = storage.HealthProjectionCursor{
			LastSeenUnix: cursor.LastSeenUnix,
			RelayActor:   cursor.RelayActor,
		}
	}
	return result, nil
}

func (handler *PublicListingHandler) encodePublicListingCursor(cursor publicListingCursor) (string, error) {
	if handler == nil || len(handler.cursorKey) != publicListingCursorKeySize ||
		!validPublicListingCursor(cursor) {
		return "", errors.New("invalid public listing cursor")
	}
	raw, err := json.Marshal(cursor)
	if err != nil || len(raw) > publicListingMaxCursorSize {
		return "", errors.New("invalid public listing cursor")
	}
	mac := hmac.New(sha256.New, handler.cursorKey)
	_, _ = mac.Write(raw)
	signature := mac.Sum(nil)
	encoded := base64.RawURLEncoding.EncodeToString(raw) + "." +
		base64.RawURLEncoding.EncodeToString(signature)
	if len(encoded) > publicListingMaxCursorSize {
		return "", errors.New("invalid public listing cursor")
	}
	return encoded, nil
}

func (handler *PublicListingHandler) decodePublicListingCursor(value string) (publicListingCursor, error) {
	if handler == nil || len(handler.cursorKey) != publicListingCursorKeySize ||
		value == "" || len(value) > publicListingMaxCursorSize {
		return publicListingCursor{}, errors.New("invalid public listing cursor")
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return publicListingCursor{}, errors.New("invalid public listing cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(raw) == 0 || len(raw) > publicListingMaxCursorSize {
		return publicListingCursor{}, errors.New("invalid public listing cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return publicListingCursor{}, errors.New("invalid public listing cursor")
	}
	mac := hmac.New(sha256.New, handler.cursorKey)
	_, _ = mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return publicListingCursor{}, errors.New("invalid public listing cursor")
	}

	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var cursor publicListingCursor
	if err := decoder.Decode(&cursor); err != nil {
		return publicListingCursor{}, errors.New("invalid public listing cursor")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validPublicListingCursor(cursor) {
		return publicListingCursor{}, errors.New("invalid public listing cursor")
	}
	canonical, err := handler.encodePublicListingCursor(cursor)
	if err != nil || canonical != value {
		return publicListingCursor{}, errors.New("invalid public listing cursor")
	}
	return cursor, nil
}

func validPublicListingCursor(cursor publicListingCursor) bool {
	if cursor.Version != publicListingCursorVersion || cursor.ObservedUnix < 0 ||
		cursor.LastSeenUnix < 0 || cursor.LastSeenUnix > cursor.ObservedUnix || cursor.RelayActor == "" {
		return false
	}
	canonical, err := v1.NormalizeRelayActorURL(cursor.RelayActor)
	return err == nil && canonical == cursor.RelayActor
}

func writeCacheablePublicListingJSON(
	response http.ResponseWriter,
	request *http.Request,
	value publicListingResponse,
) {
	body, err := json.Marshal(value)
	if err != nil {
		writePublicListingError(response, request, http.StatusServiceUnavailable, "temporarily_unavailable", "public listing temporarily unavailable")
		return
	}
	body = append(body, '\n')
	digest := sha256.Sum256(body)
	etag := `"sha256-` + base64.RawURLEncoding.EncodeToString(digest[:]) + `"`

	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", publicListingCacheControl)
	response.Header().Set("ETag", etag)
	if matchesIfNoneMatch(request.Header.Get("If-None-Match"), etag) {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	response.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = response.Write(body)
	}
}

func matchesIfNoneMatch(value string, etag string) bool {
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.HasPrefix(candidate, "W/") {
			candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "W/"))
		}
		if candidate == etag {
			return true
		}
	}
	return false
}

func writePublicListingError(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	code string,
	message string,
) {
	writeJSON(response, request, status, publicListingErrorEnvelope{
		SchemaVersion: publicListingSchemaVersion,
		Error:         publicListingErrorBody{Code: code, Message: message},
	})
}
