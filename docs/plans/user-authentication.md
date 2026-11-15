# Plan: user authentication and `stromboli login`

This is the implementation plan for [ADR 0023](../adr/0023-user-authentication.md). The ADR says what we decided and why. This file says in which order we build it. Each phase is one or a few PRs and ships on its own without breaking existing deployments.

## Goals

- People sign in with Google, GitHub or email, in a browser.
- `stromboli login` opens that same page and leaves the CLI signed in.
- Every API call knows who made it, so the dashboard and the audit trail can rely on it.
- `STROMBOLI_API_TOKEN` keeps working exactly as today.

## Non-goals (for now)

- Multi-tenancy or a hosted multi-org instance ([0002](../adr/0002-self-hosted-one-instance-per-organisation.md)).
- SAML or SCIM. Generic OIDC covers most identity providers.
- Fine-grained permissions beyond `owner` / `admin` / `member`.
- The dashboard itself.

## Phase 0: Groundwork

- `internal/auth` package with a `Principal` type `{UserID, Email, Name, Role, Kind: user|service}` and `auth.FromContext(ctx)`.
- Replace `api.NewAuthInterceptor(token)` with an authenticator that resolves, in order:
  1. a bearer token equal to `STROMBOLI_API_TOKEN` → the service principal `api-token` (owner);
  2. a bearer token matching a hashed session → that user;
  3. a session cookie with a same-origin `Origin` header → that user;

  and puts the principal on the context. With no users and no API token, the instance stays open as today and logs the same warning.
- Same resolution for `/mcp` (`mcpserver.Handler`). Webhooks keep `STROMBOLI_WEBHOOK_TOKEN`.
- New config: `STROMBOLI_PUBLIC_URL`, required as soon as any login method is on. It is used for redirect URLs and cookie `Secure`.
- Tests: the existing token behaviour is unchanged (table tests in `internal/api`).

## Phase 1: Users, sessions and the schema

Migration `00005_users.sql`:

```sql
CREATE TABLE users (
    id             uuid PRIMARY KEY,
    email          citext NOT NULL UNIQUE,
    email_verified boolean NOT NULL DEFAULT false,
    name           text NOT NULL DEFAULT '',
    avatar_url     text NOT NULL DEFAULT '',
    role           text NOT NULL,               -- owner, admin, member
    password_hash  text,                        -- argon2id, NULL when unused
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_login_at  timestamptz,
    disabled_at    timestamptz
);
CREATE TABLE identities (
    provider   text NOT NULL,                   -- google, github, oidc
    subject    text NOT NULL,                   -- provider's stable user id
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email      citext,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);
CREATE TABLE sessions (
    id           uuid PRIMARY KEY,
    token_hash   bytea NOT NULL UNIQUE,         -- sha256(token)
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind         text NOT NULL,                 -- web, cli, api
    label        text NOT NULL DEFAULT '',      -- CLI hostname, token name
    user_agent   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE TABLE invites (
    email      citext PRIMARY KEY,
    role       text NOT NULL,
    invited_by uuid REFERENCES users (id) ON DELETE SET NULL,
    expires_at timestamptz NOT NULL
);
-- Short-lived state: OAuth state/PKCE, email codes, CLI device requests.
CREATE TABLE auth_requests (
    id          text PRIMARY KEY,               -- state, device_code hash, ...
    kind        text NOT NULL,                  -- oauth, email_code, cli
    data        jsonb NOT NULL,
    user_id     uuid REFERENCES users (id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'pending',-- pending, approved, denied
    expires_at  timestamptz NOT NULL
);
```

- sqlc queries in `internal/store/queries/auth.sql`.
- Sessions: 32 random bytes encoded as base64url, with a `strb_` prefix so secret scanners can find them. Only the SHA-256 hash is stored. `last_used_at` is updated at most once a minute.
- A background sweep deletes expired sessions and auth requests.
- Join policy in one function, `auth.CanJoin(email, provider, ghOrgs)`. It checks: empty instance → owner, then invite, then allowed domain, then GitHub org.

## Phase 2: Sign-in methods and pages

Server-rendered pages (`internal/auth/web`, `html/template` + `go:embed`, no JS framework):

- `GET /login`: buttons for each enabled method, plus the email form.
- `GET /setup`: shown until the first user exists.
- `GET /auth/start/{provider}` → redirect to the provider with `state` and PKCE.
- `GET /auth/callback/{provider}`: exchange the code, find or create the user, set the cookie, redirect to `?next=` (same-origin paths only).
- `POST /auth/email`: send a code or link (SMTP), or check the password.
- `POST /auth/logout`.

Providers:

- **OIDC / Google**: `github.com/coreos/go-oidc/v3` with `golang.org/x/oauth2` (already a dependency). Google is a preset of the generic OIDC provider (issuer `https://accounts.google.com`). Require `email_verified`. The optional `hd` claim is checked against `STROMBOLI_AUTH_ALLOWED_DOMAINS`.
- **GitHub**: `oauth2` with scopes `read:user user:email`, plus `read:org` only when `STROMBOLI_AUTH_GITHUB_ORGS` is set. Use the primary verified email from `/user/emails`. The subject is the numeric user id, never the login name, which can change.
- **Email**: with `STROMBOLI_SMTP_URL`, a 6-digit code plus a link. It expires after 10 minutes, allows 5 attempts and is rate limited per email and per IP. Without SMTP, a password: argon2id via `golang.org/x/crypto/argon2`, minimum 12 characters.

Config, documented in `docs/reference/configuration.md`:

