package storagecommand

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestParseStorageCommandsAndFormats(t *testing.T) {
	for _, test := range []struct {
		args []string
		want Request
	}{
		{[]string{"status"}, Request{Action: ActionStatus, Format: FormatHuman}},
		{[]string{"check", "--format", "json"}, Request{Action: ActionCheck, Format: FormatJSON}},
		{[]string{"test-alert", "--format=human"}, Request{Action: ActionTestAlert, Format: FormatHuman}},
	} {
		got, err := Parse(test.args)
		if err != nil || got != test.want {
			t.Fatalf("Parse(%#v) = (%#v, %v), want %#v", test.args, got, err, test.want)
		}
	}
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"status", "extra"},
		{"status", "--format", "yaml"},
		{"check", "--unknown"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%#v) unexpectedly succeeded", args)
		}
	}
}

func TestExitForSampleIsStatefulOnlyForCheck(t *testing.T) {
	states := []struct {
		state storage.DatabaseGrowthState
		want  int
	}{
		{storage.DatabaseGrowthNormal, ExitOK},
		{storage.DatabaseGrowthWarning, ExitWarning},
		{storage.DatabaseGrowthCritical, ExitCritical},
		{storage.DatabaseGrowthHard, ExitHard},
	}
	for _, test := range states {
		sample := storage.DatabaseGrowthSample{State: test.state}
		if got := ExitForSample(ActionCheck, sample); got != test.want {
			t.Fatalf("ExitForSample(check, %s) = %d, want %d", test.state, got, test.want)
		}
		if got := ExitForSample(ActionStatus, sample); got != ExitOK {
			t.Fatalf("ExitForSample(status, %s) = %d", test.state, got)
		}
		if got := ExitForSample(ActionTestAlert, sample); got != ExitOK {
			t.Fatalf("ExitForSample(test-alert, %s) = %d", test.state, got)
		}
	}
	if got := ExitForSample(ActionCheck, storage.DatabaseGrowthSample{State: "invalid"}); got != ExitOperational {
		t.Fatalf("ExitForSample(invalid) = %d", got)
	}
}

func TestRenderStorageJSONIsBoundedAndIdentityFree(t *testing.T) {
	sample := representativeSample()
	var output bytes.Buffer
	if err := Render(&output, Request{Action: ActionCheck, Format: FormatJSON}, sample); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if output.Len() > 4096 {
		t.Fatalf("JSON output length = %d", output.Len())
	}
	var doc map[string]any
	if err := json.Unmarshal(output.Bytes(), &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if doc["schema"] != "activity-relay-directory.storage-admin.v1" ||
		doc["kind"] != "check" || doc["state"] != "critical" ||
		doc["write_admission"] != "allowed" {
		t.Fatalf("document = %#v", doc)
	}
	for _, forbidden := range []string{
		"relay_actor", "moderator", "operator", "reason", "database_path", "sql",
	} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("JSON output contains forbidden token %q: %s", forbidden, output.String())
		}
	}
}

func TestRenderStorageHumanIncludesGrowthAndBlockedState(t *testing.T) {
	sample := representativeSample()
	sample.State = storage.DatabaseGrowthHard
	sample.WriteAllowed = false
	sample.GrowthKnown = true
	sample.GrowthBytes = -4096
	var output bytes.Buffer
	if err := Render(&output, Request{Action: ActionStatus, Format: FormatHuman}, sample); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := output.String()
	for _, required := range []string{
		"state: hard", "growth_since_previous_sample_bytes: -4096", "write_admission: blocked",
		"critical_percent: 90", "hard_percent: 100", "inactive_retention_days: 365",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("human output missing %q:\n%s", required, text)
		}
	}
}

func TestRenderRejectsInvalidInput(t *testing.T) {
	if err := Render(nil, Request{}, representativeSample()); err == nil {
		t.Fatal("Render(nil) unexpectedly succeeded")
	}
	var output bytes.Buffer
	if err := Render(&output, Request{}, storage.DatabaseGrowthSample{State: "bad"}); err == nil {
		t.Fatal("Render(invalid state) unexpectedly succeeded")
	}
}

func representativeSample() storage.DatabaseGrowthSample {
	return storage.DatabaseGrowthSample{
		ObservedUnix:          1000,
		State:                 storage.DatabaseGrowthCritical,
		PressurePercent:       91,
		PageSize:              4096,
		PageCount:             200,
		UsedPages:             180,
		ReusablePages:         20,
		MaxPageCount:          224000,
		UsedLogicalBytes:      737280,
		AllocatedLogicalBytes: 819200,
		MainPageLimitBytes:    917504000,
		PhysicalMainBytes:     819200,
		WALBytes:              65536,
		SHMBytes:              32768,
		PhysicalFamilyBytes:   917504,
		GrowthKnown:           false,
		MaxBytes:              storage.DefaultDatabaseMaxBytes,
		WarningPercent:        storage.DefaultDatabaseWarningPercent,
		RetentionDays:         365,
		WriteAllowed:          true,
	}
}
