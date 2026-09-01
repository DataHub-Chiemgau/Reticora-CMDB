import { clearSession, getSessionToken, refreshSession } from '../auth/session';

const API_BASE = '/api/v1';

export interface PaginatedResponse<T> {
  data: T[];
  total: number;
  limit: number;
  offset: number;
  has_more: boolean;
  /**
   * Opaque keyset cursor pointing at the row after the last one returned.
   * Present only while `has_more` is true and only on endpoints that support
   * cursor pagination. Pass it back as `cursor` with the identical `sort_by`
   * and `sort_dir` to fetch the next page without skipped or repeated rows.
   */
  next_cursor?: string;
}

export interface CI {
  id: string;
  organization_id: string;
  client_id?: string;
  site_id?: string;
  room_id?: string;
  ci_type_id: string;
  name: string;
  status: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  management_ip?: string;
  firmware_version?: string;
  attributes: Record<string, unknown>;
  discovery_source?: string;
  first_seen_at?: string;
  last_seen_at?: string;
  created_at: string;
  updated_at: string;
}

export interface CICreateRequest {
  ci_type_id: string;
  client_id?: string;
  name: string;
  status?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  management_ip?: string;
  firmware_version?: string;
  attributes?: Record<string, unknown>;
  discovery_source?: string;
}

export interface CIUpdateRequest {
  name?: string;
  status?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  management_ip?: string;
  firmware_version?: string;
  attributes?: Record<string, unknown>;
  discovery_source?: string;
  last_seen_at?: string;
}

export interface Relationship {
  id: string;
  organization_id: string;
  source_ci_id: string;
  target_ci_id: string;
  rel_type: string;
  attributes: Record<string, unknown>;
  source: string;
  created_at: string;
  updated_at: string;
}

export interface Collector {
  id: string;
  organization_id: string;
  client_id?: string;
  name: string;
  version?: string;
  status: string;
  last_heartbeat?: string;
  config: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface CIListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  status?: string;
  ci_type_id?: string;
  client_id?: string;
  site_id?: string;
  room_id?: string;
  search?: string;
  sort_by?: string;
  sort_dir?: string;
}

export interface ListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
}

function mergeHeaders(options?: RequestInit) {
  const headers = new Headers(options?.headers);
  if (!headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }

  const token = getSessionToken();
  if (token) {
    headers.set('Authorization', ['Bearer', token].join(' '));
  }

  return headers;
}

