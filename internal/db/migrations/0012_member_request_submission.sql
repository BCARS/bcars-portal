-- +goose Up

-- Rename the member submission capability and broaden what it authorizes
-- (bcars-portal-4ux.6).
--
-- WHY THE NAME CHANGED
--
-- 0009 called it change_request.submit.self, when Phase 3 still assumed a
-- member could only ever correct their own record. The corrected plan says
-- otherwise: ANY authenticated member, Associate included, may suggest a
-- correction about ANOTHER person, without holding an access grant to that
-- person and without a family relationship. A capability whose name says
-- ".self" while its holder may submit about someone else is a name that lies,
-- and the next reader would reasonably grant it expecting the narrower thing.
--
-- Submission authority and record-read authority are now deliberately separate:
-- profile.self.read still answers "which records may I see", and this answers
-- "may I propose a correction at all". Neither implies the other. Suggesting a
-- correction about someone therefore reveals nothing about them, because
-- reading is a different question with a different answer.
--
-- The old code is removed rather than left as an alias. Two codes meaning one
-- thing is how a role ends up holding the one nothing checks.
--
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema.

INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('change_request.submit.member',
     'Submit, track, and withdraw own change requests, including a bounded suggestion about another person.',
     'member');

INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT role_code, 'change_request.submit.member'
  FROM role_capabilities
 WHERE capability_code = 'change_request.submit.self';

DELETE FROM role_capabilities WHERE capability_code = 'change_request.submit.self';
DELETE FROM capabilities      WHERE code            = 'change_request.submit.self';

-- +goose Down

INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('change_request.submit.self', 'Submit, track, and withdraw own change requests.', 'member');

INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT role_code, 'change_request.submit.self'
  FROM role_capabilities
 WHERE capability_code = 'change_request.submit.member';

DELETE FROM role_capabilities WHERE capability_code = 'change_request.submit.member';
DELETE FROM capabilities      WHERE code            = 'change_request.submit.member';
