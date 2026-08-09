# Runbook: Backup and Restore

Audience: whichever officer is holding the pager. Assumes no prior familiarity
with this system.

## What a backup contains, and what it does not

`portalctl backup` produces two files:

| File | Contents |
| --- | --- |
| `portal-backup-<timestamp>.db.age` | The entire database, age-encrypted |
| `portal-backup-<timestamp>.db.age.manifest.json` | Timestamp, app version, schema version, and the SHA-256 and size of the **plaintext** database |

The manifest is not secret and is not encrypted. The backup is: it holds the
full member roster, so an unencrypted copy on a laptop or a shared drive is a
roster disclosure.

**The backup does not contain the password pepper.** This is deliberate.
Restoring a *working* instance needs two independent secrets:

1. `PORTAL_BACKUP_PASSPHRASE` — decrypts the backup file
2. `PORTAL_PASSWORD_PEPPER` — makes the restored password hashes verifiable

A backup plus its passphrase gets you the data. It does not get you a system
anyone can log into. If the pepper is lost, the restore still succeeds and
every account must then go through password recovery — see
[deployment.md](../deployment.md#password-pepper).

Both secrets may live in the same password manager or secrets store. That is a
reasonable operational choice at club scale. What must never happen is the
pepper being written *into the backup file*, which would collapse two secrets
into one.

## Secret custody

Officers rotate annually. A backup nobody can decrypt is not a backup.

- **At least two current officers** hold the backup passphrase and the pepper.
- Both belong in the officer handoff checklist — see
  [handoff.md](handoff.md).
- Store them in the club's password manager, not in a file beside the backups
  and not in this repository.
- Generate the passphrase from a CSPRNG, not by inventing one:
  `openssl rand -base64 24`

## Taking a backup

```bash
export PORTAL_BACKUP_PASSPHRASE='<from the password manager>'
portalctl backup --db /var/lib/bcars-portal/portal.db --to /var/backups/bcars
```

The backup is safe to run against a live database — it uses SQLite's
`VACUUM INTO`, which takes a consistent snapshot without stopping the server.

The command prints the artifact path, the plaintext SHA-256, and the schema
version. It writes the plaintext snapshot to the destination directory only
momentarily and removes it before exiting, including when it fails. If you ever
see a `.db` file without a `.age` suffix left in the backup directory, treat it
as a disclosure: it is an unencrypted roster.

### Where to keep them

- Off the machine running the portal. A backup on the same disk does not
  survive the failure it exists for.
- Keep at least the last 7 daily and 4 weekly copies. There is no automatic
  pruning; delete old ones by hand or with a `find -mtime` cron job.
- Keep each `.age` file together with its `.manifest.json`. A restore
  **refuses to proceed** without the manifest.

## Restoring

```bash
export PORTAL_BACKUP_PASSPHRASE='<from the password manager>'
portalctl restore \
  --from /var/backups/bcars/portal-backup-20260808-031500.db.age \
  --into /var/lib/bcars-portal/restored
```

The restore will:

1. Read and validate the manifest — a missing, unreadable, or malformed
   manifest is a hard failure, not a warning
2. Decrypt the artifact
3. Verify the decrypted bytes match the manifest's SHA-256 and size
4. Run `PRAGMA integrity_check`
5. Apply any migrations the running binary is newer than
6. Run `PRAGMA foreign_key_check` and refuse the restore if anything is broken

It refuses to overwrite an existing `portal.db`. If a failed restore leaves
nothing behind, that is intentional — a partially restored database that looks
usable is worse than none.

Then point the portal at the restored path, **supply the original pepper**, and
start it:

```bash
export PORTAL_PASSWORD_PEPPER='<the ORIGINAL pepper>'
portal -db /var/lib/bcars-portal/restored/portal.db
```

The server refuses to start if the pepper does not match the one this
database's hashes were made with. That refusal is a feature: without it, every
sign-in would fail as "invalid credentials" and look like users mistyping
their passwords.

## Restore drill

**Do this once, on purpose, before you need it.** A backup that has never been
restored is a hypothesis.

1. Take a fresh backup.
2. Restore it into a scratch directory on a machine that is not production.
3. Start the portal against it on a spare port with the original pepper.
4. Sign in as an officer. Load the member list. Confirm the count matches.
5. Delete the scratch copy — it is a full roster.

Note in the handoff checklist when this was last done and by whom.

## Failure modes

| Symptom | Cause | Action |
| --- | --- | --- |
| `PORTAL_BACKUP_PASSPHRASE is not set` | No passphrase in the environment | Export it; check the password manager |
| `cannot decrypt (wrong passphrase or corrupted file)` | Wrong passphrase, or a truncated/tampered file | Try the previous passphrase if it was rotated; otherwise try an older backup |
| `manifest ... not found` | The `.manifest.json` was separated from the `.age` file | Find the matching manifest; do not bypass the check |
| `SHA-256 mismatch` | The manifest and artifact are from different backups, or the file is damaged | Use a different backup; keep the damaged pair for diagnosis |
| `restored database has N foreign key violations` | The source database was already inconsistent | Do not put it into service; restore an earlier backup and escalate |
| Restore succeeds, nobody can sign in | Wrong or missing pepper | Supply the original pepper; the server should have refused to start, so check whether the fingerprint row was cleared |

## What this runbook does not cover

- Automated scheduling. There is no built-in scheduler; use cron or a systemd
  timer, and make sure the passphrase reaches it (`EnvironmentFile`, mode
  `0600`).
- Off-site replication. Copying the `.age` files somewhere durable is out of
  scope for the tool and squarely in scope for whoever operates it.
