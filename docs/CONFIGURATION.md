# Configuration classification and error-state matrix

Activity-Relay Directory configuration is classified by operational consequence
rather than by where a value happens to be stored. Every new configuration key
must define its missing, malformed, dependency, and exposure behavior before the
implementation is accepted.

## Categories

- **Critical** — required for the applicable process to operate safely. Missing
  or malformed values fail closed and the process does not start or the command
  does not run.
- **Optional** — absence is valid and has an explicit default or disabled state.
  A supplied malformed value still fails closed when it controls runtime,
  storage, security, or private operational behavior.
- **Nice-to-have** — public presentation metadata that is not required for core
  operation. Missing values are suppressed. Malformed or incomplete values do
  not prevent the core service from running; the unsafe/partial value is not
  rendered, and the human Directory page shows a deterministic configuration
  diagnostic.

A category does not remove dependency checks. Multi-key logical objects must
specify every partial state, and conditional runtime settings must state what
becomes required when a related feature is enabled.

## Service process settings

| Key | Category | Missing / empty behavior | Malformed supplied value | Dependency / notes |
| --- | --- | --- | --- | --- |
| `DIRECTORY_PUBLIC_BASE_URL` | Critical | startup failure | startup failure | HTTPS is required when lifecycle is enabled; HTTP is allowed only for loopback development |
| `DIRECTORY_DATABASE_PATH` | Critical | startup failure | startup failure | clean absolute local SQLite path; local admin commands use this independently |
| `DIRECTORY_LISTEN_ADDRESS` | Optional | `127.0.0.1:8080` | startup failure | must resolve as a TCP listen address |
| `DIRECTORY_CONFIG_PATH` | Optional | use optional `/etc/activity-relay-directory/config.yml`; missing default file is valid | startup failure for an invalid explicit path, unreadable/missing explicit file, oversized file, malformed YAML, unknown fields, or multiple YAML documents | locates the Nice-to-have operator presentation file; field-value problems inside a structurally valid file are non-blocking |
| `DIRECTORY_LIFECYCLE_ENABLED` | Optional | `false` | startup failure | when `true`, `DIRECTORY_PUBLIC_BASE_URL` must be HTTPS |
| `DIRECTORY_PUBLIC_LISTING_ENABLED` | Optional | `false` | startup failure | independently controls JSON and human public directory views |
| `DIRECTORY_SOFT_PRUNING_ENABLED` | Optional | `false` | startup failure | when `true`, the pruning interval must be nonzero and at least the supported minimum |
| `DIRECTORY_SOFT_PRUNING_INTERVAL` | Optional | `24h` | startup failure | `0` is allowed only while soft pruning is disabled |
| `DIRECTORY_INACTIVE_RETENTION_DAYS` | Optional | `0` (indefinite) | startup/command failure | bounded integer retention policy |
| `DIRECTORY_DATABASE_MAX_BYTES` | Optional | managed default growth budget | startup/command failure | storage-growth setting |
| `DIRECTORY_DATABASE_WARNING_PERCENT` | Optional | `75` | startup/command failure | must remain below the fixed critical threshold |
| `DIRECTORY_ADMIN_EMAIL` | Optional | no administrative email notification | startup/command failure | private operational recipients; never reused as public operator contact |
| `DIRECTORY_MAIL_BACKEND` | Optional | `mail` | startup/command failure | currently only the reviewed `mail` backend is accepted |
| `DIRECTORY_MAIL_COMMAND` | Optional | `/usr/bin/mail` | startup/command failure | clean absolute command path |
| `DIRECTORY_MAIL_TIMEOUT_SECONDS` | Optional | `30` | startup/command failure | bounded positive timeout |
| `DIRECTORY_MAX_REQUEST_BODY_BYTES` | Optional | `65536` | startup failure | bounded lifecycle request-body limit |
| `DIRECTORY_TRUSTED_PROXY_PREFIXES` | Optional | no trusted proxies | startup failure | comma-separated validated direct-proxy prefixes |
| `DIRECTORY_REGISTRATION_ENABLED` | Retired guard | must be absent | any nonempty value is rejected | renamed to `DIRECTORY_LIFECYCLE_ENABLED`; never accepted as an alias |

## Operator presentation YAML

The operator file is structurally strict but its individual presentation values
are Nice-to-have. Structural YAML failures remain startup errors so the operator
is never told that a file was loaded when it was not. Once the file is parsed,
field-value failures are non-blocking and are represented only on the human
Directory page.

| Key / logical object | Category | Valid behavior | Missing behavior | Malformed / partial behavior |
| --- | --- | --- | --- | --- |
| `OPERATOR-WEBSITE` | Nice-to-have | render `Operator website` as the exact absolute HTTPS URL | suppress the website link | suppress the value and show `OPERATOR-WEBSITE is malformed in config.yml.` |
| `OPERATOR-EMAIL` | Nice-to-have | render the exact address through `mailto:` | suppress the email link | suppress the value and show `OPERATOR-EMAIL is malformed in config.yml.` |
| `FEDIVERSE-OPERATOR-ID` + `FEDIVERSE-OPERATOR-URL` | Nice-to-have multi-key object | when both are valid, render the exact `@user@host` linked to the explicit absolute HTTPS profile URL | when both are absent, suppress Fediverse presentation with no diagnostic | ID missing: `Please configure FEDIVERSE-OPERATOR-ID in config.yml.`; URL missing: `Please configure FEDIVERSE-OPERATOR-URL in config.yml.`; malformed supplied members get their own `... is malformed in config.yml.` diagnostic; no partial Fediverse link is rendered |

Operator email syntax is deliberately loose. It requires one nonempty local
part, `@`, a domain containing at least one dot, and a nonempty suffix. It does
not maintain a TLD allowlist; addresses such as `.museum`, `.technology`, or
future valid TLDs are not rejected merely because the suffix is unfamiliar.

Valid independent operator fields continue to render when another Nice-to-have
field is malformed or incomplete. For example, a valid website and email remain
visible when a Fediverse URL is supplied without its required ID; the page also
shows the missing-ID diagnostic.

Operator values and operator diagnostics are presentation-only. Neither may be
copied into `/v1/relays`, `/v1/status`, or private administrative configuration.

## Required state-matrix coverage

For every new key, development tests and RC acceptance must cover at least:

1. absent / default state;
2. valid supplied state;
3. malformed supplied state;
4. interaction with every directly dependent key; and
5. exposure boundaries for public, private, and log surfaces.

For a multi-key logical object, enumerate every valid present/absent combination
when practical. At minimum, test each member missing individually, each member
malformed individually, all members absent, all members valid, and a mixed case
that proves unrelated valid configuration remains active while the object is in
an error state.
