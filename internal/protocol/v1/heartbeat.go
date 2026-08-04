package v1

import (
	"errors"
	"net/http"
)

const (
	// HeartbeatEndpointPath is the only version 1 target for heartbeat requests.
	HeartbeatEndpointPath = "/v1/relays/heartbeat"

	// MaximumHeartbeatBodyBytes is the absolute version 1 heartbeat-body ceiling.
	// Operators may configure a smaller positive limit.
	MaximumHeartbeatBodyBytes = MaximumRegisterBodyBytes
)

var (
	ErrHeartbeatConfiguration   = errors.New("heartbeat contract configuration is invalid")
	ErrHeartbeatRequest         = errors.New("heartbeat request is invalid")
	ErrHeartbeatBodyTooLarge    = errors.New("heartbeat request body is too large")
	ErrHeartbeatProtocolVersion = errors.New("heartbeat protocol version is unsupported")
	ErrHeartbeatTarget          = errors.New("heartbeat request target is invalid")
)

// VerifiedHeartbeatRequest contains a strictly validated heartbeat body and
// the authentication result that was atomically protected against replay.
type VerifiedHeartbeatRequest struct {
	Request        IdentityRequest
	Authentication *RFC9421Verification
}

// DecodeHeartbeatRequest strictly decodes and validates one bounded version 1
// heartbeat body. It establishes an intent only, not registered state.
func DecodeHeartbeatRequest(
	body []byte,
	maximumBytes int64,
) (IdentityRequest, error) {
	if maximumBytes <= 0 || maximumBytes > MaximumHeartbeatBodyBytes {
		return IdentityRequest{}, ErrHeartbeatConfiguration
	}
	if int64(len(body)) > maximumBytes {
		return IdentityRequest{}, ErrHeartbeatBodyTooLarge
	}

	var request IdentityRequest
	if err := decodeStrictJSONObject(body, &request); err != nil {
		return IdentityRequest{}, ErrHeartbeatRequest
	}
	if request.ProtocolVersion != Version {
		return IdentityRequest{}, ErrHeartbeatProtocolVersion
	}
	if request.Operation != OperationHeartbeat {
		return IdentityRequest{}, ErrHeartbeatRequest
	}

	actor, err := NormalizeRelayActorURL(request.RelayActor)
	if err != nil || actor != request.RelayActor {
		return IdentityRequest{}, ErrHeartbeatRequest
	}

	return request, nil
}

// VerifyHeartbeatAndReserve validates the complete heartbeat request contract
// before atomically reserving its signature nonce. It does not establish that
// the relay is registered or record liveness.
func (verifier *RFC9421Verifier) VerifyHeartbeatAndReserve(
	request *http.Request,
	body []byte,
	maximumBytes int64,
	store RFC9421ReplayStore,
) (*VerifiedHeartbeatRequest, error) {
	heartbeatRequest, err := DecodeHeartbeatRequest(body, maximumBytes)
	if err != nil {
		return nil, err
	}
	if !validOperationTarget(request, HeartbeatEndpointPath) {
		return nil, ErrHeartbeatTarget
	}

	authentication, err := verifier.VerifyPOSTAndReserve(
		request,
		body,
		heartbeatRequest.RelayActor,
		store,
	)
	if err != nil {
		return nil, err
	}

	return &VerifiedHeartbeatRequest{
		Request:        heartbeatRequest,
		Authentication: authentication,
	}, nil
}
