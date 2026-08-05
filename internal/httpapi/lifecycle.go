package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/thystra/activity-relay-directory/internal/admission"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

// ErrLifecycleConfiguration identifies an incomplete lifecycle handler graph.
var ErrLifecycleConfiguration = errors.New("lifecycle handler configuration is invalid")

// LifecycleVerifier is the handler-safe authenticated request boundary. The
// production implementation is v1.RFC9421Verifier; tests may provide a closed
// recording implementation without performing network access.
type LifecycleVerifier interface {
	VerifyRegisterAndReserve(
		*http.Request,
		[]byte,
		int64,
		v1.RFC9421ReplayStore,
	) (*v1.VerifiedRegisterRequest, error)
	VerifyHeartbeatAndReserve(
		*http.Request,
		[]byte,
		int64,
		v1.RFC9421ReplayStore,
	) (*v1.VerifiedHeartbeatRequest, error)
	VerifyUnregisterAndReserve(
		*http.Request,
		[]byte,
		int64,
		v1.RFC9421ReplayStore,
	) (*v1.VerifiedUnregisterRequest, error)
}

var _ LifecycleVerifier = (*v1.RFC9421Verifier)(nil)

// LifecycleDependencies contains the already-reviewed security and storage
// gates required by the three version 1 lifecycle routes.
type LifecycleDependencies struct {
	Verifier         LifecycleVerifier
	ReplayStore      v1.RFC9421ReplayStore
	Repository       storage.RelayRepository
	SourceResolver   *admission.SourceResolver
	Limiter          *admission.Limiter
	MaximumBodyBytes int64
	Now              func() time.Time
}

// LifecycleHandler composes authenticated lifecycle operations. Construction
// alone does not enable routes; Config.LifecycleEnabled remains the fail-
// closed runtime gate used by NewHandlerWithLifecycle.
type LifecycleHandler struct {
	verifier         LifecycleVerifier
	replayStore      v1.RFC9421ReplayStore
	repository       storage.RelayRepository
	sourceResolver   *admission.SourceResolver
	limiter          *admission.Limiter
	maximumBodyBytes int64
	now              func() time.Time
}

// NewLifecycleHandler validates a complete bounded dependency graph.
func NewLifecycleHandler(
	dependencies LifecycleDependencies,
) (*LifecycleHandler, error) {
	if dependencies.Verifier == nil || dependencies.ReplayStore == nil ||
		dependencies.Repository == nil || dependencies.SourceResolver == nil ||
		dependencies.Limiter == nil || dependencies.Now == nil ||
		dependencies.MaximumBodyBytes <= 0 ||
		dependencies.MaximumBodyBytes > v1.MaximumRegisterBodyBytes {
		return nil, ErrLifecycleConfiguration
	}
	return &LifecycleHandler{
		verifier:         dependencies.Verifier,
		replayStore:      dependencies.ReplayStore,
		repository:       dependencies.Repository,
		sourceResolver:   dependencies.SourceResolver,
		limiter:          dependencies.Limiter,
		maximumBodyBytes: dependencies.MaximumBodyBytes,
		now:              dependencies.Now,
	}, nil
}

