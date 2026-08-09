-- Request-attempt log backing the reusable abuse limiter.
--
-- Every attempt is recorded regardless of outcome and regardless of whether the
-- target exists, so a count here never depends on an answer the endpoint is
-- trying not to reveal.
--
-- Keep every comment in this file ASCII: sqlc substitutes sqlc.arg() by byte
-- offset, so a multi-byte character above a query corrupts the SQL it parses.

-- name: RecordRequestAttempt :one
INSERT INTO request_attempts (operation, source_hash, target_hash, outcome, attempted_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: CountRequestAttemptsBySource :one
--
-- Attempts from one source within the window, whatever their outcome. Counting
-- limited attempts too means sustained hammering keeps extending the block
-- rather than draining it, which is the intended behaviour for abuse.
SELECT count(*) FROM request_attempts
 WHERE operation = ?
   AND source_hash = ?
   AND attempted_at >= ?;

-- name: CountRequestAttemptsByTarget :one
SELECT count(*) FROM request_attempts
 WHERE operation = ?
   AND target_hash = ?
   AND attempted_at >= ?;

-- name: DeleteRequestAttemptsBefore :exec
--
-- Pruning. The table is an abuse control, not a retention record; rows older
-- than the longest window answer no question.
DELETE FROM request_attempts WHERE attempted_at < ?;
