package storagecommand

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	ExitOK          = 0
	ExitOperational = 1
	ExitUsage       = 2
	ExitWarning     = 3
	ExitCritical    = 4
	ExitHard        = 5
)

type Action string

const (
	ActionStatus    Action = "status"
	ActionCheck     Action = "check"
	ActionTestAlert Action = "test-alert"
)

type Format string

const (
	FormatHuman Format = "human"
	FormatJSON  Format = "json"
)

type Request struct {
	Action Action
	Format Format
}

func Parse(arguments []string) (Request, error) {
	if len(arguments) < 1 {
		return Request{}, errors.New("storage action is required")
	}
	action := Action(arguments[0])
	switch action {
	case ActionStatus, ActionCheck, ActionTestAlert:
	default:
		return Request{}, errors.New("storage action is invalid")
	}
	flags := flag.NewFlagSet("admin storage "+string(action), flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	format := flags.String("format", string(FormatHuman), "human or json")
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return Request{}, errors.New("storage arguments are invalid")
	}
	request := Request{Action: action, Format: Format(*format)}
	if request.Format != FormatHuman && request.Format != FormatJSON {
		return Request{}, errors.New("storage format is invalid")
	}
	return request, nil
}

func ExitForSample(action Action, sample storage.DatabaseGrowthSample) int {
	if action != ActionCheck {
		return ExitOK
	}
	switch sample.State {
	case storage.DatabaseGrowthNormal:
		return ExitOK
	case storage.DatabaseGrowthWarning:
		return ExitWarning
	case storage.DatabaseGrowthCritical:
		return ExitCritical
	case storage.DatabaseGrowthHard:
		return ExitHard
	default:
		return ExitOperational
	}
}

type document struct {
	Schema                string                      `json:"schema"`
	Kind                  string                      `json:"kind"`
	State                 storage.DatabaseGrowthState `json:"state"`
	ObservedAtUnix        int64                       `json:"observed_at_unix"`
	PressurePercent       int                         `json:"pressure_percent"`
	PageSizeBytes         int64                       `json:"page_size_bytes"`
	PageCount             int64                       `json:"page_count"`
	UsedPages             int64                       `json:"used_pages"`
	ReusablePages         int64                       `json:"reusable_pages"`
	MaxPageCount          int64                       `json:"max_page_count"`
	UsedLogicalBytes      int64                       `json:"used_logical_bytes"`
	AllocatedLogicalBytes int64                       `json:"allocated_logical_bytes"`
	MainPageLimitBytes    int64                       `json:"main_page_limit_bytes"`
	PhysicalMainBytes     int64                       `json:"physical_main_bytes"`
	WALBytes              int64                       `json:"wal_bytes"`
	SHMBytes              int64                       `json:"shm_bytes"`
	PhysicalFamilyBytes   int64                       `json:"physical_database_family_bytes"`
	GrowthBytes           *int64                      `json:"growth_since_previous_sample_bytes,omitempty"`
	ConfiguredMaxBytes    int64                       `json:"configured_max_bytes"`
	WarningPercent        int                         `json:"warning_percent"`
	CriticalPercent       int                         `json:"critical_percent"`
	HardPercent           int                         `json:"hard_percent"`
	InactiveRetentionDays int                         `json:"inactive_retention_days"`
	WriteAdmission        string                      `json:"write_admission"`
}

func Render(
	output io.Writer,
	request Request,
	sample storage.DatabaseGrowthSample,
) error {
	if output == nil || !sample.State.Valid() {
		return errors.New("storage output is invalid")
	}
	writeAdmission := "allowed"
	if !sample.WriteAllowed {
		writeAdmission = "blocked"
	}
	if request.Format == FormatJSON {
		doc := document{
			Schema:                "activity-relay-directory.storage-admin.v1",
			Kind:                  string(request.Action),
			State:                 sample.State,
			ObservedAtUnix:        sample.ObservedUnix,
			PressurePercent:       sample.PressurePercent,
			PageSizeBytes:         sample.PageSize,
			PageCount:             sample.PageCount,
			UsedPages:             sample.UsedPages,
			ReusablePages:         sample.ReusablePages,
			MaxPageCount:          sample.MaxPageCount,
			UsedLogicalBytes:      sample.UsedLogicalBytes,
			AllocatedLogicalBytes: sample.AllocatedLogicalBytes,
			MainPageLimitBytes:    sample.MainPageLimitBytes,
			PhysicalMainBytes:     sample.PhysicalMainBytes,
			WALBytes:              sample.WALBytes,
			SHMBytes:              sample.SHMBytes,
			PhysicalFamilyBytes:   sample.PhysicalFamilyBytes,
			ConfiguredMaxBytes:    sample.MaxBytes,
			WarningPercent:        sample.WarningPercent,
			CriticalPercent:       storage.DatabaseCriticalPercent,
			HardPercent:           storage.DatabaseHardPercent,
			InactiveRetentionDays: sample.RetentionDays,
			WriteAdmission:        writeAdmission,
		}
		if sample.GrowthKnown {
			value := sample.GrowthBytes
			doc.GrowthBytes = &value
		}
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(true)
		return encoder.Encode(doc)
	}

	growth := "unknown"
	if sample.GrowthKnown {
		growth = fmt.Sprintf("%d", sample.GrowthBytes)
	}
	_, err := fmt.Fprintf(
		output,
		"state: %s\n"+
			"observed_at_unix: %d\n"+
			"pressure_percent: %d\n"+
			"page_size_bytes: %d\n"+
			"page_count: %d\n"+
			"used_pages: %d\n"+
			"reusable_pages: %d\n"+
			"max_page_count: %d\n"+
			"used_logical_bytes: %d\n"+
			"allocated_logical_bytes: %d\n"+
			"main_page_limit_bytes: %d\n"+
			"physical_main_bytes: %d\n"+
			"wal_bytes: %d\n"+
			"shm_bytes: %d\n"+
			"physical_database_family_bytes: %d\n"+
			"growth_since_previous_sample_bytes: %s\n"+
			"configured_max_bytes: %d\n"+
			"warning_percent: %d\n"+
			"critical_percent: %d\n"+
			"hard_percent: %d\n"+
			"inactive_retention_days: %d\n"+
			"write_admission: %s\n",
		sample.State,
		sample.ObservedUnix,
		sample.PressurePercent,
		sample.PageSize,
		sample.PageCount,
		sample.UsedPages,
		sample.ReusablePages,
		sample.MaxPageCount,
		sample.UsedLogicalBytes,
		sample.AllocatedLogicalBytes,
		sample.MainPageLimitBytes,
		sample.PhysicalMainBytes,
		sample.WALBytes,
		sample.SHMBytes,
		sample.PhysicalFamilyBytes,
		growth,
		sample.MaxBytes,
		sample.WarningPercent,
		storage.DatabaseCriticalPercent,
		storage.DatabaseHardPercent,
		sample.RetentionDays,
		writeAdmission,
	)
	return err
}
