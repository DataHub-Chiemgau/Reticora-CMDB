# Keycloak realm import

Two realm files describe the same `reticora` realm:

| File | Used by | Users |
|---|---|---|
| `realm-reticora.json` | production (`install-cloud.sh` renders it into `deploy/docker-compose/.generated/`) | none |
| `realm-reticora-dev.json` | the development stack (`docker-compose.yml` in the repository root) | demo user `admin@reticora.local` / `Admin-Dev-2024!`; the administrators of the seeded organizations (`make seed`, SIM-01): `admin@demo.local` / `Demo-Dev-2024!` and `admin@acme.local` / `Acme-Dev-2024!` |

The development realm is the production realm plus the demo users;
`tests/install-cloud-helpers.test.sh` fails when the two drift apart. Change
both files together. `admin@demo.local` and `admin@acme.local` carry the
`organization_id` of the organizations demo and acme of `backend/cmd/seed`
(`md5('/org/demo')`, `md5('/org/acme')`), the realm role `org_admin` and the
group `reticora-admin` like the installer's administrator, and set up OTP at
their first login; `tests/audit/demo-realm-users.test.sh` checks them (WP-433).

Manual import:

1. Start Keycloak locally or in your target environment.
2. Import `realm-reticora.json` as a realm export (never the dev realm).
3. Verify the `reticora-app` client and the realm roles exist.
4. Update client secret and redirect URIs as needed for non-local deployments.
5. Create the first administrator: user attribute `organization_id` = the
   organization UUID, realm role `org_admin`, group `reticora-admin`, a
   temporary password. `install-cloud.sh` does this itself (see below).

## Initial administrator (installer)

`install-cloud.sh` creates the administrator named by
`RETICORA_INITIAL_ADMIN_EMAIL` through the Keycloak admin CLI after the realm
import and the realm settings (see Upgrade): a random temporary password,
shown once in the installer summary and never written to disk, and the
required actions *update password* and *configure OTP*. It removes the demo
user `admin@reticora.local` of earlier releases, whose password is publicly
known. Re-runs leave an existing administrator alone.

## Server admin client and realm SMTP (installer)

The server creates the accounts of self-signups, accepted invitations and
operator-created organizations and binds org_admins to MFA through the
Keycloak admin REST API, as a confidential client with a service account
(client credentials). `install-cloud.sh` creates that client on every run,
after the initial administrator and before the server starts
(`ensure_admin_client`):

- client `reticora-server-admin` (`RETICORA_KEYCLOAK_ADMIN_CLIENT_ID`):
  confidential (`client-secret`), service account only — no standard flow,
  no implicit flow, no direct access grants;
- its secret is `RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET` of `.env`, generated
  once and never asked for; a different secret in `.env` is set in Keycloak
  by the next run, over the admin CLI's stdin (never on a command line or in
  the log);
- the service account `service-account-reticora-server-admin` holds the
  client roles `manage-users`, `view-users` and `query-users` of
  `realm-management`; missing ones are added, drifted client settings are
  reset, an unchanged client is not written.

The realm files do not carry the client: the production realm ships without
users (the service account is one), and `start --import-realm` skips an
existing realm anyway. `manage-users` lets the service account manage every
user and the group memberships of the realm, so keep the secret where the
installer puts it (`.env`, mode `0600`, and the server's environment).

With an SMTP server (`RETICORA_SMTP_HOST`, asked for by the installer) the
installer also sets it as `smtpServer` of the realm (`apply_realm_smtp`:
host, port, sender and display name, `starttls`/`ssl` from
`RETICORA_SMTP_TLS`, authentication with user and password; other settings
such as `replyTo` are kept). Keycloak sends its own e-mails over it — the
invitation of the first administrator of an organization created with
`POST /api/v1/admin/orgs` (`execute-actions-email`) and password resets.
Without `RETICORA_SMTP_HOST` the realm's mail settings are left alone, and
the server sends no such invitation: an operator-created organization then
gets no IdP account for its first administrator (`admin_invited: false`),
which is created by hand as in step 5 of the manual import above.

Keep Keycloak's own registration off (`registrationAllowed` is not set in
the realm files): an account registered in Keycloak has no
`organization_id` and no app user and cannot sign in. Organizations are
created only by the self-signup at `<RETICORA_PUBLIC_BASE_URL>/onboarding`
(TLC-01, needs the SMTP server for its mandatory e-mail verification) and by
operators (`POST /api/v1/admin/orgs`, see Operators).

Once the admin client exists, the server binds the org_admins that exist
already to MFA at its start (see Policies): they set up OTP at their next
login, and accepted invitations get their Keycloak account from the server.

## Upgrade

