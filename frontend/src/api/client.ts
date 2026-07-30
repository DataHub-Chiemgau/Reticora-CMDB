const API_BASE = '/api/v1';

export interface PaginatedResponse<T> {
  data: T[];
  total: number;
  limit: number;
  offset: number;
  has_more: boolean;
}

export interface CI {
  id: string;
  organization_id: string;
  client_id?: string;
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
  status?: string;
  ci_type_id?: string;
  client_id?: string;
  search?: string;
  sort_by?: string;
  sort_dir?: string;
}

export interface ListParams {
  limit?: number;
  offset?: number;
}

async function fetchAPI<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  if (!res.ok) {
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
  Object.entries(params as Record<string, string | number | undefined>).forEach(([key, value]) => {
    if (value !== undefined && value !== '') {
      query.set(key, String(value));
    }
  });
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
  returnAssignment(id: string, data: { return_condition?: string; notes?: string }): Promise<Assignment> {
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
  update(id: string, data: { title?: string; description?: string; category?: string; tags?: string[] }): Promise<Document> {
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
  update(id: string, data: { title?: string; description?: string; status?: string; due_date?: string; total_expected?: number }): Promise<Stocktake> {
    return fetchAPI(`/stocktakes/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/stocktakes/${id}`, { method: 'DELETE' });
  },
  addScan(stocktakeId: string, data: { asset_id?: string; ci_id?: string; scan_method?: string; scan_result: string; location_found?: string; notes?: string }): Promise<StockScan> {
    return fetchAPI(`/stocktakes/${stocktakeId}/scans`, { method: 'POST', body: JSON.stringify(data) });
  },
  listScans(stocktakeId: string, params: ListParams = {}): Promise<PaginatedResponse<StockScan>> {
    return fetchAPI(`/stocktakes/${stocktakeId}/scans${buildQuery(params)}`);
  },
};

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
  addComment(ticketId: string, data: { content: string; is_internal?: boolean }): Promise<TicketComment> {
    return fetchAPI(`/tickets/${ticketId}/comments`, { method: 'POST', body: JSON.stringify(data) });
  },
  listComments(ticketId: string, params: ListParams = {}): Promise<PaginatedResponse<TicketComment>> {
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
  update(id: string, data: { display_name?: string; status?: string; avatar_url?: string }): Promise<AppUser> {
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
  update(id: string, data: { name?: string; description?: string; lead_id?: string }): Promise<Team> {
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
  update(id: string, data: { name?: string; description?: string; permissions?: string[] }): Promise<CustomRole> {
    return fetchAPI(`/roles/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/roles/${id}`, { method: 'DELETE' });
  },
  assign(data: { user_id: string; custom_role_id: string; scope_type?: string; scope_id?: string }): Promise<unknown> {
    return fetchAPI('/roles/assign', { method: 'POST', body: JSON.stringify(data) });
  },
};
