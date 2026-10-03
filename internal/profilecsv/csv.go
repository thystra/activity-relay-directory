// Package profilecsv implements the bounded spreadsheet exchange format for
// relay descriptive profiles. It contains no discovery, network, or storage
// mutation authority.
package profilecsv

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

var ErrCSV = errors.New("relay profile CSV is invalid")

var importHeaders = []string{
	"relay",
	"participation_mode",
	"availability",
	"relay_type",
	"languages",
	"countries",
	"regions",
	"topics",
	"contact_fediverse",
	"contact_email",
	"contact_url",
	"participation_url",
	"notes",
	"source_url",
}

var exportHeaders = importHeaders[:len(importHeaders)-1]

type Record struct {
	Line      int
	Relay     string
	Profile   storage.RelayProfile
	SourceURL string
}

// Decode parses a complete already-bounded UTF-8 CSV document. Header order may
// vary, relay is required, and unknown or duplicate headers fail closed.
func Decode(body []byte, sourceLabel string, maximumRecords int) ([]Record, error) {
	if len(body) == 0 || !utf8.Valid(body) || maximumRecords <= 0 ||
		!storage.ValidDiscoverySourceLabel(sourceLabel) || sourceLabel == "" {
		return nil, ErrCSV
	}
	reader := csv.NewReader(bytes.NewReader(body))
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false

	header, err := reader.Read()
	if err != nil || len(header) == 0 || len(header) > len(importHeaders) {
		return nil, ErrCSV
	}
	indexes := make(map[string]int, len(header))
	allowed := make(map[string]struct{}, len(importHeaders))
	for _, name := range importHeaders {
		allowed[name] = struct{}{}
	}
	for index, raw := range header {
		name := strings.TrimSpace(raw)
		if _, ok := allowed[name]; !ok || name == "" {
			return nil, ErrCSV
		}
		if _, duplicate := indexes[name]; duplicate {
			return nil, ErrCSV
		}
		indexes[name] = index
	}
	if _, ok := indexes["relay"]; !ok {
		return nil, ErrCSV
	}

	records := make([]Record, 0)
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(row) != len(header) || len(records) >= maximumRecords {
			return nil, ErrCSV
		}
		line, _ := reader.FieldPos(0)
		value := func(name string) string {
			index, ok := indexes[name]
			if !ok {
				return ""
			}
			return strings.TrimSpace(unprotectCell(row[index]))
		}

		record := Record{
			Line:  line,
			Relay: value("relay"),
			Profile: storage.RelayProfile{
				ParticipationMode: value("participation_mode"),
				Availability:      value("availability"),
				RelayType:         value("relay_type"),
				ContactFediverse:  value("contact_fediverse"),
				ContactEmail:      value("contact_email"),
				ContactURL:        value("contact_url"),
				ParticipationURL:  value("participation_url"),
				Notes:             value("notes"),
			},
			SourceURL: value("source_url"),
		}
		if record.Relay == "" {
			return nil, ErrCSV
		}
		var listErr error
		if record.Profile.Languages, listErr = decodeList(value("languages")); listErr != nil {
			return nil, listErr
		}
		if record.Profile.Countries, listErr = decodeList(value("countries")); listErr != nil {
			return nil, listErr
		}
		if record.Profile.Regions, listErr = decodeList(value("regions")); listErr != nil {
			return nil, listErr
		}
		if record.Profile.Topics, listErr = decodeList(value("topics")); listErr != nil {
			return nil, listErr
		}
		normalized, err := storage.NormalizeRelayProfile(record.Profile)
		if err != nil {
			return nil, ErrCSV
		}
		source, err := storage.NormalizeProfileSource(storage.ProfileSource{
			Kind:        storage.ProfileSourceCSV,
			SourceLabel: sourceLabel,
			SourceURL:   record.SourceURL,
		})
		if err != nil {
			return nil, ErrCSV
		}
		record.Profile = normalized
		record.SourceURL = source.SourceURL
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, ErrCSV
	}
	return records, nil
}

// Encode renders deterministic operator CSV without private provenance.
// Formula-looking cells receive a reversible leading-apostrophe escape before
// normal CSV quoting so opening the file in a spreadsheet does not execute them.
func Encode(records []Record) ([]byte, error) {
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	if err := writer.Write(exportHeaders); err != nil {
		return nil, err
	}
	for _, record := range records {
		if !storage.ValidProfileRelayActor(record.Relay) {
			return nil, ErrCSV
		}
		profile, err := storage.NormalizeRelayProfile(record.Profile)
		if err != nil {
			return nil, ErrCSV
		}
		row := []string{
			record.Relay,
			profile.ParticipationMode,
			profile.Availability,
			profile.RelayType,
			encodeList(profile.Languages),
			encodeList(profile.Countries),
			encodeList(profile.Regions),
			encodeList(profile.Topics),
			profile.ContactFediverse,
			profile.ContactEmail,
			profile.ContactURL,
			profile.ParticipationURL,
			profile.Notes,
		}
		for index := range row {
			row[index] = protectCell(row[index])
		}
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeList(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	parts := make([]string, 0, strings.Count(value, ";")+1)
	var item strings.Builder
	escaped := false
	appendItem := func() error {
		normalized := strings.TrimSpace(item.String())
		if normalized == "" {
			return ErrCSV
		}
		parts = append(parts, normalized)
		item.Reset()
		return nil
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if escaped {
			if character != ';' && character != '\\' {
				return nil, ErrCSV
			}
			item.WriteByte(character)
			escaped = false
			continue
		}
		switch character {
		case '\\':
			escaped = true
		case ';':
			if err := appendItem(); err != nil {
				return nil, err
			}
		default:
			item.WriteByte(character)
		}
	}
	if escaped {
		return nil, ErrCSV
	}
	if err := appendItem(); err != nil {
		return nil, err
	}
	profile := storage.RelayProfile{Topics: parts}
	normalized, err := storage.NormalizeRelayProfile(profile)
	if err != nil {
		return nil, ErrCSV
	}
	return normalized.Topics, nil
}

func encodeList(values []string) string {
	escaped := make([]string, len(values))
	for index, value := range values {
		value = strings.ReplaceAll(value, "\\", "\\\\")
		escaped[index] = strings.ReplaceAll(value, ";", "\\;")
	}
	return strings.Join(escaped, ";")
}

func protectCell(value string) string {
	if value == "" {
		return ""
	}
	if value[0] == '\'' || formulaTrigger(value[0]) {
		return "'" + value
	}
	return value
}

func unprotectCell(value string) string {
	if len(value) >= 2 && value[0] == '\'' {
		if value[1] == '\'' || formulaTrigger(value[1]) {
			return value[1:]
		}
	}
	return value
}

func formulaTrigger(character byte) bool {
	switch character {
	case '=', '+', '-', '@':
		return true
	default:
		return false
	}
}
