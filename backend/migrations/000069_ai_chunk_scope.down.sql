-- Reverts WP-037: organization-only policy, no derived scope columns.

DROP POLICY ai_chunk_tenant_isolation ON ai_chunk;
CREATE POLICY ai_chunk_tenant_isolation ON ai_chunk
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER IF EXISTS ci_ai_chunk_scope_propagation ON ci;
DROP TRIGGER IF EXISTS ticket_ai_chunk_scope_propagation ON ticket;
DROP TRIGGER IF EXISTS asset_ai_chunk_scope_propagation ON asset;
DROP TRIGGER IF EXISTS ai_chunk_scope ON ai_chunk;
DROP FUNCTION IF EXISTS propagate_ai_chunk_scope();
DROP FUNCTION IF EXISTS derive_ai_chunk_scope();

DROP INDEX IF EXISTS idx_ai_chunk_client;
ALTER TABLE ai_chunk DROP COLUMN IF EXISTS client_id, DROP COLUMN IF EXISTS site_id;
