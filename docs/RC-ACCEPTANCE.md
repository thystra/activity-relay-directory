# Release-candidate operator acceptance

Release-candidate acceptance is deliberately different from development
validation.

Development validation is automated, implementation-aware, and exhaustive. It
should prove protocol details, exact state transitions, negative cases,
configuration parsing, package/container behavior, HTTP headers, CSP/CSS
compatibility, storage rules, and other machine-verifiable contracts.

RC acceptance is an operator-guided review of the candidate as a real operator
and user experience. The checklist should arrange meaningful system states,
perform the documented commands, show concise evidence, and ask the operator
whether the observed result is correct.

## Standard operator outcomes

Interactive RC checks use these outcomes:

- `PASS` / `YES`: the expected result was observed;
- `FAIL` / `NO`: an unexpected result was observed and is recorded;
- `RETRY`: rerun only the current check;
- `SKIP` / `N/A`: permitted only when the checklist marks the step optional,
  with a reason recorded; and
- `ABORT`: end the acceptance session deliberately.

A `FAIL` / `NO` answer is evidence, not automatically a control-flow
instruction. Unless the step is marked critical, record the failure, timestamp,
supporting evidence, and optional operator note, then offer `RETRY`, `CONTINUE`,
or `ABORT`. Continuing the checklist helps discover the complete set of RC
issues in one session.

## Critical checks

Critical checks are identified before the run. Their failure stops
automatically because continuing would be unsafe or meaningless. Typical
critical checks include:

- the candidate artifact, version, or signature is not the intended one;
- the service cannot start or cannot open its state safely;
- TLS identity is wrong for the public authority;
- an authorization or destructive-data boundary behaves incorrectly; or
- corruption or another condition makes further testing unsafe.

A cosmetic defect, awkward wording, a noncritical command result, or a
human-facing presentation problem is normally recorded as a failure and the
session continues.

## Checklist completion versus RC disposition

A checklist process can complete successfully while the candidate remains a
`NO-GO`. Script exit status should distinguish execution failure from product
acceptance. The final report should summarize at least:

- critical failures;
- recorded noncritical failures;
- retries;
- skipped checks and reasons;
- operator notes; and
- final disposition such as `GO`, `GO WITH NOTES`, or `NO-GO`.

## Configuration-state acceptance

Release candidates that expose configurable operator/user presentation must be
reviewed as a state matrix rather than only in one fully configured state. Use
the classifications in `CONFIGURATION.md` to decide which failures are critical
and which should be recorded while the service continues.

For Nice-to-have multi-key presentation objects, acceptance should include all
valid visibility combinations plus each single missing/malformed member. Assert
that valid independent fields remain visible, partial/unsafe links are absent,
and the human-facing diagnostic names the exact key that needs attention.

## Human-facing and integration checks

Machine checks can prove that HTML returned `200`, CSS returned the expected
content type, or a database row exists. RC acceptance must still ask whether the
human-visible or operator-visible behavior is correct.

A representative Directory plus Activity-Relay RC sequence is:

1. turn on the Directory and confirm the public site is visibly correct;
2. connect a candidate relay to the Directory;
3. confirm the relay appears in the operator/admin view and on the public page
   when eligible;
4. exercise the documented suspend/ban command and observe the resulting
   operator and public behavior;
5. restore/unban the relay and confirm recovery;
6. exercise heartbeat, enrollment, unregister, and other applicable command
   sequences;
7. review service and reverse-proxy logs for unexpected errors; and
8. choose explicitly whether to leave each component running or return it to a
   known offline state.

Development automation should already have proven the low-level contracts.
The RC checklist exists to verify that the assembled candidate behaves the way
an operator and user expect.
