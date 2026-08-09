# BCARS Members Portal Planning

## Status

This is a fresh, self-contained implementation. An abandoned 2025 proof of concept
existed during initial planning, but it is not part of this repository and is never a
prerequisite for development. All active requirements and decisions needed to work on
the portal live in this repository. No source code, databases, authentication,
generated queries, or migrations are inherited from the prototype.

Real membership exports, databases, uploaded files, extracted document text, API
credentials, and AI conversation data must never be committed to source control.

## Goals

The portal should:

- Give officers and the treasurer one trustworthy membership system of record.
- Work well for a small club whose members may rarely use a computer.
- Support cash and check payment recording without requiring members to use the portal.
- Let members or family members suggest corrections without directly changing official
  records.
- Provide a permissioned alternative to the unrestricted Groups.io file area.
- Add an optional conversational AI assistant that can use portal tools with exactly
  the logged-in user's authority.
- Keep Groups.io as the initial calendar and mailing-list integration point.
- Remain maintainable by future volunteer officers.

## Design Principles

- The portal must remain fully usable without AI.
- Build API-first: every user-visible query or command is an authenticated application
  operation with a stable, machine-readable contract.
- The web UI and future AI tool adapters use the same application operations and
  authorization rules. Business logic does not live only in pages, forms, or JavaScript.
- Tool-callable means an operation can be safely wrapped as a tool; it does not mean
  every operation is automatically offered to every model or user.
- Default-deny authorization is enforced by the server, never only by the UI or model.
- Prefer simple officer workflows over forcing self-service on every member.
- Preserve an audit trail for membership, payment, permission, file, and AI-assisted
  actions.
- Import external data through staging, validation, and reconciliation.
- Treat member data, payment data, restricted files, and AI conversations as sensitive.
- Prefer reversible changes, version history, and archive workflows over deletion.
- Keep the public BCARS website separate from the private portal.

## API-First Capability Model

Each capability has four layers:

1. A domain or application service that owns validation, authorization, and business
   rules.
2. A versioned API operation with structured input, output, and error contracts.
3. One or more adapters, such as the officer UI, import CLI, scheduled job, or AI tool.
4. Audit events and tests that are independent of which adapter invoked the operation.

The API contract is the durable boundary. Prefer OpenAPI-described JSON operations with
consistent pagination, filtering, validation errors, request identifiers, optimistic
concurrency, and idempotency for retryable writes.

AI tools will be a curated mapping over these operations, not an automatic exposure of
the entire API. Tool handlers may call the application service directly inside the
monolith, but must preserve the same input validation, principal, authorization, audit,
and transaction behavior as the HTTP API.

No feature is complete if it can only be performed by clicking through the UI.

## Proposed Technical Shape

- A private BCARS repository and a single deployable Go application.
- Server-rendered HTML with HTMX or similarly small progressive enhancements.
- Huma or standard Go HTTP handlers for typed application and tool APIs.
- sqlc for reviewed queries and Goose for ordered migrations.
- SQLite in WAL mode for a single persistent application instance.
- Managed PostgreSQL only if hosting requires multiple instances or lacks durable
  local storage.
- Private object storage for uploaded file contents, with metadata and permissions in
  the application database.
- Encrypted off-site database and file backups with documented restore tests.
- Server-side sessions using secure, HttpOnly, SameSite cookies.

The supported Go and dependency versions should be selected when implementation starts
rather than inherited from the archive.

## Core Data Domains

### People and Contact Information

A person is distinct from a login identity.

- A person may have multiple email, telephone, and postal contact methods.
- Multiple people may share the same email address.
- Contact email is not a unique login identifier.
- Optional spouse, parent, child, or household relationships provide context only.
- Relationships do not automatically grant permission to edit another person's record.

### Member Directory and Contact Sharing

The member directory is private club data, not a public member-list endpoint. Names and
call signs may still appear in public-facing meeting minutes, activity reports, event
results, and similar club records.

- Active Full members may access the member directory.
- Full members appear by name and call sign. Their email, phone, and postal contact
  methods are visible to other Full members according to the member's contact-sharing
  preference.
- Associate members appear to Full members by name and call sign, when they have one.
  Their contact methods are restricted by default, allowing an Associate to remain more
  private, but they may explicitly opt in to sharing selected contact information.
