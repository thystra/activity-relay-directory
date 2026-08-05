# Administrative moderation

The directory exposes retained-relay moderation only through a local operator
CLI. Initial authorization is operating-system access to the executable and the
owner-only SQLite database. There is no moderation HTTP endpoint, bearer-token
scheme, automatically granted administrative group, or preemptive blocklist.

## Boundary

`storage.ModerationRepository` accepts a canonical relay actor, a bounded
moderator identifier, a bounded reason code, and server-owned acceptance time.
It operates only when the relay already has a retained row. An actor the
directory has never recorded cannot be preemptively suspended in this model;
supporting that policy requires a separate reviewed blocklist design.

Moderator identifiers are 1 to 128 ASCII bytes, start with an alphanumeric
character, and otherwise use only alphanumerics plus `@._:+-`. Reason codes are
1 to 64 ASCII bytes, start with a lowercase letter, and otherwise use lowercase
letters, digits, underscore, or hyphen. Both are classification tokens rather
than free-form notes. They are private operational audit data and must never be
included in public directory views or errors.

## State transitions

| Operation | Current state | Internal outcome | State effect | Audit action |
| --- | --- | --- | --- | --- |
| Suspend | active | `suspended` | set suspension and update time | `suspend_applied` |
| Suspend | suspended | `already_suspended` | none | `suspend_unchanged` |
| Restore | suspended | `restored` | clear suspension and update time | `restore_applied` |
| Restore | active | `already_active` | none | `restore_unchanged` |

Every accepted decision appends one audit event, including an idempotent
decision that leaves state unchanged. A changed relay row and its event commit
in the same immediate SQLite transaction. Event failure rolls the state change
back. Acceptance time cannot precede the relay state or its latest lifecycle or
moderation event.

Suspension is independent of lifecycle state. It blocks register and heartbeat
but not unregister. Suspend leaves public-base and heartbeat metadata intact;
restore clears only suspension and does not register an unregistered relay.

## Local commands

```text
activity-relay-directory admin suspend --actor URL --moderator ID --reason CODE [--yes] [--format human|json]
activity-relay-directory admin restore --actor URL --moderator ID --reason CODE [--yes] [--format human|json]
activity-relay-directory admin show --actor URL [--format human|json]
activity-relay-directory admin audit --actor URL [--limit 1..100] [--after UNIX:ID] [--format human|json]
```

Suspend and restore require the operator to type the exact canonical actor. The
`--yes` flag is the explicit noninteractive acknowledgement for reviewed
automation; it must not be added implicitly by packaging or service wrappers.

Human output is stable `name=value` text. JSON output uses schema
`activity-relay-directory.admin.v1`. Exit classes are fixed:

| Exit | Meaning |
| ---: | --- |
| 0 | success |
| 1 | operational, database, or output failure |
| 2 | invalid invocation, invalid decision time, or failed confirmation |
| 3 | actor has no retained relay row |
| 4 | command canceled or timed out |

Errors are bounded and do not include database paths, SQL text, moderator IDs,
reason codes, or backend details.

## Private reads and pagination

`admin show` returns retained lifecycle and administrative state plus
server-owned timestamps. It does not return historical moderator or reason
tokens.

`admin audit` returns private moderation events. Its page size defaults to 50
and cannot exceed 100. Pagination uses the existing
`(relay_actor, recorded_at_unix, moderation_event_id)` index and the canonical
`UNIX:ID` cursor. A page queries at most `limit + 1` rows to determine whether a
next cursor exists. Audit reads do not write or trigger cleanup.

Audit output contains moderator and reason tokens and is therefore private.
Do not publish it, attach it to public incident reports without redaction, or
pipe it into public logging systems.

## Native and container operation

For a native installation, run the command as an account that already has
access to the executable, database directory, database, WAL, and shared-memory
files. Do not make the database group-readable merely to enable moderation.

The container already runs as the unprivileged `directory` account with its
existing private data volume. Run local administration inside that same service
container, for example:

```sh
docker compose exec directory \
  activity-relay-directory admin show \
  --actor https://relay.example/actor
```

Do not mount the database into a second host, run a second active directory
service against it, add a broad host group, or widen volume permissions. The
CLI is safe alongside the one active same-host service through SQLite WAL,
bounded reads, immediate write transactions, and the configured busy timeout.
