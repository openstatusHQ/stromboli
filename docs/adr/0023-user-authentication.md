# 0023. User accounts and `stromboli login`

- **Status:** Proposed
- **Date:** 2026-11-15

## Context

The API is protected by one shared secret, `STROMBOLI_API_TOKEN`. Everyone who uses the CLI, the MCP server or a script holds the same token, so stromboli cannot tell who did what. The clearest gap is machine actions: `ApproveActionRequest.approver` is whatever name the caller types, and that name is the audit trail ([0013](0013-approved-fly-machine-actions.md)).

A web dashboard is planned ([0005](0005-go-binary-api-first-connectrpc.md)), and a dashboard needs people who sign in. The CLI should sign in the same way, from a browser, instead of copying a token into an environment variable.

The README lists the open question "UI authentication in v1: local users only, or OIDC from the start?". Constraints:

- Self-hosted, one instance per organisation ([0002](0002-self-hosted-one-instance-per-organisation.md)). Every instance has its own address, so a social login uses an OAuth app the operator registers for that address. There is no stromboli-hosted identity service.
- One Go binary and Postgres only ([0004](0004-postgres-only-storage.md)): no Keycloak, Kratos or auth SaaS beside it.
- Existing deployments, scripts and CI keep working on upgrade.

## Decision

**Users live in stromboli's Postgres.** Tables `users`, `identities` (provider + subject → user), `sessions` and `invites`. There is no tenant column: an instance is one organisation.

**Three ways to sign in, each on only when configured:**

- **Google**: OpenID Connect (`STROMBOLI_AUTH_GOOGLE_CLIENT_ID`, `..._SECRET`). Built as a generic OIDC provider, so Okta, Keycloak, Entra ID and others work with `STROMBOLI_AUTH_OIDC_ISSUER` at no extra cost.
- **GitHub**: OAuth 2.0 plus the `/user` and `/user/emails` APIs (GitHub is not OIDC) (`STROMBOLI_AUTH_GITHUB_CLIENT_ID`, `..._SECRET`).
- **Email**: a one-time code or magic link sent by email when SMTP is configured (`STROMBOLI_SMTP_URL`). Without SMTP, email and password (argon2id), so a fresh `docker compose up` can still sign someone in.

Identities are linked to one user by **verified** email only. An unverified address never links to an existing account.

**Who may join.** The first user to sign in on an empty instance becomes its owner (Metabase-style setup). After that, a person can join if one of these holds: an owner or admin invited their email, their email domain is in `STROMBOLI_AUTH_ALLOWED_DOMAINS`, or they belong to a GitHub org in `STROMBOLI_AUTH_GITHUB_ORGS`. Everyone else is refused. Nobody can sign up on an open instance by default.

**Roles:** `owner`, `admin`, `member`. Members read everything, start investigations and approve actions. Admins also manage connectors, the catalog and users. More roles can come later if they are needed.

**Sessions are opaque random tokens, stored as SHA-256 hashes.** They are not JWTs, so revoking one takes effect at once.

- Web: an `HttpOnly`, `Secure`, `SameSite=Lax` cookie. Expires after 30 days, renewed while in use.
- CLI: a bearer token. Expires after 90 days without use. Each CLI token is listed with the host it was created on and can be revoked.
- Connect requests carrying a cookie must have a same-origin `Origin` header. This blocks CSRF from other sites.

**`stromboli login` uses a device-authorization flow** (RFC 8628 shape, served by stromboli itself):

1. The CLI calls `POST /auth/cli/start` and receives a `device_code`, a short `user_code` and a verification URL.
2. The CLI opens the browser at `<url>/auth/cli?code=ABCD-EFGH`, or prints the URL when there is no browser (SSH, containers).
3. The person signs in with any enabled method, then confirms "Authorize the stromboli CLI on `<hostname>`?" next to the same code the terminal shows.
4. The CLI polls `POST /auth/cli/token` until the request is approved, denied or expired after 10 minutes. It then stores the session token.

One code path covers local and remote terminals and needs no listener on `127.0.0.1`. The code shown in both places stops someone from approving a login they did not start.

**The CLI stores credentials per server** in `$XDG_CONFIG_HOME/stromboli/hosts.yaml` (mode 0600). The OS keychain is used when one is available. Token precedence: `--token-file`, then `STROMBOLI_API_TOKEN`, then the stored login for `--url`. Related commands: `stromboli logout`, `stromboli auth status` (who am I, on which server) and `stromboli auth token` (prints the token for scripts).

**Login pages are served by the Go server** from embedded `html/template` files at `/login`, `/auth/*` and `/setup`. When the React dashboard arrives it uses the same cookie session and can take over these pages. Browser redirects and callbacks are plain HTTP, like webhooks. Everything else is a Connect API:

- `AuthService`: `WhoAmI`, `ListSessions`, `RevokeSession`, `Logout`.
- `UserService`: `ListUsers`, `InviteUser`, `SetRole`, `RemoveUser`.

**`STROMBOLI_API_TOKEN` stays.** It is the instance's service token, for CI, scripts, webhooks and the bootstrap of a headless instance, and it acts as a built-in `owner` named `api-token`. Personal API tokens for scripts come later on the same `sessions` table (`kind = 'api'`).

**The authenticated user is the actor.** `ApproveAction` and `RejectAction` record the signed-in user, and the `approver` field is only honoured for the service token. Investigations started by a person record who started them.

## Consequences

- The dashboard gets users, roles and sessions from day one, and every API call has an actor for the audit trail.
- `stromboli login` replaces copying a shared token for people. The shared token remains for automation.
- Operators who want Google or GitHub sign-in must register an OAuth app with the callback `https://<their-instance>/auth/callback/{google,github}` and set `STROMBOLI_PUBLIC_URL`. The docs need a how-to for each provider.
- Magic links need SMTP. Without it, people use passwords, and owners reset them with `stromboli users reset-password` run against the server.
- stromboli now holds password hashes and session tokens. This raises the stakes of a database leak: hashes are argon2id, tokens are hashed, and login endpoints are rate limited.
- A shared hosted instance would still need multi-tenancy, which [0002](0002-self-hosted-one-instance-per-organisation.md) rules out. This ADR does not change that.
- Mapping Slack approvers to stromboli users (`SLACK_APPROVERS` → user emails) becomes possible. It is left for a later change.