- Associate members do not receive general member-directory access.
- Honorary members inherit directory access and visibility from the underlying Full or
  Associate membership type.
- A member may opt in or out of sharing contact information with Full members. Model
  visibility per contact method even if the initial UI offers one simple overall choice.
- Officers with the appropriate capability may view contact information for club
  administration regardless of directory-sharing preference. Administrative access is
  audited and does not change what other members can see.
- Directory and contact APIs filter fields on the server for both list and detail
  results; hiding fields only in the UI is insufficient.

The legacy Groups.io list does not distinguish these audiences. Migration must establish
an explicit sharing preference or a documented legacy default rather than treating prior
presence in the unrestricted list as permanent consent.

### ACS and ARES Sharing

- A Full member with a verified FCC license is included in BCARS ACS/ARES sharing by
  default unless they opt out when joining or later.
- The ACS/ARES preference covers sharing name and call sign with those programs; other
  contact and capability fields follow their own authorized-use rules.
- Record the preference, effective time, source, and officer or member who submitted it.
- Opting out of ACS/ARES sharing does not change Full membership, voting, dues, or normal
  directory eligibility.
- Associate members are not automatically enrolled merely because they have a call sign;
  any participation is explicit and does not change their Associate membership rights.

### Memberships and Payments

- The base membership type is Full or Associate.
- Full membership requires a valid FCC amateur-radio license and includes voting rights
  and access to the membership list.
- Associate membership does not require an FCC license, pays dues, and does not include
  voting rights or membership-list access.
- Honorary membership is not a base type. It is a fixed-term or lifetime dues waiver
  applied to either a Full or Associate member, preserving the rights of the underlying
  type.
- Honorary grants record start, optional end, lifetime status, reason, approving officer,
  approval date, and notes. A typical fixed-term reason is passing an initial license
  exam through a BCARS session.
- The two imported records using `12/31/2055` represent lifetime honorary Associate
  memberships. Migrate them to explicit lifetime grants rather than preserving 2055 as
  a fabricated paid-through date.
- Officers approve each new membership and its base type. The system records pending,
  approved, rejected, inactive, resigned, and deceased lifecycle states even though
  rejection is historically rare.
- Full-member FCC eligibility records call sign, license class, verification source,
  and verification time. Phase 1 may use manual verification.
- Unknown imported paid-through values such as `01/01/0001` become null, not real dates.
- Annual dues are currently $20 per year. Store dues rates with an effective year rather
  than hard-coding the amount throughout the application.
- The treasurer has authority to choose the paid-through date. The portal may suggest a
  normal annual result but does not enforce automatic proration.
- A late-year member may be declared paid through the following calendar year; a
  partial-year payment may cover the rest of the current year; and one payment may cover
  multiple years.
- Record actual money received separately from the resulting membership coverage:
  - payment entries record member, amount, received date, method, receiving officer,
    entering officer, optional batch or receipt number, and notes;
  - coverage events record the explicit paid-through date, reason, deciding treasurer,
    and optional linked payment;
  - honorary grants and corrections can affect dues standing without inventing a cash
    payment.
- Payment corrections use auditable reversals. Coverage corrections use new adjustment
  events rather than silently changing history.
- Current, expired, lifetime, honorary-waived, and unknown dues standing is derived from
  the latest applicable coverage and waiver records.
- Payments, coverage decisions, honorary grants, and member records support contextual
  notes. Examples include payment method details, proration reasoning, a lifetime grant,
  or relevant service performed for the club.
- Structured fields remain authoritative for calculations and permissions. Notes provide
  human context and may later support permission-aware semantic AI search; the system
  does not infer paid-through or membership rights from note text alone.
- Notes record author, time, category, source, visibility, and edit history. Payment and
  treasurer notes default to treasurer/executive visibility rather than the general
  member directory.
- Payment-card or bank credentials are never stored by the portal.

### Member Change Requests

Members and family members suggest changes; officers approve them.

A request records:

- requester identity or supplied contact details;
- target member;
- proposed field changes;
- source: portal, email, telephone, mail, meeting, or officer-entered;
- stated relationship to the member;
- review status, reviewer, review time, and notes;
- per-field approval or rejection when appropriate.

Sensitive changes such as name, email, membership status, and payment information can
require additional verification. A requester cannot approve their own sensitive change.

