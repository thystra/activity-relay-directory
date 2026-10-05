-- Activity-Relay Directory 1.3 RC hardening: bounded self-reported relay telemetry.
CREATE TABLE relay_telemetry (
    relay_actor TEXT PRIMARY KEY
        CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    receiving_instance_count INTEGER NOT NULL
        CHECK (receiving_instance_count BETWEEN 0 AND 10000000),
    reported_at_unix INTEGER NOT NULL CHECK (reported_at_unix >= 0)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER relay_telemetry_identity_guard_insert
BEFORE INSERT ON relay_telemetry
WHEN NOT EXISTS (SELECT 1 FROM relays WHERE relay_actor = NEW.relay_actor)
BEGIN
    SELECT RAISE(ABORT, 'relay telemetry requires lifecycle identity');
END;

CREATE TRIGGER relay_telemetry_cleanup_after_relay_delete
AFTER DELETE ON relays
BEGIN
    DELETE FROM relay_telemetry WHERE relay_actor = OLD.relay_actor;
END;
