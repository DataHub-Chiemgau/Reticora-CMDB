/**
 * cmdb — API surface for the enterprise CMDB + asset/inventory extension
 * (spec §20). Kept in a dedicated module so the extension is additive to the
 * existing client.ts. All calls flow through the same fetchAPI (auth, RFC
 * 7807 errors, idempotency).
 */
import { fetchAPI, type PaginatedResponse } from './client';
import type { FieldDefinition } from '../lib/fieldmeta';

function qs(params: Record<string, string | number | boolean | undefined> = {}): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

// ─── CI types (§1) ───────────────────────────────────────────────────────────

export interface CIType {
  id: string;
  organization_id?: string;
  key: string;
  name: string;
  display_name?: string;
  icon?: string;
  description?: string;
  category?: string;
  is_builtin: boolean;
  is_system: boolean;
  is_active: boolean;
  is_logical: boolean;
  version: number;
  cloned_from_id?: string;
  template_key?: string;
  lifecycle_definition_id?: string;
  capabilities: Record<string, unknown>;
  allowed_relationship_types: string[];
  ui_schema: Record<string, unknown>;
  fields?: FieldDefinition[];
  created_at: string;
  updated_at: string;
}

export interface CITypeCreateRequest {
  key?: string;
  name: string;
  display_name?: string;
  icon?: string;
  description?: string;
  category?: string;
  is_logical?: boolean;
  lifecycle_definition_id?: string;
  capabilities?: Record<string, unknown>;
  allowed_relationship_types?: string[];
  ui_schema?: Record<string, unknown>;
  fields?: FieldDefinition[];
}

export const ciTypeApi = {
  list(
    params: {
      include_inactive?: boolean;
      category?: string;
      search?: string;
      limit?: number;
      offset?: number;
    } = {},
  ) {
    return fetchAPI<PaginatedResponse<CIType>>(`/ci-types${qs(params)}`);
  },
  templates(): Promise<{ data: CIType[] }> {
    return fetchAPI('/ci-types/templates');
  },
  get(id: string): Promise<CIType> {
    return fetchAPI(`/ci-types/${id}`);
  },
  create(data: CITypeCreateRequest): Promise<CIType> {
    return fetchAPI('/ci-types', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: Partial<CITypeCreateRequest>): Promise<CIType> {
    return fetchAPI(`/ci-types/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  clone(id: string, data: { key?: string; name?: string }): Promise<CIType> {
    return fetchAPI(`/ci-types/${id}/clone`, { method: 'POST', body: JSON.stringify(data) });
  },
  deactivate(id: string): Promise<CIType> {
    return fetchAPI(`/ci-types/${id}/deactivate`, { method: 'POST', body: '{}' });
  },
  activate(id: string): Promise<CIType> {
    return fetchAPI(`/ci-types/${id}/activate`, { method: 'POST', body: '{}' });
  },
  versions(id: string): Promise<{ data: CIType[] }> {
    return fetchAPI(`/ci-types/${id}/versions`);
  },
  listFields(id: string): Promise<{ data: FieldDefinition[] }> {
    return fetchAPI(`/ci-types/${id}/fields`);
  },
  upsertField(id: string, field: FieldDefinition): Promise<FieldDefinition> {
    return fetchAPI(`/ci-types/${id}/fields`, { method: 'PUT', body: JSON.stringify(field) });
  },
  deleteField(id: string, name: string): Promise<void> {
    return fetchAPI(`/ci-types/${id}/fields/${encodeURIComponent(name)}`, { method: 'DELETE' });
  },
};

// ─── Global + instance field definitions (§2) ────────────────────────────────

export const ciFieldApi = {
  listGlobal(): Promise<{ data: FieldDefinition[] }> {
    return fetchAPI('/ci-fields/global');
  },
  upsertGlobal(field: FieldDefinition): Promise<FieldDefinition> {
    return fetchAPI('/ci-fields/global', { method: 'PUT', body: JSON.stringify(field) });
  },
  deleteGlobal(name: string): Promise<void> {
    return fetchAPI(`/ci-fields/global/${encodeURIComponent(name)}`, { method: 'DELETE' });
  },
  listInstance(ciId: string): Promise<{ data: FieldDefinition[] }> {
    return fetchAPI(`/cis/${ciId}/field-definitions`);
  },
  upsertInstance(ciId: string, field: FieldDefinition): Promise<FieldDefinition> {
    return fetchAPI(`/cis/${ciId}/field-definitions`, {
      method: 'PUT',
      body: JSON.stringify(field),
    });
  },
  deleteInstance(ciId: string, name: string): Promise<void> {
    return fetchAPI(`/cis/${ciId}/field-definitions/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    });
  },
};