### Roles and Authorization

Club positions and application permissions are related but not identical.

Executive positions:

- president;
- vice-president;
- secretary;
- treasurer.

These positions receive broad membership-management capabilities. The capability model
still keeps treasury operations distinct so payment entry, adjustments, and detailed
financial reporting can be granted and audited explicitly. The treasurer receives those
capabilities by default.

Other positions, such as trustee and activities manager, receive narrower capabilities
appropriate to their duties rather than full membership administration.

Technical roles are separate from club offices:

- administrator;
- webmaster/system administrator;
- ACS coordinator;
- member.

The webmaster/system-administrator role covers deployment, backup and recovery,
integration configuration, and operational troubleshooting. It does not automatically
confer a club office or every membership and treasury capability.

Non-secret live settings, such as whether an integration is enabled or which Groups.io
group it targets, may be managed through an authorized configuration API. API keys,
database credentials, encryption keys, and similar secrets remain in environment or
secret-manager configuration and are never returned through the portal API. If the
portal later supports secret entry, it stores only an encrypted or external secret
reference through a write-only administrative operation; neither the UI nor an AI tool
can read the secret back.

Authorization is capability-based within these roles and positions. Position changes
update the corresponding grants through an auditable process rather than through
hard-coded title checks throughout the application.

Membership approval and selection of Full or Associate type are explicit officer
capabilities. Approval records who approved the member, when, and which type was
approved; it is not implied merely by creating a contact record.

Every application operation must authorize both the action and the target resource.
Administrative recovery and role changes are audited.

## Low-Friction Access

### Ordinary Members

- Member portal accounts are optional.
- Use short-lived, single-use email sign-in links instead of member passwords.
- A shared contact email can be associated with multiple members.
- Email sign-in permits only the records and actions explicitly associated with that
  link and authenticated identity.
- Members primarily view permitted information and submit change requests.
- Members without usable email continue to use telephone, mail, or in-person service;
  an officer enters the request into the same workflow.

### Officers and Treasurer

- Officer and treasurer accounts use a password with verified email ownership.
- Password recovery uses a short-lived single-use email link or verification code.
- Recovery responses do not reveal whether an email address has an account.
- Future SMS verification may be added, but Phase 1 does not depend on a text-message
  provider or treat telephone numbers as login identity.
- Recovery is performed through an auditable officer process.
- High-impact operations require recent authentication or an explicit confirmation.
- No user may delete their own account, remove their own final administrative role, or
  approve their own sensitive request.

### Transactional Email

- Prefer sending through the existing Google-hosted `bcars.org` mail system.
- Prefer a dedicated portal sender identity, such as `portal@bcars.org`, so recovery
  mail is isolated from ordinary correspondence and officer turnover.
- `qsl@bcars.org` may be used as the Reply-To address so replies continue to reach the
  officers without requiring the portal sender inbox to become a support queue.
- If a dedicated Google sender or supported authenticated sending method is not
  practical, evaluate Mailgun or another transactional provider. Verify current limits
  and cost rather than assuming a permanent free tier.
- Sending credentials are server-side secrets. Delivery attempts and failures are
  audited without logging verification codes or recovery links.

### Interim Data Retention

BCARS informally removes people from its contact list two years after they stop being a
member. Until a formal policy is approved:

- deactivate former members rather than immediately deleting them;
- exclude them from current contact-list exports according to the agreed two-year rule;
- provide a report of records eligible for officer review after two years;
- do not automatically erase payment history, approvals, or audit events;
- require an authorized, auditable manual decision for anonymization or deletion.

## Permissioned Files and Document Library

Groups.io supports uploads but does not provide the permissions needed for club records.
The portal library will cover meeting minutes, policies, forms, officer work products,
treasurer reports, ACS material, event documents, and photos.

Detailed file UX, search, extraction, and AI retrieval are intentionally deferred. The
near-term requirement is that future file operations follow the same API-first capability
model and do not require a UI-only redesign.

### Visibility and Capabilities

Each file has a default visibility:

- public;
- active members;
- officers;
- role-restricted, such as secretary, treasurer, or ACS coordinator;
- explicitly shared with selected users for narrow exceptions.

Folder or category permissions supply defaults. Access is still checked for every list,
search, preview, download, API request, and AI tool call.

