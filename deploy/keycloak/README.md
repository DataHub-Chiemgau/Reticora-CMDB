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
