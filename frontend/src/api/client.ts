import { clearSession, getSessionToken, refreshSession } from '../auth/session';
import { useScopeStore } from '../stores/scope';
import type { components } from './generated/schema';

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
  /** Node of the location tree; site_id and room_id are derived from it. */
  location_id?: string;
  /** CI version, sent back as If-Match when editing (API-07). */
  version?: number;
  site_id?: string;
  room_id?: string;
  /** Linked asset; it is canonical for serial number, location and inventory data (DB-05). */
  asset_id?: string;
  /** Read-only data of the linked asset. */
  asset?: CIAssetFacts;
  /** Containing CI (chassis of a blade). */
  parent_ci_id?: string;
  /** Software lifecycle; only for types with a lifecycle model. */
  lifecycle_state?: string;
  /** Computed from monitoring; read-only. */
  health?: 'ok' | 'warning' | 'critical' | 'unknown';
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

export interface CIAssetFacts {
  id: string;
  asset_tag: string;
  name: string;
  serial_number?: string;
  location_id?: string;
  lifecycle_state?: string;
  status: string;
  barcode?: string;
  rfid_tag?: string;
  purchase_date?: string;
  warranty_end?: string;
  supplier?: string;
  invoice_number?: string;
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

/**
 * CIUpdateRequest is a JSON merge patch (RFC 7396, CI-04): an absent member
 * stays, null clears a nullable field.
 */
export interface CIUpdateRequest {
  name?: string;
  status?: string;
  manufacturer?: string | null;
  model?: string | null;
  serial_number?: string | null;
  management_ip?: string | null;
  firmware_version?: string | null;
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
  confidence?: number;
  first_seen_at?: string;
  last_seen_at?: string;
  verification_state?: string;
  source_system?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
  /** Structural relationship derived from containment, rack mount or location; read-only (DB-05). */
  projected?: boolean;
  /** Target of mounted_in (the rack) and located_in (the location). */
  target_location_id?: string;
}

/** Verification vocabulary accepted by the relationships API. */
export const verificationStates = ['unverified', 'verified', 'disputed', 'stale'] as const;

export type VerificationState = (typeof verificationStates)[number];

export interface RelationshipCreateRequest {
  source_ci_id: string;
  target_ci_id: string;
  rel_type: string;
  attributes?: Record<string, unknown>;
  source?: string;
  confidence?: number;
  verification_state?: string;
  source_system?: string;
  notes?: string;
}

/**
 * RelationshipUpdateRequest edits provenance and verification metadata. The
 * edge endpoints and `rel_type` are deliberately immutable server-side —
 * rewiring is expressed as delete + recreate so graph history stays truthful.
 */
export interface RelationshipUpdateRequest {
  attributes?: Record<string, unknown>;
  confidence?: number;
  verification_state?: string;
  source_system?: string;
  notes?: string;
}

/** Collector status (COL-01). */
export type CollectorStatus = 'pending' | 'active' | 'offline' | 'disabled' | 'paused';

export interface Collector {
  id: string;
  organization_id: string;
  client_id?: string;
  site_id?: string;
  name: string;
  version?: string;
  status: CollectorStatus;
  status_changed_at?: string;
  last_heartbeat_at?: string;
  cert_fingerprint?: string;
  cert_not_after?: string;
  config: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

/** Discovery scope (DIS-09). */
export type DiscoveryScopeKind = 'cidr' | 'host' | 'range';

export interface DiscoveryScope {
  id: string;
  organization_id: string;
  client_id?: string;
  site_id?: string;
  collector_id: string;
  name: string;
  kind: DiscoveryScopeKind;
  target: string;
  vrf_id?: string;
  schedule: string;
  credential_ids: string[];
  plugins: string[];
  enabled: boolean;
  last_run_at?: string;
  next_run_at?: string;
  last_result?: Record<string, unknown>;
  health?: DiscoveryScopeHealth;
  created_at: string;
  updated_at: string;
}

/** State of the last scope run with the causes of its errors (DIS-08). */
export interface DiscoveryScopeHealth {
  state: 'ok' | 'partial' | 'failed' | 'never_run';
  errors?: DiscoveryErrorState[];
}

export interface DiscoveryErrorState {
  cause: string;
  message?: string;
  protocol?: string;
  target?: string;
}

/** Discovery plugins a scope can run (DiscoveryScope.plugins in api/openapi.yaml). */
export type DiscoveryPlugin =
  'sweep' | 'snmp' | 'ssh' | 'wmi' | 'ipmi' | 'redfish' | 'power' | 'nas';

export interface DiscoveryScopeInput {
  collector_id: string;
  kind: DiscoveryScopeKind;
  target: string;
  name?: string;
  site_id?: string;
  vrf_id?: string;
  schedule?: string;
  /** Empty: the credentials of the scope's client and the organization-wide ones apply (E-67). */
  credential_ids?: string[];
  plugins?: string[];
  enabled?: boolean;
}

/**
 * DiscoveryScopePatch changes a scope; an empty site_id or vrf_id removes
 * the site or VRF, credential_ids and plugins replace the lists.
 */
export type DiscoveryScopePatch = Partial<Omit<DiscoveryScopeInput, 'collector_id'>>;

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
  search?: string;
}

function mergeHeaders(options: RequestInit | undefined, token: string | null) {
  const headers = new Headers(options?.headers);
  if (!headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }

  if (token) {
    headers.set('Authorization', ['Bearer', token].join(' '));
  }

  return headers;
}

/**
 * FieldViolation is a single rejected field from a server-side validation
 * failure. `field` is the attribute/field name exactly as the backend knows it
 * (e.g. `ram_gb`), so it maps directly onto a form input name.
 */
export interface FieldViolation {
  field: string;
  detail: string;
}

/**
 * ApiError carries the parsed RFC 7807 problem detail of a failed request.
 *
 * The backend answers field-metadata validation failures with HTTP 422 and a
 * `violations` array so clients can attach messages to the offending inputs
 * instead of dumping one opaque string at the user. Throwing a plain `Error`
 * discarded that array, so callers keep `message` for the summary banner and
 * read `violations` for per-field rendering.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly title?: string;
  readonly detail?: string;
  readonly violations: FieldViolation[];
  /**
   * dependencies are the kinds of objects that keep a resource from being
   * deleted (DependencyProblem of a 409, LOC-11), e.g. `location` or `ci`.
   */
  readonly dependencies: string[];
  /** traceId is the request id of the failed request (API-03), for support. */
  readonly traceId?: string;
  /** conflictFields are the fields a 409 names as changed since the If-Match version (API-07). */
  readonly conflictFields: string[];
  /** field is the request field a FieldProblem names (400, 409 or 422 at one input). */
  readonly field?: string;