Read, upload, replace, publish, archive, restore, and delete are separate capabilities.
Possession of a URL or file identifier never grants access.

### File Lifecycle

- Keep title, description, category, tags, owner, uploader, source, sensitivity,
  dates, retention policy, checksum, and current version as metadata.
- Store original contents privately and serve downloads only after authorization.
- Keep version history when files are replaced.
- Archive before permanent deletion and apply documented retention rules.
- Enforce file type and size limits and scan uploads for malware.
- Extract text from supported files for authorized search and AI use while preserving
  the original as the authoritative source.
- Stage Groups.io and website imports for duplicate detection and permission review.
- Audit restricted downloads and all permission, publication, archival, and deletion
  changes.
- Never place member exports, payment reports, or other private documents in the public
  website repository.

## Conversational AI Assistant

The assistant is a chat interface backed by a configurable OpenAI-compatible endpoint.
It is an agent that can answer directly or request portal tool calls.

Detailed assistant behavior and prompt design are intentionally deferred. Phase 1 only
needs stable, authorized application operations that can later receive curated tool
adapters without duplicating business logic.

The AI provider may be an external service or a BCARS-controlled endpoint. Provider
choice must not change application permissions or business rules.

### Endpoint Integration

Server-side configuration includes:

- endpoint base URL;
- model identifier;
- server-held API credential;
- request and streaming timeouts;
- maximum tool-call rounds;
- context and output limits;
- provider-specific compatibility options.

The browser never receives the provider credential. An adapter isolates the rest of the
portal from differences in tool calling, streaming, structured output, and error formats
between OpenAI-compatible providers. Startup or administrative capability checks should
detect unsupported tool behavior and fail closed.

Only the minimum necessary prompt context and tool results are sent to the endpoint.
Provider retention, model-training use, deletion, regional storage, and incident terms
must be reviewed before restricted BCARS data is enabled.

### Acting as the Logged-In User

The model has no independent account, role, or database connection.

For every chat turn:

1. The portal authenticates the browser session.
2. The server creates a trusted principal containing the user and current capabilities.
3. The server offers only relevant tool definitions to the model.
4. The model may return a structured tool-call request.
5. The portal injects the trusted principal into the tool handler.
6. The handler re-authorizes the operation and target resource.
7. The portal validates arguments, executes the operation, and audits the result.
8. The sanitized result is returned to the model for the next response.

Tool arguments never accept a trusted `user_id`, role, permission list, or tenant from
the model. Hiding a tool from the model is a convenience, not an authorization control;
every handler performs its own check.

A role change or account deactivation takes effect on the next tool execution, including
within an existing conversation.

### Tool Safety Levels

**Read tools**

Execute after normal resource authorization. Examples:

- search permitted files;
- read an authorized file or meeting minute;
- view the caller's permitted profile;
- list calendar events;
- view membership or payment status at the caller's allowed detail level.

**Draft and suggestion tools**

May create non-authoritative work after authorization. Examples:

- draft an agenda;
- prepare a member change request;
- prepare an annual activity checklist;
- draft an officer report.

**Consequential tools**

Require an explicit confirmation in the portal and normal workflow rules. Examples:

- submit a prepared change request;
- approve or reject a member change;
- record a payment or adjustment;
- change file permissions;
- publish, archive, or delete a file;
- change a role.

Initial AI releases should not expose destructive tools. Tool writes use idempotency keys
so retries cannot duplicate payments, approvals, or uploads.

The agent receives narrowly scoped tools, not arbitrary SQL, filesystem, shell, HTTP, or
email access.

### Tool and Conversation Auditing

Record:

- conversation and turn identifiers;
- authenticated user;
- endpoint and model identifier;
- offered tool names and versions;
- requested tool name and validated arguments, with sensitive-value redaction;
- authorization decision;
- affected resource identifiers;
- confirmation and final outcome;
- source document identifiers used in an answer.

Conversation transcripts may themselves contain private data. Apply access control,
retention, export, and deletion rules to them. Avoid retaining complete prompts or tool
results when a smaller audit record is sufficient.

Removing access to a file or record prevents future retrieval. Previously stored chat
content must not be treated as an authorization cache or used to bypass current access.

### Initial AI Capabilities

