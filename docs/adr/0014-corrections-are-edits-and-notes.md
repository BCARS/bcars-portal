# ADR-0014: Corrections are edits on records you can see, and notes about everyone else

- Status: Accepted
- Date: 2026-08-16
- Supersedes parts of ADR-0013 (see "Relationship to ADR-0013")

## Context

Phase 3 shipped a correction workflow built around one shape: a member picks a
single field, types one replacement value, and an officer answers yes or no.
A walkthrough of the running portal on 2026-08-16 found that shape failing at
both ends.

At the member's end, one field per submission is not how a correction arrives.
A member who has moved house has a new address and a new telephone number and
sends two suggestions, or gives up and sends a note. At the officer's end, yes
or no is not how a review ends. A member writes "my email is
joeschmoe@demo.local" with a stray character; the officer can see what was
meant, and today can only reject it and ask the member to send it again.

The machinery underneath had also collapsed in two places. Member-submitted
contact corrections could never be approved at all (`bcars-portal-b4d`, fixed).
Corrections about someone else still cannot: those items carry no target by
design, so an officer who links the request is told to link the request
(`bcars-portal-3la`). That second failure is not a loose end to tidy — it is
what a design produces when it asks a form to propose a structured change to a
record the submitter is not allowed to see, and then asks an officer to resolve
the resulting ambiguity through a queue.

## Decision

1. **An officer editing a member record edits it.** No proposal, no queue, no
   self-approval step. This is unchanged and is stated here because it is the
   baseline the rest is measured against: the review queue exists for people who
   cannot write to the record, not as ceremony around people who can.

2. **A member who can see a record gets an edit form for it.** Self, or any
   record granted to their account. The form mirrors the record they are already
   permitted to read: name, call sign, and the current value of each contact
   detail on file. It proposes; it does not write. Submitting creates one
   reviewed request carrying one item per field the member actually changed.

3. **The member's edit form changes existing values only.** Adding a contact
   detail or removing one goes in the note box, and an officer does it directly.
   A form that can create and archive rows has to be reviewed by a screen that
   can apply creations and archives, and neither the club nor the record needs
   that to correct a wrong digit.

4. **Everything else is a note to the officers.** A member reporting something
   about a person whose record they cannot see writes what they know — "Joe got
   a new cell phone, it's 814-555-0199" — and that is the whole submission. It
   carries no items, proposes no structured change, and needs no target
   resolution. An officer reads it and edits the record directly, which is
   exactly what they would do with the same sentence heard at a meeting.

5. **Review is one editable form, applied once.** The officer sees each proposed
   field beside the value currently on file, editable, with a tick to include
   it. They may correct what the member typed. Unticking a field declines it.
   One action applies everything ticked.

6. **What the member proposed is not overwritten by what the officer applied.**
   The item keeps the member's original value; the applied value and the officer
   who set it are recorded next to it. A member reading their own suggestion
   afterwards sees what they asked for and what was done, and those may differ.

## Relationship to ADR-0013

ADR-0013 stands except where this ADR narrows it.

- **Retained.** Submission still requires authentication; there is still no
  public or anonymous correction form (ADR-0013.1). A member may still report a
  correction about a person they cannot see, including an Associate about a
  household member, with no access grant and no recorded relationship
  (ADR-0013.2) — the family-helper case that ADR-0013 was written to protect
  keeps working. Submission still confers no read authority (ADR-0013.3): a
  member learns nothing about a target from sending, tracking, or being refused.
  Members still cannot write canonical data (ADR-0013's rejected "allow direct
  member edits") — an edit form is a proposal, and officer review remains the
  only application path.
- **Narrowed.** A report about a record the submitter cannot see is now a note
  rather than a structured item (ADR-0013.2, ADR-0013.4). Structured items exist
  only where the submitter could already read the record, so an item always
  arrives with its target already known. Unresolved target hints stop being an
  input to applying anything; they remain useful as text an officer reads.
- **Amended.** ADR-0013.6 said the per-field officer review is the only
  application path, which remains true, but that review is no longer confined to
  accepting or rejecting the submitted string verbatim.

## Rejected alternatives

- **Keep the single-field form and fix the target machinery.** This was the
  available small change: give items a target fallback and add a contact-method
  chooser to the review screen. It makes the broken path work without asking why
  an officer is being made to reconstruct, through a queue, a decision they could
  express by opening the record and typing.
- **Let members write directly to records they can see.** Removes the officer
  confirmation that makes broad submission safe, and ADR-0013 rejected it for
  that reason. Nothing here disturbs that.
- **Officer edits go through the queue too, for a uniform audit trail.** The
  audit trail already records officer edits. Making an officer approve their own
  typing adds a step that always ends the same way.
- **Free-text notes only, no structured items at all.** Simplest possible model,
  and it throws away the one thing the structured path does well: for a record
  the member can see, the portal knows which field and which row is meant, and
  an officer should not have to retype a value the member already typed
  correctly.

## Consequences

- The member correction form becomes an edit form over the visible record.
  `bcars-portal-245` reduced that form to a single question to fix a real defect
  — radios choosing a field beside an ungoverned dropdown. This does not
  reintroduce it: an edit form has no "which field do you mean" question,
  because every field is on screen with its own value in it.
- The review screen gains editable values, per-field inclusion, and one apply
  action. `bcars-portal-2c4` (show the value being replaced) is absorbed: the
  current value is on the screen because that is what the officer is editing
  away from.
- `bcars-portal-3la` is closed as obsolete rather than fixed. The path it
  describes stops existing: a suggestion about someone else no longer carries an
  item to apply.
- `bcars-portal-4ux.18` (record picker) survives but changes character. Linking
  is no longer required to apply anything, so it stops being a blocker and
  becomes what it always should have been — a way to file a note against the
  person it concerns.
- `member_change_request_items` needs the applied value and the applying officer
  alongside the proposed value. Existing rows have no applied value and must
  read as such rather than as "applied exactly what was proposed".
- Per-field sensitivity policy still applies to a multi-field apply: if any
  ticked field is sensitive, the apply carries the verification note, and an
  officer still cannot apply their own sensitive request.
- Item-level concurrency is unchanged in intent: the form carries the version
  the member saw, and a record edited since is a conflict the officer is shown
  rather than one that silently overwrites.