  constructor(
    status: number,
    message: string,
    options: {
      title?: string;
      detail?: string;
      violations?: FieldViolation[];
      dependencies?: string[];
      traceId?: string;
      conflictFields?: string[];
      field?: string;
    } = {},
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.title = options.title;
    this.detail = options.detail;
    this.violations = options.violations ?? [];
    this.dependencies = options.dependencies ?? [];
    this.traceId = options.traceId;
    this.conflictFields = options.conflictFields ?? [];
    this.field = options.field;
  }

  /** Whether the failure carries per-field violations that a form can render. */
  get hasViolations() {
    return this.violations.length > 0;
  }

  /** violationsByField indexes the violations for direct form lookup. */
  violationsByField(): Record<string, string> {
    const byField: Record<string, string> = {};
    for (const violation of this.violations) {
      // Keep the first message per field; the backend orders them by field
      // definition, so the first is the most relevant.
      if (!(violation.field in byField)) {
        byField[violation.field] = violation.detail;
      }
    }
    return byField;
  }

  /** fromProblem builds an ApiError from an arbitrary parsed response body. */
  static fromProblem(status: number, statusText: string, body: unknown): ApiError {
    if (typeof body !== 'object' || body === null) {
      return new ApiError(status, statusText);
    }

    const problem = body as {
      title?: unknown;
      detail?: unknown;
      violations?: unknown;
      dependencies?: unknown;
      trace_id?: unknown;
      fields?: unknown;
      field?: unknown;
    };

    const title = typeof problem.title === 'string' ? problem.title : undefined;
    const traceId = typeof problem.trace_id === 'string' ? problem.trace_id : undefined;
    const detail = typeof problem.detail === 'string' ? problem.detail : undefined;
    const field =
      typeof problem.field === 'string' && problem.field !== '' ? problem.field : undefined;

    const violations: FieldViolation[] = Array.isArray(problem.violations)
      ? problem.violations.flatMap((entry) => {
          if (typeof entry !== 'object' || entry === null) return [];
          const { field, detail: violationDetail } = entry as Record<string, unknown>;
          if (typeof field !== 'string' || field === '') return [];
          return [
            {
              field,
              detail: typeof violationDetail === 'string' ? violationDetail : '',
            },
          ];
        })
      : [];

    const dependencies: string[] = Array.isArray(problem.dependencies)
      ? problem.dependencies.filter(
          (entry): entry is string => typeof entry === 'string' && entry !== '',
        )
      : [];
    const conflictFields = Array.isArray(problem.fields)
      ? problem.fields.filter((field): field is string => typeof field === 'string')
      : [];

    return new ApiError(status, detail || title || statusText, {
      title,
      detail,
      violations,
      dependencies,
      traceId,
      conflictFields,
      field,
    });
  }
}

/**
 * SCOPED_LISTS are the list endpoints that filter by client and site; the
 * scope chosen in the layout (UI-01/UI-02) is added to their GET requests
 * unless the caller set the parameter itself. They are exactly the GET routes
 * of the API with the query parameter client_id or site_id (WP-556; checked
 * against the generated schema in client.test.ts).
 */
