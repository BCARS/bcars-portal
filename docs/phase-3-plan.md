# Phase 3 Execution Plan

This is the human-readable execution map for reviewed member requests and
optional access. Beads is the durable task and dependency source of truth. Read
[phase-3-design.md](phase-3-design.md) before claiming a Phase 3 story.

## Target outcome

Officers can capture and review corrections from every supported channel;
members can optionally use a passwordless link to see only explicitly granted
records and submit suggestions; and active approved Full members can browse and
print a server-filtered directory. Member/public input never changes canonical
records before officer review.

## Workstreams

### WS0 - Critical-path authentication hardening

Three existing hardening beads land before passwordless member access:

- `fmc.21` gives HTML recovery requests the same client-address hash as API
  requests;
- `fmc.20` adds one enumeration-safe attempt limiter that member sign-in and
  blind intake can reuse;
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

Blind public intake (`4ux.9`) joins the same workflow only after the common
request API and reusable limiter exist.

### WS3 - Optional member identity and self service

Member provisioning (`4ux.4`) explicitly maps a passwordless user to one or more
person records. Passwordless sign-in (`4ux.5`) follows the unified-cookie and
rate-limit work. The own-profile/request API (`4ux.6`) can develop in parallel
with sign-in because it authorizes a principal and access grant rather than
depending on an email provider.

Informational relationships (`4ux.8`) build on explicit access and the own-
request contract while proving that relationships never create authority.

### WS4 - Private member directory

The directory API (`4ux.7`) starts from the foundation and remains separate from
administrative member reads. It enforces Full-member caller eligibility and
filters every target contact value before serialization. The screen and print UI
(`4ux.12`) lands only after that API and the member shell exist.

### WS5 - Thin MVP adapters

The officer UI (`4ux.10`) follows all request-review, access, relationship, and
public-triage APIs. The member UI (`4ux.11`) follows passwordless authentication
and own-profile/request APIs. Both may make reasonable accessible layout and copy
decisions. Full interactive polish remains deferred.

### WS6 - Assembly proof and completion audit

`4ux.13` extends the production-assembly smoke test across the complete member
boundary and then reconciles docs and Beads with merged `main`. Closed stories
alone are not completion evidence.

## Dependency map

```text
fmc.21 -> fmc.20 ----------------------------+
6q6.3 ---------------------------------------+--> passwordless sign-in
                                             |          |
request/access schema + capabilities --------+--> access provisioning
             |                               |          |
             +--> officer request intake ----+--> own profile/request API
             |             |                            |
             |             +--> review/apply <--- 6q6.1 |
             |             |                            +--> member UI
             |             +--> blind public intake ----+       |
             |                                                  |
             +--> directory API --------------------------------+--> directory + print UI
             |
             +--> access + own-request API --> relationships

review + access + relationships + public intake --> officer UI
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
| `4ux.4` | Member access provisioning API | Explicit passwordless user-to-person grants and revocation; shared mailbox support | `4ux.1` |
| `4ux.5` | Passwordless member sign-in | Safe request/consume flow, one cookie, local fake/filelog mail | `4ux.4`, `6q6.3`, `fmc.20` |
| `4ux.6` | Own-profile/dues/request API | Granted-record read model, safe dues summary, submit/status/withdraw | `4ux.2`, `4ux.4` |
| `4ux.7` | Full-member directory API | Caller eligibility, target eligibility, contact filtering, stable query | `4ux.1` |
| `4ux.8` | Relationships and explicit delegated access | Informational links kept independent from authority | `4ux.4`, `4ux.6` |
| `4ux.9` | Blind public correction intake | Generic non-disclosing intake, abuse limits, officer linkage | `4ux.2`, `fmc.20` |
| `4ux.10` | Officer request/access UI | Queue, capture, triage, per-item review, access and relationships | `4ux.3`, `4ux.4`, `4ux.8`, `4ux.9` |
| `4ux.11` | Member profile/request UI | Passwordless entry, record chooser, safe profile, request tracking | `4ux.5`, `4ux.6` |
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
- `fmc.20` owns the reusable attempt-limiter storage/service. Phase 3 auth and
  public intake reuse it rather than adding another limiter.
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

- officer, member, and blind-public submissions enter one request model and do
  not change canonical data before review;
- a mixed per-field review applies only approved supported items exactly once,
  preserves rejected items, and refuses stale or prohibited self-review;
- one shared-email account can reach only explicitly granted records, a replayed
  link fails, and revoked access stops working within an existing session;
- a member sees safe own-profile and dues-standing data but no payment detail or
  officer/treasurer notes;
- an active approved Full member can browse and print the same filtered directory,
  while an Associate cannot;
- hidden and absent contact methods are indistinguishable to the directory caller;
- public intake reveals no member/account/match information and is rate limited;
- informational family relationships never grant access by themselves;
- no code or task depends on `scratch/`, parent/sibling files, real data, live
  SMTP, FCC, Groups.io, deployment, or another external system.

After code completion, `bcars-portal-8ou` may validate production mail
interactively. `bcars-portal-6pz` owns the later collaborative visual-design pass.
