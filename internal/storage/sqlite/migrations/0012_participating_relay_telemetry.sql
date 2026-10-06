-- Activity-Relay Directory 1.3 RC4: Protocol v3 participating-site telemetry.
ALTER TABLE relay_telemetry ADD COLUMN participating_instance_count INTEGER
    CHECK (participating_instance_count BETWEEN 0 AND 10000000);
ALTER TABLE relay_telemetry ADD COLUMN participating_reported_at_unix INTEGER
    CHECK (participating_reported_at_unix >= 0);
