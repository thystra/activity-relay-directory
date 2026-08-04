package v1

import (
	"errors"
	"net/http"
)

const (
	// UnregisterEndpointPath is the only version 1 target for unregister requests.
	UnregisterEndpointPath = "/v1/relays/unregister"

	// MaximumUnregisterBodyBytes is the absolute version 1 unregister-body ceiling.
	// Operators may configure a smaller positive limit.
	MaximumUnregisterBodyBytes = MaximumRegisterBodyBytes
)

var (
	ErrUnregisterConfiguration   = errors.New("unregister contract configuration is invalid")
	ErrUnregisterRequest         = errors.New("unregister request is invalid")
	ErrUnregisterBodyTooLarge    = errors.New("unregister request body is too large")
	ErrUnregisterProtocolVersion = errors.New("unregister protocol version is unsupported")
	ErrUnregisterTarget          = errors.New("unregister request target is invalid")
)

// VerifiedUnregisterRequest contains a strictly validated unregister body and
// the authentication result that was atomically protected against replay.
type VerifiedUnregisterRequest struct {
	Request        IdentityRequest
	Authentication *RFC9421Verification
}

// DecodeUnregisterRequest strictly decodes and validates one bounded version 1
// unregister body. It establishes an intent only, not removal state.
func DecodeUnregisterRequest(
	body []byte,
	maximumBytes int64,
) (IdentityRequest, error) {
	if maximumBytes <= 0 || maximumBytes > MaximumUnregisterBodyBytes {
		return IdentityRequest{}, ErrUnregisterConfiguration
	}
	if int64(len(body)) > maximumBytes {
		return IdentityRequest{}, ErrUnregisterBodyTooLarge
	}

	var request IdentityRequest
	if err := decodeStrictJSONObject(body, &request); err != nil {
		return IdentityRequest{}, ErrUnregisterRequest
	}
	if request.ProtocolVersion != Version {
		return IdentityRequest{}, ErrUnregisterProtocolVersion
	}
	if request.Operation != OperationUnregister {
		return IdentityRequest{}, ErrUnregisterRequest
	}

	actor, err := NormalizeRelayActorURL(request.RelayActor)
	if err != nil || actor != request.RelayActor {
		return IdentityRequest{}, ErrUnregisterRequest
	}

	return request, nil
}

// VerifyUnregisterAndReserve validates the complete unregister request contract
// before atomically reserving its signature nonce. It does not remove a listing
// or modify moderation and audit state.
func (verifier *RFC9421Verifier) VerifyUnregisterAndReserve(
	request *http.Request,
	body []byte,
	maximumBytes int64,
	store RFC9421ReplayStore,
) (*VerifiedUnregisterRequest, error) {
	unregisterRequest, err := DecodeUnregisterRequest(body, maximumBytes)
	if err != nil {
		return nil, err
	}
	if !validOperationTarget(request, UnregisterEndpointPath) {
		return nil, ErrUnregisterTarget
	}

	authentication, err := verifier.VerifyPOSTAndReserve(
		request,
		body,
		unregisterRequest.RelayActor,
		store,
	)
	if err != nil {
		return nil, err
	}

	return &VerifiedUnregisterRequest{
		Request:        unregisterRequest,
		Authentication: authentication,
	}, nil
}