export const SCOPED_LISTS: Record<string, readonly ('client_id' | 'site_id')[]> = {
  '/cis': ['client_id', 'site_id'],
  '/export/cis': ['client_id'],
  '/topology': ['client_id', 'site_id'],
  '/assets': ['client_id'],
  '/orders': ['client_id'],
  '/consumables': ['client_id'],
  '/contacts': ['client_id'],
  '/sites': ['client_id'],
  '/buildings': ['site_id'],
  '/vlans': ['site_id'],
  '/subnets': ['client_id', 'site_id'],
  '/slas': ['client_id'],
  '/forms': ['client_id'],
};

/** withScope adds the selected scope to a GET of a scoped list. */
export function withScope(path: string, method = 'GET'): string {
  if (method.toUpperCase() !== 'GET') return path;
  const [base, query = ''] = path.split('?', 2) as [string, string?];
  const keys = SCOPED_LISTS[base];
  if (!keys) return path;
  const { clientId, siteId } = useScopeStore.getState();
  const params = new URLSearchParams(query);
  const values = { client_id: clientId, site_id: siteId };
  for (const key of keys) {
    if (values[key] && !params.has(key)) params.set(key, values[key]);
  }
  const next = params.toString();
  return next ? `${base}?${next}` : base;
}

export async function fetchAPI<T>(path: string, options?: RequestInit): Promise<T> {
  return fetchURL<T>(`${API_BASE}${withScope(path, options?.method)}`, options);
}

/** Versioned is a response body with the ETag the server sent along. */
export interface Versioned<T> {
  data: T;
  /** etag is sent back as If-Match with the next change; undefined without one. */
  etag?: string;
}

/** fetchAPIVersioned is fetchAPI that also returns the response's ETag. */
export async function fetchAPIVersioned<T>(
  path: string,
  options?: RequestInit,
): Promise<Versioned<T>> {
  const res = await fetchResponse(`${API_BASE}${withScope(path, options?.method)}`, options);
  const data = (res.status === 204 ? undefined : await res.json()) as T;
  return { data, etag: res.headers.get('ETag') ?? undefined };
}

async function fetchURL<T>(url: string, options?: RequestInit): Promise<T> {
  const res = await fetchResponse(url, options);
  if (res.status === 204) {
    return undefined as T;
  }
  return res.json();
}

/** fetchResponse sends the request with the session and throws ApiError for a failure. */
async function fetchResponse(url: string, options?: RequestInit): Promise<Response> {
  const request = (token: string | null) =>
    fetch(url, {
      ...options,
      headers: mergeHeaders(options, token),
    });

  const token = getSessionToken();
  let res = await request(token);

  if (res.status === 401) {
    // One refresh per expiry (F-065): a request whose token another request
    // has renewed meanwhile retries with the new one; the others share the
    // single in-flight refresh. A failed refresh has already ended the
    // session if the server refused the cookie (401/403); after a 429 or a
    // network error the session stays and the 401 is reported.
    const current = getSessionToken();
    const renewed = (current !== null && current !== token) || (await refreshSession());
    if (renewed) {
      res = await request(getSessionToken());
      if (res.status === 401) {
        clearSession();
      }
    }
  }

  if (!res.ok) {
    const body: unknown = await res.json().catch(() => null);
    throw ApiError.fromProblem(res.status, res.statusText, body);
  }
  return res;
}

/** BFF_PATH is the GraphQL backend-for-frontend of the UI (GQL-01). */
export const BFF_PATH = '/bff/graphql';

interface GraphQLResult<T> {
  data?: T | null;
  errors?: { message: string; extensions?: { code?: string } }[];
}

/**
 * bffQuery runs a read query against the GraphQL BFF with the session of
 * the UI; mutations go through REST (GQL-02). GraphQL errors are thrown.
 */
export async function bffQuery<T>(query: string, variables?: Record<string, unknown>): Promise<T> {
  const result = await fetchURL<GraphQLResult<T>>(BFF_PATH, {
    method: 'POST',
    body: JSON.stringify({ query, variables }),
  });
  if (result.errors && result.errors.length > 0) {
    throw new Error(result.errors.map((error) => error.message).join('; '));
  }
  return result.data as T;
}

/** The largest page a list endpoint answers (API-02, parameter Limit). */
export const API_MAX_LIMIT = 200;

/**
 * Reads every page of a paginated list (API-02): the first page with the
 * maximum limit of 200, then `next_cursor` until the server sends none. A
 * list without cursor pagination (e.g. collectors, sites) is followed by
 * offset while `has_more` is set (F-362).
 * For callers that need the complete set (permission catalogue, role
 * grants, team members, the module lists of monitoring, document links,
 * assistant conversations, the collectors of the dashboard and the sites of
 * the map); a single page would silently cut it short.
 * `params` are the list's filters, sent with every page.
 */
