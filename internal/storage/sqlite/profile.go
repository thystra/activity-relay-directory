package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.ProfileRepository = (*RelayRepository)(nil)

type profileValueRecord struct {
	valueJSON    string
	sourceLabel  string
	sourceURL    string
	acceptedUnix int64
	revision     int64
}

func (repository *RelayRepository) ReplaceProfileSource(
	ctx context.Context,
	intent storage.ProfileSourceIntent,
	acceptedAt time.Time,
) (storage.ProfileMutationSummary, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.ProfileMutationSummary{}, storage.ErrRepositoryConfiguration
	}
	if !storage.ValidProfileRelayActor(intent.RelayActor) {
		return storage.ProfileMutationSummary{}, storage.ErrProfileInput
	}
	source, err := storage.NormalizeProfileSource(intent.Source)
	if err != nil {
		return storage.ProfileMutationSummary{}, err
	}
	profile, err := storage.NormalizeRelayProfile(intent.Profile)
	if err != nil {
		return storage.ProfileMutationSummary{}, err
	}
	acceptedUnix := acceptedAt.UTC().Unix()
	if acceptedUnix < 0 {
		return storage.ProfileMutationSummary{}, storage.ErrProfileTime
	}

	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return storage.ProfileMutationSummary{}, err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()

	retained, err := profileIdentityRetained(ctx, transaction, intent.RelayActor)
	if err != nil {
		return storage.ProfileMutationSummary{}, storageFailure("read profile identity", err)
	}
	if !retained {
		return storage.ProfileMutationSummary{}, storage.ErrProfileAbsent
	}
	if err := requireProfileMonotonicTime(ctx, transaction, intent.RelayActor, source.Kind, acceptedUnix); err != nil {
		return storage.ProfileMutationSummary{}, err
	}

	current, err := readProfileSourceValues(ctx, transaction, intent.RelayActor, source.Kind)
	if err != nil {
		return storage.ProfileMutationSummary{}, err
	}
	historyRevisions, err := readProfileSourceRevisions(ctx, transaction, intent.RelayActor, source.Kind)
	if err != nil {
		return storage.ProfileMutationSummary{}, err
	}
	for field, existing := range current {
		if historyRevisions[field] != existing.revision {
			return storage.ProfileMutationSummary{}, storageFailure(
				"validate profile revision", errors.New("current profile revision does not match append-only history"),
			)
		}
	}
	result := storage.ProfileMutationSummary{}
	for _, field := range storage.ProfileFields() {
		valueJSON, present, err := encodeProfileField(profile, field)
		if err != nil {
			return storage.ProfileMutationSummary{}, storageFailure("encode profile value", err)
		}
		existing, exists := current[field]
		if !present {
			if !exists {
				result.Unchanged++
				continue
			}
			revision := historyRevisions[field] + 1
			if _, err := transaction.ExecContext(ctx,
				`DELETE FROM relay_profile_values
				 WHERE relay_actor = ? AND source_kind = ? AND field_name = ?`,
				intent.RelayActor, string(source.Kind), string(field)); err != nil {
				return storage.ProfileMutationSummary{}, storageFailure("clear profile value", err)
			}
			if err := insertProfileEvent(ctx, transaction, intent.RelayActor, source, field, "clear", "", revision, acceptedUnix); err != nil {
				return storage.ProfileMutationSummary{}, err
			}
			result.Cleared++
			continue
		}

		if len(valueJSON) > storage.MaximumProfileStoredValueBytes {
			return storage.ProfileMutationSummary{}, storage.ErrProfileInput
		}
		if exists && existing.valueJSON == valueJSON && existing.sourceLabel == source.SourceLabel && existing.sourceURL == source.SourceURL {
			result.Unchanged++
			continue
		}

		revision := historyRevisions[field] + 1
		if _, err := transaction.ExecContext(ctx,
			`INSERT INTO relay_profile_values (
			    relay_actor, source_kind, field_name, value_json,
			    source_label, source_url, accepted_at_unix, revision
			) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)
			ON CONFLICT(relay_actor, source_kind, field_name) DO UPDATE SET
			    value_json = excluded.value_json,
			    source_label = excluded.source_label,
			    source_url = excluded.source_url,
			    accepted_at_unix = excluded.accepted_at_unix,
			    revision = excluded.revision`,
			intent.RelayActor, string(source.Kind), string(field), valueJSON,
			source.SourceLabel, source.SourceURL, acceptedUnix, revision); err != nil {
			return storage.ProfileMutationSummary{}, storageFailure("write profile value", err)
		}
		if err := insertProfileEvent(ctx, transaction, intent.RelayActor, source, field, "set", valueJSON, revision, acceptedUnix); err != nil {
			return storage.ProfileMutationSummary{}, err
		}
		if exists {
			result.Updated++
		} else {
			result.Created++
		}
	}

	if err := transaction.Commit(); err != nil {
		return storage.ProfileMutationSummary{}, storageFailure("commit profile source", err)
	}
	return result, nil
}

