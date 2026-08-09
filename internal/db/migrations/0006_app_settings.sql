-- +goose Up

-- Small key/value table for instance-level facts that must survive restarts
-- and be compared against the running configuration.
--
-- Its first use is the password pepper fingerprint. Argon2id hashes carry no
-- record of which pepper produced them, so a pepper that is changed or lost
-- makes every password verification fail with "invalid credentials" — a
-- message indistinguishable from a user typing the wrong password, on every
-- account at once. Recording a fingerprint turns that silent, total, and
-- deeply confusing outage into a refusal to start with an explicit message.
CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down

DROP TABLE IF EXISTS app_settings;
