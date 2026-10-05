package storage

import (
	"context"
	"errors"
	"time"
)

const MaximumReceivingInstanceCount = 10_000_000

var ErrTelemetryInput = errors.New("relay telemetry input is invalid")

type RelayTelemetry struct {
	ReceivingInstanceCount int
	ReportedAtUnix         int64
}

type TelemetryIntent struct {
	RelayActor             string
	ReceivingInstanceCount int
}

type TelemetryRepository interface {
	ReplaceRelayTelemetry(context.Context, TelemetryIntent, time.Time) error
}

func ValidateTelemetryIntent(intent TelemetryIntent, reportedAt time.Time) error {
	if !ValidProfileRelayActor(intent.RelayActor) || intent.ReceivingInstanceCount < 0 ||
		intent.ReceivingInstanceCount > MaximumReceivingInstanceCount || reportedAt.UTC().Unix() < 0 {
		return ErrTelemetryInput
	}
	return nil
}
