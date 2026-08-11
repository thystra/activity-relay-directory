-- Roadmap Tranche 17: bounded database-growth alert state.
CREATE TABLE storage_growth_state (
    singleton_id INTEGER NOT NULL PRIMARY KEY CHECK (singleton_id = 1),
    state TEXT NOT NULL CHECK (state IN ('normal', 'warning', 'critical', 'hard')),
    sampled_at_unix INTEGER NOT NULL CHECK (sampled_at_unix >= 0),
    physical_bytes INTEGER NOT NULL CHECK (physical_bytes >= 0),
    transition_at_unix INTEGER NOT NULL CHECK (transition_at_unix >= 0),
    last_email_kind TEXT CHECK (
        last_email_kind IS NULL OR
        last_email_kind IN ('warning', 'critical', 'hard-limit', 'recovered')
    ),
    last_email_at_unix INTEGER CHECK (
        last_email_at_unix IS NULL OR last_email_at_unix >= 0
    ),
    pending_kind TEXT CHECK (
        pending_kind IS NULL OR
        pending_kind IN ('warning', 'critical', 'hard-limit', 'recovered')
    ),
    pending_since_unix INTEGER CHECK (
        pending_since_unix IS NULL OR pending_since_unix >= 0
    ),
    retry_after_unix INTEGER CHECK (
        retry_after_unix IS NULL OR retry_after_unix >= 0
    ),
    retry_attempt INTEGER NOT NULL DEFAULT 0 CHECK (retry_attempt BETWEEN 0 AND 3),
    CHECK ((last_email_kind IS NULL) = (last_email_at_unix IS NULL)),
    CHECK (
        (pending_kind IS NULL AND pending_since_unix IS NULL AND retry_after_unix IS NULL AND retry_attempt = 0)
        OR
        (pending_kind IS NOT NULL AND pending_since_unix IS NOT NULL AND retry_after_unix IS NOT NULL)
    )
) STRICT, WITHOUT ROWID;

INSERT INTO storage_growth_state (
    singleton_id,
    state,
    sampled_at_unix,
    physical_bytes,
    transition_at_unix
) VALUES (1, 'normal', 0, 0, 0);

CREATE TRIGGER storage_growth_state_no_delete
BEFORE DELETE ON storage_growth_state
BEGIN
    SELECT RAISE(ABORT, 'storage growth state is retained');
END;