export async function fetchAPI<T>(path: string, options?: RequestInit): Promise<T> {
  const request = () =>
    fetch(`${API_BASE}${path}`, {
      ...options,
      headers: mergeHeaders(options),
    });

  let res = await request();

  if (res.status === 401) {
    const refreshed = await refreshSession();
    if (refreshed) {
      res = await request();
    } else {
      clearSession();
    }
  }

  if (!res.ok) {
    if (res.status === 401) {
      clearSession();
    }

    const error = await res.json().catch(() => ({ detail: res.statusText }));
    throw new Error(error.detail || res.statusText);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return res.json();
}

function buildQuery(params: object) {
  const query = new URLSearchParams();
  Object.entries(params as Record<string, string | number | boolean | undefined>).forEach(
    ([key, value]) => {
      if (value !== undefined && value !== '') {
        query.set(key, String(value));
      }
    },
  );
  const value = query.toString();
  return value ? `?${value}` : '';
}

export const ciApi = {
  list(params: CIListParams = {}): Promise<PaginatedResponse<CI>> {
    return fetchAPI(`/cis${buildQuery(params)}`);
  },

  get(id: string): Promise<CI> {
    return fetchAPI(`/cis/${id}`);
  },

  create(data: CICreateRequest): Promise<CI> {
    return fetchAPI('/cis', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },

  update(id: string, data: CIUpdateRequest): Promise<CI> {
    return fetchAPI(`/cis/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
  },

  delete(id: string): Promise<void> {
    return fetchAPI(`/cis/${id}`, { method: 'DELETE' });
  },

  relationships(id: string): Promise<PaginatedResponse<Relationship>> {
    return fetchAPI(`/cis/${id}/relationships`);
  },

  exportCIs(format: 'json' | 'csv' = 'json'): string {
    return `${API_BASE}/export/cis?format=${format}`;
  },
};

export const collectorApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<Collector>> {
    return fetchAPI(`/collectors${buildQuery(params)}`);
  },
};

// --- Topology ---

export interface TopologyNode {
  id: string;
  name: string;
  ci_type: string;
  status: string;
  client_id?: string;
  site_id?: string;
  management_ip?: string;
}

export interface TopologyEdge {
  id: string;
  source_ci_id: string;
  target_ci_id: string;
  rel_type: string;
  source?: string;
}

export interface TopologyGraphData {
  nodes: TopologyNode[];
  edges: TopologyEdge[];
}

export interface TopologyParams {
  client_id?: string;
  site_id?: string;
  ci_type?: string;
  root_ci_id?: string;
  depth?: number;
}

export interface ImpactResult {
  failed_ci_id: string;
  failed_ci: TopologyNode;
  rel_type: string;
  depth: number;
  impacted: TopologyNode[];
  count: number;
}

export const topologyApi = {
  get(params: TopologyParams = {}): Promise<TopologyGraphData> {
    return fetchAPI(`/topology${buildQuery(params)}`);
  },
  neighbors(ciId: string): Promise<TopologyGraphData> {
    return fetchAPI(`/topology/cis/${ciId}/neighbors`);
  },
  impact(ciId: string, params: { depth?: number; rel_type?: string } = {}): Promise<ImpactResult> {
    return fetchAPI(`/topology/cis/${ciId}/impact${buildQuery(params)}`);
  },
};

// --- Racks ---

export interface Rack {
  id: string;
  organization_id: string;
  room_id: string;
  name: string;
  height_u: number;
  width_mm: number;
  depth_mm: number;
  notes?: string;
  created_at: string;
  updated_at: string;
}

export interface RackMount {
  id: string;
  organization_id: string;
  rack_id: string;
  ci_id: string;
  position_u: number;
  height_u: number;
  face: string;
  created_at: string;
  updated_at: string;
}

export interface RackListParams {
  limit?: number;
  offset?: number;
  room_id?: string;
}

export const rackApi = {
  list(params: RackListParams = {}): Promise<PaginatedResponse<Rack>> {
    return fetchAPI(`/racks${buildQuery(params)}`);
  },
  get(id: string): Promise<Rack> {
    return fetchAPI(`/racks/${id}`);
  },
  listMounts(rackId: string, params: ListParams = {}): Promise<PaginatedResponse<RackMount>> {
    return fetchAPI(`/racks/${rackId}/mounts${buildQuery(params)}`);
  },
};

// --- Phase 2: Asset Management ---

export interface Asset {
  id: string;
  organization_id: string;
  client_id?: string;
  ci_id?: string;
  asset_tag: string;
  name: string;
  category: string;
  status: string;
  purchase_date?: string;
  purchase_cost?: number;
  currency?: string;
  warranty_end?: string;
  supplier?: string;
  invoice_number?: string;
  serial_number?: string;
  location?: string;
  notes?: string;
  custom_fields: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface AssetCreateRequest {
  asset_tag: string;
  name: string;
  category?: string;
  status?: string;
  client_id?: string;
  ci_id?: string;
  purchase_date?: string;
  purchase_cost?: number;
  currency?: string;
  warranty_end?: string;
  supplier?: string;
  invoice_number?: string;
  serial_number?: string;
  location?: string;
  notes?: string;
  custom_fields?: Record<string, unknown>;
}

export interface AssetUpdateRequest {
  name?: string;
  category?: string;
  status?: string;
  purchase_date?: string;
  purchase_cost?: number;
  currency?: string;
  warranty_end?: string;
  supplier?: string;
  invoice_number?: string;
  serial_number?: string;
  location?: string;
  notes?: string;
  ci_id?: string;
  custom_fields?: Record<string, unknown>;
}

export interface AssetListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  status?: string;
  category?: string;
  client_id?: string;
  search?: string;
  sort_by?: string;
  sort_dir?: string;
}

export const assetApi = {
  list(params: AssetListParams = {}): Promise<PaginatedResponse<Asset>> {
    return fetchAPI(`/assets${buildQuery(params)}`);
  },
  get(id: string): Promise<Asset> {
    return fetchAPI(`/assets/${id}`);
  },
  create(data: AssetCreateRequest): Promise<Asset> {
    return fetchAPI('/assets', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: AssetUpdateRequest): Promise<Asset> {
    return fetchAPI(`/assets/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/assets/${id}`, { method: 'DELETE' });
  },
};

// --- Phase 2: Assignment Management ---

export interface Assignment {
  id: string;
  organization_id: string;
  asset_id?: string;
  ci_id?: string;
  assigned_to: string;
  assigned_by: string;
  assignment_type: string;
  status: string;
  assigned_at: string;
  due_date?: string;
  returned_at?: string;
  return_condition?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
}

export interface AssignmentCreateRequest {
  asset_id?: string;
  ci_id?: string;
  assigned_to: string;
  assignment_type?: string;
  due_date?: string;
  notes?: string;
}

export interface AssignmentListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  status?: string;
  assigned_to?: string;
  asset_id?: string;
  search?: string;
}

export const assignmentApi = {
  list(params: AssignmentListParams = {}): Promise<PaginatedResponse<Assignment>> {
    return fetchAPI(`/assignments${buildQuery(params)}`);
  },
  get(id: string): Promise<Assignment> {
    return fetchAPI(`/assignments/${id}`);
  },
  create(data: AssignmentCreateRequest): Promise<Assignment> {
    return fetchAPI('/assignments', { method: 'POST', body: JSON.stringify(data) });
  },
  returnAssignment(
    id: string,
    data: { return_condition?: string; notes?: string },
  ): Promise<Assignment> {
    return fetchAPI(`/assignments/${id}/return`, { method: 'POST', body: JSON.stringify(data) });
  },
  transfer(id: string, data: { new_assignee: string; notes?: string }): Promise<Assignment> {
    return fetchAPI(`/assignments/${id}/transfer`, { method: 'POST', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/assignments/${id}`, { method: 'DELETE' });
  },
};

// --- Phase 2: Document Management ---

export interface Document {
  id: string;
  organization_id: string;
  title: string;
  description?: string;
  file_name: string;
  file_size: number;
  mime_type: string;
  storage_key: string;
  version: number;
  category: string;
  tags: string[];
  uploaded_by: string;
  created_at: string;
  updated_at: string;
}

export interface DocumentCreateRequest {
  title: string;
  description?: string;
  file_name: string;
  file_size: number;
  mime_type?: string;
  storage_key: string;
  category?: string;
  tags?: string[];
}

export interface DocumentListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  category?: string;
  search?: string;
}

export interface DocumentLink {
  id: string;
  document_id: string;
  entity_type: string;
  entity_id: string;
  created_at: string;
}

export const documentApi = {
  list(params: DocumentListParams = {}): Promise<PaginatedResponse<Document>> {
    return fetchAPI(`/documents${buildQuery(params)}`);
  },
  get(id: string): Promise<Document> {
    return fetchAPI(`/documents/${id}`);
  },
  create(data: DocumentCreateRequest): Promise<Document> {
    return fetchAPI('/documents', { method: 'POST', body: JSON.stringify(data) });
  },
  update(
    id: string,
    data: { title?: string; description?: string; category?: string; tags?: string[] },
  ): Promise<Document> {
    return fetchAPI(`/documents/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/documents/${id}`, { method: 'DELETE' });
  },
  link(id: string, data: { entity_type: string; entity_id: string }): Promise<DocumentLink> {
    return fetchAPI(`/documents/${id}/links`, { method: 'POST', body: JSON.stringify(data) });
  },
  getLinks(id: string): Promise<DocumentLink[]> {
    return fetchAPI(`/documents/${id}/links`);
  },
};

// --- Phase 2: Stocktake / Inventur ---

export interface Stocktake {
  id: string;
  organization_id: string;
  title: string;
  description?: string;
  status: string;
  scope: string;
  started_by?: string;
  started_at?: string;
  completed_at?: string;
  due_date?: string;
  total_expected: number;
  total_scanned: number;
  total_missing: number;
  total_surplus: number;
  created_at: string;
  updated_at: string;
}

export interface StockScan {
  id: string;
  stocktake_id: string;
  asset_id?: string;
  ci_id?: string;
  scanned_by: string;
  scan_method: string;
  scan_result: string;
  location_found?: string;
  notes?: string;
  scanned_at: string;
}

export interface StocktakeCreateRequest {
  title: string;
  description?: string;
  scope?: string;
  due_date?: string;
  total_expected?: number;
}

export interface StocktakeListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  status?: string;
  scope?: string;
  search?: string;
}

