-- +goose Up

-- Reusable request-attempt log for abuse limiting (bcars-portal-fmc.20).
--
-- WHY A SEPARATE TABLE, NOT A COUNT OVER email_links
--
-- email_links only gets a row when the address belongs to a real active user:
-- RequestRecovery returns early for an unknown address and writes nothing. A
-- limiter counting that table would therefore bound known addresses and leave
-- probing traffic unbounded, and the difference between "bounded" and
-- "unbounded" would itself answer the question the always-204 response exists
-- to hide. Every attempt is recorded here BEFORE anything looks up whether the
-- target exists, so the count cannot depend on the answer.
--
-- WHY THE TARGET IS HASHED
--
-- The rows for unknown addresses are, by definition, addresses of people who
-- are not members. Storing them in plaintext would turn an abuse control into
-- a log of everyone anyone ever probed. A keyed hash still groups repeat
-- attempts against one address without recording the address.
--
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema.

CREATE TABLE request_attempts (
    id           INTEGER PRIMARY KEY,
    -- operation namespaces the limit. Recovery is the first consumer;
    -- member sign-in and blind correction intake reuse this table rather than
    -- adding parallel limiters.
    operation    TEXT    NOT NULL CHECK (length(trim(operation)) > 0),
    -- source_hash is the clientip HMAC of the caller address. NULL when the
    -- deployment configured no secret, or the address was unavailable: an
    -- unknown source must not become a shared bucket that lets one caller
    -- exhaust everybody else's allowance.
    source_hash  TEXT,
    -- target_hash is the keyed hash of the normalized target (an email address
    -- for recovery). NULL when the request named no target.
    target_hash  TEXT,
    -- outcome records what the limiter decided, so a denial is visible here
    -- even though the audit trail is written separately by the transport.
    outcome      TEXT    NOT NULL CHECK (outcome IN ('allowed', 'limited')),
    attempted_at TEXT    NOT NULL,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- The two counting queries. Both are covered so a window count stays an index
-- range scan as the table grows.
CREATE INDEX ix_request_attempts_source
    ON request_attempts(operation, source_hash, attempted_at)
    WHERE source_hash IS NOT NULL;
CREATE INDEX ix_request_attempts_target
    ON request_attempts(operation, target_hash, attempted_at)
    WHERE target_hash IS NOT NULL;
-- Supports pruning rows older than the longest window.
CREATE INDEX ix_request_attempts_age ON request_attempts(attempted_at);

-- +goose Down

DROP TABLE IF EXISTS request_attempts;
