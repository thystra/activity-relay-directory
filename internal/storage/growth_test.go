package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDatabaseGrowthContractDefaults(t *testing.T) {
	if DefaultDatabaseMaxBytes != 1<<30 || MaximumDatabaseMaxBytes != 1<<40 {
		t.Fatalf("database byte bounds = %d/%d", DefaultDatabaseMaxBytes, MaximumDatabaseMaxBytes)
	}
	if DefaultDatabaseWarningPercent != 75 || DatabaseCriticalPercent != 90 || DatabaseHardPercent != 100 {
		t.Fatalf("database percentages = %d/%d/%d", DefaultDatabaseWarningPercent, DatabaseCriticalPercent, DatabaseHardPercent)
	}
	if DatabaseGrowthSampleInterval != 5*time.Minute || DatabaseGrowthReminderInterval != 24*time.Hour {
		t.Fatalf("database intervals = %s/%s", DatabaseGrowthSampleInterval, DatabaseGrowthReminderInterval)
	}
	if DatabaseGrowthRecoveryHysteresisPercent != 5 || MaximumDatabaseGrowthHeadroomBytes != 128<<20 {
		t.Fatalf("database recovery/headroom = %d/%d", DatabaseGrowthRecoveryHysteresisPercent, MaximumDatabaseGrowthHeadroomBytes)
	}
}

func TestDatabaseGrowthClosedVocabularies(t *testing.T) {
	for _, state := range []DatabaseGrowthState{
		DatabaseGrowthNormal, DatabaseGrowthWarning, DatabaseGrowthCritical, DatabaseGrowthHard,
	} {
		if !state.Valid() {
			t.Fatalf("state %q is invalid", state)
		}
	}
	if DatabaseGrowthState("full").Valid() {
		t.Fatal("unexpected growth state accepted")
	}
	for _, kind := range []DatabaseGrowthAlertKind{
		DatabaseGrowthAlertWarning,
		DatabaseGrowthAlertCritical,
		DatabaseGrowthAlertHard,
		DatabaseGrowthAlertRecovered,
		DatabaseGrowthAlertTest,
	} {
		if !kind.Valid() {
			t.Fatalf("alert kind %q is invalid", kind)
		}
	}
	if DatabaseGrowthAlertKind("relay-specific").Valid() {
		t.Fatal("unexpected alert kind accepted")
	}
}

func TestAllowAndDenyWriteAdmissions(t *testing.T) {
	lease, err := AllowWrites.AcquireWrite(context.Background())
	if err != nil || lease == nil {
		t.Fatalf("AllowWrites.AcquireWrite() = (%v, %v)", lease, err)
	}
	lease.Release()
	lease.Release()

	if _, err := DenyWrites.AcquireWrite(context.Background()); !errors.Is(err, ErrWriteAdmissionDenied) {
		t.Fatalf("DenyWrites error = %v", err)
	}
	if _, err := AllowWrites.AcquireWrite(nil); !errors.Is(err, ErrRepositoryConfiguration) {
		t.Fatalf("AllowWrites(nil) error = %v", err)
	}
	if _, err := DenyWrites.AcquireWrite(nil); !errors.Is(err, ErrRepositoryConfiguration) {
		t.Fatalf("DenyWrites(nil) error = %v", err)
	}
}