func (repository *RelayRepository) EffectiveProfile(
	ctx context.Context,
	relayActor string,
) (storage.RelayProfile, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.RelayProfile{}, storage.ErrRepositoryConfiguration
	}
	if !storage.ValidProfileRelayActor(relayActor) {
		return storage.RelayProfile{}, storage.ErrProfileInput
	}
	var retained int
	if err := repository.database.QueryRowContext(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM relays WHERE relay_actor = ?
		    UNION ALL
		    SELECT 1 FROM relay_discoveries WHERE relay_actor = ?
		    LIMIT 1
		)`, relayActor, relayActor).Scan(&retained); err != nil {
		return storage.RelayProfile{}, storageFailure("read profile identity", err)
	}
	if retained == 0 {
		return storage.RelayProfile{}, storage.ErrProfileAbsent
	}

	profiles, err := repository.readEffectiveProfiles(ctx, []string{relayActor})
	if err != nil {
		return storage.RelayProfile{}, err
	}
	return profiles[relayActor], nil
}

// readEffectiveProfiles resolves the same field-by-field precedence as
// EffectiveProfile for a bounded caller-supplied actor set. Callers retain
// responsibility for their own batch-size bound.
func (repository *RelayRepository) readEffectiveProfiles(
	ctx context.Context,
	relayActors []string,
) (map[string]storage.RelayProfile, error) {
	result := make(map[string]storage.RelayProfile, len(relayActors))
	if len(relayActors) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(relayActors))
	arguments := make([]any, len(relayActors))
	requested := make(map[string]struct{}, len(relayActors))
	for index, relayActor := range relayActors {
		if !storage.ValidProfileRelayActor(relayActor) {
			return nil, storage.ErrProfileInput
		}
		placeholders[index] = "?"
		arguments[index] = relayActor
		requested[relayActor] = struct{}{}
	}

	rows, err := repository.database.QueryContext(ctx,
		`SELECT relay_actor, field_name, source_kind, value_json
		 FROM relay_profile_values
		 WHERE relay_actor IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY relay_actor,
		          field_name,
		          CASE source_kind
		              WHEN 'override' THEN 3
		              WHEN 'relay' THEN 2
		              WHEN 'csv' THEN 1
		              ELSE 0
		          END DESC`, arguments...)
	if err != nil {
		return nil, storageFailure("read effective profile", err)
	}
	defer rows.Close()

	seen := make(map[string]map[storage.ProfileField]struct{}, len(relayActors))
	for rows.Next() {
		var relayActor, fieldRaw, sourceRaw, valueJSON string
		if err := rows.Scan(&relayActor, &fieldRaw, &sourceRaw, &valueJSON); err != nil {
			return nil, storageFailure("decode effective profile", err)
		}
		if _, exists := requested[relayActor]; !exists {
			return nil, storageFailure("validate effective profile", errors.New("unexpected stored profile actor"))
		}
		field := storage.ProfileField(fieldRaw)
		source := storage.ProfileSourceKind(sourceRaw)
		if !field.Valid() || !source.Valid() || storage.ProfileSourcePriority(source) == 0 {
			return nil, storageFailure("validate effective profile", errors.New("invalid stored profile vocabulary"))
		}
		actorSeen := seen[relayActor]
		if actorSeen == nil {
			actorSeen = make(map[storage.ProfileField]struct{})
			seen[relayActor] = actorSeen
		}
		if _, exists := actorSeen[field]; exists {
			continue
		}
		profile := result[relayActor]
		if err := decodeProfileField(&profile, field, valueJSON); err != nil {
			return nil, storageFailure("decode effective profile value", err)
		}
		result[relayActor] = profile
		actorSeen[field] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate effective profile", err)
	}
	for _, relayActor := range relayActors {
		profile, err := storage.NormalizeRelayProfile(result[relayActor])
		if err != nil {
			return nil, storageFailure("normalize effective profile", err)
		}
		result[relayActor] = profile
	}
	return result, nil
}

func profileIdentityRetained(ctx context.Context, transaction *sql.Tx, relayActor string) (bool, error) {
	var retained int
	if err := transaction.QueryRowContext(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM relays WHERE relay_actor = ?
		    UNION ALL
		    SELECT 1 FROM relay_discoveries WHERE relay_actor = ?
		    LIMIT 1
		)`, relayActor, relayActor).Scan(&retained); err != nil {
		return false, err
	}
	return retained == 1, nil
}