export const stocktakeApi = {
  list(params: StocktakeListParams = {}): Promise<PaginatedResponse<Stocktake>> {
    return fetchAPI(`/stocktakes${buildQuery(params)}`);
  },
  get(id: string): Promise<Stocktake> {
    return fetchAPI(`/stocktakes/${id}`);
  },
  create(data: StocktakeCreateRequest): Promise<Stocktake> {
    return fetchAPI('/stocktakes', { method: 'POST', body: JSON.stringify(data) });
  },
  update(
    id: string,
    data: {
      title?: string;
      description?: string;
      status?: string;
      due_date?: string;
      total_expected?: number;
    },
  ): Promise<Stocktake> {
    return fetchAPI(`/stocktakes/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/stocktakes/${id}`, { method: 'DELETE' });
  },
  addScan(
    stocktakeId: string,
    data: {
      asset_id?: string;
      ci_id?: string;
      scan_method?: string;
      scan_result: string;
      location_found?: string;
      notes?: string;
    },
  ): Promise<StockScan> {
    return fetchAPI(`/stocktakes/${stocktakeId}/scans`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  listScans(stocktakeId: string, params: ListParams = {}): Promise<PaginatedResponse<StockScan>> {
    return fetchAPI(`/stocktakes/${stocktakeId}/scans${buildQuery(params)}`);
  },
  difference(
    stocktakeId: string,
    params: ListParams = {},
  ): Promise<PaginatedResponse<StocktakeDifferenceEntry>> {
    return fetchAPI(`/stocktakes/${stocktakeId}/difference${buildQuery(params)}`);
  },
  complete(stocktakeId: string, applyCorrections = true): Promise<StocktakeCompletion> {
    return fetchAPI(`/stocktakes/${stocktakeId}/complete`, {
      method: 'POST',
      body: JSON.stringify({ apply_corrections: applyCorrections }),
    });
  },
};

export interface StocktakeDifferenceEntry {
  scan: StockScan;
  asset?: Asset;
}

export interface StocktakeCorrection {
  asset_id: string;
  asset_tag?: string;
  scan_result: string;
  previous_status?: string;
  new_status?: string;
  previous_location?: string;
  new_location?: string;
  applied: boolean;
  detail?: string;
}

export interface StocktakeCompletion {
  stocktake: Stocktake;
  corrections_applied: number;
  corrections: StocktakeCorrection[];
}

// --- Phase 2: Ticket System (Essential) ---

export interface Ticket {
  id: string;
  organization_id: string;
  ticket_number: number;
  title: string;
  description?: string;
  status: string;
  priority: string;
  category: string;
  reporter_id: string;
  assignee_id?: string;
  team_id?: string;
  related_ci_id?: string;
  related_asset_id?: string;
  due_date?: string;
  resolved_at?: string;
  closed_at?: string;
  tags: string[];
  created_at: string;
  updated_at: string;
}

export interface TicketComment {
  id: string;
  ticket_id: string;
  author_id: string;
  content: string;
  is_internal: boolean;
  created_at: string;
  updated_at: string;
}

export interface TicketCreateRequest {
  title: string;
  description?: string;
  priority?: string;
  category?: string;
  assignee_id?: string;
  team_id?: string;
  related_ci_id?: string;
  related_asset_id?: string;
  due_date?: string;
  tags?: string[];
}

export interface TicketUpdateRequest {
  title?: string;
  description?: string;
  status?: string;
  priority?: string;
  category?: string;
  assignee_id?: string;
  team_id?: string;
  due_date?: string;
  tags?: string[];
}

export interface TicketListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  status?: string;
  priority?: string;
  category?: string;
  assignee_id?: string;
  team_id?: string;
  search?: string;
}

export const ticketApi = {
  list(params: TicketListParams = {}): Promise<PaginatedResponse<Ticket>> {
    return fetchAPI(`/tickets${buildQuery(params)}`);
  },
  get(id: string): Promise<Ticket> {
    return fetchAPI(`/tickets/${id}`);
  },
  create(data: TicketCreateRequest): Promise<Ticket> {
    return fetchAPI('/tickets', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: TicketUpdateRequest): Promise<Ticket> {
    return fetchAPI(`/tickets/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/tickets/${id}`, { method: 'DELETE' });
  },
  addComment(
    ticketId: string,
    data: { content: string; is_internal?: boolean },
  ): Promise<TicketComment> {
    return fetchAPI(`/tickets/${ticketId}/comments`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  listComments(
    ticketId: string,
    params: ListParams = {},
  ): Promise<PaginatedResponse<TicketComment>> {
    return fetchAPI(`/tickets/${ticketId}/comments${buildQuery(params)}`);
  },
};

// --- Phase 2: Users / Teams / Roles ---

export interface AppUser {
  id: string;
  organization_id: string;
  email: string;
  display_name: string;
  avatar_url?: string;
  status: string;
  external_id?: string;
  last_login_at?: string;
  created_at: string;
  updated_at: string;
}

export interface Team {
  id: string;
  organization_id: string;
  name: string;
  description?: string;
  lead_id?: string;
  member_count: number;
  created_at: string;
  updated_at: string;
}

export interface TeamMember {
  id: string;
  team_id: string;
  user_id: string;
  role_in_team: string;
  joined_at: string;
}

export interface CustomRole {
  id: string;
  organization_id: string;
  name: string;
  description?: string;
  is_system: boolean;
  permissions: string[];
  created_at: string;
  updated_at: string;
}

export interface UserListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  search?: string;
}

export const userApi = {
  list(params: UserListParams = {}): Promise<PaginatedResponse<AppUser>> {
    return fetchAPI(`/users${buildQuery(params)}`);
  },
  get(id: string): Promise<AppUser> {
    return fetchAPI(`/users/${id}`);
  },
  create(data: { email: string; display_name: string; status?: string }): Promise<AppUser> {
    return fetchAPI('/users', { method: 'POST', body: JSON.stringify(data) });
  },
  update(
    id: string,
    data: { display_name?: string; status?: string; avatar_url?: string },
  ): Promise<AppUser> {
    return fetchAPI(`/users/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/users/${id}`, { method: 'DELETE' });
  },
};

export const teamApi = {
  list(params: UserListParams = {}): Promise<PaginatedResponse<Team>> {
    return fetchAPI(`/teams${buildQuery(params)}`);
  },
  get(id: string): Promise<Team> {
    return fetchAPI(`/teams/${id}`);
  },
  create(data: { name: string; description?: string; lead_id?: string }): Promise<Team> {
    return fetchAPI('/teams', { method: 'POST', body: JSON.stringify(data) });
  },
  update(
    id: string,
    data: { name?: string; description?: string; lead_id?: string },
  ): Promise<Team> {
    return fetchAPI(`/teams/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/teams/${id}`, { method: 'DELETE' });
  },
  listMembers(teamId: string): Promise<TeamMember[]> {
    return fetchAPI(`/teams/${teamId}/members`);
  },
  addMember(teamId: string, data: { user_id: string; role_in_team?: string }): Promise<TeamMember> {
    return fetchAPI(`/teams/${teamId}/members`, { method: 'POST', body: JSON.stringify(data) });
  },
};

export const roleApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<CustomRole>> {
    return fetchAPI(`/roles${buildQuery(params)}`);
  },
  get(id: string): Promise<CustomRole> {
    return fetchAPI(`/roles/${id}`);
  },
  create(data: { name: string; description?: string; permissions: string[] }): Promise<CustomRole> {
    return fetchAPI('/roles', { method: 'POST', body: JSON.stringify(data) });
  },
  update(
    id: string,
    data: { name?: string; description?: string; permissions?: string[] },
  ): Promise<CustomRole> {
    return fetchAPI(`/roles/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/roles/${id}`, { method: 'DELETE' });
  },
  assign(data: {
    user_id: string;
    custom_role_id: string;
    scope_type?: string;
    scope_id?: string;
  }): Promise<unknown> {
    return fetchAPI('/roles/assign', { method: 'POST', body: JSON.stringify(data) });
  },
};

// --- Search / AI assistant ---
export interface SearchHit {
  id: string;
  organization_id: string;
  entity_type: 'ci' | 'asset' | 'document' | 'ticket' | 'contact' | 'compliance' | string;
  entity_id: string;
  title: string;
  summary?: string;
  url: string;
  score: number;
  highlights?: string[];
  metadata?: Record<string, string>;
  updated_at: string;
}

export interface SearchParams extends ListParams {
  q?: string;
  type?: string;
}

export const searchApi = {
  query(params: SearchParams): Promise<PaginatedResponse<SearchHit>> {
    return fetchAPI(`/search${buildQuery(params)}`);
  },
  reindex(): Promise<{ indexed: number }> {
    return fetchAPI('/search/reindex', { method: 'POST' });
  },
};

export interface AIConversation {
  id: string;
  organization_id: string;
  user_id?: string;
  title: string;
  created_at: string;
  updated_at: string;
}
export interface AICitation {
  entity_type: string;
  entity_id: string;
  title: string;
  url: string;
  score: number;
}
export interface AIAskResponse {
  conversation_id: string;
  answer: string;
  citations: AICitation[];
  prompt_tokens: number;
  completion_tokens: number;
}
export const aiApi = {
  conversations(): Promise<AIConversation[]> {
    return fetchAPI('/ai/conversations');
  },
  createConversation(title?: string): Promise<AIConversation> {
    return fetchAPI('/ai/conversations', { method: 'POST', body: JSON.stringify({ title }) });
  },
  ask(data: { question: string; conversation_id?: string }): Promise<AIAskResponse> {
    return fetchAPI('/ai/ask', { method: 'POST', body: JSON.stringify(data) });
  },
};

// --- Stage 5: Permissions / SLA ---

export interface Permission {
  key: string;
  resource: string;
  action: string;
  description: string;
}

export interface RolePermissionGrant {
  organization_id: string;
  role_id: string;
  permission_key: string;
  granted_at: string;
  granted_by?: string;
}

export interface EffectivePermissionsResponse {
  user_id?: string;
  permissions: string[];
}

export const permissionApi = {
  list(): Promise<Permission[]> {
    return fetchAPI('/permissions');
  },
  listRole(roleId: string): Promise<RolePermissionGrant[]> {
    return fetchAPI(`/roles/${roleId}/permissions`);
  },
  replaceRole(roleId: string, permission_keys: string[]): Promise<RolePermissionGrant[]> {
    return fetchAPI(`/roles/${roleId}/permissions`, {
      method: 'PUT',
      body: JSON.stringify({ permission_keys }),
    });
  },
  effective(): Promise<EffectivePermissionsResponse> {
    return fetchAPI('/me/permissions');
  },
};

export interface SLAPolicy {
  id: string;
  organization_id: string;
  client_id?: string;
  name: string;
  priority: string;
  response_target_minutes: number;
  resolution_target_minutes: number;
  business_calendar: boolean;
  created_at: string;
  updated_at: string;
}

export interface TicketSLA {
  id: string;
  organization_id: string;
  ticket_id: string;
  sla_id: string;
  response_due_at: string;
  resolution_due_at: string;
  first_response_at?: string;
  resolved_at?: string;
  response_breached: boolean;
  resolution_breached: boolean;
  created_at: string;
  updated_at: string;
}

export interface SLAPolicyRequest {
  client_id?: string;
  name: string;
  priority: string;
  response_target_minutes: number;
  resolution_target_minutes: number;
  business_calendar?: boolean;
}

export interface SLAListParams extends ListParams {
  priority?: string;
  client_id?: string;
}

export interface SLABreachParams extends ListParams {
  status?: 'breached' | 'at_risk' | '';
}

export const slaApi = {
  list(params: SLAListParams = {}): Promise<PaginatedResponse<SLAPolicy>> {
    return fetchAPI(`/slas${buildQuery(params)}`);
  },
  create(data: SLAPolicyRequest): Promise<SLAPolicy> {
    return fetchAPI('/slas', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: Partial<SLAPolicyRequest>): Promise<SLAPolicy> {
    return fetchAPI(`/slas/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/slas/${id}`, { method: 'DELETE' });
  },
  breaches(params: SLABreachParams = {}): Promise<PaginatedResponse<TicketSLA>> {
    return fetchAPI(`/slas/breaches${buildQuery(params)}`);
  },
  getTicket(ticketId: string): Promise<TicketSLA> {
    return fetchAPI(`/tickets/${ticketId}/sla`);
  },
  attachTicket(ticketId: string, sla_id?: string): Promise<TicketSLA> {
    return fetchAPI(`/tickets/${ticketId}/sla`, {
      method: 'POST',
      body: JSON.stringify({ sla_id }),
    });
  },
};

// --- Stage 5: Forms / Workflows / Compliance ---

export type JsonRecord = Record<string, unknown>;

export interface FormDefinition {
  id: string;
  organization_id: string;
  client_id?: string;
  name: string;
  description?: string;
  schema: JsonRecord;
  ui_hints: JsonRecord;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface FormSubmission {
  id: string;
  organization_id: string;
  form_id: string;
  values: JsonRecord;
  submitted_by?: string;
  ci_id?: string;
  ticket_id?: string;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface WorkflowDefinition {
  id: string;
  organization_id: string;
  name: string;
  description?: string;
  trigger: JsonRecord;
  conditions: JsonRecord[];
  actions: JsonRecord[];
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface WorkflowStep {
  id: string;
  run_id: string;
  step_index: number;
  action_type: string;
  status: string;
  input: JsonRecord;
  output: JsonRecord;
  error?: string;
  created_at: string;
  updated_at: string;
}

export interface WorkflowRun {
  id: string;
  organization_id: string;
  workflow_id: string;
  status: string;
  trigger: string;
  context: JsonRecord;
  started_at: string;
  finished_at?: string;
  created_at: string;
  updated_at: string;
  steps?: WorkflowStep[];
}

export interface ComplianceRule {
  id: string;
  organization_id: string;
  ci_type_id?: string;
  name: string;
  description?: string;
  severity: string;
  category: string;
  expression: JsonRecord;
  remediation_hint?: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface ComplianceResult {
  id: string;
  organization_id: string;
  rule_id: string;
  ci_id: string;
  ci_type_id: string;
  status: 'pass' | 'fail' | 'not_applicable';
  details?: string;
  evaluated_at: string;
  created_at: string;
  updated_at: string;
}

export interface ComplianceScore {
  ci_type_id?: string;
  passed: number;
  failed: number;
  not_applicable: number;
  score: number;
}

export interface ComplianceEvaluationResponse {
  overall: ComplianceScore;
  by_ci_type: ComplianceScore[];
  results: ComplianceResult[];
}

export const formApi = {
  list(params: ListParams & { active?: boolean } = {}): Promise<PaginatedResponse<FormDefinition>> {
    return fetchAPI(`/forms${buildQuery(params)}`);
  },
  create(data: Partial<FormDefinition>): Promise<FormDefinition> {
    return fetchAPI('/forms', { method: 'POST', body: JSON.stringify(data) });
  },
  submit(id: string, data: { values: JsonRecord; ci_id?: string; ticket_id?: string }) {
    return fetchAPI<FormSubmission>(`/forms/${id}/submissions`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  submissions(params: ListParams & { form_id?: string } = {}) {
    return fetchAPI<PaginatedResponse<FormSubmission>>(`/form-submissions${buildQuery(params)}`);
  },
};

export const workflowApi = {
  list(params: ListParams & { active?: boolean } = {}) {
    return fetchAPI<PaginatedResponse<WorkflowDefinition>>(`/workflows${buildQuery(params)}`);
  },
  runs(params: ListParams & { workflow_id?: string; status?: string } = {}) {
    return fetchAPI<PaginatedResponse<WorkflowRun>>(`/workflow-runs${buildQuery(params)}`);
  },
  trigger(id: string, context: JsonRecord = {}) {
    return fetchAPI<WorkflowRun>(`/workflows/${id}/runs`, {
      method: 'POST',
      body: JSON.stringify({ trigger: 'manual', context }),
    });
  },
  approve(id: string, decision: 'approved' | 'rejected') {
    return fetchAPI<WorkflowRun>(`/workflow-runs/${id}/approval`, {
      method: 'POST',
      body: JSON.stringify({ decision }),
    });
  },
};

export const complianceApi = {
  rules(params: ListParams = {}) {
    return fetchAPI<PaginatedResponse<ComplianceRule>>(`/compliance/rules${buildQuery(params)}`);
  },
  results(params: ListParams & { status?: string } = {}) {
    return fetchAPI<PaginatedResponse<ComplianceResult>>(
      `/compliance/results${buildQuery(params)}`,
    );
  },
  score() {
    return fetchAPI<ComplianceEvaluationResponse>('/compliance/score');
  },
  evaluate() {
    return fetchAPI<ComplianceEvaluationResponse>('/compliance/evaluations', { method: 'POST' });
  },
  report(standard = 'iso27001') {
    return fetchAPI<SecurityReport>(`/compliance/report?standard=${encodeURIComponent(standard)}`);
  },
};

// --- Security & Privacy (Epic F) ---

export interface SecurityFinding {
  rule_id: string;
  rule_name: string;
  severity: 'critical' | 'high' | 'medium' | 'low' | string;
  category: string;
  remediation_hint?: string;
  affected_cis: string[];
}

export interface SecurityReport {
  generated_at: string;
  standard: string;
  audit_integrity: { intact: boolean; checked: number; broken_reason?: string };
  compliance_score: ComplianceScore;
  failures_by_severity: Record<string, number>;
  findings: SecurityFinding[];
  capabilities?: { feature_key: string; enabled: boolean }[];
}

export interface RetentionPolicy {
  id: string;
  organization_id: string;
  retention_days: number;
  mode: 'anonymize' | 'delete';
  created_at: string;
  updated_at: string;
}

export interface ErasureSummary {
  cutoff: string;
  mode: 'anonymize' | 'delete';
  contacts_affected: number;
  users_affected: number;
}

export const privacyApi = {
  retention(): Promise<RetentionPolicy> {
    return fetchAPI('/privacy/retention');
  },
  saveRetention(data: { retention_days?: number; mode?: string }): Promise<RetentionPolicy> {
    return fetchAPI('/privacy/retention', {
      method: 'PUT',
      body: JSON.stringify(data),
    });
  },
  runErasure(): Promise<ErasureSummary> {
    return fetchAPI('/privacy/erasure', { method: 'POST' });
  },
};

export const credentialApi = {
  rotateKeys(): Promise<{ key_version: number; rotated: number }> {
    return fetchAPI('/credentials/rotate-keys', { method: 'POST' });
  },
};

// --- IGA ---
export interface IGAConnector {
  id: string;
  organization_id: string;
  name: string;
  type: 'scim' | 'relay' | string;
  base_url?: string;
  credential_id?: string;
  collector_id?: string;
  capabilities: Record<string, boolean>;
  config?: Record<string, unknown>;
  status: string;
  last_sync_at?: string;
  created_at: string;
  updated_at: string;
}

export interface IGATask {
  id: string;
  connector_id: string;
  user_id?: string;
  external_id?: string;
  action: string;
  status: string;
  attempts: number;
  max_attempts: number;
  next_run_at: string;
  error?: string;
  created_at: string;
}

export interface IGAAccessRequest {
  id: string;
  requester_id: string;
  subject_user_id: string;
  connector_id?: string;
  entitlement: string;
  reason?: string;
  status: string;
  created_at: string;
}

export interface IGAAccessReview {
  id: string;
  name: string;
  description?: string;
  status: string;
  due_at?: string;
  created_at: string;
}

export interface IGADriftFinding {
  id: string;
  connector_id: string;
  external_id: string;
  user_id?: string;
  drift_type: string;
  severity: string;
  status: string;
  created_at: string;
}

export interface IGACreateConnectorRequest {
  name: string;
  type: string;
  base_url?: string;
  collector_id?: string;
  credential_id?: string;
  secret?: Record<string, unknown>;
  config?: Record<string, unknown>;
}

export const igaApi = {
  connectors(params: ListParams = {}): Promise<PaginatedResponse<IGAConnector>> {
    return fetchAPI(`/iga/connectors${buildQuery(params)}`);
  },
  createConnector(data: IGACreateConnectorRequest): Promise<IGAConnector> {
    return fetchAPI('/iga/connectors', { method: 'POST', body: JSON.stringify(data) });
  },
  testConnector(id: string): Promise<Record<string, unknown>> {
    return fetchAPI(`/iga/connectors/${id}/test`, { method: 'POST' });
  },
  syncConnector(id: string): Promise<Record<string, unknown>> {
    return fetchAPI(`/iga/connectors/${id}/sync`, { method: 'POST' });
  },
  tasks(params: ListParams & { status?: string } = {}): Promise<PaginatedResponse<IGATask>> {
    return fetchAPI(`/iga/tasks${buildQuery(params)}`);
  },
  retryTask(id: string): Promise<IGATask> {
    return fetchAPI(`/iga/tasks/${id}/retry`, { method: 'POST' });
  },
  accessRequests(params: ListParams = {}): Promise<PaginatedResponse<IGAAccessRequest>> {
    return fetchAPI(`/iga/access-requests${buildQuery(params)}`);
  },
  approveAccessRequest(id: string, comment = ''): Promise<IGAAccessRequest> {
    return fetchAPI(`/iga/access-requests/${id}/approve`, {
      method: 'POST',
      body: JSON.stringify({ comment }),
    });
  },
  rejectAccessRequest(id: string, comment = ''): Promise<IGAAccessRequest> {
    return fetchAPI(`/iga/access-requests/${id}/reject`, {
      method: 'POST',
      body: JSON.stringify({ comment }),
    });
  },
  reviews(params: ListParams = {}): Promise<PaginatedResponse<IGAAccessReview>> {
    return fetchAPI(`/iga/access-reviews${buildQuery(params)}`);
  },
  drift(params: ListParams = {}): Promise<PaginatedResponse<IGADriftFinding>> {
    return fetchAPI(`/iga/drift${buildQuery(params)}`);
  },
  remediateDrift(id: string): Promise<IGATask> {
    return fetchAPI(`/iga/drift/${id}/remediate`, { method: 'POST' });
  },
};

// --- Webhooks ---

export interface WebhookSubscription {
  id: string;
  organization_id: string;
  name: string;
  url: string;
  events: string[];
  is_active: boolean;
  headers?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface WebhookCreateRequest {
  name: string;
  url: string;
  secret: string;
  events: string[];
  headers?: Record<string, string>;
}

export interface WebhookDeliveryRecord {
  id: string;
  organization_id: string;
  subscription_id: string;
  event: string;
  status: 'pending' | 'retrying' | 'success' | 'failed' | 'dead';
  attempt: number;
  max_attempts: number;
  response_status?: number;
  duration_ms?: number;
  error?: string;
  next_retry_at?: string;
  delivered_at?: string;
  created_at: string;
}

export interface WebhookDeadLetter {
  id: string;
  delivery_id: string;
  organization_id: string;
  subscription_id: string;
  event: string;
  attempts: number;
  last_status_code?: number;
  last_error?: string;
  first_attempt_at?: string;
  dead_at: string;
}

export const webhookApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<WebhookSubscription>> {
    return fetchAPI(`/webhooks${buildQuery(params)}`);
  },
  create(data: WebhookCreateRequest): Promise<WebhookSubscription> {
    return fetchAPI('/webhooks', { method: 'POST', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/webhooks/${id}`, { method: 'DELETE' });
  },
  test(id: string): Promise<WebhookDeliveryRecord> {
    return fetchAPI(`/webhooks/${id}/test`, { method: 'POST' });
  },
  deliveries(
    id: string,
    params: ListParams = {},
  ): Promise<PaginatedResponse<WebhookDeliveryRecord>> {
    return fetchAPI(`/webhooks/${id}/deliveries${buildQuery(params)}`);
  },
  deadLetters(params: ListParams = {}): Promise<PaginatedResponse<WebhookDeadLetter>> {
    return fetchAPI(`/webhooks/dead-letters${buildQuery(params)}`);
  },
};

// --- Export ---

export interface ExportJobFilters {
  status?: string;
  ci_type_id?: string;
  client_id?: string;
}

export interface ExportJob {
  id: string;
  organization_id: string;
  initiated_by?: string;
  format: 'csv' | 'json' | 'datev';
  status: 'pending' | 'running' | 'completed' | 'failed' | 'expired';
  filters: ExportJobFilters;
  object_key?: string;
  row_count?: number;
  file_size_bytes?: number;
  error_message?: string;
  started_at?: string;
  completed_at?: string;
  expires_at?: string;
  download_url?: string;
  created_at: string;
  updated_at: string;
}

export interface ExportJobCreateRequest {
  format: 'csv' | 'json' | 'datev';
  filters?: ExportJobFilters;
}

export const exportApi = {
  listJobs(params: ListParams = {}): Promise<PaginatedResponse<ExportJob>> {
    return fetchAPI(`/export/jobs${buildQuery(params)}`);
  },
  createJob(data: ExportJobCreateRequest): Promise<ExportJob> {
    return fetchAPI('/export/jobs', { method: 'POST', body: JSON.stringify(data) });
  },
  getJob(id: string): Promise<ExportJob> {
    return fetchAPI(`/export/jobs/${id}`);
  },
};

// --- Monitoring ---

export interface MetricPoint {
  timestamp: string;
  value: number;
}

export interface MetricQueryParams {
  ci_id?: string;
  name?: string;
  from?: string;
  to?: string;
  step?: string;
}

export interface AlertRule {
  id: string;
  org_id: string;
  name: string;
  metric_name: string;
  condition: 'gt' | 'lt' | 'eq';
  threshold: number;
  duration: string;
  severity: 'critical' | 'warning' | 'info';
  enabled: boolean;
  pending_since?: string;
  last_fired_at?: string;
}

export interface AlertRuleCreateRequest {
  name: string;
  metric_name: string;
  condition: 'gt' | 'lt' | 'eq';
  threshold: number;
  duration?: string;
  severity?: 'critical' | 'warning' | 'info';
  enabled?: boolean;
}

export const monitoringApi = {
  queryMetrics(params: MetricQueryParams = {}): Promise<MetricPoint[]> {
    return fetchAPI(`/monitoring/metrics${buildQuery(params)}`);
  },
  listAlerts(): Promise<AlertRule[]> {
    return fetchAPI('/monitoring/alerts');
  },
  createAlert(data: AlertRuleCreateRequest): Promise<AlertRule> {
    return fetchAPI('/monitoring/alerts', { method: 'POST', body: JSON.stringify(data) });
  },
  deleteAlert(id: string): Promise<void> {
    return fetchAPI(`/monitoring/alerts/${id}`, { method: 'DELETE' });
  },
};

// --- Audit (hash chain verification) ---

export interface AuditEntry {
  id: string;
  organization_id: string;
  actor_id?: string;
  actor_email?: string;
  action: string;
  entity_type?: string;
  entity_id?: string;
  details?: Record<string, unknown>;
  hash?: string;
  previous_hash?: string;
  created_at: string;
}

export interface AuditVerifyResult {
  intact: boolean;
  checked: number;
  broken_id?: string;
  broken_at?: number;
  broken_reason?: string;
}

export const auditApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<AuditEntry>> {
    return fetchAPI(`/audit${buildQuery(params)}`);
  },
  verify(): Promise<AuditVerifyResult> {
    return fetchAPI('/audit/verify', { method: 'POST' });
  },
};

// ─── Entitlements (feature availability per plan) ───────────────────────────

export interface Entitlement {
  organization_id: string;
  feature_key: string;
  plan: string;
  enabled: boolean;
  updated_at: string;
}

export const entitlementApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<Entitlement>> {
    return fetchAPI(`/entitlements${buildQuery(params)}`);
  },
};

// ─── Sites / GIS ────────────────────────────────────────────────────────────

export interface Site {
  id: string;
  organization_id: string;
  client_id?: string;
  name: string;
  address?: string;
  geo_lat?: number | null;
  geo_lon?: number | null;
  notes?: string;
  created_at: string;
  updated_at: string;
}

export const siteApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<Site>> {
    return fetchAPI(`/sites${buildQuery(params)}`);
  },
  get(id: string): Promise<Site> {
    return fetchAPI(`/sites/${id}`);
  },
};

// ─── Buildings / Rooms (floor-plan view) ────────────────────────────────────

export interface Building {
  id: string;
  organization_id: string;
  site_id: string;
  name: string;
  floors?: number | null;
  floorplan_object_key?: string;
  created_at: string;
  updated_at: string;
}

export interface RoomLayoutPosition {
  x: number;
  y: number;
}

export interface Room {
  id: string;
  organization_id: string;
  building_id: string;
  name: string;
  floor?: number | null;
  room_type?: string;
  layout?: Record<string, RoomLayoutPosition>;
  created_at: string;
  updated_at: string;
}

export const buildingApi = {
  list(params: ListParams & { site_id?: string } = {}): Promise<PaginatedResponse<Building>> {
    return fetchAPI(`/buildings${buildQuery(params)}`);
  },
  get(id: string): Promise<Building> {
    return fetchAPI(`/buildings/${id}`);
  },
};

export const roomApi = {
  list(params: ListParams & { building_id?: string } = {}): Promise<PaginatedResponse<Room>> {
    return fetchAPI(`/rooms${buildQuery(params)}`);
  },
  get(id: string): Promise<Room> {
    return fetchAPI(`/rooms/${id}`);
  },
  updateLayout(id: string, layout: Record<string, RoomLayoutPosition>): Promise<Room> {
    return fetchAPI(`/rooms/${id}`, { method: 'PATCH', body: JSON.stringify({ layout }) });
  },
};
