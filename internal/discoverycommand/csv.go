package discoverycommand

import (
	"context"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"github.com/thystra/activity-relay-directory/internal/profilecsv"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

type InputFormat string

const (
	InputLines InputFormat = "lines"
	InputCSV   InputFormat = "csv"
)

func (format InputFormat) valid() bool {
	return format == InputLines || format == InputCSV
}

func normalizeInputFormat(format InputFormat) InputFormat {
	if format == "" {
		return InputLines
	}
	return format
}

type CSVProfileRow struct {
	Profile   storage.RelayProfile
	SourceURL string
}

// LoadCSVCandidates reads and fully parses a regular bounded import file before
// discovery probes or durable mutation. Duplicate relay hints that canonicalize
// to the same actor fail the whole CSV input.
func LoadCSVCandidates(path, sourceLabel string) ([]Candidate, error) {
	if path == "" || sourceLabel == "" {
		return nil, ErrImportFile
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaximumImportBytes {
		return nil, ErrImportFile
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrImportFile
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) ||
		openedInfo.Size() < 0 || openedInfo.Size() > MaximumImportBytes {
		return nil, ErrImportFile
	}
	body, err := io.ReadAll(io.LimitReader(file, MaximumImportBytes+1))
	if err != nil || len(body) > MaximumImportBytes || !utf8.Valid(body) {
		return nil, ErrImportFile
	}
	records, err := profilecsv.Decode(body, sourceLabel, MaximumImportCandidates)
	if err != nil {
		return nil, ErrImportFile
	}

	candidates := make([]Candidate, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if len(record.Relay) == 0 || len(record.Relay) > MaximumCandidateLineBytes {
			return nil, ErrImportFile
		}
		actor, err := candidateActorURL(record.Relay)
		if err != nil {
			return nil, ErrImportFile
		}
		if _, duplicate := seen[actor]; duplicate {
			return nil, ErrImportFile
		}
		seen[actor] = struct{}{}
		candidates = append(candidates, Candidate{
			Line: record.Line,
			URL:  record.Relay,
			CSV: &CSVProfileRow{
				Profile:   record.Profile,
				SourceURL: record.SourceURL,
			},
		})
	}
	return candidates, nil
}

func csvProfileRepository(
	request Request,
	plan Plan,
	repository Repository,
) (storage.ProfileRepository, error) {
	if normalizeInputFormat(request.InputFormat) != InputCSV ||
		len(plan.Ready)+len(plan.AlreadyKnown) == 0 {
		return nil, nil
	}
	profileRepository, ok := repository.(storage.ProfileRepository)
	if !ok {
		return nil, ErrPreparation
	}
	return profileRepository, nil
}

func applyCSVProfile(
	ctx context.Context,
	request Request,
	plan Plan,
	line int,
	relayActor string,
	profileRepository storage.ProfileRepository,
	acceptedAt time.Time,
) (*profileMutationResult, error) {
	if profileRepository == nil {
		return nil, nil
	}
	row, ok := plan.CSVRows[line]
	if !ok {
		return nil, ErrPreparation
	}
	summary, err := profileRepository.ReplaceProfileSource(ctx, storage.ProfileSourceIntent{
		RelayActor: relayActor,
		Source: storage.ProfileSource{
			Kind:        storage.ProfileSourceCSV,
			SourceLabel: request.SourceLabel,
			SourceURL:   row.SourceURL,
		},
		Profile: row.Profile,
	}, acceptedAt)
	if err != nil {
		return nil, err
	}
	return &profileMutationResult{
		Created: summary.Created, Updated: summary.Updated,
		Cleared: summary.Cleared, Unchanged: summary.Unchanged,
	}, nil
}