export async function fetchAllPages<T>(path: string, params: object = {}): Promise<T[]> {
  const items: T[] = [];
  let cursor: string | undefined;
  for (;;) {
    const query = new URLSearchParams(buildQuery(params));
    query.set('limit', String(API_MAX_LIMIT));
    if (cursor) {
      query.set('cursor', cursor);
    } else if (items.length > 0) {
      query.set('offset', String(items.length));
    }
    const page = await fetchAPI<PaginatedResponse<T>>(`${path}?${query.toString()}`);
    const rows = page.data ?? [];
    items.push(...rows);
    cursor = page.next_cursor;
    if (cursor) {
      continue;
    }
    // Offset paging ends with the last page, an empty page or the total.
    if (!page.has_more || rows.length === 0 || items.length >= page.total) {
      return items;
    }
  }
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

export interface CIBulkItemResult {
  id: string;
  result: 'ok' | 'error';
  code: number;
  error?: string;
}

export interface CIBulkResult {
  action: string;
  total: number;
  succeeded: number;
  failed: number;
  items: CIBulkItemResult[];
}

export interface CIBulkJob {
  job_id: string;
  status: string;
  kind: string;
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

  /**
   * update patches a CI. With `version` (the CI version the editor read) the
   * request carries If-Match, so a concurrent change of the same fields is
   * refused with 409 instead of being overwritten (API-07).
   */
  update(id: string, data: CIUpdateRequest, version?: number): Promise<CI> {
    return fetchAPI(`/cis/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
      headers: version !== undefined ? { 'If-Match': `"${version}"` } : undefined,
    });
  },

  delete(id: string): Promise<void> {
    return fetchAPI(`/cis/${id}`, { method: 'DELETE' });
  },

  /**
   * bulk changes the status of up to 500 CIs (API-08): below 100 ids the
   * answer is the report per id, from 100 ids on the job that produces it.
   */
  bulk(ids: string[], status: string): Promise<CIBulkResult | CIBulkJob> {
    return fetchAPI('/cis/bulk', {
      method: 'POST',
      body: JSON.stringify({ action: 'set_status', ids, status }),
    });
  },

  relationships(id: string): Promise<PaginatedResponse<Relationship>> {
    return fetchAPI(`/cis/${id}/relationships`);
  },
};

export const relationshipApi = {
  create(data: RelationshipCreateRequest): Promise<Relationship> {
    return fetchAPI('/relationships', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },

  update(id: string, data: RelationshipUpdateRequest): Promise<Relationship> {
    return fetchAPI(`/relationships/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
  },

  delete(id: string, force = false): Promise<void> {
    return fetchAPI(`/relationships/${id}${force ? '?force=true' : ''}`, { method: 'DELETE' });
  },
};

export const collectorApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<Collector>> {
    return fetchAPI(`/collectors${buildQuery(params)}`);
  },
  /** Every collector of the organization, page by page (F-362). */
  listAll(): Promise<Collector[]> {
    return fetchAllPages('/collectors');
  },
};

/** A subnet a collector reported after enrollment (scope_suggested, INS-06). */
export interface ScopeSuggestion {
  id: string;
  kind: 'scope_suggested';
  status: string;
  payload: {
    collector_id?: string;
    collector_name?: string;
    cidr?: string;
    site_id?: string;
  };
  created_at: string;
}

export const scopeSuggestionApi = {
  list(): Promise<PaginatedResponse<ScopeSuggestion>> {
    return fetchAPI('/review-items?kind=scope_suggested&status=open&limit=100');
  },
  resolve(id: string, action: 'accept' | 'dismiss'): Promise<unknown> {
    return fetchAPI(`/review-items/${id}/resolve`, {
      method: 'POST',
      body: JSON.stringify({ action }),
    });
  },
};

export const discoveryScopeApi = {
  list(collectorId: string): Promise<PaginatedResponse<DiscoveryScope>> {
    return fetchAPI(`/discovery-scopes?collector_id=${encodeURIComponent(collectorId)}&limit=100`);
  },
  create(input: DiscoveryScopeInput): Promise<DiscoveryScope> {
    return fetchAPI('/discovery-scopes', { method: 'POST', body: JSON.stringify(input) });
  },
  get(id: string): Promise<DiscoveryScope> {
    return fetchAPI(`/discovery-scopes/${id}`);
  },
  update(id: string, patch: DiscoveryScopePatch): Promise<DiscoveryScope> {
    return fetchAPI(`/discovery-scopes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/discovery-scopes/${id}`, { method: 'DELETE' });
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

/** A step of an impact path by its origin (IMP-02): the CI the failure comes from and the relationship type. */
export interface ImpactVia {
  ci_id: string;
  rel_type: string;
}

/** One hop of an impact path (IMP-02). */
export interface ImpactPathStep {
  from_ci_id: string;
  to_ci_id: string;
  rel_type: string;
  category: string;
  heuristic?: boolean;
}

/**
 * An impacted CI: every field without `fields`; with it (API-02) the id and
 * the selected fields. `via` is the last step of the shortest path, `relay`
 * the step heuristic paths take through a certain CI.
 */
export interface ImpactedCI extends Partial<TopologyNode> {
  id: string;
  heuristic?: boolean;
  hops?: number;
  via?: ImpactVia;
  relay?: ImpactVia;
  path?: ImpactPathStep[];
}

/**
 * The fields of an impacted CI the topology view asks for: no paths, no
 * client, site or management IP, so the answer stays small (NFR-02).
 */
export const IMPACT_COMPACT_FIELDS = 'name,ci_type,status,heuristic,hops,via,relay';

export interface ImpactResult {
  failed_ci_id: string;
  failed_ci: TopologyNode;
  rel_type: string;
  depth: number;
  impacted: ImpactedCI[];
  count: number;
}

export interface MultiImpactResult {
  failed_cis: TopologyNode[];
  impacted: ImpactedCI[];
  count: number;
}

export const topologyApi = {
  get(params: TopologyParams = {}): Promise<TopologyGraphData> {
    return fetchAPI(`/topology${buildQuery(params)}`);
  },
  neighbors(ciId: string): Promise<TopologyGraphData> {
    return fetchAPI(`/topology/cis/${ciId}/neighbors`);
  },
  impact(
    ciId: string,
    params: { depth?: number; rel_type?: string; fields?: string } = {},
  ): Promise<ImpactResult> {
    return fetchAPI(`/impact/${ciId}${buildQuery(params)}`);
  },
  /** Joint impact of several CIs failing at once (IMP-05). */
  impactMany(
    ciIds: string[],
    params: { rel_type?: string; fields?: string } = {},
  ): Promise<MultiImpactResult> {
    return fetchAPI(`/impact${buildQuery({ ci_ids: ciIds.join(','), ...params })}`);
  },
};

// --- Racks ---

export interface Rack {
  id: string;
  organization_id: string;
  room_id: string;
  name: string;
  height_units: number;
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
  /** Occupancy of a rack: mounts by position and free units per face (RCK-02). */
  layout(rackId: string): Promise<RackLayout> {
    return fetchAPI(`/racks/${rackId}/layout`);
  },
  /** Mounts a CI at a position or moves its mount (RCK-02); 409 on a collision. */
  putMount(
    rackId: string,
    ciId: string,
    body: { position_u: number; height_u?: number; face?: string },
  ): Promise<RackMount> {
    return fetchAPI(`/racks/${rackId}/mounts/${ciId}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    });
  },
};

export interface RackLayout {
  rack: Rack;
  mounts: RackMount[];
  free: Record<string, number[]>;
}

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
    return fetchAllPages(`/documents/${id}/links`);
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
  /** is_builtin marks the seeded standard roles (RBA-02); only they can be invited (TLC-03). */
  is_builtin?: boolean;
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
    return fetchAllPages(`/teams/${teamId}/members`);
  },
  addMember(teamId: string, data: { user_id: string; role_in_team?: string }): Promise<TeamMember> {
    return fetchAPI(`/teams/${teamId}/members`, { method: 'POST', body: JSON.stringify(data) });
  },
};

export type Invitation = components['schemas']['Invitation'];
export type InvitationRequest = components['schemas']['InvitationRequest'];
export type InvitationAcceptance = components['schemas']['InvitationAcceptance'];
export type AcceptedInvitation = components['schemas']['AcceptedInvitation'];

/**
 * invitationApi issues, renews and revokes the organization's invitations
 * (TLC-03, user:manage); accept is public, the token is the credential.
 */
export const invitationApi = {
  list(): Promise<Invitation[]> {
    return fetchAllPages('/invitations');
  },
  issue(data: InvitationRequest): Promise<Invitation> {
    return fetchAPI('/invitations', { method: 'POST', body: JSON.stringify(data) });
  },
  renew(id: string): Promise<Invitation> {
    return fetchAPI(`/invitations/${id}/renew`, { method: 'POST' });
  },
  revoke(id: string): Promise<void> {
    return fetchAPI(`/invitations/${id}`, { method: 'DELETE' });
  },
  accept(data: InvitationAcceptance): Promise<AcceptedInvitation> {
    return fetchAPI('/invitations/accept', { method: 'POST', body: JSON.stringify(data) });
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
    return fetchAPI('/role-assignments', { method: 'POST', body: JSON.stringify(data) });
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
    return fetchAllPages('/ai/conversations');
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
    return fetchAllPages('/permissions');
  },
  listRole(roleId: string): Promise<RolePermissionGrant[]> {
    return fetchAllPages(`/roles/${roleId}/permissions`);
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

/** Credential metadata; the secret material is never returned. */
export interface Credential {
  id: string;
  organization_id: string;
  /** Unset: organization-wide. */
  client_id?: string;
  name: string;
  kind: string;
  scope?: string;
  key_version: number;
  created_at: string;
  updated_at: string;
}

export const credentialApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<Credential>> {
    return fetchAPI(`/credentials${buildQuery(params)}`);
  },
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
  status: 'pending' | 'delivering' | 'delivered' | 'failed' | 'dead';
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
  /** retry queues a failed or dead delivery again (WHK-01). */
  retry(id: string, deliveryId: string): Promise<WebhookDeliveryRecord> {
    return fetchAPI(`/webhooks/${id}/deliveries/${deliveryId}/retry`, { method: 'POST' });
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
  /** Fields of the file and the visible CI fields it leaves out (EXP-01). */
  manifest?: {
    format: string;
    fields: string[];
    omitted: { field: string; reason: string }[];
  };
  created_at: string;
  updated_at: string;
}

export interface ExportJobCreateRequest {
  format: 'csv' | 'json' | 'datev';
  filters?: ExportJobFilters;
}

export const exportApi = {
  listJobs(params: ListParams = {}): Promise<PaginatedResponse<ExportJob>> {
    return fetchAPI(`/exports${buildQuery(params)}`);
  },
  createJob(data: ExportJobCreateRequest): Promise<ExportJob> {
    return fetchAPI('/exports', { method: 'POST', body: JSON.stringify(data) });
  },
  getJob(id: string): Promise<ExportJob> {
    return fetchAPI(`/exports/${id}`);
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

export interface CIMetricSeries {
  name: string;
  points: MetricPoint[];
}

export interface CIMetricsResponse {
  ci_id: string;
  from: string;
  series: CIMetricSeries[];
}

export const monitoringApi = {
  ciMetrics(
    ciId: string,
    params: { name?: string; step?: string } = {},
  ): Promise<CIMetricsResponse> {
    return fetchAPI(`/cis/${ciId}/metrics${buildQuery(params)}`);
  },
  queryMetrics(params: MetricQueryParams = {}): Promise<MetricPoint[]> {
    return fetchAllPages('/monitoring/metrics', params);
  },
  listAlerts(): Promise<AlertRule[]> {
    return fetchAllPages('/monitoring/alerts');
  },
  createAlert(data: AlertRuleCreateRequest): Promise<AlertRule> {
    return fetchAPI('/monitoring/alerts', { method: 'POST', body: JSON.stringify(data) });
  },
  deleteAlert(id: string): Promise<void> {
    return fetchAPI(`/monitoring/alerts/${id}`, { method: 'DELETE' });
  },
};

// --- Audit (hash chain verification) ---

/**
 * AuditEntry is the generated OpenAPI type, so the viewer reads exactly the
 * fields the API returns (timestamp, resource_type, resource_id; F-078).
 */
export type AuditEntry = components['schemas']['AuditEntry'];

export interface AuditVerifyResult {
  intact: boolean;
  checked: number;
  broken_id?: string;
  broken_at?: number;
  broken_reason?: string;
}

export const auditApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<AuditEntry>> {
    return fetchAPI(`/audit-log${buildQuery(params)}`);
  },
  verify(): Promise<AuditVerifyResult> {
    return fetchAPI('/audit-log/verify', { method: 'POST' });
  },
};

// ─── Entitlements (feature availability per plan) ───────────────────────────

export interface Entitlement {
  organization_id: string;
  feature_key: string;
  plan: string;
  enabled: boolean;
  /** Named quotas (ENT-02): max_cis, max_collectors, max_users, max_api_keys. */
  limits: Record<string, number>;
  valid_until?: string;
  source: 'manual' | 'selfsignup' | 'billing' | 'reseller';
  updated_at: string;
}

export const entitlementApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<Entitlement>> {
    return fetchAPI(`/entitlements${buildQuery(params)}`);
  },
};

/**
 * A module with a phase tag from P2 or [A] and whether this release serves
 * it (WP-214). Switched-off modules have no routes; the UI hides them.
 */
export interface ModuleStatus {
  key: string;
  phase: 'P2' | 'P3' | 'P4' | 'A';
  nav: string[];
  enabled: boolean;
}

export const moduleApi = {
  list(): Promise<{ data: ModuleStatus[] }> {
    return fetchAPI('/modules');
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

/** VRF of the organization (TEN-10); every organization has the default VRF. */
export interface VRF {
  id: string;
  organization_id: string;
  name: string;
  description?: string;
  is_default: boolean;
  created_at: string;
  updated_at: string;
}

export const vrfApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<VRF>> {
    return fetchAPI(`/vrfs${buildQuery(params)}`);
  },
};

export const siteApi = {
  list(params: ListParams & { client_id?: string } = {}): Promise<PaginatedResponse<Site>> {
    return fetchAPI(`/sites${buildQuery(params)}`);
  },
  /** Every site the caller may read, page by page (F-362). */
  listAll(): Promise<Site[]> {
    return fetchAllPages('/sites');
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
  /** get returns the room with its ETag, the version a layout change names as If-Match. */
  get(id: string): Promise<Versioned<Room>> {
    return fetchAPIVersioned(`/rooms/${id}`);
  },
  /**
   * updateLayout replaces the room's layout. With an ETag the change is sent
   * with If-Match: a room changed since is refused with 409 (F-342).
   */
  updateLayout(
    id: string,
    layout: Record<string, RoomLayoutPosition>,
    etag?: string,
  ): Promise<Versioned<Room>> {
    return fetchAPIVersioned(`/rooms/${id}`, {
      method: 'PATCH',
      body: JSON.stringify({ layout }),
      headers: etag ? { 'If-Match': etag } : undefined,
    });
  },
};

// ─── Block D modules: consumables, orders, maintenance, disposal, keys, trainings, desks ───

export interface Consumable {
  id: string;
  organization_id: string;
  client_id?: string;
  name: string;
  sku?: string;
  category: string;
  unit: string;
  stock_level: number;
  min_level: number;
  location?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
}

export const consumableApi = {
  list(params: ListParams & { low_stock?: boolean } = {}): Promise<PaginatedResponse<Consumable>> {
    return fetchAPI(`/consumables${buildQuery(params)}`);
  },
  create(data: Partial<Consumable> & { name: string }): Promise<Consumable> {
    return fetchAPI('/consumables', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: Partial<Consumable>): Promise<Consumable> {
    return fetchAPI(`/consumables/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/consumables/${id}`, { method: 'DELETE' });
  },
  addMovement(
    id: string,
    data: { direction: 'in' | 'out'; quantity: number; reason?: string },
  ): Promise<unknown> {
    return fetchAPI(`/consumables/${id}/movements`, { method: 'POST', body: JSON.stringify(data) });
  },
};

export interface Order {
  id: string;
  organization_id: string;
  order_number: string;
  title: string;
  status: string;
  supplier?: string;
  total_cost?: number;
  currency?: string;
  items?: { id: string; description: string; quantity: number; unit_price?: number }[];
  created_at: string;
}

export const orderApi = {
  list(params: ListParams & { status?: string } = {}): Promise<PaginatedResponse<Order>> {
    return fetchAPI(`/orders${buildQuery(params)}`);
  },
  create(data: { title: string; supplier?: string; notes?: string }): Promise<Order> {
    return fetchAPI('/orders', { method: 'POST', body: JSON.stringify(data) });
  },
  addItem(
    id: string,
    data: { description: string; quantity: number; unit_price?: number },
  ): Promise<Order> {
    return fetchAPI(`/orders/${id}/items`, { method: 'POST', body: JSON.stringify(data) });
  },
  submit(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/submit`, { method: 'POST', body: '{}' });
  },
  approve(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/approve`, { method: 'POST', body: '{}' });
  },
  reject(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/reject`, { method: 'POST', body: '{}' });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/orders/${id}`, { method: 'DELETE' });
  },
};

export interface MaintenanceWindow {
  id: string;
  organization_id: string;
  title: string;
  description?: string;
  starts_at: string;
  ends_at: string;
  status: string;
  ci_ids?: string[];
  created_at: string;
}

/** Stored customer notification of a maintenance window (WP-540). */
export interface MaintenanceNotification {
  id: string;
  organization_id: string;
  maintenance_window_id: string;
  client_id?: string;
  channel: string;
  /** pending until a channel has delivered it; then sent or failed. */
  status: 'pending' | 'sent' | 'failed' | string;
  sent_at?: string;
  created_at: string;
}

export const maintenanceApi = {
  list(
    params: ListParams & { status?: string } = {},
  ): Promise<PaginatedResponse<MaintenanceWindow>> {
    return fetchAPI(`/maintenance-windows${buildQuery(params)}`);
  },
  create(data: {
    title: string;
    starts_at: string;
    ends_at: string;
    description?: string;
    ci_ids?: string[];
  }): Promise<MaintenanceWindow> {
    return fetchAPI('/maintenance-windows', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: { status?: string }): Promise<MaintenanceWindow> {
    return fetchAPI(`/maintenance-windows/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /**
   * notify queues one notification per affected client. `notified` counts
   * delivered notifications, `queued` those still pending (WP-063).
   */
  notify(id: string): Promise<{
    notified: number;
    queued: number;
    notifications: MaintenanceNotification[];
  }> {
    return fetchAPI(`/maintenance-windows/${id}/notify`, { method: 'POST', body: '{}' });
  },
  /** notifications lists the stored notifications of a window (one per client and channel). */
  notifications(id: string): Promise<{ data: MaintenanceNotification[] }> {
    return fetchAPI(`/maintenance-windows/${id}/notifications`);
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/maintenance-windows/${id}`, { method: 'DELETE' });
  },
};

export interface DisposalRecord {
  id: string;
  organization_id: string;
  asset_id?: string;
  ci_id?: string;
  method: string;
  certificate_ref?: string;
  data_carrier?: string;
  performed_by?: string;
  performed_at: string;
  created_at: string;
}

export const disposalApi = {
  list(params: ListParams & { method?: string } = {}): Promise<PaginatedResponse<DisposalRecord>> {
    return fetchAPI(`/disposal-records${buildQuery(params)}`);
  },
  create(data: {
    method: string;
    asset_id?: string;
    ci_id?: string;
    certificate_ref?: string;
    data_carrier?: string;
    notes?: string;
  }): Promise<DisposalRecord> {
    return fetchAPI('/disposal-records', { method: 'POST', body: JSON.stringify(data) });
  },
};

export interface KeyItem {
  id: string;
  organization_id: string;
  name: string;
  key_type: string;
  identifier?: string;
  status: string;
  location?: string;
  created_at: string;
}

export const keyApi = {
  list(
    params: ListParams & { status?: string; key_type?: string } = {},
  ): Promise<PaginatedResponse<KeyItem>> {
    return fetchAPI(`/keys${buildQuery(params)}`);
  },
  create(data: {
    name: string;
    key_type?: string;
    identifier?: string;
    location?: string;
  }): Promise<KeyItem> {
    return fetchAPI('/keys', { method: 'POST', body: JSON.stringify(data) });
  },
  issue(id: string, assignedTo: string): Promise<unknown> {
    return fetchAPI(`/keys/${id}/issue`, {
      method: 'POST',
      body: JSON.stringify({ assigned_to: assignedTo }),
    });
  },
  returnKey(id: string): Promise<KeyItem> {
    return fetchAPI(`/keys/${id}/return`, { method: 'POST', body: '{}' });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/keys/${id}`, { method: 'DELETE' });
  },
};

export interface TrainingCourse {
  id: string;
  organization_id: string;
  title: string;
  category?: string;
  validity_months?: number;
  created_at: string;
}

export const trainingApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<TrainingCourse>> {
    return fetchAPI(`/trainings${buildQuery(params)}`);
  },
  create(data: {
    title: string;
    description?: string;
    category?: string;
    validity_months?: number;
  }): Promise<TrainingCourse> {
    return fetchAPI('/trainings', { method: 'POST', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/trainings/${id}`, { method: 'DELETE' });
  },
};

export interface Desk {
  id: string;
  organization_id: string;
  room_id?: string;
  name: string;
  status: string;
  created_at: string;
}

export interface DeskBooking {
  id: string;
  desk_id: string;
  user_id: string;
  starts_at: string;
  ends_at: string;
  status: string;
}

export const deskApi = {
  list(params: ListParams & { status?: string } = {}): Promise<PaginatedResponse<Desk>> {
    return fetchAPI(`/desks${buildQuery(params)}`);
  },
  create(data: { name: string; room_id?: string }): Promise<Desk> {
    return fetchAPI('/desks', { method: 'POST', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/desks/${id}`, { method: 'DELETE' });
  },
  book(id: string, data: { starts_at: string; ends_at: string }): Promise<DeskBooking> {
    return fetchAPI(`/desks/${id}/bookings`, { method: 'POST', body: JSON.stringify(data) });
  },
  listBookings(id: string): Promise<{ data: DeskBooking[] }> {
    return fetchAPI(`/desks/${id}/bookings`);
  },
  cancelBooking(id: string): Promise<DeskBooking> {
    return fetchAPI(`/desk-bookings/${id}/cancel`, { method: 'POST', body: '{}' });
  },
};

export function assetLabelURL(assetId: string): string {
  return `${API_BASE}/assets/${assetId}/label.svg`;
}

// ─── Endpoint agents + security findings (Block E) ──────────────────────────

export interface AgentPolicy {
  interval_seconds: number;
  metrics_enabled: boolean;
  inventory_enabled: boolean;
}

export interface EndpointAgent {
  id: string;
  organization_id: string;
  agent_id: string;
  hostname: string;
  version?: string;
  os?: string;
  arch?: string;
  ci_id?: string;
  status: string;
  last_heartbeat?: string;
  policy: AgentPolicy;
  created_at: string;
}

export const agentApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<EndpointAgent>> {
    return fetchAPI(`/agents${buildQuery(params)}`);
  },
  enroll(data: {
    agent_id: string;
    hostname: string;
    version?: string;
    os?: string;
    arch?: string;
  }): Promise<EndpointAgent> {
    return fetchAPI('/agents/enroll', { method: 'POST', body: JSON.stringify(data) });
  },
  updatePolicy(id: string, data: Partial<AgentPolicy>): Promise<EndpointAgent> {
    return fetchAPI(`/agents/${id}/policy`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  disable(id: string): Promise<EndpointAgent> {
    return fetchAPI(`/agents/${id}/disable`, { method: 'POST', body: '{}' });
  },
  enable(id: string): Promise<EndpointAgent> {
    return fetchAPI(`/agents/${id}/enable`, { method: 'POST', body: '{}' });
  },
};

export interface SecurityFinding {
  id: string;
  organization_id: string;
  ci_id?: string;
  kind: string;
  severity: string;
  title: string;
  package_name?: string;
  installed_version?: string;
  fixed_version?: string;
  reference?: string;
  status: string;
  detected_at: string;
}

export const securityFindingApi = {
  list(
    params: ListParams & { status?: string; severity?: string; kind?: string } = {},
  ): Promise<PaginatedResponse<SecurityFinding>> {
    return fetchAPI(`/security/findings${buildQuery(params)}`);
  },
  summary(): Promise<{ by_severity: Record<string, number>; open_total: number }> {
    return fetchAPI('/security/findings/summary');
  },
  update(id: string, status: string): Promise<SecurityFinding> {
    return fetchAPI(`/security/findings/${id}`, {
      method: 'PATCH',
      body: JSON.stringify({ status }),
    });
  },
};