Keycloak starts with `start --import-realm` and imports the realm file only
while the realm does not exist: on an existing installation it logs
`Realm 'reticora' already exists. Import skipped` and keeps the realm of the
release that installed it (F-061; `tests/keycloak-realm-upgrade.test.sh`
shows it with the realm of the release before WP-044). The policies, the MFA
flow, roles, groups and the acr scope of a newer realm file therefore reach
an installation only through the installer: every run of `install-cloud.sh`
(first installation and re-run) applies them with the admin CLI
(`apply_realm_settings`, before the initial administrator is created):

- **User profile:** declares the attributes of the realm file that are
  missing (`organization_id`, admin only). Keycloak drops undeclared
  attributes: without it the installer's administrator would be created
  without organization.
- **Roles and groups:** creates the missing realm roles (`mfa_required`,
  `operator`, …) and adds `mfa_required` to `org_admin` and `operator`;
  creates the missing groups and maps their realm roles: `reticora-admin`
  carries `mfa_required`, `operators` carries `operator`.
- **acr:** adds the client scope `acr` as a default scope of `reticora-app`
  (and creates the scope as Keycloak does when the realm lacks it).
- **Browser flow:** creates the flow `browser-mfa` of the realm file when it
  is missing and binds it when Keycloak's built-in `browser` is bound. An
  unbound `browser-mfa` that differs from the realm file (e.g. left by an
  interrupted run) is rebuilt. A bound `browser-mfa` that differs, and a
  browser flow of your own, stay bound; the installer warns. To take the
  bundled flow instead: duplicate your flow, bind the duplicate, delete
  `browser-mfa`, re-run the installer, bind the new `browser-mfa`
  (Authentication → Bind flow) and delete the duplicate.
- **Password policy and lockout:** adds the missing password policies and
  raises the minimums (`length`, `upperCase`, `lowerCase`, `digits`,
  `specialChars`, `passwordHistory`) that are lower; enables the brute-force
  detection, lowers `failureFactor` to 5 and raises shorter waits and failure
  memory. Stricter values and further policies of yours are kept.
- **acr levels:** sets the realm attribute `acr.loa.map` when it is missing.

Every change is logged (`Keycloak realm: …`), every stricter value that is
kept as well (`Keycloak realm: kept …`); a run without anything to change
reports `Keycloak realm settings match the bundled realm`. Users, the client
`reticora-app` (apart from the rotated secret, redirect URIs and web origins,
see `docs/troubleshooting-installation.md`, and its post-logout redirect URIs,
see Sign-out) and all other realm settings are
left alone; the server's admin client and the realm's SMTP server are the
installer's (see Server admin client and realm SMTP). A failing call aborts the installer with this section as the hint;
check the Keycloak log and re-run the installer.

### Keycloak version (SR-60, WP-873)

The stack runs Keycloak 26 (`quay.io/keycloak/keycloak:26.8.2`, pinned by
digest in `deploy/docker-compose/docker-compose.yml`). An installation of an
earlier release (Keycloak 24) is upgraded by the next `install-cloud.sh`
run: Keycloak 26 migrates `keycloak-db` on its first start, then the
installer applies the realm settings as above.
`tests/keycloak-realm-upgrade.test.sh` runs the realm of step 1 on
Keycloak 24 and every later step on Keycloak 26 on the same database.
Changes of the version that the compose file carries:

- `KC_PROXY=edge` no longer exists; `KC_PROXY_HEADERS=xforwarded` lets
  Keycloak take scheme and host of the issuer from the `/realms/` proxy of
  nginx, as before.
- The temporary master administrator of the first start comes from
  `KC_BOOTSTRAP_ADMIN_USERNAME` and `KC_BOOTSTRAP_ADMIN_PASSWORD`; `.env`
  keeps `KEYCLOAK_ADMIN` and `KEYCLOAK_ADMIN_PASSWORD`, which the installer
  rotates as before.
- `/health/ready` is served on the management port 9000, not on 8080; the
  compose healthcheck probes 9000 inside the container (the port is not
  published).

## Policies (AUT-09, CH26)

- **Password policy:** at least 12 characters with upper and lower case, a
  digit and a special character, not the username or e-mail, no reuse of the
  last 5 passwords.
- **Brute-force protection:** after 5 failures the account is locked
  temporarily, with growing waits of up to 15 minutes; the lockout signal is
  shown on the login page.
