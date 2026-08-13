-- +goose Up

-- Text size is a per-user accessibility preference, not a per-browser one
-- (bcars-portal-6pz.1).
--
-- WHY THE USER ROW AND NOT A COOKIE OR localStorage
--
-- Officers share machines. The treasurer who needs 22pt type sits down at the
-- same club laptop the secretary just used, so a preference stored against the
-- browser is a preference stored against the wrong person: it would follow
-- whoever sat down next and would be lost the moment that officer signed in
-- from anywhere else. Storing it on the user row means the choice is carried by
-- the account and applied server-side on the first render, so the page never
-- paints at one size and resizes to another.
--
-- This is UI chrome, not member data. It carries no preference history and is
-- deliberately outside the immutable preference-history pattern that member
-- consent and contact preferences use: there is no audit question that
-- "which type size was this officer using in March" answers.
--
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema.

ALTER TABLE users
    ADD COLUMN text_size TEXT NOT NULL DEFAULT 'base'
    CHECK (text_size IN ('base', 'large'));

-- +goose Down

ALTER TABLE users DROP COLUMN text_size;
