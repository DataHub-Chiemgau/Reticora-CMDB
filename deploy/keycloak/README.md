# Keycloak realm import

Two realm files describe the same `reticora` realm:

| File | Used by | Users |
|---|---|---|
| `realm-reticora.json` | production (`install-cloud.sh` renders it into `deploy/docker-compose/.generated/`) | none |
| `realm-reticora-dev.json` | the development stack (`docker-compose.yml` in the repository root) | demo user `admin@reticora.local` / `Admin-Dev-2024!` |

The development realm is the production realm plus the demo user;
`tests/install-cloud-helpers.test.sh` fails when the two drift apart. Change
both files together.

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
import: a random temporary password, shown once in the installer summary and
never written to disk, and the required actions *update password* and
*configure OTP*. It removes the demo user `admin@reticora.local` of earlier
releases, whose password is publicly known. Re-runs leave an existing
administrator alone.

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
    invitation e-mail): the server maps `mfa_required` to the new account
    with its Keycloak admin client and deletes the account again when the
    mapping fails, so no org_admin is created without MFA. The client needs
    nothing beyond `realm-management/manage-users`;
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
  service account with `realm-management/manage-users`). Without it, every
  grant of org_admin answers 503 outside `RETICORA_ENVIRONMENT=development`
  (fail closed); in development nothing is bound. The group `reticora-admin`
  and the installer administrator are covered by the realm and need no
  admin client.

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

## Notes on the bundled realm

- The organization of a user is the user attribute `organization_id` (the
  organization UUID, CH26/AUT-09). It is declared in the realm's user profile
  (Keycloak 24 drops undeclared attributes) and only administrators may view
  or edit it. The `organization_id` mapper on the `reticora-app` client emits
  it into the ID token; a login without it is rejected. Group names are never
  used to pick the organization.
- A first login creates an app user only with an admission (AUT-01): a
  pending invitation for the organization and the verified e-mail, an account
  created by an administrator or SCIM, or as the first user of an organization
  without users. Every other first login is answered with 403.
- IdP groups and roles map to the standard roles of the organization by an
  exact mapping (RBA-02): `reticora-admin`/`org_admin` → org_admin,
  `reticora-engineer`/`engineer` → engineer, `reticora-viewer`/`viewer` →
  viewer. The session receives the permissions those roles hold in the
  database; other group names grant nothing. `client_technician` is valid only
  in a client scope and is assigned in Reticora, not through the IdP.
- `uma_authorization` is intentionally **not** listed as a default role: on a
  fresh Keycloak import that role does not exist yet at role-resolution time
  and the server aborts with "Unable to find composite realm role".