- **MFA:** the browser flow `browser-mfa` asks for a TOTP code from every user
  holding the realm role `mfa_required`; users without OTP must set it up at
  login. `org_admin` and `operator` contain `mfa_required`, so MFA is
  mandatory for them. Assign `mfa_required` to further users or groups to
  extend the requirement per organization policy.

  These accounts hold `mfa_required` and set up OTP (required action
  *configure OTP*) at their first login:

  - the initial administrator of `install-cloud.sh` (see above);
  - the first org_admin of a self-signup (`POST /api/v1/signup/verify`) and
    the first org_admin the operator path creates (`POST /api/v1/admin/orgs`,
    invitation e-mail with `RETICORA_SMTP_HOST`): the server maps
    `mfa_required` to the new account with its Keycloak admin client and
    deletes the account again when the mapping fails, so no org_admin is
    created without MFA. The client needs nothing beyond
    `realm-management/manage-users`;
  - every member of the group `reticora-admin`, which the app maps to
    org_admin: the group carries the realm role `mfa_required` (deliberately
    not `org_admin`: app rights come from the database, the realm only marks
    MFA). In the development realm this includes the demo user
    `admin@reticora.local`.

  Every later grant of the app role org_admin binds the account the same way
  before it is stored (WP-358):

  - `POST /api/v1/role-assignments` (and `/roles/assign`) with the standard
    role org_admin: the account of the user's OIDC subject, or, for a user
    created through `POST /api/v1/users` that never signed in, the account
    of its exact e-mail address (`users?email=…&exact=true`); without such
    an account the first login binds the account it links;
  - the acceptance of an org_admin invitation (`POST
    /api/v1/invitations/accept`): the new account gets `mfa_required` and
    *configure OTP*; accounts of other invited roles get neither;
  - the first login that accepts a pending org_admin invitation;
  - every login that grants org_admin through an IdP group other than
    `reticora-admin` (a group named `org_admin`, which the realm does not
    bind to MFA) or through `RETICORA_DEFAULT_PROVISION_ROLE=org_admin` (a
    user who holds that default role; its assignment binds the account
    before it is stored): the login callback binds the account of the ID
    token's subject before it issues the session (WP-367). The group
    `reticora-admin` is not bound by the server (E-65); outside development
    the server refuses to start with the default role org_admin and without
    the admin client;
  - at every server start (scheduler worker) the reconciliation binds every
    active org_admin with an OIDC subject of every organization, including
    org_admins created before this binding existed. Failures are logged per
    user; the run is repeated up to five times.

  The mapping is idempotent and checks the effective roles first
  (`role-mappings/realm/composite`), so group members are not mapped twice.
  When it fails, the grant does not happen and the API answers 503 (role
  assignment, invitation acceptance, login callback). Revoking org_admin
  leaves `mfa_required` in place.

  **Prerequisite:** the Keycloak admin client
  (`RETICORA_KEYCLOAK_ADMIN_CLIENT_ID` / `RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET`,
  service account with `realm-management/manage-users`); `install-cloud.sh`
  creates it as `reticora-server-admin` (see Server admin client and realm
  SMTP). Without it, every grant of org_admin answers 503 outside
  `RETICORA_ENVIRONMENT=development` (fail closed); in development nothing
  is bound. The group `reticora-admin` and the installer administrator are
  covered by the realm and need no admin client.

## Operators (SEC-07)

The operator path `/api/v1/admin/*` admits personalized operators: members
of the realm group `operators` whose login used a second factor. The shared
`RETICORA_OPERATOR_TOKEN` is for bootstrap and break-glass only.

- **Group:** `operators` carries the realm role `operator`, which contains
  `mfa_required`; every member therefore sets up OTP at the first login
  (*configure OTP*) and enters a code at every later one. Make an account an
  operator by adding it to the group (Admin console → Users → Groups, or
  `kcadm.sh update users/<id>/groups/<group-id> -r reticora -n`). An operator
  needs no `organization_id`; operator sessions never reach the tenant API.
- **Server:** `RETICORA_OPERATOR_GROUP` (default `operators`) names the group
  the `groups` claim must contain (group name without path, as the client
  mapper emits it). `RETICORA_OPERATOR_MFA_ACR` (default `2`) lists the acr
  values that prove the second factor; an `amr` claim with a second factor
  (`otp`, `mfa`, `webauthn`, …) counts as well, the bundled realm emits none.
- **acr / level of authentication:** the realm attribute `acr.loa.map`
  (`{"1":1,"2":2}`) maps the acr values to the levels of the flow
  `browser-mfa`: level 1 is username and password (subflow `browser-mfa
  password`), level 2 is reached only after an OTP (subflow `browser-mfa level
  2`: holders of `mfa_required` have entered their OTP in `browser-mfa otp`
  just before, everyone else enters it there). Level 2 has the maximum age 0:
  every operator login asks for a fresh OTP, an existing SSO session at level
  1 is stepped up with the OTP only. The client scope `acr` is a default scope
  of `reticora-app`, so the ID token carries `acr` (`1` after the password,
  `2` after the OTP when level 2 was requested, `0` for a pure SSO login).
