-- The member directory (bcars-portal-4ux.7).
--
-- Contact values are filtered HERE, in SQL, not in a service or a template. A
-- value the caller may not see is never selected, so it cannot be leaked by a
-- DTO field someone forgets to strip, a template that renders the wrong
-- variable, or a debug log. The Phase 2 audit found a treasury page reachable
-- only because nothing consumed the link it stored; the same class of mistake
-- with a hidden phone number would be a privacy breach rather than an
-- inconvenience.
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
-- Rows are active approved Full and Associate members.
--
-- An email or phone survives only when the latest visibility decision for that
-- contact permits full_members. With no decision on file the row falls back to
-- the Phase 1 domain default: a Full member's contact is shareable with Full
-- members, an Associate's is not. An imported `import_default` event is a
-- recorded decision like any other, so it keeps its result until an officer
-- supersedes it.
--
-- A withheld value and an absent one both come back as the empty string. That
-- is deliberate: one representation means a caller cannot distinguish "this
-- member hid their number" from "this member has no number on file", which is
-- exactly the non-disclosure the design asks for.
--
-- Ordering is by sort_name then person id. The id tie-breaker is what keeps a
-- page boundary from shifting between calls when two members sort equally, so
-- paging cannot silently skip someone.
SELECT p.id                                     AS person_id,
       p.display_name                           AS display_name,
       p.call_sign                              AS call_sign,
       m.base_type                              AS base_type,
       CAST(COALESCE((SELECT cm.value_raw
          FROM contact_methods cm
         WHERE cm.person_id = p.id
           AND cm.kind = 'email'
           AND cm.archived_at IS NULL
           AND CASE
                 WHEN (SELECT ev.audience
                         FROM contact_method_visibility_events ev
                        WHERE ev.contact_method_id = cm.id
                        ORDER BY ev.effective_at DESC, ev.id DESC
                        LIMIT 1) IS NULL
                 THEN m.base_type = 'full'
                 ELSE (SELECT ev.audience
                         FROM contact_method_visibility_events ev
                        WHERE ev.contact_method_id = cm.id
                        ORDER BY ev.effective_at DESC, ev.id DESC
                        LIMIT 1) = 'full_members'
               END
         ORDER BY cm.is_primary DESC, cm.id
         LIMIT 1), '') AS TEXT)                 AS email,
       CAST(COALESCE((SELECT cm.value_raw
          FROM contact_methods cm
         WHERE cm.person_id = p.id
           AND cm.kind = 'phone'
           AND cm.archived_at IS NULL
           AND CASE
                 WHEN (SELECT ev.audience
                         FROM contact_method_visibility_events ev
                        WHERE ev.contact_method_id = cm.id
                        ORDER BY ev.effective_at DESC, ev.id DESC
                        LIMIT 1) IS NULL
                 THEN m.base_type = 'full'
                 ELSE (SELECT ev.audience
                         FROM contact_method_visibility_events ev
                        WHERE ev.contact_method_id = cm.id
                        ORDER BY ev.effective_at DESC, ev.id DESC
                        LIMIT 1) = 'full_members'
               END
         ORDER BY cm.is_primary DESC, cm.id
         LIMIT 1), '') AS TEXT)                 AS phone
  FROM persons p
  JOIN memberships m ON m.person_id = p.id
 WHERE m.lifecycle = 'approved'
   AND m.ended_on IS NULL
   AND m.base_type IN ('full', 'associate')
   AND p.deactivated_at IS NULL
   AND p.deceased_at IS NULL
   AND (sqlc.narg(search) IS NULL
        OR p.display_name LIKE '%' || sqlc.narg(search) || '%'
        OR p.call_sign LIKE '%' || sqlc.narg(search) || '%')
 ORDER BY p.sort_name, p.id
 LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountDirectoryEntries :one
--
-- The same population as ListDirectoryEntries, for pagination. It deliberately
-- shares the eligibility predicate and NOT the contact filtering, because a
-- total must not depend on what the caller may see.
SELECT count(*)
  FROM persons p
  JOIN memberships m ON m.person_id = p.id
 WHERE m.lifecycle = 'approved'
   AND m.ended_on IS NULL
   AND m.base_type IN ('full', 'associate')
   AND p.deactivated_at IS NULL
   AND p.deceased_at IS NULL
   AND (sqlc.narg(search) IS NULL
        OR p.display_name LIKE '%' || sqlc.narg(search) || '%'
        OR p.call_sign LIKE '%' || sqlc.narg(search) || '%');
