-- ARD 1.3.1-rc1: consecutive actor probe failures are bounded and durable.
-- Older diagnostics are not invented; an existing failed check counts as the
-- first failure. Keep the seven-day policy for relays with no recent proof of
-- life. Active relays receive the earlier one-hour retry after upgrade.
ALTER TABLE relay_probe_diagnostics
ADD COLUMN actor_failure_streak INTEGER NOT NULL DEFAULT 0
    CHECK (actor_failure_streak BETWEEN 0 AND 4);

UPDATE relay_probe_diagnostics
SET actor_failure_streak = 1,
    next_check_at_unix = CASE
      WHEN MAX(
        COALESCE((SELECT last_seen_at_unix FROM relays
                  WHERE relays.relay_actor = relay_probe_diagnostics.relay_actor), -1),
        COALESCE((SELECT first_discovered_at_unix FROM relay_discoveries
                  WHERE relay_discoveries.relay_actor = relay_probe_diagnostics.relay_actor), -1),
        COALESCE((SELECT actor_last_success_at_unix FROM relay_observations
                  WHERE relay_observations.relay_actor = relay_probe_diagnostics.relay_actor), -1)
      ) <= updated_at_unix - 604800
      THEN next_check_at_unix
      ELSE MIN(next_check_at_unix, updated_at_unix + 3600)
    END
WHERE EXISTS (
    SELECT 1 FROM relay_observations AS observation
    WHERE observation.relay_actor = relay_probe_diagnostics.relay_actor
      AND observation.actor_state = 'unreachable'
);
