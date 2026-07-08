# Keycloak realm import

1. Start Keycloak locally or in your target environment.
2. Open the Keycloak admin console and create or select the target instance.
3. Import `realm-reticora.json` as a realm export.
4. Verify the `reticora-app` client, default realm roles, and the `admin@reticora.local` test user exist.
5. Update client secret and redirect URIs as needed for non-local deployments.