1. Conversational search across authorized meeting minutes, policies, forms, and
   calendar information with citations to source records.
2. Agenda suggestions based on prior minutes, incomplete actions, and upcoming events.
3. An annual recurring tracker identifying activities from prior years and whether they
   have been addressed this year.
4. Member assistance that explains permitted information and prepares a correction
   request for officer approval.
5. Treasurer questions and reports using authorized, deterministic tools.

Financial totals and membership counts come from reviewed database queries, not model
memory or arithmetic. Answers distinguish sourced facts from suggestions and inferences.
Imported documents are untrusted data, not instructions to the agent.

## Groups.io and Calendar

- Treat Groups.io as the initial calendar source of truth.
- Continue consuming the public iCal feed used by the BCARS website.
- Do not duplicate calendar editing in the portal MVP.
- Import membership and ACS exports through staged reconciliation.
- Preserve Groups.io external identifiers when available.
- Begin mailing-list integration with discrepancy reports and dry runs.
- Add automatic invitations, role changes, or two-way calendar writes only after
  credential storage, conflict handling, retries, and ownership rules are established.

## Delivery Plan

### Phase 0 - Decision Register

Phase 0 should resolve decisions that affect the Phase 1 schema or security model. File
library and AI-provider details can remain deferred because the API-first boundary keeps
those additions open.

| Decision | Status | Current direction or question |
| --- | --- | --- |
| Implementation baseline | Decided | Fresh implementation; archive is reference only. |
| Capability architecture | Decided | API-first application services; UI and future tools are adapters. |
| Family relationships | Decided | Optional context only; no automatic delegated edit rights. |
| Shared email | Decided | Allowed for member contact; contact email is not identity. |
| Member-originated updates | Decided | Suggestions require officer review before changing canonical data. |
| Initial payment channels | Decided | Design for officer-recorded cash and checks; online payment is later. |
| Calendar authority | Decided | Keep Groups.io as source of truth and consume its iCal feed. |
| Membership authority | Decided | The portal becomes authoritative after the reconciled import; Groups.io is a downstream mailing-list and calendar service. |
| Membership types and rights | Decided | Full requires a valid FCC license and has voting and membership-list rights. Associate has neither right. Honorary is a fixed-term or lifetime dues waiver layered on Full or Associate. |
| Membership approval | Decided | Officers approve new members and the Full or Associate type; record an explicit approval even though denials are historically rare. |
| Annual dues | Decided | Current rate is $20 per year. Store an effective-year rate rather than hard-coding it. |
| Paid-through rules | Decided | Treasurer explicitly chooses paid-through and may prorate, extend through the following year, or accept multiple years. Suggestions are allowed; rigid automatic allocation is not. |
| Payment and coverage history | Decided | Record money received separately from paid-through coverage events, with an audit link when related. |
| Lifetime honorary import | Decided | The two 2055 entries are lifetime honorary Associate members. Migrate to explicit lifetime grants and retain the service rationale as restricted notes. |
| Member status lifecycle | Decided in principle | Use pending, approved, rejected, inactive, resigned, and deceased for record lifecycle; derive current, expired, lifetime, honorary-waived, and unknown dues standing separately. |
| Member directory/contact-list visibility | Decided in principle | Active Full members can use the directory. Full and Associate members appear by name/call sign; Full-member contact follows sharing preference, while Associate contact is restricted by default with explicit opt-in. Associates cannot browse the directory. |
| ACS/ARES sharing | Decided | Verified Full members share name/call sign with ACS/ARES by default unless they opt out. Associate participation is explicit. |
| Officer capability matrix | Decided in principle | President, vice-president, secretary, and treasurer receive broad member-management access. Trustee, activities manager, and other officers receive narrower grants. Exact treasury and audit capabilities remain explicit. |
| System administration | Decided in principle | A separate webmaster/system-administrator role manages operations and live integration configuration without automatically receiving every club-governance capability. |
| Officer authentication | Decided | Password plus verified email; recovery by short-lived email link or code. SMS verification may be added later. |
| Member email-link access | Proposed | Optional and passwordless; not required for the administrative MVP. |
| Blind public correction form | Open | Decide whether unauthenticated users may submit a suggestion without seeing existing data. |
| Import authority | Decided | Use Groups.io JSON as the primary initial source because it preserves row IDs, with CSV as a cross-check. |
| Hosting and database | Decided for Phase 1 | Use a persistent single application instance with SQLite and encrypted off-site backups. Revisit PostgreSQL only if hosting or scaling requires it. |
| Transactional email | Decided in principle | Prefer a dedicated sender on Google-hosted bcars.org email, with qsl@bcars.org as Reply-To if desired. Verify the available Google sending method; evaluate Mailgun or another provider only as fallback. |
| Backup ownership | Decided | The appointed webmaster owns backup monitoring and restore tests; currently John Hogenmiller. |
| Retention | Deferred with interim rule | Informal practice removes former members from the contact list after two years. Do not automatically destroy canonical, payment, or audit records until a formal retention policy distinguishes those datasets. |
| Files and AI providers | Deferred | Preserve architectural seams now; select storage and model endpoints in later phases. |

