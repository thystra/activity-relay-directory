# Administrative moderation

The directory has a dormant storage contract for administratively suspending
and restoring a retained relay. This contract establishes durable state and
private audit semantics only. It does not expose an HTTP endpoint, operator
CLI, public outcome, or runtime wiring.

## Boundary

`storage.ModerationRepository` accepts a canonical relay actor, a bounded
moderator identifier, a bounded reason code, and server-owned acceptance time.
It operates only when the relay already has a retained row. An actor the
directory has never recorded cannot be preemptively suspended in this model;
supporting that policy would require a separate reviewed blocklist design.

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

## Remaining operator surface

Before an administrator can invoke these transitions, a later tranche must
define authenticated and authorized local operator access, command semantics,
reason-code policy, safe audit inspection, concurrency behavior, redacted
errors, and operational recovery. That review must also decide whether a
preemptive blocklist is needed. None of this storage work enables registration
or deployment.
