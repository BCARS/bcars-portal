# Officer Handoff Guide

This guide is for club officers taking over portal administration. You do not
need programming knowledge to follow these steps.

## Ownership & Contacts

| Role | Responsibility |
|------|---------------|
| Portal Administrator | User management, imports, backups, day-to-day operation |
| Technical Contact | Server maintenance, upgrades, deployment, troubleshooting |

Update these contacts when officers change.

## 1. Administrator Bootstrap

When a new administrator takes over:

```bash
# Create an invitation for the new admin
./bin/portalctl bootstrap-admin \
  --email newofficer@yourclub.org \
  --db data/portal.db \
  --base-url https://portal.yourclub.org \
  --force
```

This prints an invitation URL. Send it securely to the new officer. They will
set their password and gain administrator access.

### Revoking a previous administrator

1. Sign in as the new administrator
2. Navigate to Admin → Users
3. Find the previous administrator
4. Revoke their `administrator` role grant

## 2. Member Maintenance

### Viewing members
Navigate to **Members** in the sidebar. Use search to find specific members by
name or call sign.

### Adding a member manually
1. Click **Add Member**
2. Fill in display name and call sign
3. Add contact methods (email, phone, address)
4. Create a membership (Full, Associate, etc.)
5. Approve the membership

### Editing a member
Click a member's name → **Edit** to update their information.

### Deactivating a member
From the member detail page, click **Deactivate**. This preserves their
records but removes them from active member lists.

## 3. Importing Members

### Practice import (synthetic data)
Before importing real data, practice with the included synthetic fixtures:

```bash
# The synthetic CSV is checked into the repository at:
# fixtures/synthetic/groupsio_contact.csv
```

1. Navigate to **Imports** in the sidebar
2. Click **Upload** and select the synthetic CSV
3. Review the staged rows — some may need manual decisions
4. Use **Skip** or **Approve** buttons on manual rows
5. Click **Commit Import** to apply

### Real import (from Groups.io export)
Real member exports are supplied separately — they are never committed to the
repository.

1. Export your contact list from Groups.io as CSV or JSON
2. Follow the same import steps as above
3. Review manual rows carefully — these are records that need human judgment
4. Notes from the export (e.g., "Paid via PayPal on...") are imported as
   individual notes, deduplicated across re-imports

## 4. Audit Inspection

The portal logs all administrative actions as audit events.

- **API**: `GET /api/v1/audit-events` — returns paginated audit events
- **Database**: Audit events are stored in the `audit_events` table

Review audit events periodically and after any security concern.

## 4b. Secrets the outgoing officer must hand over

These are not in the repository, not in the backups, and not recoverable if
lost. At least two current officers should hold each one.

| Secret | Used by | If lost |
| --- | --- | --- |
| `PORTAL_PASSWORD_PEPPER` | The server, for every password hash | Every account must go through password recovery |
| `PORTAL_BACKUP_PASSPHRASE` | `portalctl backup` / `restore` | Every existing backup becomes unreadable |
| `PORTAL_SMTP_PASSWORD` | Outbound mail | Recovery and invitation mail stops; reissue from the mail provider |

Confirm the incoming officer can actually use them — have them run a restore
drill before the handover is considered complete.

## 5. Backup Schedule

### Recommended schedule
- **Daily**: Automated backup to a secure location
- **Before any upgrade**: Manual backup

### Creating a backup

```bash
export PORTAL_BACKUP_PASSPHRASE='<from the password manager>'
./bin/portalctl backup --db data/portal.db --to /backups/bcars-portal/
```

This creates an age-encrypted, WAL-safe backup with a SHA-256 manifest. Keep at
least 7 daily backups and one monthly backup for 6 months.

Full procedure, failure modes, and the restore drill:
[backup-restore.md](backup-restore.md).

### Restore drill
Practice restoring quarterly. See
[backup-restore.md](backup-restore.md#restore-drill) for the full drill; the
short version is that a restore needs the backup passphrase **and** the
original password pepper, and a backup alone will not produce a system anyone
can sign into.

## 6. Deployment Upgrade

Packaging is a checked-in [`Dockerfile`](../../Dockerfile) plus an example
Kubernetes manifest, or the plain binaries on a host; see
[deployment.md](../deployment.md#production-packaging). No service unit is
shipped, so do not assume a particular process manager or install path.

Whichever shape is in use, an upgrade is: take an encrypted backup, run
`portal -migrate-only` against the database with the **new** binary or image,
then start the new version and confirm readiness. Migrations run before the new
server serves traffic, never implicitly during it.

Before any upgrade, the technical contact must take an encrypted backup, retain
the previously deployed binary or image tag, and follow the procedure for the
actual host.
After restart, verify `GET /healthz` and `GET /readyz`; if either fails, restore
the previous binary and, if a data rollback is required, follow
[backup-restore.md](backup-restore.md#restoring) using the matching `.db.age`
artifact.

## 7. Logs

Application logs go to stderr. Their storage and query commands depend on the
process supervisor selected by deferred packaging Bead `bcars-portal-fmc.8`.

Logs automatically redact email addresses, phone numbers, and credentials.
See `docs/log-retention.md` for retention policy.

## 8. Secret Rotation

### Session secrets
Sessions are stored in the database with a 24-hour TTL.
To force all users to re-authenticate:

```bash
# In the SQLite database
sqlite3 data/portal.db "UPDATE sessions SET revoked_at = datetime('now') WHERE revoked_at IS NULL;"
```

### Admin password reset
If an administrator is locked out:

```bash
./bin/portalctl bootstrap-admin \
  --email admin@yourclub.org \
  --db data/portal.db \
  --force
```

## 9. Escalation

If you encounter an issue you cannot resolve:

1. Check the logs for error messages
2. Note the request ID from the error page (shown for server errors)
3. Contact the technical contact with the request ID and description
4. If the portal is down, restore from the latest backup

## Checklist for Officer Transition

- [ ] New officer receives invitation and sets password
- [ ] New officer can sign in and see the member list
- [ ] Previous officer's administrator role is revoked
- [ ] Backup schedule is transferred or automated
- [ ] Technical contact information is updated
- [ ] This document is reviewed and updated if needed
