-- +goose Up

-- The licence class and Volunteer Examiner status the club already knows
-- (bcars-portal-um9).
--
-- Both arrive in the Groups.io export, and both were parsed, normalized and
-- staged by the importer and then dropped at commit, because the portal had
-- nowhere to put them. A club importing its real list lost information it had
-- been keeping for years.
--
-- WHY ON THE PERSON AND NOT IN fcc_verifications
--
-- fcc_verifications records what an OFFICER verified: a call sign, the source
-- they checked, who checked it and when. An imported value is none of those.
-- It is what the old list said, which is the club's recorded belief and not a
-- verification anybody performed. Writing it there would manufacture evidence.
--
-- So these two columns are a claim, and fcc_verifications remains the record of
-- a verified fact. A later verification can supersede the claim without
-- overwriting it, and an officer reading a member can tell which of the two
-- they are looking at.
--
-- license_class is stored lowercased, the way the importer already normalizes
-- it ("technician", "general", "extra", "advanced", "novice"). It is NOT
-- constrained to a fixed list: a club list is fifty years of hand-typed
-- history, and a CHECK constraint here would refuse an import rather than
-- record what the club actually holds.
--
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema.

ALTER TABLE persons ADD COLUMN license_class TEXT;

ALTER TABLE persons
    ADD COLUMN volunteer_examiner INTEGER NOT NULL DEFAULT 0
    CHECK (volunteer_examiner IN (0, 1));

-- +goose Down

ALTER TABLE persons DROP COLUMN volunteer_examiner;

ALTER TABLE persons DROP COLUMN license_class;