// ─── Relationship types (§11) ────────────────────────────────────────────────

export interface RelationshipType {
  id: string;
  key: string;
  forward_label: string;
  reverse_label: string;
  source_ci_types: string[];
  target_ci_types: string[];
  direction: string;
  cardinality: string;
  category?: string;
  impact_participation: boolean;
  is_system: boolean;
  description?: string;
}

export const relationshipTypeApi = {
  list(params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<RelationshipType>>(`/relationship-types${qs(params)}`);
  },
  get(key: string): Promise<RelationshipType> {
    return fetchAPI(`/relationship-types/${encodeURIComponent(key)}`);
  },
  create(
    data: Partial<RelationshipType> & { key: string; forward_label: string; reverse_label: string },
  ) {
    return fetchAPI('/relationship-types', { method: 'POST', body: JSON.stringify(data) });
  },
  update(key: string, data: Partial<RelationshipType>) {
    return fetchAPI(`/relationship-types/${encodeURIComponent(key)}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
  },
  delete(key: string): Promise<void> {
    return fetchAPI(`/relationship-types/${encodeURIComponent(key)}`, { method: 'DELETE' });
  },
};

// ─── Lifecycle (§8) ──────────────────────────────────────────────────────────

export interface LifecycleState {
  id: string;
  key: string;
  label: string;
  is_initial: boolean;
  is_terminal: boolean;
  sort_order: number;
  required_fields: string[];
}

export interface LifecycleTransition {
  id: string;
  from_state_key?: string;
  to_state_key: string;
  key: string;
  label: string;
  required_fields: string[];
}

export interface LifecycleDefinition {
  id: string;
  key: string;
  name: string;
  applies_to: string;
  is_system: boolean;
  description?: string;
  states?: LifecycleState[];
  transitions?: LifecycleTransition[];
}

export const lifecycleApi = {
  list(params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<LifecycleDefinition>>(`/lifecycle-definitions${qs(params)}`);
  },
  get(id: string): Promise<LifecycleDefinition> {
    return fetchAPI(`/lifecycle-definitions/${id}`);
  },
  create(data: {
    key: string;
    name: string;
    applies_to?: string;
    description?: string;
    states: unknown[];
    transitions?: unknown[];
  }) {
    return fetchAPI('/lifecycle-definitions', { method: 'POST', body: JSON.stringify(data) });
  },
  transition(
    entityType: 'assets' | 'cis',
    id: string,
    toState: string,
    context: Record<string, unknown> = {},
    reason = '',
  ) {
    return fetchAPI(`/${entityType}/${id}/lifecycle-transitions`, {
      method: 'POST',
      body: JSON.stringify({ to_state: toState, context, reason }),
    });
  },
};

// ─── Locations (§7) ──────────────────────────────────────────────────────────

export interface LocationNode {
  id: string;
  organization_id: string;
  client_id?: string;
  parent_id?: string;
  node_type: string;
  name: string;
  barcode?: string;
  attributes: Record<string, unknown>;
  sort_order: number;
  children?: LocationNode[];
  created_at: string;
  updated_at: string;
}

export const locationApi = {
  list(
    params: { parent_id?: string; node_type?: string; root_only?: boolean; search?: string } = {},
  ) {
    return fetchAPI<{ data: LocationNode[] }>(`/locations${qs(params)}`);
  },
  tree(): Promise<{ data: LocationNode[] }> {
    return fetchAPI('/locations/tree');
  },
  get(id: string): Promise<LocationNode> {
    return fetchAPI(`/locations/${id}`);
  },
  create(data: {
    parent_id?: string;
    node_type: string;
    name: string;
    barcode?: string;
    client_id?: string;
    attributes?: Record<string, unknown>;
  }) {
    return fetchAPI('/locations', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: Partial<LocationNode>) {
    return fetchAPI(`/locations/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/locations/${id}`, { method: 'DELETE' });
  },
};

// ─── Movements + quantity items (§9, §6) ─────────────────────────────────────

export interface StockMovement {
  id: string;
  item_kind: string;
  asset_id?: string;
  quantity_item_id?: string;
  movement_type: string;
  from_location_id?: string;
  to_location_id?: string;
  quantity?: number;
  actor_id?: string;
  reason?: string;
  created_at: string;
}

export interface QuantityItem {
  id: string;
  sku?: string;
  name: string;
  category: string;
  unit: string;
  stock_level: number;
  min_level: number;
  location_id?: string;
}

export const inventoryApi = {
  listMovements(
    params: {
      asset_id?: string;
      movement_type?: string;
      location_id?: string;
      limit?: number;
      offset?: number;
    } = {},
  ) {
    return fetchAPI<PaginatedResponse<StockMovement>>(`/stock-movements${qs(params)}`);
  },
  record(data: {
    item_kind?: string;
    asset_id?: string;
    quantity_item_id?: string;
    movement_type: string;
    from_location_id?: string;
    to_location_id?: string;
    quantity?: number;
    reason?: string;
  }) {
    return fetchAPI('/stock-movements', { method: 'POST', body: JSON.stringify(data) });
  },
  assetMovements(assetId: string, params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<StockMovement>>(`/assets/${assetId}/movements${qs(params)}`);
  },
  listItems(params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<QuantityItem>>(`/inventory/items${qs(params)}`);
  },
  createItem(data: {
    name: string;
    sku?: string;
    category?: string;
    unit?: string;
    stock_level?: number;
    min_level?: number;
    location_id?: string;
  }) {
    return fetchAPI('/inventory/items', { method: 'POST', body: JSON.stringify(data) });
  },
  convertToAsset(itemId: string, data: { asset_tag: string; serial_number?: string }) {
    return fetchAPI<{ asset_id: string }>(`/inventory/items/${itemId}/convert`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  availability(params: { item_kind?: string; item_id?: string } = {}) {
    return fetchAPI<{ data: Availability[] }>(`/inventory/availability${qs(params)}`);
  },
};

export interface Availability {
  item_kind: string;
  item_id: string;
  total: number;
  available: number;
  reserved: number;
  assigned: number;
  repair: number;
  unavailable: number;
}

// ─── Reservations (§10) ──────────────────────────────────────────────────────

export interface Reservation {
  id: string;
  item_kind: string;
  asset_id?: string;
  quantity_item_id?: string;
  quantity: number;
  state: string;
  reserved_from: string;
  reserved_until?: string;
  assignee_id?: string;
  project_ref?: string;
  expires_at?: string;
  reason?: string;
}

export const reservationApi = {
  list(params: { state?: string; limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<Reservation>>(`/reservations${qs(params)}`);
  },
  create(data: {
    asset_id?: string;
    quantity_item_id?: string;
    quantity?: number;
    reserved_until?: string;
    expires_at?: string;
    assignee_id?: string;
    project_ref?: string;
    reason?: string;
  }) {
    return fetchAPI('/reservations', { method: 'POST', body: JSON.stringify(data) });
  },
  release(id: string): Promise<Reservation> {
    return fetchAPI(`/reservations/${id}/release`, { method: 'POST', body: '{}' });
  },
};

// ─── Composition (§5, §14) ───────────────────────────────────────────────────

export interface Composition {
  id: string;
  parent_asset_id: string;
  child_ci_id?: string;
  child_asset_id?: string;
  role?: string;
  position?: string;
  configuration_only: boolean;
  independently_serialized: boolean;
  independently_assignable: boolean;
  independently_locatable: boolean;
  independently_lifecycle_managed: boolean;
}

export interface ParentAssetRef {
  id: string;
  name: string;
  asset_tag?: string;
  status?: string;
  serial_number?: string;
  barcode?: string;
  rfid_tag?: string;
  purchase_date?: string;
  purchase_cost?: number;
  currency?: string;
  supplier?: string;
  invoice_number?: string;
  warranty_end?: string;
  location?: string;
}

/**
 * The parent asset owning a CI, plus the inventory fields it contributes.
 * `parent_asset` is absent when the parent could not be read; the composition
 * link alone is still meaningful.
 */
export interface CIParent {
  composition: Composition;
  parent_asset?: ParentAssetRef;
  inherited_fields?: string[];
}

export const compositionApi = {
  children(assetId: string) {
    return fetchAPI<PaginatedResponse<Composition>>(`/assets/${assetId}/children`);
  },
  /** Resolves the parent asset of a CI. Rejects with a 404 ApiError when the CI is standalone. */
  parentOfCI(ciId: string) {
    return fetchAPI<CIParent>(`/cis/${ciId}/parent`);
  },
  create(data: {
    parent_asset_id: string;
    child_ci_id?: string;
    child_asset_id?: string;
    role?: string;
    configuration_only?: boolean;
    independently_serialized?: boolean;
  }) {
    return fetchAPI('/compositions', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: Partial<Composition>) {
    return fetchAPI(`/compositions/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/compositions/${id}`, { method: 'DELETE' });
  },
};

// ─── Field provenance / overrides (§13) ─────────────────────────────────────

export interface FieldValue {
  id: string;
  ci_id: string;
  field_name: string;
  discovered_value?: unknown;
  discovered_source?: string;
  discovered_at?: string;
  override_value?: unknown;
  override_author?: string;
  override_reason?: string;
  override_at?: string;
  protected: boolean;
  effective_value?: unknown;
  diverged: boolean;
}

export const overrideApi = {
  listForCI(ciId: string): Promise<{ data: FieldValue[] }> {
    return fetchAPI(`/cis/${ciId}/fields`);
  },
  setOverride(ciId: string, field: string, value: unknown, reason: string, protectedFlag = true) {
    return fetchAPI(`/cis/${ciId}/fields/${encodeURIComponent(field)}/override`, {
      method: 'PUT',
      body: JSON.stringify({ value, reason, protected: protectedFlag }),
    });
  },
  clearOverride(ciId: string, field: string): Promise<FieldValue> {
    return fetchAPI(`/cis/${ciId}/fields/${encodeURIComponent(field)}/override`, {
      method: 'DELETE',
    });
  },
  conflicts(params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<FieldValue>>(`/reconciliation/conflicts${qs(params)}`);
  },
};

// ─── History / point-in-time (§16) ───────────────────────────────────────────

export interface EntityChange {
  id: string;
  entity_type: string;
  entity_id: string;
  actor_id?: string;
  change_type: string;
  field_name?: string;
  old_value?: unknown;
  new_value?: unknown;
  comment?: string;
  created_at: string;
}

export interface StateSnapshot {
  entity_type: string;
  entity_id: string;
  at: string;
  fields: Record<string, unknown>;
  deleted: boolean;
}

export const historyApi = {
  list(entityType: string, id: string, params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<EntityChange>>(`/history/${entityType}/${id}${qs(params)}`);
  },
  stateAt(entityType: 'assets' | 'cis', id: string, at?: string): Promise<StateSnapshot> {
    return fetchAPI(`/${entityType}/${id}/state${qs({ at })}`);
  },
};

// ─── Impact (§15) ────────────────────────────────────────────────────────────

export interface ImpactResult {
  failed_ci_id: string;
  impacted: Array<{ id: string; name: string; ci_type: string; status: string }>;
  count: number;
}

export interface BlastRadius {
  ci_id: string;
  affected_cis: Array<{ id: string; name: string; ci_type: string }>;
  affected_count: number;
  client_ids?: string[];
  site_ids?: string[];
  single_points_of_failure?: Array<{ id: string; name: string }>;
}

export const impactApi = {
  impact(ciId: string, params: { depth?: number; rel_type?: string } = {}) {
    return fetchAPI<ImpactResult>(`/topology/cis/${ciId}/impact${qs(params)}`);
  },
  dependencies(ciId: string, direction: 'upstream' | 'downstream', depth?: number) {
    return fetchAPI<{ nodes: Array<{ id: string; name: string }>; count: number }>(
      `/cis/${ciId}/dependencies${qs({ direction, depth })}`,
    );
  },
  blastRadius(ciId: string): Promise<BlastRadius> {
    return fetchAPI(`/cis/${ciId}/blast-radius`);
  },
};

// ─── Saved views + filter query (§17) ────────────────────────────────────────

export interface SavedView {
  id: string;
  owner_id?: string;
  name: string;
  entity_kind: string;
  filter_spec: Record<string, unknown>;
  shared: boolean;
  created_at: string;
}

export interface QueryResult {
  id: string;
  entity_kind: string;
  name: string;
  summary?: string;
  attributes?: Record<string, unknown>;
}

export const savedViewApi = {
  list(params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<SavedView>>(`/saved-views${qs(params)}`);
  },
  presets(): Promise<{ data: SavedView[] }> {
    return fetchAPI('/saved-views/presets');
  },
  create(data: {
    name: string;
    entity_kind: string;
    filter_spec: Record<string, unknown>;
    shared?: boolean;
  }) {
    return fetchAPI('/saved-views', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: Partial<SavedView>) {
    return fetchAPI(`/saved-views/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/saved-views/${id}`, { method: 'DELETE' });
  },
  query(
    spec: Record<string, unknown> & { entity_kind?: string },
    params: { limit?: number; offset?: number } = {},
  ) {
    return fetchAPI<PaginatedResponse<QueryResult>>(`/search/query${qs(params)}`, {
      method: 'POST',
      body: JSON.stringify(spec),
    });
  },
};
