-- Activity-Relay Directory 1.2: retain relay candidates that cannot yet be
-- verified as ActivityPub relay actors. These rows are deliberately separate
-- from relay_discoveries so failed actor validation never fabricates a verified
-- relay identity.

CREATE TABLE relay_discovery_candidates (
    candidate_actor_url TEXT NOT NULL PRIMARY KEY
        CHECK (length(CAST(candidate_actor_url AS BLOB)) BETWEEN 1 AND 4096),
    public_base_url TEXT NOT NULL
        CHECK (length(CAST(public_base_url AS BLOB)) BETWEEN 1 AND 2048),
    candidate_state TEXT NOT NULL
        CHECK (candidate_state IN ('unreachable', 'incompatible', 'resolved')),
    last_failure_code TEXT
        CHECK (
            last_failure_code IS NULL OR
            last_failure_code IN ('actor_unreachable', 'actor_invalid')
        ),
    first_seen_at_unix INTEGER NOT NULL CHECK (first_seen_at_unix >= 0),
    last_checked_at_unix INTEGER NOT NULL
        CHECK (last_checked_at_unix >= first_seen_at_unix),
    last_success_at_unix INTEGER
        CHECK (
            last_success_at_unix IS NULL OR
            last_success_at_unix BETWEEN first_seen_at_unix AND last_checked_at_unix
        ),
    failure_count INTEGER NOT NULL CHECK (failure_count >= 0),
    updated_at_unix INTEGER NOT NULL CHECK (updated_at_unix >= last_checked_at_unix),
    CHECK (
        (candidate_state = 'unreachable' AND
            last_failure_code = 'actor_unreachable' AND failure_count >= 1) OR
        (candidate_state = 'incompatible' AND
            last_failure_code = 'actor_invalid' AND failure_count >= 1) OR
        (candidate_state = 'resolved' AND
            last_failure_code IS NULL AND last_success_at_unix IS NOT NULL)
    )
) STRICT, WITHOUT ROWID;

CREATE INDEX relay_discovery_candidates_recheck_idx
    ON relay_discovery_candidates (
        candidate_state,
        last_checked_at_unix,
        candidate_actor_url
    )
    WHERE candidate_state != 'resolved';

CREATE TABLE discovery_candidate_events (
    discovery_candidate_event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    candidate_actor_url TEXT NOT NULL
        CHECK (length(CAST(candidate_actor_url AS BLOB)) BETWEEN 1 AND 4096),
    action TEXT NOT NULL
        CHECK (action IN ('candidate_added', 'candidate_updated', 'candidate_resolved')),
    candidate_state TEXT NOT NULL
        CHECK (candidate_state IN ('unreachable', 'incompatible', 'resolved')),
    failure_code TEXT
        CHECK (
            failure_code IS NULL OR
            failure_code IN ('actor_unreachable', 'actor_invalid')
        ),
    operator_id TEXT NOT NULL CHECK (
        length(CAST(operator_id AS BLOB)) BETWEEN 1 AND 128 AND
        length(operator_id) = length(CAST(operator_id AS BLOB)) AND
        substr(operator_id, 1, 1) GLOB '[A-Za-z0-9]' AND
        operator_id NOT GLOB '*[^A-Za-z0-9@._:+-]*'
    ),
    reason_code TEXT NOT NULL CHECK (
        length(CAST(reason_code AS BLOB)) BETWEEN 1 AND 64 AND
        length(reason_code) = length(CAST(reason_code AS BLOB)) AND
        substr(reason_code, 1, 1) GLOB '[a-z]' AND
        reason_code NOT GLOB '*[^a-z0-9_-]*'
    ),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('manual', 'file')),
    source_label TEXT CHECK (
        source_label IS NULL OR (
            length(CAST(source_label AS BLOB)) BETWEEN 1 AND 128 AND
            length(source_label) = length(CAST(source_label AS BLOB)) AND
            substr(source_label, 1, 1) GLOB '[A-Za-z0-9]' AND
            source_label NOT GLOB '*[^A-Za-z0-9@._:+-]*'
        )
    ),
    recorded_at_unix INTEGER NOT NULL CHECK (recorded_at_unix >= 0),
    CHECK (
        (candidate_state = 'resolved' AND failure_code IS NULL) OR
        (candidate_state = 'unreachable' AND failure_code = 'actor_unreachable') OR
        (candidate_state = 'incompatible' AND failure_code = 'actor_invalid')
    )
) STRICT;

CREATE INDEX discovery_candidate_events_actor_time_idx
    ON discovery_candidate_events (
        candidate_actor_url,
        recorded_at_unix,
        discovery_candidate_event_id
    );

CREATE TRIGGER discovery_candidate_events_no_update
BEFORE UPDATE ON discovery_candidate_events
BEGIN
    SELECT RAISE(ABORT, 'discovery candidate events are append-only');
END;

CREATE TRIGGER discovery_candidate_events_no_delete
BEFORE DELETE ON discovery_candidate_events
BEGIN
    SELECT RAISE(ABORT, 'discovery candidate events are append-only');
END;