Phase 1 may start with the decided architecture above. The exact Google sending method
and precise executive/treasury capability matrix should be resolved before email recovery
and authorization stories are accepted. The remaining decisions can stay open behind
disabled or future-facing capabilities.

### Phase 1 - Administrative Membership MVP

#### Outcome

An authorized officer can sign in, preview and reconcile the current Groups.io contact
export, establish the portal's canonical member records, manage those records through
both the API and a simple administrative UI, view the audit trail, and restore the
system from backup.

Phase 1 validates the architecture on membership administration. It does not implement
the AI runtime or file library, but every operation is ready for a future tool adapter.

#### Workstream 1: Repository and Engineering Foundation

- Initialize the private Git repository with a clear README, license decision, and
  development instructions.
- Add ignores and automated secret/PII checks for databases, exports, uploads, extracted
  text, credentials, logs, backups, and AI transcripts.
- Establish Go module layout, configuration loading, structured logging, migrations,
  sqlc generation, formatting, linting, tests, and CI.
- Add architecture decision records for database choice, authentication, authorization,
  API conventions, audit behavior, and deployment.
- Provide fake member/import fixtures that contain no real BCARS information.

#### Workstream 2: API and Application Contracts

- Define versioned `/api/v1` conventions and generate or validate an OpenAPI document.
- Standardize resource identifiers, timestamps, pagination, filtering, sorting,
  validation errors, request IDs, and authorization errors.
- Require optimistic concurrency for member updates so one officer cannot silently
  overwrite another officer's changes.
- Require idempotency keys for import commits and other retryable commands.
- Implement application services independently of HTTP and HTML rendering.
- Maintain a capability catalog recording the operation name, input/output schema,
  required permission, audit event, confirmation level, and whether it is eligible for
  future AI tool exposure.

The initial API surface should cover:

- session identity and current capabilities;
- member search, list, detail, create, update, deactivate, and reactivate;
- contact-method create, update, make-primary, and archive;
- member-directory and ACS/ARES sharing-preference maintenance;
- membership application, approval, rejection, lifecycle, and Full/Associate type;
- Full-member FCC license verification;
- honorary grant create, update, expire, and revoke;
- dues rate and paid-through coverage maintenance within the agreed rules;
- permissioned member, honorary, coverage, and payment-context notes;
- import upload, validation, reconciliation preview, commit, and result report;
- authorized member export;
- audit-event search for authorized officers;
- health and readiness checks that disclose no private data.

Hard deletion of members is not part of Phase 1.

#### Workstream 3: Database and Migration Baseline

- Create reviewed migrations for people, contact methods, memberships, external IDs,
  users, sessions, roles, capabilities, role grants, import runs, staged import rows,
  reconciliation decisions, membership approvals, FCC verifications, dues rates,
  honorary grants, coverage events, categorized notes, and audit events.
- Store audience/visibility metadata on contact methods and dated ACS/ARES sharing
  preferences rather than one irreversible global privacy flag.
- Keep login identities separate from people and contact methods.
- Permit shared contact values while retaining normalization helpers for matching.
- Use stable internal IDs and preserve Groups.io row IDs as external identifiers.
- Add created, updated, deactivated, and version fields required for auditing and
  optimistic concurrency.
- Enforce foreign keys and database constraints in production and tests.
- Seed capabilities explicitly; do not infer full administrative access merely from an
  officer title.

#### Workstream 4: Officer Authentication and Authorization

