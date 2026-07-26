# ADR-0006: Preference history pattern for sharing preferences

- Status: Accepted
- Date: 2026-07-26

## Context

`PLANNING.md` requires that contact-method audience visibility and ACS/ARES
sharing be modelled as dated preferences with a source and actor, not as a
single irreversible privacy flag. The Groups.io import has to plant a
"legacy default" for both without pretending the member consented.

## Decision

Use one append-only "preference event" pattern for both:

- `contact_method_visibility_events(contact_method_id, audience, source,
  effective_at, actor_user_id, note)`
- `acs_ares_sharing_events(person_id, participates, source, effective_at,
  actor_user_id, reason)`

Current value = latest row for the subject; the domain layer computes a
type-based default when no row exists. Writes only insert.

## Rejected alternatives

- **Boolean column on `contact_methods`**: loses history and provenance;
  makes "why is this hidden?" unanswerable.
- **Generic EAV preferences table**: harder to enforce per-preference
  domain rules and validation.

## Consequences

- Two extra tables and one small "latest per subject" view.
- Importer's `source=import_default` events are trivially distinguishable
  from real consent, which drives the "sharing preference needs review"
  officer UI flag (WS5.6, WS7.4).
