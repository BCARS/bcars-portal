# ADR-0012: Members and officers share one password authentication model

- Status: Accepted
- Date: 2026-08-10

## Context

The original low-friction access goal said members should not have to maintain
another password for a portal they may visit only a few times each year. Phase 3
translated that preference into routine passwordless sign-in: a short-lived
email link would create a member session directly.

That translation became problematic when the identity model was corrected.
There is no separate officer account and member account for one person. One
`users` row may hold the member role and officer roles at the same time. A
passwordless link for a treasurer would therefore create a session carrying
treasury capabilities unless the application also tracked authentication method
and downscoped sessions.

The product need is simpler than that machinery. Members need an optional
account, a normal way to sign in, and a way to recover a forgotten credential.
The application already has password authentication and an enumeration-safe,
rate-limited, short-lived, single-use recovery flow.

## Decision

1. **Every user uses the same email-and-password sign-in.** Member and officer
   roles add capabilities to one identity; they do not select different
   authentication methods or session types.
2. **Recovery is also initial password setup.** Officer provisioning may create
   an active user with no password, because an officer must not choose a
   credential for someone else. The user requests the existing recovery flow,
   sets the first password, and thereafter signs in normally. The same flow
   replaces a forgotten password.
3. **There is no routine passwordless sign-in.** Phase 3 does not add a
   `member_sign_in` email-link purpose, magic-link endpoint, authentication
   method on sessions, or capability downscoping based on how a session began.
4. **Email remains an identifier and recovery destination, not record
   authority.** A matching contact method never provisions an account or grants
   record access. An officer explicitly creates or reuses the login identity and
   separately manages `member_access_grants` under ADR-0010.
5. **Optional and shared access remain supported.** A member may decline an
   account. One shared-mailbox account may hold explicit grants to several
   person records and has one password for that shared identity.
6. **High-impact step-up is a separate control.** If role grants or other
   privileged operations require recent password entry, `bcars-portal-6q6.5`
   implements that generically. Phase 3 member authentication does not invent a
   weaker session class as a substitute.

## Rejected alternatives

- **Downscope sessions created by member links**: requires authentication-method
  session state and capability filtering solely to support an authentication
  mechanism BCARS does not need.
- **Refuse member links for identities with officer roles**: preserves stronger
  officer login but prevents an officer from using the member view through the
  same identity.
- **Treat a passwordless member link as a full officer login**: simple to build,
  but makes the routine member sign-in design carry more authority than intended.
- **Create separate member and officer identities**: contradicts the domain
  model. A person is a member, and an office only adds permissions.

## Consequences

- `bcars-portal-4ux.5` becomes integration and onboarding work around the
  existing password/recovery/session services rather than a new authentication
  protocol.
- The assembled Phase 3 smoke test must provision a member, set the initial
  password through fake/filelog recovery mail, sign out, and sign back in with
  that password before exercising member capabilities.
- Existing recovery abuse controls, token storage, expiry, replay prevention,
  password policy, pepper, and session cookie configuration apply equally to
  members and officers.
- Production SMTP validation still covers invitations and recovery, including
  initial member password setup. It has no member magic-link message to test.
- Stale passwordless wording in live code and generated API descriptions is
  removed when `bcars-portal-4ux.5` implements the revised flow. Historical
  audit provenance is not rewritten.
