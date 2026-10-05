package v2

import (
	"net/http"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

// RFC9421Verifier reuses the reviewed directory cryptographic profile while
// enforcing the version 2 signature tag and version 2 request targets.
type RFC9421Verifier struct {
	core *v1.RFC9421Verifier
}

func NewRFC9421Verifier(core *v1.RFC9421Verifier) (*RFC9421Verifier, error) {
	if core == nil {
		return nil, v1.ErrRFC9421Configuration
	}
	return &RFC9421Verifier{core: core}, nil
}

type VerifiedRegisterRequest struct {
	Request        RegisterRequest
	Authentication *v1.RFC9421Verification
}

type VerifiedHeartbeatRequest struct {
	Request        HeartbeatRequest
	Authentication *v1.RFC9421Verification
}

type VerifiedIdentityRequest struct {
	Request        IdentityRequest
	Authentication *v1.RFC9421Verification
}

func (verifier *RFC9421Verifier) VerifyRegisterAndReserve(
	request *http.Request,
	body []byte,
	maximumBytes int64,
	store v1.RFC9421ReplayStore,
) (*VerifiedRegisterRequest, error) {
	decoded, err := DecodeRegisterRequest(body, maximumBytes)
	if err != nil {
		return nil, err
	}
	if !validOperationTarget(request, RegisterEndpointPath) {
		return nil, ErrRegisterTarget
	}
	authentication, err := verifier.verifyAndReserve(request, body, decoded.RelayActor, store)
	if err != nil {
		return nil, err
	}
	return &VerifiedRegisterRequest{Request: decoded, Authentication: authentication}, nil
}

func (verifier *RFC9421Verifier) VerifyHeartbeatAndReserve(
	request *http.Request,
	body []byte,
	maximumBytes int64,
	store v1.RFC9421ReplayStore,
) (*VerifiedHeartbeatRequest, error) {
	decoded, err := DecodeHeartbeatRequest(body, maximumBytes)
	if err != nil {
		return nil, err
	}
	if !validOperationTarget(request, HeartbeatEndpointPath) {
		return nil, ErrHeartbeatTarget
	}
	authentication, err := verifier.verifyAndReserve(request, body, decoded.RelayActor, store)
	if err != nil {
		return nil, err
	}
	return &VerifiedHeartbeatRequest{Request: decoded, Authentication: authentication}, nil
}

func (verifier *RFC9421Verifier) VerifyUnregisterAndReserve(
	request *http.Request,
	body []byte,
	maximumBytes int64,
	store v1.RFC9421ReplayStore,
) (*VerifiedIdentityRequest, error) {
	decoded, err := DecodeUnregisterRequest(body, maximumBytes)
	if err != nil {
		return nil, err
	}
	if !validOperationTarget(request, UnregisterEndpointPath) {
		return nil, ErrUnregisterTarget
	}
	authentication, err := verifier.verifyAndReserve(request, body, decoded.RelayActor, store)
	if err != nil {
		return nil, err
	}
	return &VerifiedIdentityRequest{Request: decoded, Authentication: authentication}, nil
}

func (verifier *RFC9421Verifier) verifyAndReserve(
	request *http.Request,
	body []byte,
	relayActor string,
	store v1.RFC9421ReplayStore,
) (*v1.RFC9421Verification, error) {
	if verifier == nil || verifier.core == nil {
		return nil, v1.ErrRFC9421Configuration
	}
	return verifier.core.VerifyPOSTAndReserveWithTag(
		request, body, relayActor, RFC9421SignatureTag, store,
	)
}
