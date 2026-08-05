package httpapi

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/admission"
	"github.com/thystra/activity-relay-directory/internal/config"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	lifecycleTestActor = "https://relay.example/actor"
	lifecycleTestBase  = "https://relay.example"
)

var lifecycleTestNow = time.Unix(1_785_867_400, 0).UTC()

type recordingLifecycleVerifier struct {
	mu          sync.Mutex
	err         error
	calls       []v1.Operation
	bodies      [][]byte
	maximums    []int64
	replayStore v1.RFC9421ReplayStore
}

func (verifier *recordingLifecycleVerifier) record(
	operation v1.Operation,
	body []byte,
	maximum int64,
	store v1.RFC9421ReplayStore,
) error {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.calls = append(verifier.calls, operation)
	verifier.bodies = append(verifier.bodies, append([]byte(nil), body...))
	verifier.maximums = append(verifier.maximums, maximum)
	verifier.replayStore = store
	return verifier.err
}

func (verifier *recordingLifecycleVerifier) VerifyRegisterAndReserve(
	_ *http.Request,
	body []byte,
	maximum int64,
	store v1.RFC9421ReplayStore,
) (*v1.VerifiedRegisterRequest, error) {
	if err := verifier.record(v1.OperationRegister, body, maximum, store); err != nil {
		return nil, err
	}
	return &v1.VerifiedRegisterRequest{
		Request: v1.RegisterRequest{
			ProtocolVersion: v1.Version,
			Operation:       v1.OperationRegister,
			RelayActor:      lifecycleTestActor,
			PublicBaseURL:   lifecycleTestBase,
		},
		Authentication: lifecycleTestAuthentication(),
	}, nil
}

func (verifier *recordingLifecycleVerifier) VerifyHeartbeatAndReserve(
	_ *http.Request,
	body []byte,
	maximum int64,
	store v1.RFC9421ReplayStore,
) (*v1.VerifiedHeartbeatRequest, error) {
	if err := verifier.record(v1.OperationHeartbeat, body, maximum, store); err != nil {
		return nil, err
	}
	return &v1.VerifiedHeartbeatRequest{
		Request: v1.IdentityRequest{
			ProtocolVersion: v1.Version,
			Operation:       v1.OperationHeartbeat,
			RelayActor:      lifecycleTestActor,
		},
		Authentication: lifecycleTestAuthentication(),
	}, nil
}

func (verifier *recordingLifecycleVerifier) VerifyUnregisterAndReserve(
	_ *http.Request,
	body []byte,
	maximum int64,
	store v1.RFC9421ReplayStore,
) (*v1.VerifiedUnregisterRequest, error) {
	if err := verifier.record(v1.OperationUnregister, body, maximum, store); err != nil {
		return nil, err
	}
	return &v1.VerifiedUnregisterRequest{
		Request: v1.IdentityRequest{
			ProtocolVersion: v1.Version,
			Operation:       v1.OperationUnregister,
			RelayActor:      lifecycleTestActor,
		},
		Authentication: lifecycleTestAuthentication(),
	}, nil
}

func lifecycleTestAuthentication() *v1.RFC9421Verification {
	return &v1.RFC9421Verification{
		KeyID:    lifecycleTestActor + "#main-key",
		KeyOwner: lifecycleTestActor,
		KeyActor: lifecycleTestActor,
		Nonce:    "private-test-nonce",
	}
}

type recordingRelayRepository struct {
	mu         sync.Mutex
	err        error
	outcome    map[v1.Operation]v1.Outcome
	operations []v1.Operation
	register   storage.RegisterIntent
	identity   storage.IdentityIntent
	acceptedAt time.Time
}

type allowingReplayStore struct {
	mu    sync.Mutex
	calls int
}

func (store *allowingReplayStore) ReserveRFC9421Replay(
	context.Context,
	v1.RFC9421ReplayKey,
	time.Time,
) (bool, error) {
	store.mu.Lock()
	store.calls++
	store.mu.Unlock()
	return true, nil
}