func (handler *LifecycleHandler) serve(
	response http.ResponseWriter,
	request *http.Request,
	operation v1.Operation,
) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeProtocolError(
			response,
			request,
			http.StatusMethodNotAllowed,
			v1.ErrorInvalidRequest,
		)
		return
	}
	if handler == nil {
		writeProtocolError(
			response,
			request,
			http.StatusServiceUnavailable,
			v1.ErrorLifecycleUnavailable,
		)
		return
	}

	source, err := handler.sourceResolver.Source(request)
	if err != nil {
		writeProtocolError(
			response,
			request,
			http.StatusBadRequest,
			v1.ErrorInvalidRequest,
		)
		return
	}
	permit, admissionResult := handler.limiter.AdmitSource(
		request.Context(),
		operation,
		source,
	)
	if !admissionResult.Allowed() {
		writeAdmissionError(response, request, admissionResult)
		return
	}
	defer permit.Release()

	body, err := readBoundedBody(request, handler.maximumBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errLifecycleBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeProtocolError(
			response,
			request,
			status,
			v1.ErrorInvalidRequest,
		)
		return
	}

	actor, publicBaseURL, err := handler.verify(
		request,
		body,
		operation,
	)
	if err != nil {
		writeLifecycleError(response, request, err)
		return
	}
	actorAdmission := permit.AdmitActor(
		request.Context(),
		operation,
		actor,
	)
	if !actorAdmission.Allowed() {
		writeAdmissionError(response, request, actorAdmission)
		return
	}

	outcome, err := handler.persist(
		request.Context(),
		operation,
		actor,
		publicBaseURL,
		handler.now(),
	)
	if err != nil {
		writeLifecycleError(response, request, err)
		return
	}
	if !outcome.ValidFor(operation) {
		writeProtocolError(
			response,
			request,
			http.StatusInternalServerError,
			v1.ErrorInternal,
		)
		return
	}

	status := http.StatusOK
	if outcome == v1.OutcomeCreated {
		status = http.StatusCreated
	}
	writeJSON(response, request, status, v1.OperationResponse{
		ProtocolVersion: v1.Version,
		Operation:       operation,
		Outcome:         outcome,
		RelayActor:      actor,
	})
}

func (handler *LifecycleHandler) verify(
	request *http.Request,
	body []byte,
	operation v1.Operation,
) (string, string, error) {
	switch operation {
	case v1.OperationRegister:
		verified, err := handler.verifier.VerifyRegisterAndReserve(
			request,
			body,
			handler.maximumBodyBytes,
			handler.replayStore,
		)
		if err != nil {
			return "", "", err
		}
		if verified == nil || verified.Authentication == nil {
			return "", "", ErrLifecycleConfiguration
		}
		return verified.Request.RelayActor, verified.Request.PublicBaseURL, nil
	case v1.OperationHeartbeat:
		verified, err := handler.verifier.VerifyHeartbeatAndReserve(
			request,
			body,
			handler.maximumBodyBytes,
			handler.replayStore,
		)
		if err != nil {
			return "", "", err
		}
		if verified == nil || verified.Authentication == nil {
			return "", "", ErrLifecycleConfiguration
		}
		return verified.Request.RelayActor, "", nil
	case v1.OperationUnregister:
		verified, err := handler.verifier.VerifyUnregisterAndReserve(
			request,
			body,
			handler.maximumBodyBytes,
			handler.replayStore,
		)
		if err != nil {
			return "", "", err
		}
		if verified == nil || verified.Authentication == nil {
			return "", "", ErrLifecycleConfiguration
		}
		return verified.Request.RelayActor, "", nil
	default:
		return "", "", ErrLifecycleConfiguration
	}
}

func (handler *LifecycleHandler) persist(
	ctx context.Context,
	operation v1.Operation,
	actor string,
	publicBaseURL string,
	acceptedAt time.Time,
) (v1.Outcome, error) {
	switch operation {
	case v1.OperationRegister:
		return handler.repository.Register(
			ctx,
			storage.RegisterIntent{
				RelayActor:    actor,
				PublicBaseURL: publicBaseURL,
			},
			acceptedAt,
		)
	case v1.OperationHeartbeat:
		return handler.repository.Heartbeat(
			ctx,
			storage.IdentityIntent{RelayActor: actor},
			acceptedAt,
		)
	case v1.OperationUnregister:
		return handler.repository.Unregister(
			ctx,
			storage.IdentityIntent{RelayActor: actor},
			acceptedAt,
		)
	default:
		return "", ErrLifecycleConfiguration
	}
}

var errLifecycleBodyTooLarge = errors.New("lifecycle request body is too large")

func readBoundedBody(request *http.Request, maximum int64) ([]byte, error) {
	if request == nil || request.Body == nil || maximum <= 0 ||
		request.ContentLength > maximum {
		if request != nil && request.ContentLength > maximum {
			return nil, errLifecycleBodyTooLarge
		}
		return nil, errors.New("lifecycle request body is invalid")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maximum+1))
	if err != nil {
		return nil, errors.New("lifecycle request body is invalid")
	}
	if int64(len(body)) > maximum {
		return nil, errLifecycleBodyTooLarge
	}
	return body, nil
}

