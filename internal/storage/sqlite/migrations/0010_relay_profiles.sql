-- Activity-Relay Directory 1.3: source-scoped descriptive relay profiles.
-- Profile claims remain private persistence until the separately reviewed
-- public projection tranche exposes effective values.

CREATE TABLE relay_profile_values (
    relay_actor TEXT NOT NULL
        CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    source_kind TEXT NOT NULL
        CHECK (source_kind IN ('csv', 'relay', 'override')),
    field_name TEXT NOT NULL
        CHECK (field_name IN (
            'participation_mode',
            'availability',
            'relay_type',
            'languages',
            'countries',
            'regions',
            'topics',
            'contact_fediverse',
            'contact_email',
            'contact_url',
            'participation_url',
            'notes'
        )),
    value_json TEXT NOT NULL CHECK (
        length(CAST(value_json AS BLOB)) BETWEEN 2 AND 4096 AND
        json_valid(value_json) AND
        json_type(value_json) IN ('text', 'array')
    ),
    source_label TEXT CHECK (
        source_label IS NULL OR (
            length(CAST(source_label AS BLOB)) BETWEEN 1 AND 128 AND
            length(source_label) = length(CAST(source_label AS BLOB)) AND
            substr(source_label, 1, 1) GLOB '[A-Za-z0-9]' AND
            source_label NOT GLOB '*[^A-Za-z0-9@._:+-]*'
        )
    ),
    source_url TEXT CHECK (
        source_url IS NULL OR (
            length(CAST(source_url AS BLOB)) BETWEEN 1 AND 2048 AND
            substr(source_url, 1, 8) = 'https://'
        )
    ),
    accepted_at_unix INTEGER NOT NULL CHECK (accepted_at_unix >= 0),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    PRIMARY KEY (relay_actor, source_kind, field_name),
    CHECK (
        (field_name IN ('languages', 'countries', 'regions', 'topics') AND json_type(value_json) = 'array') OR
        (field_name NOT IN ('languages', 'countries', 'regions', 'topics') AND json_type(value_json) = 'text')
    ),
    CHECK (
        (source_kind = 'relay' AND source_label IS NULL AND source_url IS NULL) OR
        (source_kind = 'override' AND source_label IS NOT NULL AND source_url IS NULL) OR
        (source_kind = 'csv' AND source_label IS NOT NULL)
    )
) STRICT, WITHOUT ROWID;

CREATE INDEX relay_profile_values_effective_idx
    ON relay_profile_values (relay_actor, field_name, source_kind);

CREATE TABLE relay_profile_events (
    profile_event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    relay_actor TEXT NOT NULL
        CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    source_kind TEXT NOT NULL
        CHECK (source_kind IN ('csv', 'relay', 'override')),
    field_name TEXT NOT NULL
        CHECK (field_name IN (
            'participation_mode',
            'availability',
            'relay_type',
            'languages',
            'countries',
            'regions',
            'topics',
            'contact_fediverse',
            'contact_email',
            'contact_url',
            'participation_url',
            'notes'
        )),
    action TEXT NOT NULL CHECK (action IN ('set', 'clear')),
    value_json TEXT CHECK (
        value_json IS NULL OR (
            length(CAST(value_json AS BLOB)) BETWEEN 2 AND 4096 AND
            json_valid(value_json) AND
            json_type(value_json) IN ('text', 'array')
        )
    ),
    source_label TEXT CHECK (
        source_label IS NULL OR (
            length(CAST(source_label AS BLOB)) BETWEEN 1 AND 128 AND
            length(source_label) = length(CAST(source_label AS BLOB)) AND
            substr(source_label, 1, 1) GLOB '[A-Za-z0-9]' AND
            source_label NOT GLOB '*[^A-Za-z0-9@._:+-]*'
        )
    ),
    source_url TEXT CHECK (
        source_url IS NULL OR (
            length(CAST(source_url AS BLOB)) BETWEEN 1 AND 2048 AND
            substr(source_url, 1, 8) = 'https://'
        )
    ),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    recorded_at_unix INTEGER NOT NULL CHECK (recorded_at_unix >= 0),
    CHECK (
        (action = 'set' AND value_json IS NOT NULL) OR
        (action = 'clear' AND value_json IS NULL)
    ),
    CHECK (
        action = 'clear' OR
        (field_name IN ('languages', 'countries', 'regions', 'topics') AND json_type(value_json) = 'array') OR
        (field_name NOT IN ('languages', 'countries', 'regions', 'topics') AND json_type(value_json) = 'text')
    ),
    CHECK (
        (source_kind = 'relay' AND source_label IS NULL AND source_url IS NULL) OR
        (source_kind = 'override' AND source_label IS NOT NULL AND source_url IS NULL) OR
        (source_kind = 'csv' AND source_label IS NOT NULL)
    )
) STRICT;

CREATE INDEX relay_profile_events_actor_source_time_idx
    ON relay_profile_events (
        relay_actor,
        source_kind,
        recorded_at_unix,
        profile_event_id
    );

CREATE UNIQUE INDEX relay_profile_events_actor_source_field_revision_idx
    ON relay_profile_events (relay_actor, source_kind, field_name, revision);

CREATE TRIGGER relay_profile_events_no_update
BEFORE UPDATE ON relay_profile_events
BEGIN
    SELECT RAISE(ABORT, 'relay profile events are append-only');
END;

CREATE TRIGGER relay_profile_events_no_delete
BEFORE DELETE ON relay_profile_events
BEGIN
    SELECT RAISE(ABORT, 'relay profile events are append-only');
END;

CREATE TRIGGER relay_profile_values_identity_guard_insert
BEFORE INSERT ON relay_profile_values
WHEN NOT EXISTS (SELECT 1 FROM relays WHERE relay_actor = NEW.relay_actor)
 AND NOT EXISTS (SELECT 1 FROM relay_discoveries WHERE relay_actor = NEW.relay_actor)
BEGIN
    SELECT RAISE(ABORT, 'relay profile requires retained identity');
END;

CREATE TRIGGER relay_profile_values_identity_guard_actor_update
BEFORE UPDATE OF relay_actor ON relay_profile_values
WHEN NOT EXISTS (SELECT 1 FROM relays WHERE relay_actor = NEW.relay_actor)
 AND NOT EXISTS (SELECT 1 FROM relay_discoveries WHERE relay_actor = NEW.relay_actor)
BEGIN
    SELECT RAISE(ABORT, 'relay profile requires retained identity');
END;

-- Current assertions must not survive deletion of the last retained relay
-- identity. Private append-only profile history remains audit evidence, matching
-- the existing retention treatment of discovery/moderation history.
CREATE TRIGGER relay_profile_values_cleanup_after_relay_delete
AFTER DELETE ON relays
WHEN NOT EXISTS (SELECT 1 FROM relay_discoveries WHERE relay_actor = OLD.relay_actor)
BEGIN
    DELETE FROM relay_profile_values WHERE relay_actor = OLD.relay_actor;
END;

CREATE TRIGGER relay_profile_values_cleanup_after_discovery_delete
AFTER DELETE ON relay_discoveries
WHEN NOT EXISTS (SELECT 1 FROM relays WHERE relay_actor = OLD.relay_actor)
BEGIN
    DELETE FROM relay_profile_values WHERE relay_actor = OLD.relay_actor;
END;
