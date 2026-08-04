package v1

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestClosedVocabulary(t *testing.T) {
	for _, operation := range []Operation{
		OperationRegister,
		OperationHeartbeat,
		OperationUnregister,
	} {
		if !operation.Valid() {
			t.Fatalf("operation %q is not valid", operation)
		}
	}

	for _, state := range []HealthState{
		HealthHealthy,
		HealthStale,
		HealthDead,
		HealthPrune,
	} {
		if !state.Valid() {
			t.Fatalf("health state %q is not valid", state)
		}
	}

	for _, state := range []AdministrativeState{
		AdministrativeActive,
		AdministrativeSuspended,
	} {
		if !state.Valid() {
			t.Fatalf("administrative state %q is not valid", state)
		}
	}

	if Operation("renew").Valid() {
		t.Fatal("unknown operation is valid")
	}
	if HealthState("offline").Valid() {
		t.Fatal("unknown health state is valid")
	}
	if AdministrativeState("deleted").Valid() {
		t.Fatal("unknown administrative state is valid")
	}

	for _, code := range []ErrorCode{
		ErrorInvalidRequest,
		ErrorUnsupportedProtocolVersion,
		ErrorAuthenticationFailed,
		ErrorReplayDetected,
		ErrorRegistrationUnavailable,
		ErrorRelaySuspended,
		ErrorRateLimited,
		ErrorInternal,
	} {
		if !code.Valid() {
			t.Fatalf("error code %q is not valid", code)
		}
	}
	if ErrorCode("not_found").Valid() {
		t.Fatal("unknown error code is valid")
	}
}

func TestFixtureManifest(t *testing.T) {
	expected := []string{
		"error-response.registration-unavailable.json",
		"heartbeat-request.valid.json",
		"heartbeat-response.recorded.json",
		"register-request.valid.json",
		"register-response.created.json",
		"register-response.unchanged.json",
		"register-response.updated.json",
		"unregister-request.valid.json",
		"unregister-response.absent.json",
		"unregister-response.removed.json",
	}

	entries, err := os.ReadDir(fixtureDirectory())
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}

	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected fixture subdirectory: %s", entry.Name())
		}
		actual = append(actual, entry.Name())
	}

	if !slices.Equal(actual, expected) {
		t.Fatalf("fixture manifest = %q, want %q", actual, expected)
	}
}

func TestRequestFixtures(t *testing.T) {
	register := decodeFixture[RegisterRequest](t, "register-request.valid.json")
	if register.ProtocolVersion != Version || register.Operation != OperationRegister {
		t.Fatalf("register fixture envelope = %#v", register)
	}
	if register.RelayActor == "" || register.PublicBaseURL == "" {
		t.Fatalf("register fixture identity = %#v", register)
	}

	for _, test := range []struct {
		name      string
		operation Operation
	}{
		{name: "heartbeat-request.valid.json", operation: OperationHeartbeat},
		{name: "unregister-request.valid.json", operation: OperationUnregister},
	} {
		request := decodeFixture[IdentityRequest](t, test.name)
		if request.ProtocolVersion != Version || request.Operation != test.operation {
			t.Fatalf("%s envelope = %#v", test.name, request)
		}
		if request.RelayActor == "" {
			t.Fatalf("%s has an empty relay_actor", test.name)
		}
	}
}

func TestResponseFixtures(t *testing.T) {
	for _, name := range []string{
		"register-response.created.json",
		"register-response.unchanged.json",
		"register-response.updated.json",
		"heartbeat-response.recorded.json",
		"unregister-response.absent.json",
		"unregister-response.removed.json",
	} {
		response := decodeFixture[OperationResponse](t, name)
		if response.ProtocolVersion != Version {
			t.Fatalf("%s protocol_version = %d", name, response.ProtocolVersion)
		}
		if !response.Operation.Valid() {
			t.Fatalf("%s operation = %q", name, response.Operation)
		}
		if !response.Outcome.ValidFor(response.Operation) {
			t.Fatalf(
				"%s outcome %q is invalid for %q",
				name,
				response.Outcome,
				response.Operation,
			)
		}
		if response.RelayActor == "" {
			t.Fatalf("%s has an empty relay_actor", name)
		}
	}
}

func TestErrorFixture(t *testing.T) {
	response := decodeFixture[ErrorResponse](
		t,
		"error-response.registration-unavailable.json",
	)
	if response.ProtocolVersion != Version {
		t.Fatalf("protocol_version = %d", response.ProtocolVersion)
	}
	if response.Error.Code != ErrorRegistrationUnavailable {
		t.Fatalf("error code = %q", response.Error.Code)
	}
	if !response.Error.Code.Valid() || response.Error.Message == "" {
		t.Fatalf("error document = %#v", response.Error)
	}
}

func TestOutcomeOperationBoundaries(t *testing.T) {
	for operation, outcomes := range map[Operation][]Outcome{
		OperationRegister:   {OutcomeCreated, OutcomeUpdated, OutcomeUnchanged},
		OperationHeartbeat:  {OutcomeRecorded},
		OperationUnregister: {OutcomeRemoved, OutcomeAbsent},
	} {
		for _, outcome := range outcomes {
			if !outcome.ValidFor(operation) {
				t.Fatalf("outcome %q is invalid for %q", outcome, operation)
			}
		}
	}

	if OutcomeRecorded.ValidFor(OperationRegister) {
		t.Fatal("heartbeat outcome is valid for register")
	}
	if OutcomeCreated.ValidFor(OperationUnregister) {
		t.Fatal("register outcome is valid for unregister")
	}
	if Outcome("accepted").ValidFor(OperationHeartbeat) {
		t.Fatal("unknown outcome is valid")
	}
}

func decodeFixture[T any](t *testing.T, name string) T {
	t.Helper()

	path := filepath.Join(fixtureDirectory(), name)
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var value T
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("%s contains trailing JSON", path)
	}

	return value
}

func fixtureDirectory() string {
	return filepath.Join("..", "..", "..", "testdata", "directory", "v1")
}
