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
  source?: string;
  last_seen?: string;
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
  source?: string;
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