func (repository *recordingRelayRepository) Register(
	_ context.Context,
	intent storage.RegisterIntent,
	acceptedAt time.Time,
) (v1.Outcome, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.operations = append(repository.operations, v1.OperationRegister)
	repository.register = intent
	repository.acceptedAt = acceptedAt
	return repository.result(v1.OperationRegister)
}

func (repository *recordingRelayRepository) Heartbeat(
	_ context.Context,
	intent storage.IdentityIntent,
	acceptedAt time.Time,
) (v1.Outcome, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.operations = append(repository.operations, v1.OperationHeartbeat)
	repository.identity = intent
	repository.acceptedAt = acceptedAt
	return repository.result(v1.OperationHeartbeat)
}

func (repository *recordingRelayRepository) Unregister(
	_ context.Context,
	intent storage.IdentityIntent,
	acceptedAt time.Time,
) (v1.Outcome, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.operations = append(repository.operations, v1.OperationUnregister)
	repository.identity = intent
	repository.acceptedAt = acceptedAt
	return repository.result(v1.OperationUnregister)
}

func (repository *recordingRelayRepository) result(
	operation v1.Operation,
) (v1.Outcome, error) {
	if repository.err != nil {
		return "", repository.err
	}
	if outcome := repository.outcome[operation]; outcome != "" {
		return outcome, nil
	}
	return map[v1.Operation]v1.Outcome{
		v1.OperationRegister:   v1.OutcomeCreated,
		v1.OperationHeartbeat:  v1.OutcomeRecorded,
		v1.OperationUnregister: v1.OutcomeRemoved,
	}[operation], nil
}

