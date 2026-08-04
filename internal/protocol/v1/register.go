package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const (
	// RegisterEndpointPath is the only version 1 target for register requests.
	RegisterEndpointPath = "/v1/relays/register"

	// MaximumRegisterBodyBytes is the absolute version 1 register-body ceiling.
	// Operators may configure a smaller positive limit.
	MaximumRegisterBodyBytes = int64(1024 * 1024)
)

var (
	ErrRegisterConfiguration   = errors.New("register contract configuration is invalid")
	ErrRegisterRequest         = errors.New("register request is invalid")
	ErrRegisterBodyTooLarge    = errors.New("register request body is too large")
	ErrRegisterProtocolVersion = errors.New("register protocol version is unsupported")
	ErrRegisterTarget          = errors.New("register request target is invalid")

	errStrictJSONObject = errors.New("strict JSON object is invalid")
)

// VerifiedRegisterRequest contains a strictly validated register body and the
// authentication result that was atomically protected against replay.
type VerifiedRegisterRequest struct {
	Request        RegisterRequest
	Authentication *RFC9421Verification
}

// DecodeRegisterRequest strictly decodes and validates one bounded version 1
// register body. The supplied limit must also respect the protocol ceiling.
func DecodeRegisterRequest(
	body []byte,
	maximumBytes int64,
) (RegisterRequest, error) {
	if maximumBytes <= 0 || maximumBytes > MaximumRegisterBodyBytes {
		return RegisterRequest{}, ErrRegisterConfiguration
	}
	if int64(len(body)) > maximumBytes {
		return RegisterRequest{}, ErrRegisterBodyTooLarge
	}

	var request RegisterRequest
	if err := decodeStrictJSONObject(body, &request); err != nil {
		return RegisterRequest{}, ErrRegisterRequest
	}
	if request.ProtocolVersion != Version {
		return RegisterRequest{}, ErrRegisterProtocolVersion
	}
	if request.Operation != OperationRegister {
		return RegisterRequest{}, ErrRegisterRequest
	}

	identity, err := NormalizeRelayIdentity(
		request.RelayActor,
		request.PublicBaseURL,
	)
	if err != nil || identity.RelayActor != request.RelayActor ||
		identity.PublicBaseURL != request.PublicBaseURL {
		return RegisterRequest{}, ErrRegisterRequest
	}

	return request, nil
}

// VerifyRegisterAndReserve validates the complete register request contract
// before atomically reserving its signature nonce. It does not persist or
// otherwise mutate registration state.
func (verifier *RFC9421Verifier) VerifyRegisterAndReserve(
	request *http.Request,
	body []byte,
	maximumBytes int64,
	store RFC9421ReplayStore,
) (*VerifiedRegisterRequest, error) {
	registerRequest, err := DecodeRegisterRequest(body, maximumBytes)
	if err != nil {
		return nil, err
	}
	if !validRegisterTarget(request) {
		return nil, ErrRegisterTarget
	}

	authentication, err := verifier.VerifyPOSTAndReserve(
		request,
		body,
		registerRequest.RelayActor,
		store,
	)
	if err != nil {
		return nil, err
	}

	return &VerifiedRegisterRequest{
		Request:        registerRequest,
		Authentication: authentication,
	}, nil
}

func validRegisterTarget(request *http.Request) bool {
	return request != nil && request.URL != nil &&
		request.Method == http.MethodPost &&
		request.URL.EscapedPath() == RegisterEndpointPath &&
		request.URL.RawQuery == "" && !request.URL.ForceQuery &&
		request.URL.Fragment == "" && request.URL.RawFragment == "" &&
		(request.RequestURI == "" || request.RequestURI == RegisterEndpointPath)
}

func decodeStrictJSONObject(body []byte, destination any) error {
	if err := rejectDuplicateTopLevelJSONNames(body); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errStrictJSONObject
	}
	return nil
}

func rejectDuplicateTopLevelJSONNames(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errStrictJSONObject
	}

	seen := make(map[string]struct{}, 4)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errStrictJSONObject
		}
		name, ok := token.(string)
		if !ok {
			return errStrictJSONObject
		}
		if _, duplicate := seen[name]; duplicate {
			return errStrictJSONObject
		}
		seen[name] = struct{}{}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errStrictJSONObject
		}
	}

	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errStrictJSONObject
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errStrictJSONObject
	}
	return nil
}
