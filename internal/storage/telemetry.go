package storage

import (
	"context"
	"errors"
	"time"
)

const MaximumReceivingInstanceCount = 10_000_000
const MaximumParticipatingInstanceCount = 10_000_000

var ErrTelemetryInput = errors.New("relay telemetry input is invalid")

type RelayTelemetry struct {
	ReceivingInstanceCount      *int
	ReceivingReportedAtUnix     *int64
	ParticipatingInstanceCount  *int
	ParticipatingReportedAtUnix *int64
}

type TelemetryIntent struct {
	RelayActor             string
	ReceivingInstanceCount int
}

type ParticipatingTelemetryIntent struct {
	RelayActor                 string
	ParticipatingInstanceCount int
}

type TelemetryRepository interface {
	ReplaceRelayTelemetry(context.Context, TelemetryIntent, time.Time) error
	ReplaceRelayParticipatingTelemetry(context.Context, ParticipatingTelemetryIntent, time.Time) error
}

func ValidateTelemetryIntent(intent TelemetryIntent, reportedAt time.Time) error {
	if !ValidProfileRelayActor(intent.RelayActor) || intent.ReceivingInstanceCount < 0 ||
		intent.ReceivingInstanceCount > MaximumReceivingInstanceCount || reportedAt.UTC().Unix() < 0 {
		return ErrTelemetryInput
	}
	return nil
}

func ValidateParticipatingTelemetryIntent(intent ParticipatingTelemetryIntent, reportedAt time.Time) error {
	if !ValidProfileRelayActor(intent.RelayActor) || intent.ParticipatingInstanceCount < 0 ||
		intent.ParticipatingInstanceCount > MaximumParticipatingInstanceCount || reportedAt.UTC().Unix() < 0 {
		return ErrTelemetryInput
	}
	return nil
}
