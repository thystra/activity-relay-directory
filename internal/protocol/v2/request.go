package v2

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	RegisterEndpointPath   = "/v2/relays/register"
	HeartbeatEndpointPath  = "/v2/relays/heartbeat"
	UnregisterEndpointPath = "/v2/relays/unregister"

	MaximumRegisterBodyBytes   = int64(1024 * 1024)
	MaximumHeartbeatBodyBytes  = MaximumRegisterBodyBytes
	MaximumUnregisterBodyBytes = MaximumRegisterBodyBytes
)

var (
	ErrRegisterConfiguration   = errors.New("version 2 register contract configuration is invalid")
	ErrRegisterRequest         = errors.New("version 2 register request is invalid")
	ErrRegisterBodyTooLarge    = errors.New("version 2 register request body is too large")
	ErrRegisterProtocolVersion = errors.New("version 2 register protocol version is unsupported")
	ErrRegisterTarget          = errors.New("version 2 register request target is invalid")

	ErrHeartbeatConfiguration   = errors.New("version 2 heartbeat contract configuration is invalid")
	ErrHeartbeatRequest         = errors.New("version 2 heartbeat request is invalid")
	ErrHeartbeatBodyTooLarge    = errors.New("version 2 heartbeat request body is too large")
	ErrHeartbeatProtocolVersion = errors.New("version 2 heartbeat protocol version is unsupported")
	ErrHeartbeatTarget          = errors.New("version 2 heartbeat request target is invalid")

	ErrUnregisterConfiguration   = errors.New("version 2 unregister contract configuration is invalid")
	ErrUnregisterRequest         = errors.New("version 2 unregister request is invalid")
	ErrUnregisterBodyTooLarge    = errors.New("version 2 unregister request body is too large")
	ErrUnregisterProtocolVersion = errors.New("version 2 unregister protocol version is unsupported")
	ErrUnregisterTarget          = errors.New("version 2 unregister request target is invalid")
)

type profileWire struct {
	ParticipationMode *string   `json:"participation_mode"`
	Availability      *string   `json:"availability"`
	RelayType         *string   `json:"relay_type"`
	Languages         *[]string `json:"languages"`
	Countries         *[]string `json:"countries"`
	Regions           *[]string `json:"regions"`
	Topics            *[]string `json:"topics"`
	ContactFediverse  *string   `json:"contact_fediverse"`
	ContactEmail      *string   `json:"contact_email"`
	ContactURL        *string   `json:"contact_url"`
	ParticipationURL  *string   `json:"participation_url"`
	Notes             *string   `json:"notes"`
}

type telemetryWire struct {
	ReceivingInstanceCount *int `json:"receiving_instance_count"`
}

type registerWire struct {
	ProtocolVersion int            `json:"protocol_version"`
	Operation       Operation      `json:"operation"`
	RelayActor      string         `json:"relay_actor"`
	PublicBaseURL   string         `json:"public_base_url"`
	Profile         profileWire    `json:"profile"`
	Telemetry       *telemetryWire `json:"telemetry,omitempty"`
}

func (profile profileWire) normalized() (storage.RelayProfile, error) {
	if profile.ParticipationMode == nil || profile.Availability == nil ||
		profile.RelayType == nil || profile.Languages == nil ||
		profile.Countries == nil || profile.Regions == nil || profile.Topics == nil ||
		profile.ContactFediverse == nil || profile.ContactEmail == nil ||
		profile.ContactURL == nil || profile.ParticipationURL == nil || profile.Notes == nil {
		return storage.RelayProfile{}, storage.ErrProfileInput
	}
	normalized, err := storage.NormalizeRelayProfile(storage.RelayProfile{
		ParticipationMode: *profile.ParticipationMode,
		Availability:      *profile.Availability,
		RelayType:         *profile.RelayType,
		Languages:         *profile.Languages,
		Countries:         *profile.Countries,
		Regions:           *profile.Regions,
		Topics:            *profile.Topics,
		ContactFediverse:  *profile.ContactFediverse,
		ContactEmail:      *profile.ContactEmail,
		ContactURL:        *profile.ContactURL,
		ParticipationURL:  *profile.ParticipationURL,
		Notes:             *profile.Notes,
	})
	if err != nil {
		return storage.RelayProfile{}, err
	}
	switch normalized.ParticipationMode {
	case "", "open", "restricted", "closed":
		return normalized, nil
	default:
		return storage.RelayProfile{}, storage.ErrProfileInput
	}
}

func (telemetry *telemetryWire) normalized(relayActor string) (*storage.TelemetryIntent, error) {
	if telemetry == nil {
		return nil, nil
	}
	if telemetry.ReceivingInstanceCount == nil {
		return nil, storage.ErrTelemetryInput
	}
	intent := storage.TelemetryIntent{RelayActor: relayActor, ReceivingInstanceCount: *telemetry.ReceivingInstanceCount}
	if intent.ReceivingInstanceCount < 0 || intent.ReceivingInstanceCount > storage.MaximumReceivingInstanceCount {
		return nil, storage.ErrTelemetryInput
	}
	return &intent, nil
}

type heartbeatWire struct {
	ProtocolVersion int            `json:"protocol_version"`
	Operation       Operation      `json:"operation"`
	RelayActor      string         `json:"relay_actor"`
	Telemetry       *telemetryWire `json:"telemetry,omitempty"`
}

