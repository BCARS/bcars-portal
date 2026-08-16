-- +goose Up

-- What the officer applied, recorded next to what the member proposed
-- (bcars-portal-ssz.1, ADR-0014).
--
-- WHY THE TWO ARE NOT THE SAME FACT
--
-- Until now they could not differ. An officer answered yes or no to the string
-- the member sent, so approval applied that string and the proposal was a
-- complete record of what happened. ADR-0014 lets the officer amend the value
-- while approving it -- a member sends their new address with one character
-- mistyped, the officer drops the stray character and applies -- and the moment
-- that is possible, proposed_value stops being an answer to "what changed".
--
-- No example address appears in this file. check-no-secrets.sh rejects an
-- email-like literal in any tracked non-test file, the same rule that shaped
-- the comment on parseContactValue, and it is right to: a real one reaches
-- production source exactly the way an illustrative one does.
--
-- Both are kept. proposed_value is what the member asked for and is never
-- rewritten; applied_value is what reached the record. A member reading their
-- own suggestion afterwards is entitled to see both, because they may differ
-- and the difference is the officer's, not theirs.
--
-- WHY OLD ROWS ARE LEFT NULL RATHER THAN BACKFILLED
--
-- It is tempting to copy proposed_value into applied_value for rows already
-- applied, on the reasoning above: before this change the applier had no way to
-- write anything else, so the two were equal by construction. That inference is
-- lossy in one case. A contact item's proposed_value carries a "kind:value"
-- prefix that the applier strips, so a copy would record "phone:814-555-0199"
-- as the value that reached the record, which is not a telephone number and
-- never was in that column's meaning.
--
-- So NULL here means "applied before the portal recorded this", which is true,
-- rather than a reconstruction that reads as fact. Every row written from now
-- on carries a value -- the empty string for the operations that set no value,
-- such as making a contact primary -- so NULL keeps meaning only that.
--
-- That invariant is not a CHECK. SQLite cannot add one without rebuilding the
-- table, and a constraint that every already-applied row violates would abort
-- the migration on any database with review history. It is held by the applier
-- and by its test instead.
--
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema.

ALTER TABLE member_change_request_items
    ADD COLUMN applied_value TEXT;

-- +goose Down

ALTER TABLE member_change_request_items DROP COLUMN applied_value;
