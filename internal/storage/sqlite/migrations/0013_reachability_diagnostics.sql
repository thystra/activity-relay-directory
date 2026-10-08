-- Diagnostic evidence is separate from the existing tier-driving observation
-- columns. A cascading reference prevents orphans after verified hard retention.
CREATE TABLE relay_probe_diagnostics (
    relay_actor TEXT NOT NULL PRIMARY KEY REFERENCES relay_observations(relay_actor) ON DELETE CASCADE,
    actor_stage TEXT NOT NULL DEFAULT '' CHECK(length(actor_stage) <= 16),
    actor_code TEXT NOT NULL DEFAULT '' CHECK(length(actor_code) <= 32),
    actor_http_status INTEGER NOT NULL DEFAULT 0 CHECK(actor_http_status BETWEEN 0 AND 599),
    inbox_stage TEXT NOT NULL DEFAULT '' CHECK(length(inbox_stage) <= 16),
    inbox_code TEXT NOT NULL DEFAULT '' CHECK(length(inbox_code) <= 32),
    inbox_http_status INTEGER NOT NULL DEFAULT 0 CHECK(inbox_http_status BETWEEN 0 AND 599),
    next_check_at_unix INTEGER NOT NULL CHECK(next_check_at_unix >= 0),
    updated_at_unix INTEGER NOT NULL CHECK(updated_at_unix >= 0),
    CHECK(next_check_at_unix >= updated_at_unix)
) STRICT, WITHOUT ROWID;