func writeLifecycleError(
	response http.ResponseWriter,
	request *http.Request,
	err error,
) {
	switch {
	case errors.Is(err, v1.ErrRegisterProtocolVersion),
		errors.Is(err, v1.ErrHeartbeatProtocolVersion),
		errors.Is(err, v1.ErrUnregisterProtocolVersion):
		writeProtocolError(response, request, http.StatusBadRequest, v1.ErrorUnsupportedProtocolVersion)
	case errors.Is(err, v1.ErrRFC9421Replay):
		writeProtocolError(response, request, http.StatusConflict, v1.ErrorReplayDetected)
	case errors.Is(err, v1.ErrRFC9421Malformed),
		errors.Is(err, v1.ErrRFC9421Policy),
		errors.Is(err, v1.ErrRFC9421Time),
		errors.Is(err, v1.ErrRFC9421Digest),
		errors.Is(err, v1.ErrRFC9421Key),
		errors.Is(err, v1.ErrRFC9421Crypto),
		errors.Is(err, v1.ErrRFC9421ActorBinding):
		writeProtocolError(response, request, http.StatusUnauthorized, v1.ErrorAuthenticationFailed)
	case errors.Is(err, storage.ErrRelaySuspended):
		writeProtocolError(response, request, http.StatusForbidden, v1.ErrorRelaySuspended)
	case errors.Is(err, storage.ErrEnrollmentClosed):
		writeProtocolError(response, request, http.StatusForbidden, v1.ErrorEnrollmentClosed)
	case errors.Is(err, storage.ErrRelayAbsent):
		writeProtocolError(response, request, http.StatusConflict, v1.ErrorRelayNotRegistered)
	case errors.Is(err, v1.ErrRegisterRequest),
		errors.Is(err, v1.ErrHeartbeatRequest),
		errors.Is(err, v1.ErrUnregisterRequest),
		errors.Is(err, v1.ErrRegisterTarget),
		errors.Is(err, v1.ErrHeartbeatTarget),
		errors.Is(err, v1.ErrUnregisterTarget),
		errors.Is(err, v1.ErrRegisterBodyTooLarge),
		errors.Is(err, v1.ErrHeartbeatBodyTooLarge),
		errors.Is(err, v1.ErrUnregisterBodyTooLarge):
		writeProtocolError(response, request, http.StatusBadRequest, v1.ErrorInvalidRequest)
	default:
		writeProtocolError(response, request, http.StatusInternalServerError, v1.ErrorInternal)
	}
}

func writeAdmissionError(
	response http.ResponseWriter,
	request *http.Request,
	result admission.Result,
) {
	switch result.Decision {
	case admission.DecisionSourceRateLimited,
		admission.DecisionActorRateLimited,
		admission.DecisionConcurrencyLimited,
		admission.DecisionCapacityLimited:
		if seconds := retryAfterSeconds(result.RetryAfter); seconds > 0 {
			response.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeProtocolError(response, request, http.StatusTooManyRequests, v1.ErrorRateLimited)
	default:
		writeProtocolError(response, request, http.StatusInternalServerError, v1.ErrorInternal)
	}
}

func retryAfterSeconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	seconds := int64(duration / time.Second)
	if duration%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}

func writeProtocolError(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	code v1.ErrorCode,
) {
	writeJSON(response, request, status, v1.ErrorResponse{
		ProtocolVersion: v1.Version,
		Error: v1.ErrorDocument{
			Code:    code,
			Message: protocolErrorMessage(code),
		},
	})
}

func protocolErrorMessage(code v1.ErrorCode) string {
	switch code {
	case v1.ErrorInvalidRequest:
		return "request is invalid"
	case v1.ErrorUnsupportedProtocolVersion:
		return "protocol version is unsupported"
	case v1.ErrorAuthenticationFailed:
		return "authentication failed"
	case v1.ErrorReplayDetected:
		return "request has already been received"
	case v1.ErrorLifecycleUnavailable:
		return "lifecycle service is unavailable"
	case v1.ErrorEnrollmentClosed:
		return "directory enrollment is closed"
	case v1.ErrorRelayNotRegistered:
		return "relay is not registered"
	case v1.ErrorRelaySuspended:
		return "relay is suspended"
	case v1.ErrorRateLimited:
		return "request rate limit exceeded"
	default:
		return "internal server error"
	}
}
