# Keycloak realm import

1. Start Keycloak locally or in your target environment.
2. Open the Keycloak admin console and create or select the target instance.
3. Import `realm-reticora.json` as a realm export.
4. Verify the `reticora-app` client, default realm roles, the demo
   organization group and the `admin@reticora.local` test user exist.
5. Update client secret and redirect URIs as needed for non-local deployments.

## Notes on the bundled realm

- The demo user is a member of the group `00000000-0000-0000-0000-000000000001`
  (a UUID). The backend derives the tenant (organization) from the first
  UUID-named group in the token's `groups` claim, so every user that should
  log in must belong to exactly such a group. A `groups` claim mapper on the
  `reticora-app` client emits group memberships into the ID token.
- The demo user is also in the `reticora-admin` group, which the backend maps
  to the full permission set (any group whose name contains `admin`/`owner`).
  Create additional groups such as `reticora-editor` or `reticora-viewer` for
  write / read-only users.
- `uma_authorization` is intentionally **not** listed as a default role: on a
  fresh Keycloak import that role does not exist yet at role-resolution time
  and the server aborts with "Unable to find composite realm role".