func lifecycleTestLimiter(t *testing.T, source, actor admission.Rate) *admission.Limiter {
	t.Helper()
	limiter, err := admission.New(admission.Config{
		Source:             source,
		Actor:              actor,
		MaxSources:         64,
		MaxActors:          64,
		MaxConcurrent:      8,
		IdleTTL:            24 * time.Hour,
		CleanupLimit:       16,
		OverloadRetryAfter: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("admission.New() error = %v", err)
	}
	return limiter
}

func lifecycleTestHTTPHandler(
	t *testing.T,
	verifier LifecycleVerifier,
	repository storage.RelayRepository,
	limiter *admission.Limiter,
	maximum int64,
) (http.Handler, v1.RFC9421ReplayStore) {
	t.Helper()
	sourceResolver, err := admission.NewSourceResolver(nil)
	if err != nil {
		t.Fatalf("NewSourceResolver() error = %v", err)
	}
	replayStore := &allowingReplayStore{}
	lifecycle, err := NewLifecycleHandler(LifecycleDependencies{
		Verifier:         verifier,
		ReplayStore:      replayStore,
		Repository:       repository,
		SourceResolver:   sourceResolver,
		Limiter:          limiter,
		MaximumBodyBytes: maximum,
		Now:              func() time.Time { return lifecycleTestNow },
	})
	if err != nil {
		t.Fatalf("NewLifecycleHandler() error = %v", err)
	}
	cfg := config.Config{
		PublicBaseURL:       "https://directory.example",
		LifecycleEnabled:    true,
		MaxRequestBodyBytes: maximum,
	}
	return NewHandlerWithLifecycle(
		cfg,
		"test-version",
		func(context.Context) error { return nil },
		lifecycle,
		func(context.Context) (bool, error) { return true, nil },
	), replayStore
}

func generousLifecycleLimiter(t *testing.T) *admission.Limiter {
	t.Helper()
	return lifecycleTestLimiter(
		t,
		admission.Rate{Burst: 32, RefillInterval: time.Second},
		admission.Rate{Burst: 32, RefillInterval: time.Second},
	)
}

func TestLifecycleRoutesComposeVerificationAdmissionAndPersistence(t *testing.T) {
	for _, test := range []struct {
		operation v1.Operation
		path      string
		status    int
		outcome   v1.Outcome
	}{
		{v1.OperationRegister, v1.RegisterEndpointPath, http.StatusCreated, v1.OutcomeCreated},
		{v1.OperationHeartbeat, v1.HeartbeatEndpointPath, http.StatusOK, v1.OutcomeRecorded},
		{v1.OperationUnregister, v1.UnregisterEndpointPath, http.StatusOK, v1.OutcomeRemoved},
	} {
		t.Run(string(test.operation), func(t *testing.T) {
			verifier := &recordingLifecycleVerifier{}
			repository := &recordingRelayRepository{}
			handler, replayStore := lifecycleTestHTTPHandler(
				t,
				verifier,
				repository,
				generousLifecycleLimiter(t),
				4096,
			)
			body := []byte(`{"bounded":"body"}`)
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.status {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
			var document v1.OperationResponse
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if document.ProtocolVersion != v1.Version ||
				document.Operation != test.operation ||
				document.Outcome != test.outcome ||
				document.RelayActor != lifecycleTestActor {
				t.Fatalf("response = %#v", document)
			}
			if len(verifier.calls) != 1 || verifier.calls[0] != test.operation ||
				!bytes.Equal(verifier.bodies[0], body) || verifier.maximums[0] != 4096 ||
				verifier.replayStore != replayStore {
				t.Fatalf("verifier record = %#v", verifier)
			}
			if len(repository.operations) != 1 || repository.operations[0] != test.operation ||
				!repository.acceptedAt.Equal(lifecycleTestNow) {
				t.Fatalf("repository record = %#v", repository)
			}
			if test.operation == v1.OperationRegister {
				if repository.register.RelayActor != lifecycleTestActor ||
					repository.register.PublicBaseURL != lifecycleTestBase {
					t.Fatalf("register intent = %#v", repository.register)
				}
			} else if repository.identity.RelayActor != lifecycleTestActor {
				t.Fatalf("identity intent = %#v", repository.identity)
			}
		})
	}
}

func TestLifecycleRegisterRouteAcceptsActivityRelayClientFixture(t *testing.T) {
	fixtureBytes, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"testdata",
		"directory",
		"v1",
		"activity-relay-register.valid.json",
	))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		Method         string `json:"method"`
		Scheme         string `json:"scheme"`
		Authority      string `json:"authority"`
		Target         string `json:"target"`
		ContentType    string `json:"content_type"`
		ContentDigest  string `json:"content_digest"`
		Date           string `json:"date"`
		Body           string `json:"body"`
		SignatureInput string `json:"signature_input"`
		Signature      string `json:"signature"`
		KeyID          string `json:"key_id"`
		KeyOwner       string `json:"key_owner"`
		KeyActor       string `json:"key_actor"`
		PublicKeyPEM   string `json:"public_key_pem"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	fixtureNow, err := http.ParseTime(fixture.Date)
	if err != nil {
		t.Fatalf("parse fixture date: %v", err)
	}
	block, trailing := pem.Decode([]byte(fixture.PublicKeyPEM))
	if block == nil || len(trailing) != 0 || block.Type != "PUBLIC KEY" {
		t.Fatal("fixture key is not one public-key block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse fixture key: %v", err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("fixture key type = %T", parsed)
	}
	resolver := v1.RFC9421KeyResolverFunc(func(
		context.Context,
		string,
	) (v1.RFC9421ResolvedKey, error) {
		return v1.RFC9421ResolvedKey{
			KeyID:     fixture.KeyID,
			Owner:     fixture.KeyOwner,
			ActorID:   fixture.KeyActor,
			PublicKey: publicKey,
		}, nil
	})
	verifier, err := v1.NewRFC9421Verifier(v1.RFC9421VerifierOptions{
		Authority:   fixture.Authority,
		KeyResolver: resolver,
		Now:         func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("NewRFC9421Verifier() error = %v", err)
	}
	repository := &recordingRelayRepository{}
	handler, replayStore := lifecycleTestHTTPHandler(
		t,
		verifier,
		repository,
		generousLifecycleLimiter(t),
		4096,
	)
	request, err := http.NewRequest(
		fixture.Method,
		fixture.Scheme+"://"+fixture.Authority+fixture.Target,
		bytes.NewBufferString(fixture.Body),
	)
	if err != nil {
		t.Fatalf("create fixture request: %v", err)
	}
	request.Host = fixture.Authority
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Content-Type", fixture.ContentType)
	request.Header.Set("Content-Digest", fixture.ContentDigest)
	request.Header.Set("Date", fixture.Date)
	request.Header.Set("Signature-Input", fixture.SignatureInput)
	request.Header.Set("Signature", fixture.Signature)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if repository.register.RelayActor != lifecycleTestActor ||
		repository.register.PublicBaseURL != lifecycleTestBase {
		t.Fatalf("register intent = %#v", repository.register)
	}
	store := replayStore.(*allowingReplayStore)
	store.mu.Lock()
	replayCalls := store.calls
	store.mu.Unlock()
	if replayCalls != 1 {
		t.Fatalf("replay reservations = %d, want 1", replayCalls)
	}
}

func TestLifecycleRoutesFailClosedWhenUnavailable(t *testing.T) {
	for _, handler := range []http.Handler{
		testHandler(),
		NewHandlerWithLifecycle(config.Config{
			PublicBaseURL:    "https://directory.example",
			LifecycleEnabled: true,
		}, "test-version", func(context.Context) error { return nil }, nil, nil),
	} {
		request := httptest.NewRequest(http.MethodPost, v1.RegisterEndpointPath, bytes.NewBufferString("private-body"))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertProtocolError(t, response, http.StatusServiceUnavailable, v1.ErrorLifecycleUnavailable)
		if bytes.Contains(response.Body.Bytes(), []byte("private-body")) {
			t.Fatalf("unavailable response disclosed body: %q", response.Body.String())
		}
	}
}

func TestLifecycleMethodAndBodyBoundsPrecedeVerification(t *testing.T) {
	verifier := &recordingLifecycleVerifier{}
	repository := &recordingRelayRepository{}
	handler, _ := lifecycleTestHTTPHandler(
		t,
		verifier,
		repository,
		generousLifecycleLimiter(t),
		4,
	)

	request := httptest.NewRequest(http.MethodGet, v1.RegisterEndpointPath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertProtocolError(t, response, http.StatusMethodNotAllowed, v1.ErrorInvalidRequest)
	if response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("Allow = %q", response.Header().Get("Allow"))
	}

	request = httptest.NewRequest(http.MethodPost, v1.RegisterEndpointPath, bytes.NewBufferString("12345"))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertProtocolError(t, response, http.StatusRequestEntityTooLarge, v1.ErrorInvalidRequest)
	if len(verifier.calls) != 0 || len(repository.operations) != 0 {
		t.Fatalf("bounded rejection reached dependencies: verifier=%d repository=%d", len(verifier.calls), len(repository.operations))
	}
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("private body read detail")
}

func (failingReadCloser) Close() error { return nil }

var _ io.ReadCloser = failingReadCloser{}

func TestLifecycleBodyReadFailureIsRedacted(t *testing.T) {
	verifier := &recordingLifecycleVerifier{}
	repository := &recordingRelayRepository{}
	handler, _ := lifecycleTestHTTPHandler(
		t,
		verifier,
		repository,
		generousLifecycleLimiter(t),
		4096,
	)
	request := httptest.NewRequest(http.MethodPost, v1.RegisterEndpointPath, nil)
	request.Body = failingReadCloser{}
	request.ContentLength = -1
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertProtocolError(t, response, http.StatusBadRequest, v1.ErrorInvalidRequest)
	if bytes.Contains(response.Body.Bytes(), []byte("private body read detail")) ||
		len(verifier.calls) != 0 || len(repository.operations) != 0 {
		t.Fatalf("body read failure leaked or reached dependencies: %q", response.Body.String())
	}
}

func TestLifecyclePermitIsReleasedOnEveryVerificationFailure(t *testing.T) {
	verifier := &recordingLifecycleVerifier{err: v1.ErrRFC9421Crypto}
	repository := &recordingRelayRepository{}
	handler, _ := lifecycleTestHTTPHandler(
		t,
		verifier,
		repository,
		lifecycleTestLimiter(
			t,
			admission.Rate{Burst: 100, RefillInterval: time.Second},
			admission.Rate{Burst: 100, RefillInterval: time.Second},
		),
		4096,
	)
	for index := 0; index < 64; index++ {
		request := httptest.NewRequest(http.MethodPost, v1.RegisterEndpointPath, bytes.NewBufferString("{}"))
		// Use one valid direct source; the source bucket has enough initial burst
		// for this test and every failed request must still release concurrency.
		request.RemoteAddr = "192.0.2.1:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertProtocolError(t, response, http.StatusUnauthorized, v1.ErrorAuthenticationFailed)
	}
	if len(verifier.calls) != 64 || len(repository.operations) != 0 {
		t.Fatalf("calls = verifier:%d repository:%d", len(verifier.calls), len(repository.operations))
	}
}

func TestLifecycleSourceAndActorAdmissionOrdering(t *testing.T) {
	t.Run("source before verifier", func(t *testing.T) {
		verifier := &recordingLifecycleVerifier{}
		repository := &recordingRelayRepository{}
		handler, _ := lifecycleTestHTTPHandler(
			t,
			verifier,
			repository,
			lifecycleTestLimiter(
				t,
				admission.Rate{Burst: 1, RefillInterval: time.Minute},
				admission.Rate{Burst: 8, RefillInterval: time.Second},
			),
			4096,
		)
		for index, want := range []int{http.StatusCreated, http.StatusTooManyRequests} {
			request := httptest.NewRequest(http.MethodPost, v1.RegisterEndpointPath, bytes.NewBufferString("{}"))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != want {
				t.Fatalf("request %d status = %d", index, response.Code)
			}
			if want == http.StatusTooManyRequests && response.Header().Get("Retry-After") == "" {
				t.Fatal("rate-limited response omitted Retry-After")
			}
		}
		if len(verifier.calls) != 1 || len(repository.operations) != 1 {
			t.Fatalf("calls = verifier:%d repository:%d", len(verifier.calls), len(repository.operations))
		}
	})

	t.Run("actor after verifier", func(t *testing.T) {
		verifier := &recordingLifecycleVerifier{}
		repository := &recordingRelayRepository{}
		handler, _ := lifecycleTestHTTPHandler(
			t,
			verifier,
			repository,
			lifecycleTestLimiter(
				t,
				admission.Rate{Burst: 8, RefillInterval: time.Second},
				admission.Rate{Burst: 1, RefillInterval: time.Minute},
			),
			4096,
		)
		for index := 0; index < 2; index++ {
			request := httptest.NewRequest(http.MethodPost, v1.RegisterEndpointPath, bytes.NewBufferString("{}"))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			want := []int{http.StatusCreated, http.StatusTooManyRequests}[index]
			if response.Code != want {
				t.Fatalf("request %d status = %d", index, response.Code)
			}
		}
		if len(verifier.calls) != 2 || len(repository.operations) != 1 {
			t.Fatalf("calls = verifier:%d repository:%d", len(verifier.calls), len(repository.operations))
		}
	})
}

func TestLifecycleErrorMappingIsStableAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   v1.ErrorCode
	}{
		{"version", v1.ErrRegisterProtocolVersion, http.StatusBadRequest, v1.ErrorUnsupportedProtocolVersion},
		{"request", v1.ErrRegisterRequest, http.StatusBadRequest, v1.ErrorInvalidRequest},
		{"authentication", v1.ErrRFC9421Crypto, http.StatusUnauthorized, v1.ErrorAuthenticationFailed},
		{"replay", v1.ErrRFC9421Replay, http.StatusConflict, v1.ErrorReplayDetected},
		{"replay storage", v1.ErrRFC9421ReplayStore, http.StatusInternalServerError, v1.ErrorInternal},
		{"private unknown", errors.New("private key ID and database path"), http.StatusInternalServerError, v1.ErrorInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := &recordingLifecycleVerifier{err: test.err}
			repository := &recordingRelayRepository{}
			handler, _ := lifecycleTestHTTPHandler(t, verifier, repository, generousLifecycleLimiter(t), 4096)
			request := httptest.NewRequest(http.MethodPost, v1.RegisterEndpointPath, bytes.NewBufferString("private-body"))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertProtocolError(t, response, test.status, test.code)
			for _, private := range []string{"private-body", "key ID", "database path"} {
				if bytes.Contains(response.Body.Bytes(), []byte(private)) {
					t.Fatalf("response disclosed %q: %q", private, response.Body.String())
				}
			}
			if len(repository.operations) != 0 {
				t.Fatalf("verification failure reached repository: %#v", repository.operations)
			}
		})
	}
}

func TestLifecycleStorageErrorsAndOutcomes(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		outcome v1.Outcome
		status  int
		code    v1.ErrorCode
	}{
		{"absent", storage.ErrRelayAbsent, "", http.StatusConflict, v1.ErrorRelayNotRegistered},
		{"enrollment closed", storage.ErrEnrollmentClosed, "", http.StatusForbidden, v1.ErrorEnrollmentClosed},
		{"suspended", storage.ErrRelaySuspended, "", http.StatusForbidden, v1.ErrorRelaySuspended},
		{"storage", storage.ErrStorageFailure, "", http.StatusInternalServerError, v1.ErrorInternal},
		{"invalid outcome", nil, v1.OutcomeRecorded, http.StatusInternalServerError, v1.ErrorInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := &recordingLifecycleVerifier{}
			repository := &recordingRelayRepository{
				err:     test.err,
				outcome: map[v1.Operation]v1.Outcome{v1.OperationRegister: test.outcome},
			}
			handler, _ := lifecycleTestHTTPHandler(t, verifier, repository, generousLifecycleLimiter(t), 4096)
			request := httptest.NewRequest(http.MethodPost, v1.RegisterEndpointPath, bytes.NewBufferString("{}"))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertProtocolError(t, response, test.status, test.code)
		})
	}
}

func TestLifecycleStatusReportsAvailabilityOnlyWithCompleteEnabledGraph(t *testing.T) {
	verifier := &recordingLifecycleVerifier{}
	handler, _ := lifecycleTestHTTPHandler(
		t,
		verifier,
		&recordingRelayRepository{},
		generousLifecycleLimiter(t),
		4096,
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if document["lifecycle_available"] != true {
		t.Fatalf("lifecycle_available = %#v", document["lifecycle_available"])
	}
	if document["enrollment_open"] != true {
		t.Fatalf("enrollment_open = %#v", document["enrollment_open"])
	}
}

func TestNewLifecycleHandlerRejectsIncompleteGraph(t *testing.T) {
	if handler, err := NewLifecycleHandler(LifecycleDependencies{}); handler != nil ||
		!errors.Is(err, ErrLifecycleConfiguration) {
		t.Fatalf("NewLifecycleHandler(empty) = (%#v, %v)", handler, err)
	}
}

func assertProtocolError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	code v1.ErrorCode,
) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d, body = %q", response.Code, status, response.Body.String())
	}
	var document v1.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if document.ProtocolVersion != v1.Version || document.Error.Code != code ||
		document.Error.Message == "" || len(document.Error.Message) > 128 {
		t.Fatalf("error response = %#v", document)
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("response headers = %#v", response.Header())
	}
}
