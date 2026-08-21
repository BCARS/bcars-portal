-- +goose Up

-- Marking a note done, and saying who did it (bcars-portal-ssz.6, ADR-0014.4).
--
-- WHY A NOTE NEEDS THIS AT ALL
--
-- A report about a record the sender cannot see carries no structured change:
-- it is a sentence an officer reads and acts on by editing the record. Nothing
-- about that act touches the request, so without a way to close it the pile
-- only grows, and next month's officer re-reads notes that were dealt with
-- weeks ago to work out whether they were dealt with.
--
-- The state already exists -- member_change_requests.status has 'resolved', and
-- resolved_at records when. What was missing is WHO, and what they did about it.
--
-- resolved_by is the officer who closed it. The audit trail already knows, but
-- the queue cannot show a name it has to join the audit log to find.
--
-- resolution_note is optional on purpose. One line -- "added the new mobile" --
-- is what stops the next officer opening the record to work out what happened;
-- requiring it would turn a note into paperwork, which is the ceremony this
-- design is removing.
--
-- WHAT THIS IS NOT
--
-- It is not a per-item outcome. An item's decision stays on the item, where a
-- reason is already required for a rejection because the member reads it. This
-- is the REQUEST saying an officer is finished with it.
--
-- Neither column is constrained to be present when status is 'resolved'.
-- Requests resolved before this migration have neither, and a CHECK that every
-- existing row violates would abort on any database with review history. NULL
-- here means "resolved before the portal recorded this", the same convention
-- migration 0016 uses for applied_value.
--
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema.

ALTER TABLE member_change_requests
    ADD COLUMN resolved_by INTEGER REFERENCES users(id);

ALTER TABLE member_change_requests
    ADD COLUMN resolution_note TEXT;

-- +goose Down

ALTER TABLE member_change_requests DROP COLUMN resolution_note;
ALTER TABLE member_change_requests DROP COLUMN resolved_by;
