# Log Retention & Redaction

## Structured Logging

The BCARS Portal uses structured JSON logging via `slog`. All log output goes
to stderr by default.

### PII Redaction

The `obs` package provides redaction helpers that **must** be used when logging
any potentially sensitive data:

| Helper | Purpose | Example output |
|--------|---------|----------------|
| `obs.SafeEmail(email)` | Mask email local part | `j***@example.com` |
| `obs.SafePhone(phone)` | Show only last 4 digits | `***-4567` |
| `obs.RedactString(s)` | Auto-redact emails, phones, tokens, passwords | `[REDACTED]` |
| `obs.EmailAttr(email)` | slog attribute with masked email | `"email":"j***@example.com"` |

**Never** log:
- Full email addresses
- Phone numbers
- Postal addresses
- Passwords, tokens, or API keys
- Raw request bodies
- Uploaded file contents

**Always** include:
- Request IDs (via `obs.LoggerFrom(ctx)`)
- User IDs (numeric, not email)
- Action names (dot-separated)
- Timestamps (automatic with slog)

### Code Example

```go
// ✗ BAD
log.Info("login", slog.String("email", user.Email))

// ✓ GOOD
log.Info("login", obs.EmailAttr(user.Email), slog.Int64("user_id", user.ID))
```

## Retention Defaults (Local Deployment)

| Log type | Default retention | Rotation |
|----------|-------------------|----------|
| Application logs (stderr) | 90 days | External (logrotate or systemd journal) |
| Audit events (SQLite) | Indefinite | Part of database backup cycle |

### Recommended logrotate configuration

```
/var/log/bcars-portal/*.log {
    daily
    rotate 90
    compress
    delaycompress
    missingok
    notifempty
    create 0640 portal portal
}
```

### systemd journal

If running under systemd, logs go to the journal automatically. Set retention:

```ini
# /etc/systemd/journald.conf.d/bcars-portal.conf
[Journal]
MaxRetentionSec=90d
```

## File Permissions

- Database file: `0640` (owner: portal user, group: portal group)
- Log directory: `0750`
- Backup files: `0640`
- No world-readable permissions on any data or log file

## Incident Response

If PII is accidentally logged:
1. Identify the affected log files and time range
2. Redact or delete the specific entries
3. Rotate the log file immediately
4. Document the incident and remediation
