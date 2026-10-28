/**
 * cmdb — API surface for the enterprise CMDB + asset/inventory extension
 * (spec §20). Kept in a dedicated module so the extension is additive to the
 * existing client.ts. All calls flow through the same fetchAPI (auth, RFC
 * 7807 errors, idempotency).
 */
import { fetchAllPages, fetchAPI, type PaginatedResponse } from './client';
import type { FieldDefinition } from '../lib/fieldmeta';
import type { components } from './generated/schema';

function qs(params: Record<string, string | number | boolean | undefined> = {}): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

// ─── CI types (§1) ───────────────────────────────────────────────────────────

/** A label per language (MET-01). */
export type LocalizedText = Partial<Record<'de' | 'en', string>>;

/** The label in the UI language, falling back to the other language. */
export function localizedText(text: LocalizedText | undefined, language: string): string {
  if (!text) return '';
  const lang = language.toLowerCase().startsWith('de') ? 'de' : 'en';
  return text[lang] ?? text.de ?? text.en ?? '';
}

export interface CIType {
  id: string;
  organization_id?: string;
  key: string;
  name: string;
  display_name: LocalizedText;
  icon?: string;
  description?: string;
  category?: string;
  is_builtin: boolean;
  is_system: boolean;
  is_active: boolean;
  is_logical: boolean;
  /** Watched by the offline detector (LCY-05). */
  is_observable: boolean;
  /** Version of the generated attribute schema. */
  schema_version: number;
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
  display_name?: LocalizedText;
  icon?: string;
  description?: string;
  category?: string;
  is_logical?: boolean;
  is_observable?: boolean;
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

// Index flags of an attribute definition (MET-15, DB-04): a change queues
// the job that builds idx_attr_<id>, idx_attr_<id>_u or the index of the key
// (idx_attr_key_<key>, E-73).
export interface AttributeIndexFlags {
  searchable: boolean;
  unique: boolean;
  unique_scope: 'org' | 'client';
}

export type AttributeDefinition = components['schemas']['AttributeDefinition'];
export type AttributeIndexJob = components['schemas']['Job'];

// The job that brings the index of a definition in line with its flags.
export const ATTRIBUTE_INDEX_JOB_KIND = 'attribute.index';

export const attributeDefinitionApi = {
  // Every definition of the filter: the list pages at 200 (API-02, F-215),
  // so the field editor follows next_cursor instead of reading one page.
  async list(params: { ci_type_id?: string; scope?: 'type' | 'global' }): Promise<{
    data: AttributeDefinition[];
  }> {
    return { data: await fetchAllPages<AttributeDefinition>('/attribute-definitions', params) };
  },
  // The index jobs of the organization, newest first (JOB-03, job:read).
  indexJobs(): Promise<PaginatedResponse<AttributeIndexJob>> {
    return fetchAPI(`/jobs?kind=${ATTRIBUTE_INDEX_JOB_KIND}&limit=100`);
  },
  setIndexFlags(id: string, flags: AttributeIndexFlags): Promise<AttributeDefinition> {
    return fetchAPI(`/attribute-definitions/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(flags),
    });
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

/** Relationship type metadata (REL-01, CH27). */
export interface RelationshipType {
  id: string;
  key: string;
  display_name: string;
  reverse_name: string;
  directed: boolean;
  category: 'network' | 'power' | 'hosting' | 'logical' | 'physical';
  impact_direction: 'source_depends_on_target' | 'target_depends_on_source' | 'none' | 'undirected';
  redundant: boolean;
  allowed_source_types: string[];
  allowed_target_types: string[];
  cardinality: string;
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
    data: Partial<RelationshipType> & { key: string; display_name: string; reverse_name: string },
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

// The canonical location tree (LOC-10); the types come from the OpenAPI
// specification (npm run generate:api).
export type LocationKind = components['schemas']['LocationKind'];
export type Location = components['schemas']['Location'];
export type LocationTreeNode = components['schemas']['LocationTreeNode'];
export type LocationCreateRequest = components['schemas']['LocationCreateRequest'];
export type LocationUpdateRequest = components['schemas']['LocationUpdateRequest'];

/** LOCATION_KINDS lists the kinds of the tree in hierarchy order. */
export const LOCATION_KINDS: LocationKind[] = [
  'site',
  'building',
  'room',
  'rack',
  'warehouse',
  'zone',
  'shelf',
  'bin',
];

/** LOCATION_CHILD_KINDS is the parent matrix: the kinds allowed below a kind. */
export const LOCATION_CHILD_KINDS: Record<LocationKind, LocationKind[]> = {
  site: ['building', 'warehouse'],
  building: ['room'],
  room: ['rack'],
  rack: [],
  warehouse: ['zone'],
  zone: ['shelf'],
  shelf: ['bin'],
  bin: [],
};

/** clientApi lists the clients a site can belong to. */
export const clientApi = {
  /** One page of 200 clients; `offset` reads the following pages (F-340). */
  list({ offset = 0 }: { offset?: number } = {}) {
    return fetchAPI<components['schemas']['ClientListResponse']>(
      `/clients?limit=200${offset > 0 ? `&offset=${offset}` : ''}`,
    );
  },
  get(id: string) {
    return fetchAPI<components['schemas']['Client']>(`/clients/${encodeURIComponent(id)}`);
  },
};

export const locationApi = {
  list(
    params: { parent_id?: string; kind?: LocationKind; root_only?: boolean; search?: string } = {},
  ) {
    return fetchAPI<components['schemas']['LocationListResponse']>(`/locations${qs(params)}`);
  },
  tree() {
    return fetchAPI<components['schemas']['LocationTreeResponse']>('/locations/tree');
  },
  get(id: string) {
    return fetchAPI<Location>(`/locations/${id}`);
  },
  create(data: LocationCreateRequest) {
    return fetchAPI<Location>('/locations', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: LocationUpdateRequest) {
    return fetchAPI<Location>(`/locations/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
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
  /** The provenance of every typed field of the CI (REC-10). */
  provenance(ciId: string) {
    return fetchAPI<{ data: components['schemas']['CIFieldProvenance'][] }>(
      `/cis/${ciId}/provenance`,
    );
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
  /** Trail of the entry in the unified CI and location history (CI-06, LOC-11). */
  source?: 'ci_change' | 'entity_change' | 'audit' | 'location_change';
  /** Site of the CI when the change was made. */
  site_id?: string;
  /** Kind of a CI change (AUD-03); only ci_change entries carry it and the fields below. */
  change_kind?: ChangeKind;
  /**
   * Change set of a ci_change entry: the field rows of one write. Absent for
   * a row written before the change sets, which is an entry of its own.
   */
  change_set_id?: string;
  /** Changed fields of the change set, ordered by field name. */
  field_changes?: FieldChange[];
  /** Source of the CI change as in the audit log (`source` names the trail). */
  change_source?: 'ui' | 'api' | 'collector' | 'import' | 'system';
  collector_id?: string;
  request_id?: string;
  correlation_id?: string;
  observed_at?: string;
  actor_user_id?: string;
}

/** Kinds of a CI change (AUD-03). */
export type ChangeKind =
  | 'created'
  | 'updated'
  | 'deleted'
  | 'restored'
  | 'merged'
  | 'type_changed'
  | 'relationship_added'
  | 'relationship_removed'
  | 'status_changed';

/** One changed field of a change set (AUD-03). */
export interface FieldChange {
  field: string;
  old: unknown;
  new: unknown;
}

export interface StateSnapshot {
  entity_type: string;
  entity_id: string;
  at: string;
  fields: Record<string, unknown>;
  deleted: boolean;
}

/** A page of a history; audit_included is false without audit:read (E-78). */
export interface HistoryPage extends PaginatedResponse<EntityChange> {
  audit_included?: boolean;
}

export const historyApi = {
  list(
    entityType: string,
    id: string,
    params: { limit?: number; offset?: number; change_kind?: ChangeKind } = {},
  ) {
    return fetchAPI<HistoryPage>(`/history/${entityType}/${id}${qs(params)}`);
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

/**
 * The blast radius of a CI failure: affected_count counts the certain hits,
 * heuristic_count the CIs reached only over connected_to (IMP-03, WP-490).
 */
export type BlastRadius = components['schemas']['BlastRadius'];

/**
 * The fields of an impacted CI that ImpactResult declares (API-02): without
 * them the server answers with every field and the paths, megabytes for a
 * core switch (NFR-02).
 */
const IMPACT_RESULT_FIELDS = 'name,ci_type,status';

export const impactApi = {
  impact(ciId: string, params: { depth?: number; rel_type?: string } = {}) {
    return fetchAPI<ImpactResult>(
      `/impact/${ciId}${qs({ ...params, fields: IMPACT_RESULT_FIELDS })}`,
    );
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

export interface SavedViewSortKey {
  field: string;
  dir?: 'asc' | 'desc';
}

export interface SavedView {
  id: string;
  owner_id?: string;
  name: string;
  entity_kind: string;
  filter_spec: Record<string, unknown>;
  /** Sort keys and columns of the view (VIE-01). */
  sort?: SavedViewSortKey[];
  columns?: string[];
  shared: boolean;
  /** Role ids whose holders may read the view. */
  shared_with_roles?: string[];
  created_at: string;
}

export interface QueryResult {
  id: string;
  entity_kind: string;
  name: string;
  summary?: string;
  attributes?: Record<string, unknown>;
  /** The values of the columns the run asked for, by field (VIE-01). */
  columns?: Record<string, unknown>;
}

/**
 * The fields a view sorts by or shows, per entity kind: the whitelist of the
 * query engine (backend savedview astFields, VIE-01). attributes.<key> and
 * id are accepted as well, the page offers these.
 */
export const VIEW_FIELDS: Readonly<Record<string, readonly string[]>> = {
  ci: [
    'name',
    'status',
    'hostname',
    'fqdn',
    'serial_number',
    'manufacturer',
    'model',
    'os_name',
    'os_version',
    'management_ip',
    'lifecycle_state',
    'discovery_source',
    'health',
    'ci_type',
    'client_id',
    'site_id',
    'location_id',
    'is_manual',
    'created_at',
    'updated_at',
    'last_seen_at',
  ],
  asset: [
    'name',
    'asset_tag',
    'category',
    'status',
    'serial_number',
    'supplier',
    'lifecycle_state',
    'barcode',
    'rfid_tag',
    'purchase_cost',
    'purchase_date',
    'warranty_end',
    'client_id',
    'location_id',
    'created_at',
  ],
};

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
    sort?: SavedViewSortKey[];
    columns?: string[];
    shared?: boolean;
    shared_with_roles?: string[];
  }) {
    return fetchAPI('/saved-views', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: Partial<SavedView>) {
    return fetchAPI(`/saved-views/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/saved-views/${id}`, { method: 'DELETE' });
  },
  /**
   * Runs a filter spec; sort and columns are those of the view being run
   * and order the rows and select the columns of each row.
   */
  query(
    spec: Record<string, unknown> & {
      entity_kind?: string;
      sort?: SavedViewSortKey[];
      columns?: string[];
    },
    params: { limit?: number; offset?: number } = {},
  ) {
    return fetchAPI<PaginatedResponse<QueryResult>>(`/search/query${qs(params)}`, {
      method: 'POST',
      body: JSON.stringify(spec),
    });
  },
};

// ─── CI bulk (API-08, BLK-01) ────────────────────────────────────────────────

/** The report of a bulk change per id, also the result of a ci.bulk job. */
export type CIBulkReport = components['schemas']['CIBulkResult'];

/** BULK_MAX_IDS is the most ids one bulk change takes (API-08). */
export const BULK_MAX_IDS = 500;

/** The page size of collectCIs: the largest page the CI list answers (API-02). */
const COLLECT_PAGE_SIZE = 200;

export interface CollectedCIs {
  /** The matches in list order, at most `max`. */
  cis: { id: string; name: string }[];
  /** All matches of the filter, also those beyond `max`. */
  total: number;
}

/**
 * collectCIs gathers the ids and names of the CIs of the filtered CI list
 * page by page, up to `max` (WP-520, F-328). It reads the list the CMDB page
 * shows, with its filters and the selected scope, so it never holds a CI the
 * list would not show.
 */
export async function collectCIs(
  filter: { search?: string; status?: string; ci_type_id?: string },
  max = BULK_MAX_IDS,
): Promise<CollectedCIs> {
  const seen = new Set<string>();
  const cis: CollectedCIs['cis'] = [];
  let total = 0;
  for (let offset = 0; cis.length < max; offset += COLLECT_PAGE_SIZE) {
    const page = await fetchAPI<PaginatedResponse<{ id: string; name: string }>>(
      `/cis${qs({ ...filter, limit: COLLECT_PAGE_SIZE, offset })}`,
    );
    const rows = page.data ?? [];
    total = page.total;
    for (const ci of rows) {
      if (cis.length < max && !seen.has(ci.id)) {
        seen.add(ci.id);
        cis.push({ id: ci.id, name: ci.name });
      }
    }
    if (!page.has_more || rows.length === 0) {
      break;
    }
  }
  return { cis, total };
}