- **Login URL:** the operator login is an authorization code flow with PKCE
  of the client `reticora-app` that requests level 2 with `acr_values=2`:

  ```text
  <issuer>/protocol/openid-connect/auth?client_id=reticora-app&response_type=code
    &redirect_uri=<RETICORA_OIDC_REDIRECT_URL>&scope=openid%20profile%20email
    &state=<random>&code_challenge=<S256 of the verifier>&code_challenge_method=S256
    &acr_values=2
  ```

  The code is redeemed with `POST /api/v1/admin/auth/callback`
  (`{"code", "state", "code_verifier"}`) instead of the SPA callback; the
  answer is a 15-minute operator session. Without `acr_values=2` the ID
  token has acr `1` even after an OTP, and the login is refused (403), as is
  every login without the group. Every attempt is recorded in
  `operator_audit` (refusals with the failed condition and the acr).
  `frontend/e2e-real/support/stack.ts` (`operatorAuthorization`) builds this
  URL; `frontend/e2e-real/operator-login.spec.ts` runs the login against the
  installed stack.

## Sign-out (RP-initiated logout, F-351)

The sign-out of the SPA (`Abmelden` in the header, and the sign-out the
onboarding and the login page offer after a self-signup or an accepted
invitation, `frontend/src/auth/signOut.ts`) ends the Reticora session
(`POST /api/v1/auth/logout`) and then Keycloak's single sign-on session: the
browser goes to the `end_session_endpoint` of `GET /api/v1/auth/config` with
`client_id=reticora-app` and
`post_logout_redirect_uri=<origin of RETICORA_OIDC_REDIRECT_URL>/login`, and
Keycloak sends it back to the sign-in page. Without that session the next
sign-in asks for credentials, so an account created in a browser where
another account (e.g. the installer's administrator) is still signed in signs
in as itself. A fresh login with `prompt=login` instead does not work with
Keycloak: while the session of another user is alive it ends on the error
page "You are already authenticated as different user".

- `reticora-app` accepts its redirect URIs as post-logout redirect URIs
  (`"attributes": {"post.logout.redirect.uris": "+"}`); they include
  `<origin>/*`. Keycloak imports the attribute only with a new realm, so every
  run of `install-cloud.sh` sets it on the running client like the redirect
  URIs (`sync_realm_client`, logged as
  `Keycloak rotation: client reticora-app post.logout.redirect.uris set to +`).
  A value set in the console, e.g. `-` (none), is reset; with a list that
  lacks the origin Keycloak answers the sign-out with "Invalid redirect uri".
- The SPA keeps no ID token (the server issues its own session), so the
  request carries no `id_token_hint`: while its session is alive, Keycloak
  asks once to confirm the logout and then returns to the sign-in page;
  without a session it returns at once.
- Without an `end_session_endpoint` only the Reticora session ends. Another
  OpenID provider must accept `<origin>/login` as post-logout redirect URI
  of the client.

## Notes on the bundled realm

- The organization of a user is the user attribute `organization_id` (the
  organization UUID, CH26/AUT-09). It is declared in the realm's user profile
  (Keycloak drops undeclared attributes) and only administrators may view
  or edit it. The `organization_id` mapper on the `reticora-app` client emits
  it into the ID token; a login without it is rejected. Group names are never
  used to pick the organization.
- A first login creates an app user only with an admission (AUT-01): a
  pending invitation for the organization and the verified e-mail, an account
  created by an administrator or SCIM, or as the first user of an organization
  without users. Every other first login is answered with 403.
- **Role model (RBA-02):** the app roles of a session come from the
  database only, from two sources: the role assignments of the user in
  Reticora (with their client and site scope) and the IdP groups of the ID
  token (claim `groups` of the client mapper, group names without path),
  mapped exactly to the standard roles of the organization:
  `reticora-admin` → org_admin, `reticora-engineer` → engineer,
  `reticora-viewer` → viewer (groups named `org_admin`, `engineer` or
  `viewer` map the same way). The session receives the permissions these
  roles hold in the database; other group names grant nothing. The realm
  files contain only the groups `reticora-admin` and `operators` (operator
  path, see Operators): create `reticora-engineer` and `reticora-viewer`
  when you manage these roles in Keycloak. `client_technician` is valid only
  in a client scope and is assigned in Reticora, not through the IdP.
- **Realm roles grant no app right:** the backend does not read Keycloak's
  realm roles (`realm_access.roles`); `reticora-app` has no `roles` scope, so
  its tokens carry none. The realm roles `mfa_required`, `org_admin` and
  `operator` only control the MFA requirement of the browser flow (see
  Policies); `engineer`, `viewer` and `client_technician` have no effect.
  Assigning a realm role in Keycloak changes no right in Reticora
  (`backend/internal/identity/oidc_test.go`).
- `uma_authorization` is intentionally **not** listed as a default role: on a
  fresh Keycloak import that role does not exist yet at role-resolution time
  and the server aborts with "Unable to find composite realm role".