- Implement officer invitation, password sign-in, email verification, logout, session
  expiration, and short-lived email-link or verification-code recovery.
- Integrate the selected Google-hosted BCARS sending method behind an email service
  interface so a fallback transactional provider does not change recovery workflows.
- Bootstrap the first administrator through a documented one-time procedure that does
  not ship a default password.
- Authorize every application operation and target resource through one policy layer.
- Seed broad member-management grants for president, vice-president, secretary, and
  treasurer; seed narrower starting grants for trustee and activities manager.
- Keep webmaster/system-administrator, integration configuration, treasury, audit, and
  club-office capabilities independently assignable.
- Return minimal, consistent forbidden responses without leaking whether inaccessible
  member or audit records exist.
- Audit sign-in, recovery, capability grants, denied sensitive operations, exports, and
  administrative record changes.

#### Workstream 5: Staged Groups.io Import

- Accept the current Groups.io JSON and CSV export formats without exposing raw uploads
  through public storage.
- Normalize dates, call signs, email addresses, phone numbers, membership values, and
  checkbox values while retaining the original staged values for review.
- Normalize `Full` and `full` to Full. Treat imported Honorary as a dues-waiver indicator
  and require an officer to choose the underlying Full or Associate type for each row.
- Flag directory-contact and ACS/ARES preferences as legacy/defaulted or needing review,
  because the current Groups.io list does not preserve the required audience separation.
- Convert `01/01/0001` to unknown/null. Convert the two known `12/31/2055` honorary rows
  to lifetime honorary Associate grants; require an explicit reconciliation decision if
  any additional lifetime-like dates appear.
- Match in order by preserved external ID, normalized call sign, normalized email, and
  finally manual review. Names alone are never an automatic match.
- Show creates, updates, unchanged rows, conflicts, validation failures, and proposed
  field-level changes before commit.
- Save officer reconciliation decisions and make import commit transactional,
  idempotent, and auditable.
- Produce a result report without placing real member data in application logs.

#### Workstream 6: Administrative Member Operations

- Search and filter by call sign, name, membership type, membership status, and data
  quality flags.
- View a member timeline containing imports and administrative changes, without payment
  details until Phase 2.
- Create, update, deactivate, and reactivate member records.
- Record officer approval or rejection, approved membership type, and manual FCC
  verification for Full members.
- Create and expire honorary grants without changing the underlying membership type.
- Add categorized, permissioned notes to member, approval, honorary, and coverage records
  while preserving author and edit history.
- Manage multiple and shared contact methods without treating email as identity.
- Manage per-contact directory visibility and ACS/ARES sharing preferences.
- Explain validation failures and concurrent-edit conflicts in officer-friendly terms.
- Export only fields permitted by the requesting officer's capabilities and record the
  export in the audit log.

#### Workstream 7: Administrative UI

- Build a small accessible UI over the same application operations and contracts.
- Provide dashboard links for member search, import review, data-quality issues, and
  recent audit activity.
- Do not add UI-only mutations or hidden business rules.
- Ensure common workflows work with keyboard navigation and clear, plain-language
  confirmation and error messages.

#### Workstream 8: Operations and Recovery

- Package one reproducible deployment with health and readiness checks.
- Document configuration, secret rotation, upgrade, rollback, backup, and restore.
- Back up the database encrypted off site and verify a restore into an isolated
  environment.
- Assign automated backup monitoring and periodic restore verification to the appointed
  webmaster, currently John Hogenmiller.
- Define log retention and redact contact values, import rows, session secrets, and
  authentication tokens.
- Provide a documented officer handoff checklist.

#### Phase 1 Verification and Acceptance

Phase 1 is complete when:

- a clean environment can migrate from zero and bootstrap its first administrator;
- an officer can authenticate and an unauthorized user cannot access member APIs;
- the 62-row contact export can be staged without directly changing canonical data;
- the import preview identifies creates, updates, conflicts, sentinel dates, and manual
  decisions before an officer commits it;
- the two known 2055 Honorary rows become lifetime honorary Associate grants, while any
  other Honorary row requires an explicit Full or Associate classification and waiver
  period;
- the committed import has no orphan login identities and permits shared emails;
- officers can search, create, update, deactivate, reactivate, and export members through
  both documented API operations and the administrative UI;
