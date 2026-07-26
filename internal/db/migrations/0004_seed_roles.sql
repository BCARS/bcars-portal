-- +goose Up

-- Role seed (from design §5).

INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('administrator',      'Full system access.',                  'technical');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('webmaster',          'Technical admin without member ops.',   'technical');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('president',          'Club president.',                       'executive');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('vice_president',     'Club vice president.',                  'executive');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('secretary',          'Club secretary.',                       'executive');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('treasurer',          'Club treasurer.',                       'executive');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('trustee',            'Board trustee.',                        'officer');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('activities_manager', 'Activities manager.',                   'officer');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('acs_coordinator',    'ACS/ARES coordinator.',                 'officer');
INSERT OR IGNORE INTO roles (code, description, kind) VALUES
    ('member',             'Regular member (no admin UI in P1).',   'officer');

-- Role → capability mapping.

-- administrator: all capabilities
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT 'administrator', code FROM capabilities;

-- webmaster
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code) VALUES
    ('webmaster', 'session.self.read'),
    ('webmaster', 'system.admin'),
    ('webmaster', 'integration.config.write'),
    ('webmaster', 'audit.read'),
    ('webmaster', 'user.invite'),
    ('webmaster', 'role.grant');

-- president, vice_president, secretary: same exec set
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT role, cap FROM (
    SELECT 'president' AS role UNION ALL
    SELECT 'vice_president' UNION ALL
    SELECT 'secretary'
) roles
CROSS JOIN (
    SELECT 'session.self.read'          AS cap UNION ALL
    SELECT 'member.read'                UNION ALL
    SELECT 'member.create'              UNION ALL
    SELECT 'member.update'              UNION ALL
    SELECT 'member.deactivate'          UNION ALL
    SELECT 'member.export'              UNION ALL
    SELECT 'contact_method.write'       UNION ALL
    SELECT 'sharing_pref.write.officer' UNION ALL
    SELECT 'membership.approve'         UNION ALL
    SELECT 'membership.lifecycle'       UNION ALL
    SELECT 'fcc.verify'                 UNION ALL
    SELECT 'honorary.grant'             UNION ALL
    SELECT 'notes.write.officer'        UNION ALL
    SELECT 'import.upload'              UNION ALL
    SELECT 'import.commit'              UNION ALL
    SELECT 'audit.read'
) caps;

-- treasurer: same as president + treasurer notes
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT 'treasurer', cap FROM (
    SELECT 'session.self.read'          AS cap UNION ALL
    SELECT 'member.read'                UNION ALL
    SELECT 'member.create'              UNION ALL
    SELECT 'member.update'              UNION ALL
    SELECT 'member.deactivate'          UNION ALL
    SELECT 'member.export'              UNION ALL
    SELECT 'contact_method.write'       UNION ALL
    SELECT 'sharing_pref.write.officer' UNION ALL
    SELECT 'membership.approve'         UNION ALL
    SELECT 'membership.lifecycle'       UNION ALL
    SELECT 'fcc.verify'                 UNION ALL
    SELECT 'honorary.grant'             UNION ALL
    SELECT 'notes.write.officer'        UNION ALL
    SELECT 'import.upload'              UNION ALL
    SELECT 'import.commit'              UNION ALL
    SELECT 'audit.read'                 UNION ALL
    SELECT 'notes.write.treasurer'      UNION ALL
    SELECT 'notes.read.treasurer'
) caps;

-- trustee, activities_manager: limited
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT role, cap FROM (
    SELECT 'trustee' AS role UNION ALL
    SELECT 'activities_manager'
) roles
CROSS JOIN (
    SELECT 'session.self.read'    AS cap UNION ALL
    SELECT 'member.read'          UNION ALL
    SELECT 'contact_method.write' UNION ALL
    SELECT 'notes.write.officer'
) caps;

-- acs_coordinator
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code) VALUES
    ('acs_coordinator', 'session.self.read'),
    ('acs_coordinator', 'member.read');

-- member
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code) VALUES
    ('member', 'session.self.read');

-- +goose Down

DELETE FROM role_capabilities;
DELETE FROM roles WHERE code IN (
    'administrator', 'webmaster', 'president', 'vice_president',
    'secretary', 'treasurer', 'trustee', 'activities_manager',
    'acs_coordinator', 'member'
);
