# Keycloak realm import

1. Start Keycloak locally or in your target environment.
2. Open the Keycloak admin console and create or select the target instance.
3. Import `realm-reticora.json` as a realm export.
4. Verify the `reticora-app` client, default realm roles and the
   `admin@reticora.local` test user exist.
5. Update client secret and redirect URIs as needed for non-local deployments.

## Notes on the bundled realm

- The organization of a user is the user attribute `organization_id` (the
  organization UUID, CH26/AUT-09). The `organization_id` mapper on the
  `reticora-app` client emits it into the ID token; a login without it is
  rejected. Group names are never used to pick the organization.
- A first login creates an app user only with an admission (AUT-01): a
  pending invitation for the organization and the verified e-mail, an account
  created by an administrator or SCIM, or as the first user of an organization
  without users. Every other first login is answered with 403.
- The demo user is in the `reticora-admin` group, which the backend maps
  to the full permission set (any group whose name contains `admin`/`owner`).
  Create additional groups such as `reticora-editor` or `reticora-viewer` for
  write / read-only users.
- `uma_authorization` is intentionally **not** listed as a default role: on a
  fresh Keycloak import that role does not exist yet at role-resolution time
  and the server aborts with "Unable to find composite realm role".
