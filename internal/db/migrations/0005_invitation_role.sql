-- +goose Up

-- An invitation must carry the role it is intended to confer, so that
-- consuming it produces a user who can actually do the job they were invited
-- to do. Without this, portalctl bootstrap-admin creates an invitation whose
-- consumption yields an account with no capabilities at all, and a fresh
-- installation can never produce a working administrator.
--
-- NULL means "no elevated role" — an ordinary invitation. Only the bootstrap
-- path and an explicit officer invitation set it.
ALTER TABLE email_links ADD COLUMN intended_role_code TEXT REFERENCES roles(code);

-- +goose Down

ALTER TABLE email_links DROP COLUMN intended_role_code;
