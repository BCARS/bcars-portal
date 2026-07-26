-- +goose Up

-- Capability catalog seed (from design §5 and authz.All in Go code).
-- Idempotent: INSERT OR IGNORE allows re-running safely.

INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('session.self.read',          'Read own session and effective capabilities.',                                   'system');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('role.grant',                 'Grant or revoke roles and direct capabilities.',                                 'system');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('user.invite',                'Invite a new officer or user.',                                                  'system');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('integration.config.write',   'Manage non-secret integration settings.',                                        'system');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('system.admin',               'Bootstrap and low-level administrative operations.',                              'system');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('member.read',                'Read canonical member data (server-filtered by role).',                           'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('member.export',              'Export member data to CSV or JSON.',                                              'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('member.create',              'Create a person and pending membership.',                                         'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('member.update',              'Update person fields.',                                                           'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('member.deactivate',          'Deactivate or reactivate a member.',                                              'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('contact_method.write',       'Add, edit, make-primary, or archive contact methods.',                            'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('sharing_pref.write.officer', 'Change directory visibility or ACS-ARES sharing prefs on behalf of a member.',   'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('membership.approve',         'Approve or reject membership applications and set the base type.',                'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('membership.lifecycle',       'Change membership lifecycle to inactive, resigned, or deceased.',                 'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('fcc.verify',                 'Record or revoke an FCC license verification.',                                   'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('honorary.grant',             'Create, edit, expire, or revoke honorary grants.',                                'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('notes.write.officer',        'Write officer-visibility notes.',                                                 'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('import.upload',              'Upload and stage an import file for review.',                                     'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('import.commit',              'Commit a reconciled import run to canonical data.',                                'membership');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('notes.write.treasurer',      'Write treasurer-visibility notes.',                                               'treasury');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('notes.read.treasurer',       'Read treasurer-visibility notes.',                                                'treasury');
INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('audit.read',                 'Search and read audit events.',                                                   'audit');

-- +goose Down

DELETE FROM capabilities WHERE code IN (
    'session.self.read', 'role.grant', 'user.invite', 'integration.config.write',
    'system.admin', 'member.read', 'member.export', 'member.create', 'member.update',
    'member.deactivate', 'contact_method.write', 'sharing_pref.write.officer',
    'membership.approve', 'membership.lifecycle', 'fcc.verify', 'honorary.grant',
    'notes.write.officer', 'import.upload', 'import.commit',
    'notes.write.treasurer', 'notes.read.treasurer', 'audit.read'
);