func requireProfileMonotonicTime(
	ctx context.Context,
	transaction *sql.Tx,
	relayActor string,
	source storage.ProfileSourceKind,
	acceptedUnix int64,
) error {
	var latest sql.NullInt64
	if err := transaction.QueryRowContext(ctx,
		`SELECT MAX(recorded_at_unix)
		 FROM relay_profile_events
		 WHERE relay_actor = ? AND source_kind = ?`, relayActor, string(source)).Scan(&latest); err != nil {
		return storageFailure("read profile event time", err)
	}
	if latest.Valid && acceptedUnix < latest.Int64 {
		return storage.ErrProfileTime
	}
	return nil
}

func readProfileSourceValues(
	ctx context.Context,
	transaction *sql.Tx,
	relayActor string,
	source storage.ProfileSourceKind,
) (map[storage.ProfileField]profileValueRecord, error) {
	rows, err := transaction.QueryContext(ctx,
		`SELECT field_name, value_json, COALESCE(source_label, ''),
		        COALESCE(source_url, ''), accepted_at_unix, revision
		 FROM relay_profile_values
		 WHERE relay_actor = ? AND source_kind = ?`, relayActor, string(source))
	if err != nil {
		return nil, storageFailure("read profile source", err)
	}
	defer rows.Close()
	values := make(map[storage.ProfileField]profileValueRecord)
	for rows.Next() {
		var fieldRaw string
		var value profileValueRecord
		if err := rows.Scan(&fieldRaw, &value.valueJSON, &value.sourceLabel, &value.sourceURL, &value.acceptedUnix, &value.revision); err != nil {
			return nil, storageFailure("decode profile source", err)
		}
		field := storage.ProfileField(fieldRaw)
		if !field.Valid() || value.revision < 1 {
			return nil, storageFailure("validate profile source", errors.New("invalid stored profile value"))
		}
		values[field] = value
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate profile source", err)
	}
	return values, nil
}

func readProfileSourceRevisions(
	ctx context.Context,
	transaction *sql.Tx,
	relayActor string,
	source storage.ProfileSourceKind,
) (map[storage.ProfileField]int64, error) {
	rows, err := transaction.QueryContext(ctx,
		`SELECT field_name, MAX(revision)
		 FROM relay_profile_events
		 WHERE relay_actor = ? AND source_kind = ?
		 GROUP BY field_name`, relayActor, string(source))
	if err != nil {
		return nil, storageFailure("read profile revisions", err)
	}
	defer rows.Close()
	revisions := make(map[storage.ProfileField]int64)
	for rows.Next() {
		var fieldRaw string
		var revision int64
		if err := rows.Scan(&fieldRaw, &revision); err != nil {
			return nil, storageFailure("decode profile revision", err)
		}
		field := storage.ProfileField(fieldRaw)
		if !field.Valid() || revision < 1 {
			return nil, storageFailure("validate profile revision", errors.New("invalid stored profile revision"))
		}
		revisions[field] = revision
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate profile revisions", err)
	}
	return revisions, nil
}

func insertProfileEvent(
	ctx context.Context,
	transaction *sql.Tx,
	relayActor string,
	source storage.ProfileSource,
	field storage.ProfileField,
	action string,
	valueJSON string,
	revision int64,
	recordedUnix int64,
) error {
	var value any
	if action == "set" {
		value = valueJSON
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO relay_profile_events (
		    relay_actor, source_kind, field_name, action, value_json,
		    source_label, source_url, revision, recorded_at_unix
		) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		relayActor, string(source.Kind), string(field), action, value,
		source.SourceLabel, source.SourceURL, revision, recordedUnix); err != nil {
		return storageFailure("record profile event", err)
	}
	return nil
}

