-- +goose Up

-- Member self-service is password authentication, not a passwordless link
-- (bcars-portal-4ux.5, ADR-0012).
--
-- WHY A MIGRATION RATHER THAN AN EDIT TO 0009
--
-- 0009_member_requests_access.sql set the member role description to "Club
-- member with optional passwordless self-service access." while the plan still
-- called for magic-link sign-in. That row is live data an operator can read in
-- a deployed database, so correcting it is a schema change, not a comment fix:
-- editing the applied migration in place would leave every existing
-- installation describing a mechanism the portal does not implement, and would
-- silently rewrite what 0009 actually did. The statement below is idempotent
-- and matches only the stale text, so a database whose description was already
-- corrected by hand is left alone.
--
-- Nothing about authority changes here. The member role keeps exactly the
-- capabilities 0009 gave it, and record visibility still comes only from
-- member_access_grants (ADR-0010).
--
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema.

UPDATE roles
   SET description = 'Club member with optional password self-service access.'
 WHERE code = 'member'
   AND description = 'Club member with optional passwordless self-service access.';

-- +goose Down

UPDATE roles
   SET description = 'Club member with optional passwordless self-service access.'
 WHERE code = 'member'
   AND description = 'Club member with optional password self-service access.';
