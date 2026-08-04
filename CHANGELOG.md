# Changelog

## Unreleased

### Added

- Initial Go service scaffold.
- Health, readiness, and schema-versioned status endpoints.
- Strict environment configuration validation.
- Non-root, read-only container runtime.
- Test and container-build workflows.
- Version 1 lifecycle, outcome, error, health, and administrative vocabulary.
- Strictly decoded JSON request and response contract fixtures.
- Canonical HTTPS relay actor/public-base normalization and origin binding.
- Optional Nginx, Apache, and Caddy reverse-proxy examples.

### Security

- Registration is disabled and unavailable in the initial scaffold.
- Non-loopback public URLs require HTTPS.
- Request-body limits are bounded even before request endpoints exist.