| Variable | Purpose |
|---|---|
| `STROMBOLI_PUBLIC_URL` | External URL, used for callbacks |
| `STROMBOLI_AUTH_GOOGLE_CLIENT_ID` / `_SECRET` | Google sign-in |
| `STROMBOLI_AUTH_GITHUB_CLIENT_ID` / `_SECRET` | GitHub sign-in |
| `STROMBOLI_AUTH_OIDC_ISSUER` / `_CLIENT_ID` / `_SECRET` / `_NAME` | Any OIDC provider |
| `STROMBOLI_SMTP_URL`, `STROMBOLI_SMTP_FROM` | Email codes and invites |
| `STROMBOLI_AUTH_PASSWORD` | `true`/`false`; defaults to on when SMTP is off |
| `STROMBOLI_AUTH_ALLOWED_DOMAINS` | Domains that may join without an invite |
| `STROMBOLI_AUTH_GITHUB_ORGS` | GitHub orgs that may join without an invite |

Security checklist: `state` and PKCE on every OAuth flow, constant-time comparisons, rate limits on `/auth/*`, `Secure` cookies when the public URL is https, open-redirect check on `next`, and no tokens in logs.

## Phase 3: `stromboli login`

Server (plain HTTP under `/auth/cli`, rate limited):

- `POST /auth/cli/start` `{hostname}` → `{device_code, user_code, verification_uri, verification_uri_complete, interval, expires_in}`.
- `GET /auth/cli?code=ABCD-EFGH` → sends the person to `/login` first if needed, then shows a confirm page with the hostname and code, with Authorize and Deny buttons.
- `POST /auth/cli/token` `{device_code}` → `authorization_pending` | `slow_down` | `access_denied` | `expired_token` | `{token, user}`. On approval, create a `sessions` row with `kind='cli'` and `label=hostname`. The device code works once.

CLI (`cmd/stromboli/auth.go`, following [0015](../adr/0015-cli-follows-clig.md)):

```text
stromboli login [--url URL] [--no-browser]
  Opening https://stromboli.example.com/auth/cli?code=WDJB-MJHT
  Confirm this code in your browser: WDJB-MJHT
  Logged in to stromboli.example.com as ada@example.com (admin).

stromboli logout            # revokes the session on the server, then forgets it
stromboli auth status       # server, user, role, token source (env/file/login)
stromboli auth token        # prints the token, for scripts (stdout only)
```

- Reuse `openBrowser` from `cmd/stromboli/oauth.go`. Without a TTY or with `--no-browser`, print the URL.
- Credential store `cmd/stromboli/credstore.go`: OS keychain via `github.com/zalando/go-keyring` when available, else `$XDG_CONFIG_HOME/stromboli/hosts.yaml` at mode 0600. Store `default_url` too, so `--url` is only needed once.
- `app.api()` token order: `--token-file`, then `STROMBOLI_API_TOKEN`, then the stored login for the URL.
- On `Unauthenticated`, the message in `ui.go` suggests `stromboli login`.
- `stromboli mcp` uses the stored login the same way.

## Phase 4: Users API, roles and actors

- Proto `proto/stromboli/v1/auth.proto`: `AuthService` (`WhoAmI`, `ListSessions`, `RevokeSession`, `Logout`) and `UserService` (`ListUsers`, `InviteUser`, `SetRole`, `RemoveUser`). Run `buf lint` and `buf breaking` (additive only).
- Role check in the interceptor from a per-procedure table: write RPCs on `ConnectorService` and `CatalogService`, plus `UserService`, need `admin` or above.
- CLI: `stromboli users list|invite|set-role|rm`, and `stromboli users reset-password` for owners.
- `ApproveAction` / `RejectAction`: `decided_by` comes from the principal. `approver` is honoured only for the service principal. Add `started_by` to investigations (migration plus proto field).
- Invite emails when SMTP is configured. Otherwise `invite` prints the sign-up link.

## Phase 5: Docs and polish

- How-tos: "Sign in with Google", "Sign in with GitHub", "Use your OIDC provider", "Set up email sign-in".
- Update `README.md` Quickstart, `docs/reference/cli.md`, `docs/reference/api.md`, `docs/explanation/safety-model.md`, and the open question in `docs/adr/README.md`.
- Update `deploy/` templates (fly, railway, compose) with `STROMBOLI_PUBLIC_URL`.
- Update the Claude Code skill in `skills/` to use `stromboli login`.

## Later

- Personal API tokens (`stromboli auth token create --name ci`, `kind='api'`).
- Map Slack approvers to users by email, so Slack approvals record the same actor.
- The React dashboard takes over `/login` using the same cookie session and APIs.
- Optional: passkeys (WebAuthn).

## Testing

- Unit: the token resolver, the join policy, session expiry, device-flow state transitions, the `next` redirect check.
- Integration (Postgres, like the existing store tests): full device flow with a fake browser step, and GitHub and OIDC callbacks against `httptest` fake providers.
- CLI: `main_test.go`-style tests for `login`, `logout`, `auth status` and token precedence, with a temporary `XDG_CONFIG_HOME`.
- Manual: a Google and a GitHub OAuth app pointed at a local instance through `STROMBOLI_PUBLIC_URL=http://localhost:8080`.

## Open questions

1. **Email without SMTP:** offer passwords, as planned, or require SMTP for email sign-in and rely on Google/GitHub otherwise?
2. **Member permissions:** may a `member` approve machine actions, or only admins?
3. **OS keychain:** worth the extra dependency (and cgo on some platforms), or the 0600 file only, like `gh` without a keyring?
4. **Hosted openstatus:** should the openstatus product ever run stromboli for customers? If so, 0002 needs revisiting before the schema settles.
