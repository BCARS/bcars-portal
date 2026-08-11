# Phase 3 Execution Plan

This is the human-readable execution map for reviewed member requests and
optional access. Beads is the durable task and dependency source of truth. Read
[phase-3-design.md](phase-3-design.md) before claiming a Phase 3 story.

## Target outcome

Officers can capture and review corrections from every supported channel;
authenticated members, including Associates, can suggest corrections about
themselves or another person without target-record access; and active approved
Full members can browse and print a server-filtered directory. Member input
never changes canonical records before officer review.

## Workstreams

### WS0 - Critical-path authentication hardening

Three existing hardening beads landed before member access:

- `fmc.21` gives HTML recovery requests the same client-address hash as API
  requests;
- `fmc.20` adds the enumeration-safe attempt limiter used by recovery;
- `6q6.3` unifies the API and HTML session cookie.

`6q6.1` settles the generic confirmation contract before the request-review API
declares consequential approvals. Other children of the delivery-hardening epic
continue independently and do not block Phase 3.

### WS1 - Schema and policy foundation

`4ux.1` is the only Phase 3 schema bottleneck. It adds access grants, typed
requests/items, review provenance, informational relationships, capabilities,
and the compatibility rule for existing `users.person_id` links.

After it merges, request intake, member access provisioning, and the directory
read model can proceed in parallel.

### WS2 - Reviewed request workflow

The officer intake/query API (`4ux.2`) proves that proposals are durable without
changing canonical data. Per-item review/apply (`4ux.3`) follows, using explicit
domain adapters, optimistic concurrency, idempotency, sensitivity policy, and
the generic confirmation control.

Authenticated cross-member suggestions join the same workflow in `4ux.6`.
`4ux.9`, the accidental anonymous-public intake story, is superseded by that
member request contract.

### WS3 - Optional member identity and self service

Member provisioning (`4ux.4`) explicitly maps a user to one or more person
records without choosing the user's password. Password onboarding and sign-in
(`4ux.5`) reuse the existing recovery, password, unified-cookie, and rate-limit
work. The profile/request API (`4ux.6`) keeps profile reads grant-bound while
allowing any authenticated member to suggest a correction about another person
without target access.

Informational relationships (`4ux.8`) build directly on the Phase 3 foundation
and prove that relationships neither create authority nor gate correction
submission.

### WS4 - Private member directory

The directory API (`4ux.7`) starts from the foundation and remains separate from
administrative member reads. It enforces Full-member caller eligibility and
filters every target contact value before serialization. The screen and print UI
(`4ux.12`) lands only after that API and the member shell exist.

### WS5 - Thin MVP adapters

The officer UI (`4ux.10`) follows all request-review, access, relationship, and
member-suggestion triage APIs. The member UI (`4ux.11`) follows shared password
authentication and profile/request APIs. Both may make reasonable accessible
layout and copy decisions. Full interactive polish remains deferred.

### WS6 - Assembly proof and completion audit

`4ux.13` extends the production-assembly smoke test across the complete member
boundary and then reconciles docs and Beads with merged `main`. Closed stories
alone are not completion evidence.

## Dependency map

```text
fmc.21 -> fmc.20 ----------------------------+
6q6.3 ---------------------------------------+--> password setup + sign-in
                                             |          |
request/access schema + capabilities --------+--> access provisioning
             |                               |          |
             +--> officer request intake ----+--> own profile/request API
             |             |                            |
             |             +--> review/apply <--- 6q6.1 |
             |             |                            +--> member UI
             |                                                  |
             +--> directory API --------------------------------+--> directory + print UI
             |
             +--> profile + member-request API     relationships

review + member requests + access + relationships --> officer UI
all Phase 3 stories ------------------------------> smoke + completion audit
```

## Planned beads

The Phase 3 epic is `bcars-portal-4ux`. Claim a ready child, never the epic
itself. IDs below abbreviate the `bcars-portal-` prefix; live dependencies and
full acceptance criteria are in Beads.

