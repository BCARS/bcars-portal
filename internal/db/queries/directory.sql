-- The member directory (bcars-portal-4ux.7, extended by 4ux.14).
--
-- Contact values are filtered HERE, in SQL, not in a service or a template. A
-- value the caller may not see is never selected, so it cannot be leaked by a
-- DTO field someone forgets to strip, a template that renders the wrong
-- variable, or a debug log.
--
-- Keep every comment in this file ASCII: sqlc substitutes sqlc.arg() by byte
-- offset, so a multi-byte character above a query corrupts the SQL it parses.

-- name: CountDirectoryEligibleGrants :one
--
-- The caller's eligibility probe: does this user hold an active grant to a
-- person whose membership is an active approved FULL membership.
--
-- Associates may hold grants and use their own profile, but may not browse the
-- directory. Eligibility is deliberately a separate question from holding the
-- directory.read capability, which is why the capability alone never answers
-- it.
SELECT count(*)
  FROM member_access_grants g
  JOIN memberships m ON m.person_id = g.person_id
 WHERE g.user_id = ?
   AND g.revoked_at IS NULL
   AND m.base_type = 'full'
   AND m.lifecycle = 'approved'
   AND m.ended_on IS NULL;

-- name: ListDirectoryEntries :many
--
-- Rows are active approved members, optionally narrowed to one membership type.
-- Contact values are NOT here; see ListDirectoryContacts, which returns every
-- shared contact rather than one per kind.
--
-- Ordering is by sort_name then person id. The id tie-breaker keeps a page
-- boundary from shifting between calls when two members sort equally, so paging
-- cannot silently skip someone.
SELECT p.id           AS person_id,
       p.display_name AS display_name,
       p.call_sign    AS call_sign,
       m.base_type    AS base_type
  FROM persons p
  JOIN memberships m ON m.person_id = p.id
 WHERE m.lifecycle = 'approved'
   AND m.ended_on IS NULL
   AND m.base_type IN ('full', 'associate')
   AND (sqlc.narg(base_type) IS NULL OR m.base_type = sqlc.narg(base_type))
   AND p.deactivated_at IS NULL
   AND p.deceased_at IS NULL
   AND (sqlc.narg(search) IS NULL
        OR p.display_name LIKE '%' || sqlc.narg(search) || '%'
        OR p.call_sign LIKE '%' || sqlc.narg(search) || '%')
 ORDER BY p.sort_name, p.id
 LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: ListDirectoryContacts :many
--
-- EVERY shared contact for the members on one page.
--
-- A member may share more than one number, and both can matter: a member whose
-- mobile has no signal at home needs the landline listed too. Returning one
-- contact per kind silently dropped the rest (bcars-portal-4ux.14).
--
-- The page CTE repeats the listing's population, filter, ordering, and bounds,
-- so this returns contacts for exactly the members on that page and no others.
-- That is why there is no per-member query and no N+1.
--
-- A contact survives only when the latest visibility decision for it permits
-- full_members. With no decision on file it falls back to the Phase 1 domain
-- default: a Full member's contact is shareable with Full members, an
-- Associate's is not. An imported import_default event is a recorded decision
-- like any other, so it keeps its result until an officer supersedes it.
--
-- Primary first, then by id, so a member's main number leads.
WITH page AS (
    SELECT p.id AS person_id, p.sort_name AS sort_name, m.base_type AS base_type
      FROM persons p
      JOIN memberships m ON m.person_id = p.id
     WHERE m.lifecycle = 'approved'
       AND m.ended_on IS NULL
       AND m.base_type IN ('full', 'associate')
       AND (sqlc.narg(base_type) IS NULL OR m.base_type = sqlc.narg(base_type))
       AND p.deactivated_at IS NULL
       AND p.deceased_at IS NULL
       AND (sqlc.narg(search) IS NULL
            OR p.display_name LIKE '%' || sqlc.narg(search) || '%'
            OR p.call_sign LIKE '%' || sqlc.narg(search) || '%')
     ORDER BY p.sort_name, p.id
     LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset)
)
SELECT page.person_id AS person_id,
       cm.kind        AS kind,
       cm.value_raw   AS value,
       cm.label       AS label,
       cm.is_primary  AS is_primary
  FROM page
  JOIN contact_methods cm ON cm.person_id = page.person_id
 WHERE cm.archived_at IS NULL
   AND cm.kind IN ('email', 'phone')
   AND CASE
         WHEN (SELECT ev.audience
                 FROM contact_method_visibility_events ev
                WHERE ev.contact_method_id = cm.id
                ORDER BY ev.effective_at DESC, ev.id DESC
                LIMIT 1) IS NULL
         THEN page.base_type = 'full'
         ELSE (SELECT ev.audience
                 FROM contact_method_visibility_events ev
                WHERE ev.contact_method_id = cm.id
                ORDER BY ev.effective_at DESC, ev.id DESC
                LIMIT 1) = 'full_members'
       END
 ORDER BY page.sort_name, page.person_id, cm.kind, cm.is_primary DESC, cm.id;

-- name: CountDirectoryEntries :one
--
-- The same population as ListDirectoryEntries, for pagination. It shares the
-- membership filter so a total always matches what is being listed, and does
-- NOT share the contact filtering, because a total must not depend on what the
-- caller may see.
SELECT count(*)
  FROM persons p
  JOIN memberships m ON m.person_id = p.id
 WHERE m.lifecycle = 'approved'
   AND m.ended_on IS NULL
   AND m.base_type IN ('full', 'associate')
   AND (sqlc.narg(base_type) IS NULL OR m.base_type = sqlc.narg(base_type))
   AND p.deactivated_at IS NULL
   AND p.deceased_at IS NULL
   AND (sqlc.narg(search) IS NULL
        OR p.display_name LIKE '%' || sqlc.narg(search) || '%'
        OR p.call_sign LIKE '%' || sqlc.narg(search) || '%');
