# ADR-0015: An imported contact detail is published to the member directory

- Status: Accepted
- Date: 2026-09-17
- Builds on ADR-0006 (preference history pattern)

## Context

The member directory query treats an absent visibility decision as permission
to publish: when a contact method has no `contact_method_visibility_event` on
file, it is listed if the person's membership base type is `full`. Nothing ever
wrote such an event, so in practice every contact method reached the directory
through that fallback.

A walkthrough on 2026-08-16 made the consequence concrete. Committing a 21-row
Groups.io import put 20 members into the directory with their email addresses
and telephone numbers visible immediately. None of them had recorded any
preference, because none of them had ever used the portal
(`bcars-portal-v5j`).

Two things made that more than a choice of default:

1. The directory told members it showed "the contact details each one has
   agreed to share". For an imported member with no event on file that sentence
   was false.
2. The member could not find out what was happening. Their own record said
   "Shared with: Club default", named no rule and offered no control; only an
   officer holding `sharing_pref.write.officer` could change it.

The Phase 1 design had already anticipated the importer planting
`source=import_default` events. The importer never did, so the rule lived in a
SQL `CASE` branch and nowhere else.

## Decision

**A Groups.io roster is a list of people who have already shared these details
with the club, so the club publishes them.** An imported Full member's email
address and telephone number appear in the member directory. The owner decided
this on 2026-08-16; it is recorded here rather than left implicit.

Three things follow, and they are the substance of this ADR:

- **The importer records the decision.** Every contact method it creates gets a
  `contact_method_visibility_event` with `source=import_default`, the
  committing officer as `actor_user_id`, and a note saying this is a club
  default and not the member's own choice. The audience reproduces what the
  NULL fallback already did, so committing an import changes nobody's
  visibility — it only makes the reason for it readable.
- **A postal address is hidden regardless of membership type.** The directory
  has never listed one, and the decision above was about the details a roster
  already circulated among its members. A home address is not one of them.
- **The copy says what actually happens.** The directory and the member's own
  record describe what the club lists, not what each member agreed to share,
  and a record showing the club default now states what that default resolves
  to instead of naming it.

The NULL fallback in `ListDirectoryEntries` stays as written. It is the
behaviour for every contact method created before this ADR and by every path
other than import, and removing it would be a silent change to who is listed.

## Consequences

- The audit trail answers "who decided this, and when" for imported details.
  It answers it with the import, honestly: an officer committed a roster, and
  no member was asked.
- `import_default` remains distinguishable from real consent, which is what
  ADR-0006 built it for. A future "sharing preference needs review" queue can
  find exactly these rows.
- Imports committed before this ADR keep their NULL rows and their current
  visibility. Backfilling them is tracked separately; a backfill can only
  record that the club decided, not who or when.
- A member still cannot change their own sharing preference directly. Telling
  them the rule is the part this ADR fixes; the control is described below.

## Update: a member can ask to change it (bcars-portal-qku)

The member's correction form now offers, for each email address and telephone
number, "List this in the member directory?" with two answers. Changing it
files a `contact_method.visibility.set` item, which an officer reviews and
applies like any other correction; the resulting event records
`source=member_request`. A postal address gets no choice, because the directory
never lists one.

It is a proposal rather than a switch for the same reason every other field on
that form is (ADR-0013, ADR-0014): only an officer changes canonical data, and a
visibility decision is canonical data the directory reads.

The form offers two answers, not three. `hidden` and `officers_only` both keep
a detail out of the directory, so to a member they are one answer, and leaving
an officers-only detail on "No" proposes nothing. An officer reviewing the item
still chooses among all three.

Audiences are now a closed set enforced in the domain, at filing and at apply.
Before this, only the HTTP API's enum held it; an officer amending a reviewed
value could have recorded any string, which the directory would have read as
"not `full_members`" and hidden without anyone choosing that.

## Rejected alternatives

- **Hide imported details until the member opts in.** Truthful, and it would
  have emptied the directory of everyone who had not yet signed in — which is
  most of the club. The directory's purpose is to let members reach each other,
  and a directory of the few who have logged in does not serve it.
- **Leave the fallback as the only statement of the rule.** This is the status
  quo the walkthrough found. It works and it is unreadable: nothing outside one
  SQL `CASE` branch says what the club decided.