func DecodeRegisterRequest(body []byte, maximumBytes int64) (RegisterRequest, error) {
	if maximumBytes <= 0 || maximumBytes > MaximumRegisterBodyBytes {
		return RegisterRequest{}, ErrRegisterConfiguration
	}
	if int64(len(body)) > maximumBytes {
		return RegisterRequest{}, ErrRegisterBodyTooLarge
	}
	var wire registerWire
	if err := decodeStrictJSON(body, &wire); err != nil {
		return RegisterRequest{}, ErrRegisterRequest
	}
	if wire.ProtocolVersion != Version {
		return RegisterRequest{}, ErrRegisterProtocolVersion
	}
	if wire.Operation != OperationRegister {
		return RegisterRequest{}, ErrRegisterRequest
	}
	identity, err := v1.NormalizeRelayIdentity(wire.RelayActor, wire.PublicBaseURL)
	if err != nil || identity.RelayActor != wire.RelayActor || identity.PublicBaseURL != wire.PublicBaseURL {
		return RegisterRequest{}, ErrRegisterRequest
	}
	profile, err := wire.Profile.normalized()
	if err != nil {
		return RegisterRequest{}, ErrRegisterRequest
	}
	telemetry, err := wire.Telemetry.normalized(wire.RelayActor)
	if err != nil {
		return RegisterRequest{}, ErrRegisterRequest
	}
	return RegisterRequest{
		ProtocolVersion: wire.ProtocolVersion,
		Operation:       wire.Operation,
		RelayActor:      wire.RelayActor,
		PublicBaseURL:   wire.PublicBaseURL,
		Profile:         profile,
		Telemetry:       telemetry,
	}, nil
}

func DecodeHeartbeatRequest(body []byte, maximumBytes int64) (HeartbeatRequest, error) {
	if maximumBytes <= 0 || maximumBytes > MaximumHeartbeatBodyBytes {
		return HeartbeatRequest{}, ErrHeartbeatConfiguration
	}
	if int64(len(body)) > maximumBytes {
		return HeartbeatRequest{}, ErrHeartbeatBodyTooLarge
	}
	var wire heartbeatWire
	if err := decodeStrictJSON(body, &wire); err != nil {
		return HeartbeatRequest{}, ErrHeartbeatRequest
	}
	if wire.ProtocolVersion != Version {
		return HeartbeatRequest{}, ErrHeartbeatProtocolVersion
	}
	if wire.Operation != OperationHeartbeat {
		return HeartbeatRequest{}, ErrHeartbeatRequest
	}
	actor, err := v1.NormalizeRelayActorURL(wire.RelayActor)
	if err != nil || actor != wire.RelayActor {
		return HeartbeatRequest{}, ErrHeartbeatRequest
	}
	telemetry, err := wire.Telemetry.normalized(wire.RelayActor)
	if err != nil {
		return HeartbeatRequest{}, ErrHeartbeatRequest
	}
	return HeartbeatRequest{ProtocolVersion: wire.ProtocolVersion, Operation: wire.Operation, RelayActor: wire.RelayActor, Telemetry: telemetry}, nil
}

func DecodeUnregisterRequest(body []byte, maximumBytes int64) (IdentityRequest, error) {
	return decodeIdentityRequest(body, maximumBytes, MaximumUnregisterBodyBytes, OperationUnregister,
		ErrUnregisterConfiguration, ErrUnregisterBodyTooLarge, ErrUnregisterProtocolVersion, ErrUnregisterRequest)
}

func decodeIdentityRequest(
	body []byte,
	maximumBytes, protocolMaximum int64,
	operation Operation,
	configurationErr, bodyTooLargeErr, versionErr, requestErr error,
) (IdentityRequest, error) {
	if maximumBytes <= 0 || maximumBytes > protocolMaximum {
		return IdentityRequest{}, configurationErr
	}
	if int64(len(body)) > maximumBytes {
		return IdentityRequest{}, bodyTooLargeErr
	}
	var request IdentityRequest
	if err := decodeStrictJSON(body, &request); err != nil {
		return IdentityRequest{}, requestErr
	}
	if request.ProtocolVersion != Version {
		return IdentityRequest{}, versionErr
	}
	if request.Operation != operation {
		return IdentityRequest{}, requestErr
	}
	actor, err := v1.NormalizeRelayActorURL(request.RelayActor)
	if err != nil || actor != request.RelayActor {
		return IdentityRequest{}, requestErr
	}
	return request, nil
}

func validOperationTarget(request *http.Request, endpointPath string) bool {
	return request != nil && request.URL != nil && request.Method == http.MethodPost &&
		request.URL.EscapedPath() == endpointPath && request.URL.RawQuery == "" &&
		!request.URL.ForceQuery && request.URL.Fragment == "" && request.URL.RawFragment == "" &&
		(request.RequestURI == "" || request.RequestURI == endpointPath)
}

func decodeStrictJSON(body []byte, destination any) error {
	if duplicateJSONName(body) {
		return errors.New("duplicate JSON name")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func duplicateJSONName(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var scan func() bool
	scan = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return true
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return false
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				name, ok := nameToken.(string)
				if err != nil || !ok {
					return true
				}
				if _, exists := seen[name]; exists {
					return true
				}
				seen[name] = struct{}{}
				if scan() {
					return true
				}
			}
			end, err := decoder.Token()
			return err != nil || end != json.Delim('}')
		case '[':
			for decoder.More() {
				if scan() {
					return true
				}
			}
			end, err := decoder.Token()
			return err != nil || end != json.Delim(']')
		default:
			return true
		}
	}
	if scan() {
		return true
	}
	_, err := decoder.Token()
	return !errors.Is(err, io.EOF)
}