| Bead | Story | Scope | Depends on |
| --- | --- | --- | --- |
| `4ux.1` | Request/access foundation | One migration, sqlc, access/request/relationship schema, capability matrix, identity ADR | none |
| `4ux.2` | Officer request capture/query API | Channel-aware typed intake and triage reads; no canonical mutation | `4ux.1` |
| `4ux.3` | Per-field review/apply API | Explicit adapters, sensitivity policy, conflict/idempotency/self-review controls | `4ux.2`, `6q6.1` |
| `4ux.4` | Member access provisioning API | Explicit user-to-person grants and revocation; shared mailbox support; no officer-chosen password | `4ux.1` |
| `4ux.5` | Member password onboarding and sign-in | Initial password via recovery, normal password sign-in, one cookie, member-safe routing | `4ux.4`, `6q6.3`, `fmc.20`, `4ux.15` |
| `4ux.6` | Profile/dues/member-request API | Grant-bound reads; authenticated self-or-other suggestions; submit/status/withdraw | `4ux.2`, `4ux.4`, `4ux.16` |
| `4ux.7` | Full-member directory API | Caller eligibility, target eligibility, contact filtering, stable query | `4ux.1` |
| `4ux.8` | Informational relationships | Context only; neither access authority nor a prerequisite for suggestions | `4ux.1` |
| `4ux.9` | Superseded public correction intake | No anonymous portal surface; intended member behavior moved to `4ux.6` | `4ux.16` |
| `4ux.10` | Officer request/access UI | Queue, capture, member-suggestion triage, per-item review, access and relationships | `4ux.3`, `4ux.4`, `4ux.6`, `4ux.8` |
| `4ux.11` | Member profile/request UI | Password setup/sign-in entry, record chooser, safe profile, request tracking | `4ux.5`, `4ux.6` |
| `4ux.12` | Directory and print UI | Plain sortable table, “Not shared,” letter-print view | `4ux.7`, `4ux.11` |
| `4ux.13` | Production smoke and completion audit | Real binaries and synthetic end-to-end authorization/review proof | every other Phase 3 story |

## Intentional ready work

At Phase 3 kickoff the useful parallel lanes are:

- `4ux.1`, the schema/capability foundation;
- `fmc.21` then `fmc.20`, client identity and request limiting;
- `6q6.3`, the unified session cookie;
- `6q6.1`, the generic confirmation decision.

No Phase 3 UI is ready before its API. The remaining production-hardening,
packaging, import-normalization, audit-query, and external tasks may proceed
independently but are not hidden prerequisites for this phase.

## Parallelism and collision rules

- Do not split `4ux.1` across agents; it owns the first Phase 3 migration,
  capability seed, generated queries, and identity/access ADR.
- `fmc.20` owns the reusable attempt-limiter storage/service used by recovery.
  Authenticated member suggestions do not add an anonymous intake limiter.
- After `4ux.1`, request intake, access provisioning, and directory API may run
  in parallel.
- `4ux.3` owns request-decision/apply semantics. UI tasks do not reproduce those
  mutations or sensitivity rules.
- API tasks that regenerate sqlc/OpenAPI/catalog artifacts rebase sequentially
  and rerun the matching diff gates after conflict resolution.
- Member and officer UI routes use the configured shared cookie and merged domain
  services; they do not invent a second auth stack.
- External/interactive beads remain deferred unless the repository owner starts
  them explicitly.

## Acceptance protocol for every story

Each Bead contains observable story-specific criteria. Every implementation PR
also:

1. works from a standalone clone with synthetic data only;
2. preserves generic capability and audit middleware invariants;
3. proves the user-visible security/property claim through its consumer, not
   merely the implementation pieces;
4. adds negative authorization and non-disclosure tests;
5. commits intentional sqlc, OpenAPI, and capability-catalog artifacts;
6. runs `make build`, `make test`, `make lint`, `make migration-updown`,
   `make sqlc-diff`, `make openapi-diff`, and `make smoke`;
7. uses one scoped branch and PR, waits for every required CI check, and merges
   only when CI is green;
8. closes and pushes the Bead only after merge.

## Phase 3 completion criteria

Phase 3 is complete only when all epic children are merged and `4ux.13` proves
on assembled binaries that:

- officer and authenticated-member submissions enter one request model and do
  not change canonical data before review;
- a mixed per-field review applies only approved supported items exactly once,
  preserves rejected items, and refuses stale or prohibited self-review;
- one shared-email account can set its password through one single-use recovery
  link, sign in with that password, reach only explicitly granted records, and
  lose that access within an existing session when its grant is revoked;
- a member sees safe own-profile and dues-standing data but no payment detail or
  officer/treasurer notes;
- an active approved Full member can browse and print the same filtered directory,
  while an Associate cannot;
- hidden and absent contact methods are indistinguishable to the directory caller;
- an Associate can suggest a correction about another person without directory
  or profile access, while an anonymous caller cannot submit;
- informational family relationships never grant access by themselves;
- no code or task depends on `scratch/`, parent/sibling files, real data, live
  SMTP, FCC, Groups.io, deployment, or another external system.

After code completion, `bcars-portal-8ou` may validate production mail
interactively. `bcars-portal-6pz` owns the later collaborative visual-design pass.