- concurrent edits and repeated import commits cannot silently overwrite or duplicate
  data;
- every sensitive read, export, import commit, and mutation produces an audit event;
- authorization tests cover allowed and denied cases for each operation;
- the OpenAPI contract and capability catalog are checked in CI;
- a backup can be restored successfully and the restore procedure is understandable by
  another officer;
- no real member data, credentials, database, backup, or sensitive log output is tracked
  by Git.

#### Explicitly Out of Phase 1

- member sign-in and self-service;
- member-facing directory browsing;
- member change-request submission and approval UI;
- payment ledger and treasurer workflows;
- ACS resource management;
- file uploads and document search;
- AI endpoint integration, chat UI, and actual model tool registration;
- Groups.io write-back or mailing-list automation;
- portal calendar editing;
- officer elections, event attendance, QRZ, and online payments.

### Phase 2 - Treasurer Workflow

The implementation contract and focused dependency plan are now recorded in
`docs/phase-2-design.md` and `docs/phase-2-plan.md`. Beads is the live source of
truth for story status and dependencies.

- Add the payment ledger for cash and checks, including batches, receipts, reversals,
  and officer attribution.
- Record received date, payment method, and contextual treasurer notes on each payment.
- Let the treasurer explicitly apply or adjust paid-through coverage independently of
  the amount received.
- Offer non-binding suggestions for $20 annual dues, partial-year amounts, and multi-year
  payments without preventing treasurer discretion.
- Support examples such as $10 through the current year, $30 through the following year,
  and one payment covering several full years.
- Add current, expiring, expired, lifetime, and unknown-status views.
- Include honorary-waived standing and show the underlying Full or Associate rights.
- Allow categorized notes for proration, multi-year coverage, honorary grants, unusual
  payment circumstances, and other explanations without using prose as calculation input.
- Add treasurer exports and member-visible summary status.
- Verify that non-treasurer roles cannot retrieve payment details.

### Phase 3 - Member Requests and Optional Access

- Implement officer-entered requests from telephone, mail, and meetings.
- Add public blind correction suggestions that reveal no existing private data.
- Add optional email-link member access.
- Add the Full-member directory with server-filtered name, call sign, and permitted
  contact methods; Associates do not receive directory browsing access.
- Let members change contact-sharing and ACS/ARES preferences through the normal
  officer-review workflow.
- Implement per-field review, approval, rejection, and verification notes.
- Add optional informational family relationships without delegated edit permission.

### Phase 4 - Permissioned Files

- Implement private storage, metadata, authorization, versioning, archive, and audit.
- Import and classify existing meeting minutes and selected Groups.io files.
- Add authorized full-text search and source links.
- Test permission changes, revocation, direct URL access, and restricted downloads.

### Phase 5 - Read-Only AI Pilot

- Configure the OpenAI-compatible adapter and endpoint capability checks.
- Implement permission-aware read tools and cited search.
- Pilot agenda suggestions and the annual recurring tracker with officers.
- Test prompt injection, excessive data disclosure, role changes during conversations,
  unavailable sources, provider failure, tool retries, and transcript retention.
- Keep the pilot read-only except for drafts that have no authoritative effect.

### Phase 6 - AI Workflow Tools

- Add member change-request preparation and explicit submission.
- Add authorized treasurer questions backed by deterministic queries.
- Add confirmed officer review tools only after authorization and audit tests pass.
- Evaluate usability with both active and infrequent portal users.

### Phase 7 - ACS and Groups.io Integration

- Import ACS capabilities and certifications through staged call-sign reconciliation.
- Add member suggestions and ACS coordinator verification.
- Add mailing-list discrepancy reports and carefully scoped synchronization.

### Phase 8 - Extended Club Operations

- Officer history, nominations, and ballots.
- Event and activity records with attendance, documents, and photos.
- Optional QRZ integration.
- Reconsider online payments and calendar editing only when there is a demonstrated need.

## Definition of Done for Any Feature

A feature is not complete until it has:

- server-side authorization tests for allowed and denied roles;
- audit events for sensitive reads and changes;
- validation and safe error behavior;
- backup and recovery consideration;
- an accessible non-AI workflow;
- tests showing the AI cannot exceed the logged-in user's authority, when tools apply;
- documentation suitable for the next volunteer officer.