func encodeProfileField(profile storage.RelayProfile, field storage.ProfileField) (string, bool, error) {
	var value any
	switch field {
	case storage.ProfileFieldParticipationMode:
		value = profile.ParticipationMode
	case storage.ProfileFieldAvailability:
		value = profile.Availability
	case storage.ProfileFieldRelayType:
		value = profile.RelayType
	case storage.ProfileFieldLanguages:
		if len(profile.Languages) > 0 {
			value = profile.Languages
		}
	case storage.ProfileFieldCountries:
		if len(profile.Countries) > 0 {
			value = profile.Countries
		}
	case storage.ProfileFieldRegions:
		if len(profile.Regions) > 0 {
			value = profile.Regions
		}
	case storage.ProfileFieldTopics:
		if len(profile.Topics) > 0 {
			value = profile.Topics
		}
	case storage.ProfileFieldContactFediverse:
		value = profile.ContactFediverse
	case storage.ProfileFieldContactEmail:
		value = profile.ContactEmail
	case storage.ProfileFieldContactURL:
		value = profile.ContactURL
	case storage.ProfileFieldParticipationURL:
		value = profile.ParticipationURL
	case storage.ProfileFieldNotes:
		value = profile.Notes
	default:
		return "", false, storage.ErrProfileInput
	}
	if text, ok := value.(string); ok && text == "" {
		return "", false, nil
	}
	if value == nil {
		return "", false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false, err
	}
	return string(encoded), true, nil
}

func decodeProfileField(profile *storage.RelayProfile, field storage.ProfileField, valueJSON string) error {
	if profile == nil || valueJSON == "" || len(valueJSON) > storage.MaximumProfileStoredValueBytes {
		return errors.New("invalid profile value")
	}
	if field.MultiValue() {
		var values []string
		if err := json.Unmarshal([]byte(valueJSON), &values); err != nil || len(values) == 0 {
			return errors.New("invalid profile list")
		}
		switch field {
		case storage.ProfileFieldLanguages:
			profile.Languages = values
		case storage.ProfileFieldCountries:
			profile.Countries = values
		case storage.ProfileFieldRegions:
			profile.Regions = values
		case storage.ProfileFieldTopics:
			profile.Topics = values
		default:
			return errors.New("invalid profile list field")
		}
		return nil
	}

	var value string
	if err := json.Unmarshal([]byte(valueJSON), &value); err != nil || value == "" {
		return errors.New("invalid profile scalar")
	}
	switch field {
	case storage.ProfileFieldParticipationMode:
		profile.ParticipationMode = value
	case storage.ProfileFieldAvailability:
		profile.Availability = value
	case storage.ProfileFieldRelayType:
		profile.RelayType = value
	case storage.ProfileFieldContactFediverse:
		profile.ContactFediverse = value
	case storage.ProfileFieldContactEmail:
		profile.ContactEmail = value
	case storage.ProfileFieldContactURL:
		profile.ContactURL = value
	case storage.ProfileFieldParticipationURL:
		profile.ParticipationURL = value
	case storage.ProfileFieldNotes:
		profile.Notes = value
	default:
		return fmt.Errorf("invalid profile scalar field %q", field)
	}
	return nil
}

// ProfileSourceProfile returns the currently retained assertion set for one
// source kind without resolving higher-priority sources. It is used by local
// administrative preflight so CSV imports can show a read-only field delta
// before operator confirmation.
func (repository *RelayRepository) ProfileSourceProfile(
	ctx context.Context,
	relayActor string,
	kind storage.ProfileSourceKind,
) (storage.RelayProfile, error) {
	if repository == nil || repository.database == nil || ctx == nil ||
		!storage.ValidProfileRelayActor(relayActor) || !kind.Valid() {
		return storage.RelayProfile{}, storage.ErrProfileInput
	}
	rows, err := repository.database.QueryContext(ctx,
		`SELECT field_name, value_json
		 FROM relay_profile_values
		 WHERE relay_actor = ? AND source_kind = ?
		 ORDER BY field_name`, relayActor, string(kind))
	if err != nil {
		return storage.RelayProfile{}, storageFailure("read profile source preview", err)
	}
	defer rows.Close()
	var profile storage.RelayProfile
	for rows.Next() {
		var fieldRaw, valueJSON string
		if err := rows.Scan(&fieldRaw, &valueJSON); err != nil {
			return storage.RelayProfile{}, storageFailure("decode profile source preview", err)
		}
		field := storage.ProfileField(fieldRaw)
		if !field.Valid() {
			return storage.RelayProfile{}, storageFailure("validate profile source preview", errors.New("invalid stored profile field"))
		}
		if err := decodeProfileField(&profile, field, valueJSON); err != nil {
			return storage.RelayProfile{}, storageFailure("decode profile source preview value", err)
		}
	}
	if err := rows.Err(); err != nil {
		return storage.RelayProfile{}, storageFailure("iterate profile source preview", err)
	}
	return storage.NormalizeRelayProfile(profile)
}
