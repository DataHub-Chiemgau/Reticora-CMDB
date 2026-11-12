import { clearSession, getSessionToken, refreshSession } from '../auth/session';
import { useScopeStore } from '../stores/scope';
import { useMetamodelDraftStore } from '../stores/metamodelDraft';
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
  /**
   * Child contract of a CI with a parent (AST-05, WP-273), absent without one.
   * configuration: no asset of its own; asset is the one of the nearest
   * ancestor with an asset (asset_source_ci_id) and read-only here.
   * standalone: own asset, serial number, location and lifecycle.
   */
  child_mode?: 'configuration' | 'standalone';
  /** The CI whose asset a configuration child shows under asset (WP-273). */
  asset_source_ci_id?: string;
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
  /** Host name of the CI; the picker shows it with the management IP (WP-859). */
  hostname?: string;
  firmware_version?: string;
  attributes: Record<string, unknown>;
  /**
   * Keys of the catalog tags of the CI (CI-07, CI-08), sorted; [] without
   * tags. Read-only here: PUT /cis/{id}/tags (ciApi.setTags) writes them.
   */
  tags: string[];
  /**
   * Owner of the CI (CI-07, WP-280): the responsible user (app_user.id) and
   * team. With asset_id both mirror the asset's owner and cannot be written
   * (422 ci_owner_from_asset, DB-05). Not the contact role owner.
   */
  owner_user_id?: string;
  owner_team_id?: string;
  discovery_source?: string;
  first_seen_at?: string;
  last_seen_at?: string;
  created_at: string;
  updated_at: string;
}

/**
 * Read-only data of the asset of a CI (DB-05). The deprecated names
 * asset_tag, warranty_end and supplier (sunset 2028-06-30, E-120 (c)) are
 * left out on purpose: the frontend reads inventory_no, warranty_until and
 * supplier_id (AST-01, WP-271, WP-810).
 */
export interface CIAssetFacts {
  id: string;
  /** Inventory number of the asset (AST-01, WP-738). */
  inventory_no: string;
  name: string;
  serial_number?: string;
  location_id?: string;
  lifecycle_state?: string;
  status: string;
  barcode?: string;
  rfid_tag?: string;
  purchase_date?: string;
  /** End of the warranty of the asset (AST-01, WP-738). */
  warranty_until?: string;
  /** Supplier record of the asset (E-41); its name comes from GET /suppliers/{id}. */
  supplier_id?: string;
  /** Contract of the asset (WP-271). */
  contract_id?: string;
  invoice_number?: string;
  manufacturer?: string;
  model?: string;
  /** Owning user and team of the asset (WP-271); the CI mirrors them. */
  owner_user_id?: string;
  owner_team_id?: string;
}

export interface CICreateRequest {
  ci_type_id: string;
  client_id?: string;
  /** Node of the location tree; site and room follow from it (WP-317). */
  location_id?: string;
  name: string;
  status?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  hardware_uuid?: string;
  management_ip?: string;
  primary_mac?: string;
  hostname?: string;
  fqdn?: string;
  os_name?: string;
  os_version?: string;
  firmware_version?: string;
  sys_object_id?: string;
  attributes?: Record<string, unknown>;
  discovery_source?: string;
  /** Templates assigned with the CI in this order (MET-16, WP-710). */
  templates?: Array<{ template_id: string; version?: number }>;
  /** Owning user (WP-280); 422 on owner_user_id for an unknown user. */
  owner_user_id?: string;
  /** Owning team (WP-280); outside the team scope 403 (TEN-05, E-11). */
  owner_team_id?: string;
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
  /** Owning user; "" clears it (WP-280). Not with asset_id (422). */
  owner_user_id?: string | null;
  /** Owning team; "" clears it (WP-280). Not with asset_id (422). */
  owner_team_id?: string | null;
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

/**
 * A discovery edge removed with force that discovery does not create again
 * (REL-03): who removed it, why and when (F-273).
 */
export type RelationshipSuppression = components['schemas']['RelationshipSuppression'];

/** Verification vocabulary accepted by the relationships API. */
export const verificationStates = ['unverified', 'verified', 'disputed', 'stale'] as const;

export type VerificationState = (typeof verificationStates)[number];

/** One row of the history of a relationship (REL-09, WP-296), derived from the schema (K27). */
export type RelationshipChange = components['schemas']['RelationshipChange'];

/** The kinds of a change of a relationship (WP-296), as change_kind names them. */
export const relationshipChangeKinds = [
  'created',
  'updated',
  'verification_changed',
  'refreshed',
  'removed',
  'suppressed',
  'rewired',
  'restored',
] as const satisfies readonly RelationshipChange['change_kind'][];

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
  /** Allowed CI types, comma-separated (API-02 field filter; the ObjectPicker, WP-318). */
  'filter[ci_type_id][in]'?: string;
  /** The value suggestions of the guided creation (WP-317): one type, prefix filters. */
  'filter[ci_type_id]'?: string;
  'filter[manufacturer]'?: string;
  'filter[manufacturer][like]'?: string;
  'filter[model][like]'?: string;
  /** Sparse fieldset, comma-separated (API-02); id is always returned. */
  fields?: string;
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
 * FieldErrorInfo is the error of one form field (ApiError.fieldErrors):
 * invalid (a violation, 400/422), conflict (409, e.g. a taken
 * inventory_no or a field changed since the read version) or forbidden
 * (403, refused by the field permissions).
 */
export interface FieldErrorInfo {
  kind: 'invalid' | 'conflict' | 'forbidden';
  message: string;
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
  /**
   * conflictFields are the fields of the problem member fields: of a 409 the
   * fields changed since the If-Match version (API-07), of a 403
   * urn:reticora:problem:forbidden the fields the field permissions refuse
   * (RBA-07, WP-729), e.g. hostname or attributes.cost_center of a filter;
   * the filter builder marks those comparisons (WP-315). The problem type
   * is type (WP-318).
   */
  readonly conflictFields: string[];
  /** field is the request field a FieldProblem names (400, 409 or 422 at one input). */
  readonly field?: string;
  /**
   * problem is the parsed problem body as sent, for the extension members
   * of one operation (e.g. stale_items of a refused metamodel publication).
   */
  readonly problem: Record<string, unknown>;
  /**
   * type is the problem type URI of the body (RFC 7807), e.g.
   * PROBLEM_FEATURE_NOT_ENTITLED; undefined without a problem body.
   */
  readonly type?: string;
  /**
   * currentVersion is current_version of a 409 version conflict (API-07):
   * the version the object has now, to reload it before a new attempt.
   */
  readonly currentVersion?: number;
  /**
   * guard is the failed system guard of a refused lifecycle transition
   * (422 lifecycle-guard, WP-279), e.g. storage_location.
   */
  readonly guard?: string;
  /** missing are the references the failed guard misses, e.g. location_id. */
  readonly missing: string[];

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
      problem?: Record<string, unknown>;
      type?: string;
      currentVersion?: number;
      guard?: string;
      missing?: string[];
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
    this.problem = options.problem ?? {};
    this.type = options.type;
    this.currentVersion = options.currentVersion;
    this.guard = options.guard;
    this.missing = options.missing ?? [];
  }

  /**
   * fieldErrors joins what a form shows at its inputs (WP-319): the
   * violations (first message per field), the field of a FieldProblem with
   * the detail, and the conflictFields of a 409 (changed since the read
   * version) or of a 403 (refused by the field permissions, RBA-07). A
   * field keeps its first message; the summary of the error stays message.
   */
  fieldErrors(): Record<string, FieldErrorInfo> {
    const errors: Record<string, FieldErrorInfo> = {};
    const kind: FieldErrorInfo['kind'] =
      this.status === 403 ? 'forbidden' : this.status === 409 ? 'conflict' : 'invalid';
    const add = (field: string, entry: FieldErrorInfo) => {
      if (field !== '' && !(field in errors)) errors[field] = entry;
    };
    for (const [field, detail] of Object.entries(this.violationsByField())) {
      add(field, { kind: 'invalid', message: detail || this.message });
    }
    if (this.field) add(this.field, { kind, message: this.detail || this.message });
    for (const field of this.conflictFields) {
      add(field, { kind, message: this.detail || this.message });
    }
    return errors;
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
      type?: unknown;
      title?: unknown;
      detail?: unknown;
      violations?: unknown;
      dependencies?: unknown;
      trace_id?: unknown;
      fields?: unknown;
      field?: unknown;
      current_version?: unknown;
      guard?: unknown;
      missing?: unknown;
    };

    const title = typeof problem.title === 'string' ? problem.title : undefined;
    const type = typeof problem.type === 'string' && problem.type !== '' ? problem.type : undefined;
    const traceId = typeof problem.trace_id === 'string' ? problem.trace_id : undefined;
    const detail = typeof problem.detail === 'string' ? problem.detail : undefined;
    const field =
      typeof problem.field === 'string' && problem.field !== '' ? problem.field : undefined;

    const violations: FieldViolation[] = Array.isArray(problem.violations)
      ? problem.violations.flatMap((entry) => {
          if (typeof entry !== 'object' || entry === null) return [];
          const {
            field,
            detail: violationDetail,
            message: violationMessage,
          } = entry as Record<string, unknown>;
          if (typeof field !== 'string' || field === '') return [];
          // A lifecycle problem names the text message (LifecycleProblem).
          const text =
            typeof violationDetail === 'string'
              ? violationDetail
              : typeof violationMessage === 'string'
                ? violationMessage
                : '';
          return [{ field, detail: text }];
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
    const currentVersion =
      typeof problem.current_version === 'number' ? problem.current_version : undefined;
    const guard =
      typeof problem.guard === 'string' && problem.guard !== '' ? problem.guard : undefined;
    const missing = Array.isArray(problem.missing)
      ? problem.missing.filter(
          (entry): entry is string => typeof entry === 'string' && entry !== '',
        )
      : [];

    return new ApiError(status, detail || title || statusText, {
      title,
      detail,
      violations,
      dependencies,
      traceId,
      conflictFields,
      field,
      problem: body as Record<string, unknown>,
      type,
      currentVersion,
      guard,
      missing,
    });
  }
}

/** The problem type of a 403 for a feature the plan does not include (ENT-06, E-44). */
export const PROBLEM_FEATURE_NOT_ENTITLED = 'https://reticora.io/problems/feature-not-entitled';
/** The problem type of a 403 for a reached limit of the plan (ENT-06, E-44). */
export const PROBLEM_ENTITLEMENT_LIMIT = 'https://reticora.io/problems/entitlement-limit';
/** The problem type of a 422 for a deactivated CI type (MET-05, WP-249, WP-317). */
export const PROBLEM_TYPE_INACTIVE = 'https://reticora.io/problems/type-inactive';
/** The violation text of POST /cis on ci_type_id for a deactivated type (WP-249). */
export const TYPE_INACTIVE_DETAIL = 'the CI type is deactivated';

/**
 * isTypeInactive reports a refused creation of a deactivated CI type: the
 * problem type type-inactive, or a 422 whose violation on ci_type_id names
 * the deactivated type (the form POST /cis answers today).
 */
export function isTypeInactive(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 422) return false;
  if (error.type === PROBLEM_TYPE_INACTIVE) return true;
  return error.violations.some(
    (v) => v.field === 'ci_type_id' && v.detail.startsWith(TYPE_INACTIVE_DETAIL),
  );
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
  // The types hidden for the chosen client are left out (WP-858);
  // include_inactive=true lists the whole catalog.
  '/ci-types': ['client_id'],
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
  // The address lists filter by client (IPM-20, WP-909); /ip-addresses is
  // the path until its sunset (API-01).
  '/ips': ['client_id'],
  '/ip-addresses': ['client_id'],
  // The AS numbers filter by client (IPM-16, WP-1066).
  '/asns': ['client_id'],
  '/slas': ['client_id'],
  '/business-calendars': ['client_id'],
  '/forms': ['client_id'],
  // Monitoring P3 (WP-938, E-160): probes, monitoring groups and collector
  // groups filter by client (collector groups also by site).
  '/monitoring/probes': ['client_id'],
  '/monitoring/groups': ['client_id'],
  '/collector-groups': ['client_id', 'site_id'],
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

/**
 * DRAFT_PREFIXES are the metamodel resources whose writes a draft captures
 * (MET-53, WP-720): every POST, PUT, PATCH and DELETE below them is in
 * DraftCapableRoutes of the backend, except DRAFT_EXEMPT. Instance fields
 * (/cis/{id}/field-definitions, MET-14) and /metamodel/* are no metamodel
 * resource routes.
 */
export const DRAFT_PREFIXES = [
  '/ci-types',
  '/attribute-definitions',
  '/ci-fields/global',
  '/attribute-sets',
  '/templates',
  '/rules',
  '/layouts',
] as const;

/**
 * DRAFT_EXEMPT are the writes below DRAFT_PREFIXES that change no metamodel
 * (DraftExemptRoutes): the template migration (job template.migrate) and
 * the rebuild of an index (job attribute.index). The backend refuses
 * ?draft there with 400 draft-unsupported.
 */
const DRAFT_EXEMPT = [
  /^\/templates\/[^/]+\/migrations$/,
  /^\/attribute-definitions\/[^/]+\/reindex$/,
];

const DRAFT_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

/** draftCapable reports whether a write of path can be captured in a draft. */
export function draftCapable(path: string, method = 'GET'): boolean {
  if (!DRAFT_METHODS.has(method.toUpperCase())) return false;
  const base = path.split('?', 1)[0] ?? '';
  if (DRAFT_EXEMPT.some((re) => re.test(base))) return false;
  return DRAFT_PREFIXES.some((prefix) => base === prefix || base.startsWith(`${prefix}/`));
}

/**
 * withDraft adds the active metamodel draft (stores/metamodelDraft) as
 * `draft=<id>` to a capturable metamodel write, unless the caller set the
 * parameter itself; without an active draft the path stays as it is.
 */
export function withDraft(path: string, method = 'GET'): string {
  const { draft } = useMetamodelDraftStore.getState();
  if (!draft || !draftCapable(path, method)) return path;
  const [base, query = ''] = path.split('?', 2) as [string, string?];
  const params = new URLSearchParams(query);
  if (params.has('draft')) return path;
  params.set('draft', draft.id);
  return `${base}?${params.toString()}`;
}

/** apiURL is the URL of an API path with the scope and the active draft. */
function apiURL(path: string, method?: string): string {
  return `${API_BASE}${withDraft(withScope(path, method), method)}`;
}

/**
 * noteDraftCapture counts a write the backend captured in a draft (202 on a
 * request with ?draft): the item list of the draft is read again.
 */
function noteDraftCapture(url: string, res: Response) {
  if (res.status !== 202) return;
  const query = url.split('?', 2)[1];
  if (query && new URLSearchParams(query).has('draft')) {
    useMetamodelDraftStore.getState().bumpItemRevision();
  }
}

export async function fetchAPI<T>(path: string, options?: RequestInit): Promise<T> {
  const url = apiURL(path, options?.method);
  const res = await fetchResponse(url, options);
  noteDraftCapture(url, res);
  if (res.status === 204) {
    return undefined as T;
  }
  return res.json();
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
  const url = apiURL(path, options?.method);
  const res = await fetchResponse(url, options);
  noteDraftCapture(url, res);
  const data = (res.status === 204 ? undefined : await res.json()) as T;
  return { data, etag: res.headers.get('ETag') ?? undefined };
}

/**
 * fetchBlob reads a binary answer of an API path, e.g. a PDF label: with the
 * bearer token, one refresh after a 401 and a failure as ApiError, as
 * fetchResponse does (WP-526). A link with the URL alone would carry no token.
 */
export async function fetchBlob(path: string, options?: RequestInit): Promise<Blob> {
  const res = await fetchResponse(apiURL(path, options?.method), options);
  return res.blob();
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
  errors?: { message: string; path?: (string | number)[]; extensions?: { code?: string } }[];
}

/** BffFieldError is one GraphQL error of a partial answer: the field path and the code. */
export interface BffFieldError {
  message: string;
  /** path is the field path of the error, e.g. ['asset', 'documents']; empty for the query. */
  path: (string | number)[];
  /** code is extensions.code, e.g. FORBIDDEN or UNAVAILABLE. */
  code?: string;
}

/** BffPartialResult is the data of a BFF query with the errors of its fields. */
export interface BffPartialResult<T> {
  data: T | null;
  errors: BffFieldError[];
}

/**
 * bffQuery runs a read query against the GraphQL BFF with the session of
 * the UI; mutations go through REST (GQL-02). GraphQL errors are thrown.
 *
 * The BFF is API-only for now (GQL-01, F-332): no page uses it in phase 1,
 * every UI read goes through REST; the first aggregate view starts to use
 * this client. See docs/README.md, section "GraphQL-BFF".
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

/**
 * bffQueryPartial runs a read query like bffQuery, but a partial answer is
 * not thrown (WP-809): the data comes back with the errors of the failed
 * fields (path and extensions.code), so a page hides only the section whose
 * field the server refused (FORBIDDEN) or left out (UNAVAILABLE, module
 * off). A failed request (HTTP status) is still thrown as ApiError.
 */
export async function bffQueryPartial<T>(
  query: string,
  variables?: Record<string, unknown>,
): Promise<BffPartialResult<T>> {
  const result = await fetchURL<GraphQLResult<T>>(BFF_PATH, {
    method: 'POST',
    body: JSON.stringify({ query, variables }),
  });
  return {
    data: result.data ?? null,
    errors: (result.errors ?? []).map((error) => ({
      message: error.message,
      path: error.path ?? [],
      code: error.extensions?.code,
    })),
  };
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

export function buildQuery(params: object) {
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
  result: 'ok' | 'error' | 'skipped';
  code: number;
  error?: string;
  /** Problem type of an error (RFC 7807). */
  type?: string;
  /** Fields the error names. */
  fields?: string[];
}

/** The partial failure report of a bulk on CIs or assets (BulkResult, API-08). */
export interface CIBulkResult {
  action: string;
  total: number;
  succeeded: number;
  failed: number;
  skipped?: number;
  cancelled?: boolean;
  /**
   * The job ci.bulk or asset.bulk the change ran in within the request
   * (below 100 ids, WP-305, WP-784); POST /jobs/{id}/revert undoes it.
   */
  bulk_job_id?: string;
  items: CIBulkItemResult[];
}

/** The 202 of a bulk: the bulk job (from 100 ids on) or, for export, the export job. */
export interface CIBulkJob {
  job_id: string;
  status: string;
  kind: string;
}

/**
 * A bulk request (BLK-02): POST /cis/bulk (CIBulkRequest, WP-304, WP-780)
 * or POST /assets/bulk (AssetBulkRequest and AssetBulkExport, WP-781). The
 * BLK-01 form {action: 'set_status', ids, status} stays valid; every other
 * action carries its parameters in params.
 */
export interface BulkRequest {
  action: string;
  ids: string[];
  status?: string;
  params?: Record<string, unknown>;
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
   * setTags replaces the tags of a CI with catalog keys (PUT /cis/{id}/tags,
   * CI-08); [] removes every tag, an unknown key is 422 and is not created.
   * With `version` the request carries If-Match like update: 412 when the
   * version is unknown, 409 when the tags changed since (API-07).
   */
  setTags(id: string, tags: string[], version?: number): Promise<CI> {
    return fetchAPI(`/cis/${id}/tags`, {
      method: 'PUT',
      body: JSON.stringify({ tags }),
      headers: version !== undefined ? { 'If-Match': `"${version}"` } : undefined,
    });
  },

  /**
   * bulk applies one action to up to 500 CIs (API-08, BLK-02): below 100 ids
   * the answer is the report per id (with the bulk_job_id to revert it),
   * from 100 ids on the job that produces it; export answers its export
   * job. bulk(ids, status) is the BLK-01 status change.
   */
  bulk(request: string[] | BulkRequest, status?: string): Promise<CIBulkResult | CIBulkJob> {
    const body: BulkRequest = Array.isArray(request)
      ? { action: 'set_status', ids: request, status }
      : request;
    return fetchAPI('/cis/bulk', { method: 'POST', body: JSON.stringify(body) });
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

  /**
   * The history of an edge, newest first (GET /relationships/{id}/changes,
   * WP-296); the rows stay after the edge is deleted.
   */
  changes(id: string): Promise<PaginatedResponse<RelationshipChange>> {
    return fetchAPI(`/relationships/${id}/changes?limit=100`);
  },

  /**
   * A discovery edge is deleted with force and then suppressed (REL-04);
   * reason is stored with the suppression (F-273).
   */
  delete(id: string, force = false, reason?: string): Promise<void> {
    const query = new URLSearchParams();
    if (force) query.set('force', 'true');
    if (reason) query.set('reason', reason);
    const search = query.toString();
    return fetchAPI(`/relationships/${id}${search ? `?${search}` : ''}`, { method: 'DELETE' });
  },
};

/** Suppressions of removed discovery edges: listed per CI and lifted (F-273). */
export const relationshipSuppressionApi = {
  list(ciId: string): Promise<PaginatedResponse<RelationshipSuppression>> {
    return fetchAPI(`/relationship-suppressions?ci_id=${encodeURIComponent(ciId)}&limit=100`);
  },

  lift(id: string): Promise<void> {
    return fetchAPI(`/relationship-suppressions/${id}`, { method: 'DELETE' });
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

/**
 * A node of the topology (K27): a CI, or with include=network a subnet or a
 * VLAN projected from IPAM (IMP-11, WP-298). node_kind is absent for a CI; a
 * projected node carries no ci_type and no status and is no CI: it cannot
 * fail in the simulation, be the start CI or open a CI page (WP-818).
 */
export type TopologyNode = components['schemas']['TopologyNode'];

/** The kinds of a topology node (TopologyNode.node_kind, IMP-11, WP-298). */
export const NODE_KINDS = ['ci', 'subnet', 'vlan'] as const;
export type NodeKind = (typeof NODE_KINDS)[number];

/** isProjectedNode tells a projected subnet or VLAN apart from a CI (IMP-11). */
export function isProjectedNode(node: Pick<TopologyNode, 'node_kind'>): boolean {
  return node.node_kind !== undefined && node.node_kind !== 'ci';
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
  /** network adds the subnets and VLANs projected from IPAM (IMP-11, WP-298). */
  include?: 'network';
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

/**
 * The blocks of /impact for include=services,clients,sites,spof (IMP-08,
 * WP-297); summary and truncated come with every answer (K27).
 */
export const IMPACT_PANEL_INCLUDE = 'services,clients,sites,spof';

export interface MultiImpactResult extends Pick<
  components['schemas']['ImpactAnalysisFields'],
  | 'summary'
  | 'truncated'
  | 'affected_services'
  | 'client_ids'
  | 'site_ids'
  | 'service_spofs'
  | 'spof_truncated'
  | 'blast_radius'
> {
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
    params: { rel_type?: string; fields?: string; include?: string } = {},
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

/** A mount of the rack layout: it always names the CI and its type (F-252). */
export type RackLayoutMount = components['schemas']['RackLayoutMount'];

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
};

export interface RackLayout {
  rack: Rack;
  mounts: RackLayoutMount[];
  free: Record<string, number[]>;
}

// --- Phase 2: Asset Management ---

// Asset and the request types of the writes come from the OpenAPI contract
// (K27, WP-319) with the AST-01 names (inventory_no, purchase_price,
// warranty_until, supplier_id, primary_ci_id; WP-739, WP-272) and version
// (WP-740). The deprecated status and location stay in the contract until
// E-120 removes them; the UI shows lifecycle_state and location_id.
export type Asset = components['schemas']['Asset'];
export type AssetCreateRequest = components['schemas']['CreateAssetRequest'];
export type AssetUpdateRequest = components['schemas']['UpdateAssetRequest'];

/** AssetFilterOperator is an operator of filter[field][op] (API-02). */
export type AssetFilterOperator = 'gte' | 'lte' | 'like' | 'in';

/** AssetFilter is one field filter of GET /assets: a value, optionally with an operator. */
export interface AssetFilter {
  field: string;
  value: string;
  op?: AssetFilterOperator;
}

export interface AssetListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  /** Deprecated status filter of the dashboard widget (E-120). */
  status?: string;
  category?: string;
  client_id?: string;
  search?: string;
  sort_by?: string;
  sort_dir?: string;
  /** sort is a field of the sort whitelist, a leading - sorts descending (API-02). */
  sort?: string;
  /** fields are the response fields; id is always returned (API-02). */
  fields?: readonly string[];
  /** filter are the field filters, sent as filter[field] or filter[field][op]. */
  filter?: readonly AssetFilter[];
}

/**
 * assetListQuery builds the query of GET /assets after api.ParseListQuery:
 * the plain parameters, sort, fields (comma-separated) and every filter as
 * filter[<field>] or filter[<field>][<op>].
 */
export function assetListQuery(params: AssetListParams): string {
  const { fields, filter, ...plain } = params;
  const query: Record<string, string | number | undefined> = { ...plain };
  if (fields && fields.length > 0) query.fields = fields.join(',');
  for (const entry of filter ?? []) {
    if (entry.value === '') continue;
    query[entry.op ? `filter[${entry.field}][${entry.op}]` : `filter[${entry.field}]`] =
      entry.value;
  }
  return buildQuery(query);
}

export const assetApi = {
  list(params: AssetListParams = {}): Promise<PaginatedResponse<Asset>> {
    return fetchAPI(`/assets${assetListQuery(params)}`);
  },
  /**
   * bulk applies one action to up to 500 assets (POST /assets/bulk, WP-781):
   * set_status among them instead of parallel PATCHes. Every asset bulk is
   * a job asset.bulk; below 100 ids it runs in the request and the report
   * names it as bulk_job_id, from 100 ids on the answer is 202 with it.
   */
  bulk(request: BulkRequest): Promise<CIBulkResult | CIBulkJob> {
    return fetchAPI('/assets/bulk', { method: 'POST', body: JSON.stringify(request) });
  },
  get(id: string): Promise<Asset> {
    return fetchAPI(`/assets/${id}`);
  },
  create(data: AssetCreateRequest): Promise<Asset> {
    return fetchAPI('/assets', { method: 'POST', body: JSON.stringify(data) });
  },
  /**
   * update patches an asset. With `version` (the asset version the editor
   * read, WP-740) the request carries If-Match like ciApi.update, so a
   * concurrent change is refused with 409 instead of being overwritten.
   */
  update(id: string, data: AssetUpdateRequest, version?: number): Promise<Asset> {
    return fetchAPI(`/assets/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
      headers: version !== undefined ? { 'If-Match': `"${version}"` } : undefined,
    });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/assets/${id}`, { method: 'DELETE' });
  },
  /**
   * linkCI links a CI to the asset (AST-04, WP-272): primary true makes it
   * the primary CI, false clears the primary CI when it is this one.
   */
  linkCI(id: string, data: LinkAssetCIRequest): Promise<AssetCILink> {
    return fetchAPI(`/assets/${id}/cis`, { method: 'POST', body: JSON.stringify(data) });
  },
  /** unlinkCI releases the CI from the asset (DELETE /assets/{id}/cis/{ci_id}). */
  unlinkCI(id: string, ciId: string): Promise<void> {
    return fetchAPI(`/assets/${id}/cis/${ciId}`, { method: 'DELETE' });
  },
};

export type LinkAssetCIRequest = components['schemas']['LinkAssetCIRequest'];
export type AssetCILink = components['schemas']['AssetCILink'];

// --- Phase 2: Assignment Management ---

// Assignment and the request types of the writes come from the OpenAPI
// contract (K27, WP-746): the holder is a user (assigned_to) or a team
// (assigned_team_id), due_at the due date.
export type Assignment = components['schemas']['Assignment'];
export type AssignAssetRequest = components['schemas']['AssignAssetRequest'];
export type ReturnAssetRequest = components['schemas']['ReturnAssetRequest'];
export type TransferAssetRequest = components['schemas']['TransferAssetRequest'];
/** The return movement POST /assets/{id}/return answers. */
export type AssetReturnMovement = components['schemas']['StockMovement'];

export interface AssignmentListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  status?: string;
  assigned_to?: string;
  asset_id?: string;
  search?: string;
}

/**
 * assignmentApi reads below /assignments (read only, E-38) and writes
 * through the asset (WP-746): assign, return and transfer take the asset
 * id; return and transfer name the current holder as expected_holder (409
 * when it changed meanwhile).
 */
export const assignmentApi = {
  list(params: AssignmentListParams = {}): Promise<PaginatedResponse<Assignment>> {
    return fetchAPI(`/assignments${buildQuery(params)}`);
  },
  get(id: string): Promise<Assignment> {
    return fetchAPI(`/assignments/${id}`);
  },
  assign(assetId: string, data: AssignAssetRequest): Promise<Assignment> {
    return fetchAPI(`/assets/${assetId}/assign`, { method: 'POST', body: JSON.stringify(data) });
  },
  returnAsset(assetId: string, data: ReturnAssetRequest): Promise<AssetReturnMovement> {
    return fetchAPI(`/assets/${assetId}/return`, { method: 'POST', body: JSON.stringify(data) });
  },
  transfer(assetId: string, data: TransferAssetRequest): Promise<Assignment> {
    return fetchAPI(`/assets/${assetId}/transfer`, { method: 'POST', body: JSON.stringify(data) });
  },
};

// --- Phase 2: Document Management ---

/** A document with the scan status of its current version (WP-731, WP-733), from the schema (K27). */
export type Document = components['schemas']['Document'];

/** One stored version of a document's content (WP-731). */
export type DocumentVersion = components['schemas']['DocumentVersion'];

/** The stored bytes of the organization and its limit max_storage_mb (WP-731). */
export type DocumentUsage = components['schemas']['DocumentUsage'];

/** The malware scan status of a document version (SEC-09, WP-733). */
export type ScanStatus = DocumentVersion['scan_status'];

/** The scan states of a version; only clean is downloadable (E-42). */
export const SCAN_STATUSES = [
  'pending_scan',
  'clean',
  'infected',
  'scan_failed',
] as const satisfies readonly ScanStatus[];

/** The problem type of a 409 for a download of a version whose scan is pending or failed. */
export const PROBLEM_DOCUMENT_PENDING_SCAN = 'https://reticora.io/problems/document-pending-scan';
/** The problem type of a 409 for a download or link of a quarantined (infected) document. */
export const PROBLEM_DOCUMENT_QUARANTINED = 'https://reticora.io/problems/document-quarantined';
/** The problem type of a 422 for content that does not match its declared type (WP-268). */
export const PROBLEM_MIME_MISMATCH = 'https://reticora.io/problems/mime-mismatch';

export type DocumentCreateRequest = components['schemas']['CreateDocumentRequest'];

export interface DocumentListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  category?: string;
  search?: string;
  /** Only documents linked to this target (WP-811); requires entity_id. */
  entity_type?: string;
  entity_id?: string;
}

export type DocumentLink = components['schemas']['DocumentLink'];

/** The target of a document link (CreateDocumentLinkRequest). */
export type DocumentLinkTarget = components['schemas']['CreateDocumentLinkRequest'];

/** UploadOptions follow the transfer of a document's content. */
export interface UploadOptions {
  /** onProgress receives the sent and the total bytes of the body. */
  onProgress?: (loaded: number, total: number) => void;
  /** signal aborts the transfer. */
  signal?: AbortSignal;
}

/** parseBody reads the JSON body of an XMLHttpRequest; anything else is null. */
function parseBody(xhr: XMLHttpRequest): unknown {
  try {
    return xhr.responseText ? (JSON.parse(xhr.responseText) as unknown) : null;
  } catch {
    return null;
  }
}

/**
 * sendFile sends a file as the raw body of a request per XMLHttpRequest,
 * because fetch reports no upload progress: Content-Type is the type of the
 * file, the session token goes as Bearer. A 401 refreshes the session once
 * and repeats the transfer, as fetchResponse does (WP-526); a failure is an
 * ApiError from the problem body (with type and traceId).
 */
async function sendFile<T>(
  method: 'PUT' | 'POST',
  path: string,
  file: Blob,
  options: UploadOptions = {},
): Promise<T> {
  const url = apiURL(path, method);
  const send = (token: string | null) =>
    new Promise<XMLHttpRequest>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open(method, url);
      xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
      if (token) xhr.setRequestHeader('Authorization', ['Bearer', token].join(' '));
      xhr.upload.onprogress = (event) => {
        options.onProgress?.(event.loaded, event.lengthComputable ? event.total : file.size);
      };
      xhr.onload = () => resolve(xhr);
      xhr.onerror = () => reject(new ApiError(0, 'Network error'));
      xhr.onabort = () => reject(new DOMException('Aborted', 'AbortError'));
      if (options.signal) {
        if (options.signal.aborted) {
          reject(new DOMException('Aborted', 'AbortError'));
          return;
        }
        options.signal.addEventListener('abort', () => xhr.abort(), { once: true });
      }
      xhr.send(file);
    });

  const token = getSessionToken();
  let xhr = await send(token);
  if (xhr.status === 401) {
    const current = getSessionToken();
    const renewed = (current !== null && current !== token) || (await refreshSession());
    if (renewed) {
      xhr = await send(getSessionToken());
      if (xhr.status === 401) {
        clearSession();
      }
    }
  }
  if (xhr.status < 200 || xhr.status >= 300) {
    throw ApiError.fromProblem(xhr.status, xhr.statusText, parseBody(xhr));
  }
  return parseBody(xhr) as T;
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
  update(id: string, data: components['schemas']['UpdateDocumentRequest']): Promise<Document> {
    return fetchAPI(`/documents/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/documents/${id}`, { method: 'DELETE' });
  },
  /** PUT /documents/{id}/content: the first content of a new document (WP-268). */
  uploadContent(id: string, file: Blob, options?: UploadOptions): Promise<Document> {
    return sendFile('PUT', `/documents/${id}/content`, file, options);
  },
  /** POST /documents/{id}/versions: a new version of a document (WP-731). */
  createVersion(id: string, file: Blob, options?: UploadOptions): Promise<DocumentVersion> {
    return sendFile('POST', `/documents/${id}/versions`, file, options);
  },
  /** One page of the versions of a document, newest first (WP-731). */
  listVersions(id: string, cursor?: string): Promise<PaginatedResponse<DocumentVersion>> {
    return fetchAPI(`/documents/${id}/versions${buildQuery({ cursor })}`);
  },
  /** The presigned download URL of a version (default the current one); 409 unless clean. */
  contentUrl(id: string, version?: number): Promise<{ download_url: string }> {
    return fetchAPI(`/documents/${id}/content${buildQuery({ version })}`);
  },
  link(id: string, data: DocumentLinkTarget): Promise<DocumentLink> {
    return fetchAPI(`/documents/${id}/links`, { method: 'POST', body: JSON.stringify(data) });
  },
  getLinks(id: string): Promise<DocumentLink[]> {
    return fetchAllPages(`/documents/${id}/links`);
  },
  /** DELETE /documents/{id}/links/{link_id} (WP-267). */
  unlink(id: string, linkId: string): Promise<void> {
    return fetchAPI(`/documents/${id}/links/${linkId}`, { method: 'DELETE' });
  },
  /** The storage usage of the organization (WP-731). */
  usage(): Promise<DocumentUsage> {
    return fetchAPI('/documents/usage');
  },
};

// --- Phase 2: Suppliers and contracts (MGT-12, WP-808) ---

// Supplier, contract and contact and the request types come from the
// OpenAPI contract (K27; WP-736, WP-269). Both lists read api.ParseListQuery
// (filter[field][op], sort, cursor) and take no client_id parameter, so they
// are not in SCOPED_LISTS: ContractsPage sends the chosen client itself as
// filter[client_id], the suppliers stay without a scope filter.
export type Supplier = components['schemas']['Supplier'];
export type SupplierCreateRequest = components['schemas']['CreateSupplierRequest'];
export type SupplierUpdateRequest = components['schemas']['UpdateSupplierRequest'];
export type Contract = components['schemas']['Contract'];
export type ContractCreateRequest = components['schemas']['CreateContractRequest'];
export type ContractUpdateRequest = components['schemas']['UpdateContractRequest'];
export type Contact = components['schemas']['Contact'];

/** FilteredListParams is a page of a list read with api.ParseListQuery (API-02). */
export interface FilteredListParams {
  limit?: number;
  cursor?: string;
  /** sort is a field of the sort whitelist, a leading - sorts descending. */
  sort?: string;
  /** filter are the field filters, sent as filter[field] or filter[field][op]. */
  filter?: readonly AssetFilter[];
}

/** ifMatch is the If-Match header of a change based on version (API-07). */
function ifMatch(version?: number): HeadersInit | undefined {
  return version !== undefined ? { 'If-Match': `"${version}"` } : undefined;
}

export const supplierApi = {
  list(params: FilteredListParams = {}): Promise<PaginatedResponse<Supplier>> {
    return fetchAPI(`/suppliers${assetListQuery(params)}`);
  },
  get(id: string): Promise<Supplier> {
    return fetchAPI(`/suppliers/${id}`);
  },
  create(data: SupplierCreateRequest): Promise<Supplier> {
    return fetchAPI('/suppliers', { method: 'POST', body: JSON.stringify(data) });
  },
  /** update patches a supplier; with version the request carries If-Match (412 when stale). */
  update(id: string, data: SupplierUpdateRequest, version?: number): Promise<Supplier> {
    return fetchAPI(`/suppliers/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
      headers: ifMatch(version),
    });
  },
  /** delete removes a supplier; one still referenced by a contract is 422. */
  delete(id: string): Promise<void> {
    return fetchAPI(`/suppliers/${id}`, { method: 'DELETE' });
  },
};

export const contractApi = {
  list(params: FilteredListParams = {}): Promise<PaginatedResponse<Contract>> {
    return fetchAPI(`/contracts${assetListQuery(params)}`);
  },
  get(id: string): Promise<Contract> {
    return fetchAPI(`/contracts/${id}`);
  },
  create(data: ContractCreateRequest): Promise<Contract> {
    return fetchAPI('/contracts', { method: 'POST', body: JSON.stringify(data) });
  },
  /** update patches a contract; with version the request carries If-Match (412 when stale). */
  update(id: string, data: ContractUpdateRequest, version?: number): Promise<Contract> {
    return fetchAPI(`/contracts/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
      headers: ifMatch(version),
    });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/contracts/${id}`, { method: 'DELETE' });
  },
};

/**
 * contactApi reads the contacts (AUD-01); name, phone and e-mail come from
 * the PII vault (WP-242). /contacts is a scoped list (client_id).
 */
export const contactApi = {
  list(params: { limit?: number; cursor?: string } = {}): Promise<PaginatedResponse<Contact>> {
    return fetchAPI(`/contacts${buildQuery(params)}`);
  },
};

// --- Phase 2: Import (IMP-IO-01, WP-322) ---

// The import profiles, runs and the preview come from the OpenAPI contract
// (K27, WP-774, WP-301, WP-302). Import profiles and runs are organization
// wide: no list takes client_id or site_id, SCOPED_LISTS stays unchanged.
export type ImportProfile = components['schemas']['ImportProfile'];
export type ImportMapping = components['schemas']['ImportMapping'];
export type ImportColumnMapping = components['schemas']['ImportColumnMapping'];
export type ImportTransform = components['schemas']['ImportTransform'];
export type CreateImportProfileRequest = components['schemas']['CreateImportProfileRequest'];
export type UpdateImportProfileRequest = components['schemas']['UpdateImportProfileRequest'];
export type ImportPreview = components['schemas']['ImportPreview'];
export type ImportPreviewRequest = components['schemas']['ImportPreviewRequest'];
export type ImportStartDocumentRequest = components['schemas']['ImportStartDocumentRequest'];
export type ImportJobAccepted = components['schemas']['ImportJobAccepted'];
export type ImportRun = components['schemas']['ImportRun'];
export type ImportError = components['schemas']['ImportError'];
export type ImportErrorList = components['schemas']['ImportErrorListResponse'];

/** The problem types of a refused import file (422) and of an unfinished report (409). */
export const PROBLEM_IMPORT_FORMAT_UNSUPPORTED =
  'https://reticora.io/problems/import-format-unsupported';
export const PROBLEM_IMPORT_SHEET_NOT_FOUND = 'https://reticora.io/problems/import-sheet-not-found';
export const PROBLEM_IMPORT_FILE_TOO_COMPLEX =
  'https://reticora.io/problems/import-file-too-complex';
export const PROBLEM_IMPORT_FILE_TOO_LARGE = 'https://reticora.io/problems/import-file-too-large';
export const PROBLEM_IMPORT_REPORT_NOT_READY =
  'https://reticora.io/problems/import-report-not-ready';

/**
 * ImportJobResult is the result of a finished import.run job (GET
 * /jobs/{id}, importer.JobResult): the data rows read and the rows per
 * outcome; a dry run writes nothing but counts the same way.
 */
export interface ImportJobResult {
  dry_run: boolean;
  total: number;
  created: number;
  updated: number;
  skipped: number;
  failed: number;
}

/** importJobResult reads the counters of a job result; undefined while there are none. */
export function importJobResult(result: unknown): ImportJobResult | undefined {
  if (!result || typeof result !== 'object') return undefined;
  const r = result as Record<string, unknown>;
  const n = (key: string) => (typeof r[key] === 'number' ? (r[key] as number) : 0);
  if (typeof r.total !== 'number') return undefined;
  return {
    dry_run: r.dry_run === true,
    total: n('total'),
    created: n('created'),
    updated: n('updated'),
    skipped: n('skipped'),
    failed: n('failed'),
  };
}

/**
 * importApi reads and writes the mapping profiles (/import-profiles,
 * WP-774), previews a file (POST /imports/preview, WP-301), starts a run
 * (POST /imports with document_id, WP-301/WP-302) and reads its projection
 * and the download link of its CSV report (GET /imports/{id}[/report]) and
 * its row errors page by page (GET /imports/{id}/errors).
 * The progress of a run is the job (GET /jobs/{id}, useImportRun).
 */
export const importApi = {
  listProfiles(
    params: { limit?: number; cursor?: string } = {},
  ): Promise<PaginatedResponse<ImportProfile>> {
    return fetchAPI(`/import-profiles${buildQuery(params)}`);
  },
  getProfile(id: string): Promise<ImportProfile> {
    return fetchAPI(`/import-profiles/${id}`);
  },
  createProfile(data: CreateImportProfileRequest): Promise<ImportProfile> {
    return fetchAPI('/import-profiles', { method: 'POST', body: JSON.stringify(data) });
  },
  updateProfile(id: string, data: UpdateImportProfileRequest): Promise<ImportProfile> {
    return fetchAPI(`/import-profiles/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  deleteProfile(id: string): Promise<void> {
    return fetchAPI(`/import-profiles/${id}`, { method: 'DELETE' });
  },
  /** The header and at most 20 rows of a scanned file; no job (422 for a refused file). */
  preview(data: ImportPreviewRequest): Promise<ImportPreview> {
    return fetchAPI('/imports/preview', { method: 'POST', body: JSON.stringify(data) });
  },
  /** Starts a run as job import.run (202); dry_run only checks and counts. */
  start(data: ImportStartDocumentRequest): Promise<ImportJobAccepted> {
    return fetchAPI('/imports', { method: 'POST', body: JSON.stringify(data) });
  },
  get(id: string): Promise<ImportRun> {
    return fetchAPI(`/imports/${id}`);
  },
  /** The presigned link (15 min) of the CSV report of a finished run; 409 while it runs. */
  reportUrl(id: string): Promise<{ download_url: string }> {
    return fetchAPI(`/imports/${id}/report`);
  },
  /** A page of the row errors of a finished run (line, field, code, message); 409 while it runs. */
  errors(id: string, params: { limit?: number; cursor?: string } = {}): Promise<ImportErrorList> {
    return fetchAPI(`/imports/${id}/errors${buildQuery(params)}`);
  },
};

// --- Phase 2: Stocktake / Inventur ---
// The types come from the OpenAPI schema (K27): area location_id, frozen
// expected stock (items), scans classified by the server, the difference
// list and the movements booked by the completion (WP-287, WP-761).

export type Stocktake = components['schemas']['Stocktake'];
export type StocktakeStatus = Stocktake['status'];
export type StockScan = components['schemas']['StockScan'];
export type StockScanCreateRequest = components['schemas']['CreateStockScanRequest'];
export type ScanMethod = StockScan['scan_method'];
export type ScanResult = StockScan['scan_result'];
export type StocktakeItem = components['schemas']['StocktakeItem'];
export type StocktakeDifference = components['schemas']['StocktakeDifference'];
export type StocktakeCorrection = components['schemas']['StocktakeCorrection'];
export type StocktakeCompletion = components['schemas']['StocktakeCompletion'];
export type StocktakeCreateRequest = components['schemas']['CreateStocktakeRequest'];
export type StocktakeUpdateRequest = components['schemas']['UpdateStocktakeRequest'];

export interface StocktakeListParams {
  limit?: number;
  offset?: number;
  cursor?: string;
  status?: string;
  scope?: string;
  search?: string;
}

/** CursorParams page a cursor list (API-02): limit and the cursor of the previous page. */
export interface CursorParams {
  limit?: number;
  cursor?: string;
}

export const stocktakeApi = {
  list(params: StocktakeListParams = {}): Promise<PaginatedResponse<Stocktake>> {
    return fetchAPI(`/stocktakes${buildQuery(params)}`);
  },
  get(id: string): Promise<Stocktake> {
    return fetchAPI(`/stocktakes/${id}`);
  },
  /** create sends title, description, due_date and the area location_id; scope is deprecated. */
  create(data: StocktakeCreateRequest): Promise<Stocktake> {
    return fetchAPI('/stocktakes', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: StocktakeUpdateRequest): Promise<Stocktake> {
    return fetchAPI(`/stocktakes/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/stocktakes/${id}`, { method: 'DELETE' });
  },
  /** start moves a planned stocktake to in_progress and freezes the expected stock. */
  start(id: string): Promise<Stocktake> {
    return fetchAPI(`/stocktakes/${id}/start`, { method: 'POST' });
  },
  /** cancel ends a planned or in-progress stocktake without completion. */
  cancel(id: string): Promise<Stocktake> {
    return fetchAPI(`/stocktakes/${id}/cancel`, { method: 'POST' });
  },
  /** items is a page of the frozen expected stock (GET …/items, cursor). */
  items(id: string, params: CursorParams = {}): Promise<PaginatedResponse<StocktakeItem>> {
    return fetchAPI(`/stocktakes/${id}/items${buildQuery(params)}`);
  },
  /** addScan records an asset or a counted quantity; the server classifies it. */
  addScan(stocktakeId: string, data: StockScanCreateRequest): Promise<StockScan> {
    return fetchAPI(`/stocktakes/${stocktakeId}/scans`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  listScans(stocktakeId: string, params: CursorParams = {}): Promise<PaginatedResponse<StockScan>> {
    return fetchAPI(`/stocktakes/${stocktakeId}/scans${buildQuery(params)}`);
  },
  /** differences is a page of the difference list (GET …/differences, cursor). */
  differences(
    stocktakeId: string,
    params: CursorParams = {},
  ): Promise<PaginatedResponse<StocktakeDifference>> {
    return fetchAPI(`/stocktakes/${stocktakeId}/differences${buildQuery(params)}`);
  },
  /** complete books the corrections of the difference list (WP-761); apply_corrections is gone. */
  complete(stocktakeId: string): Promise<StocktakeCompletion> {
    return fetchAPI(`/stocktakes/${stocktakeId}/complete`, { method: 'POST', body: '{}' });
  },
};

// --- Phase 2: Ticket System (Essential) ---

/** The states of the ticket state machine (TKT-01, WP-290); a ticket starts as new. */
export type TicketStatus = 'new' | 'open' | 'in_progress' | 'waiting' | 'resolved' | 'closed';

export type TicketPriority = 'low' | 'medium' | 'high' | 'critical';

export interface Ticket {
  id: string;
  organization_id: string;
  ticket_number: number;
  title: string;
  description?: string;
  status: TicketStatus;
  priority: TicketPriority | string;
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
  /** The writing user; null exactly for a system comment (author_type system, WP-291). */
  author_id: string | null;
  author_type: 'user' | 'system';
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
  status?: TicketStatus;
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

/** The targets of a ticket link (TicketLink.entity_type, WP-764). */
export type TicketLinkEntityType = 'asset' | 'ci' | 'contract' | 'location' | 'review_item';

/** TicketLink is an object linked to a ticket besides its primary object (WP-764). */
export interface TicketLink {
  id: string;
  organization_id: string;
  ticket_id: string;
  entity_type: TicketLinkEntityType;
  entity_id: string;
  created_at: string;
}

export interface TicketLinkTarget {
  entity_type: TicketLinkEntityType;
  entity_id: string;
}

/** CursorParams page a list over the signed next_cursor (API-02). */
export interface CursorParams {
  limit?: number;
  cursor?: string;
}

/** ReviewItemTicketRequest opens a ticket for a review item (WP-291). */
export interface ReviewItemTicketRequest {
  priority?: TicketPriority;
  assignee_id?: string;
  team_id?: string;
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
  /** listLinks reads the object links of a ticket, oldest first (WP-764). */
  listLinks(ticketId: string, params: CursorParams = {}): Promise<PaginatedResponse<TicketLink>> {
    return fetchAPI(`/tickets/${ticketId}/links${buildQuery(params)}`);
  },
  addLink(ticketId: string, target: TicketLinkTarget): Promise<TicketLink> {
    return fetchAPI(`/tickets/${ticketId}/links`, {
      method: 'POST',
      body: JSON.stringify(target),
    });
  },
  removeLink(ticketId: string, linkId: string): Promise<void> {
    return fetchAPI(`/tickets/${ticketId}/links/${linkId}`, { method: 'DELETE' });
  },
  /** listAttachments reads the documents linked to a ticket, newest first (WP-765). */
  listAttachments(
    ticketId: string,
    params: CursorParams = {},
  ): Promise<PaginatedResponse<Document>> {
    return fetchAPI(`/tickets/${ticketId}/attachments${buildQuery(params)}`);
  },
  /** createFromReview opens a ticket for an open review item; the item stays open (WP-291). */
  createFromReview(reviewItemId: string, data: ReviewItemTicketRequest = {}): Promise<Ticket> {
    return fetchAPI(`/review-items/${reviewItemId}/ticket`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
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

/**
 * UserStatus is the status PATCH /users/{id} sets: inactive deactivates the
 * user and revokes the sessions, active reactivates it (TLC-04).
 */
export type UserStatus = 'active' | 'inactive';

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
    data: { display_name?: string; status?: UserStatus; avatar_url?: string },
  ): Promise<AppUser> {
    return fetchAPI(`/users/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /**
   * delete removes the user (TLC-04); a user owning open objects or still
   * referenced by records answers 409 and stays.
   */
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

/** The claims of the signed-in session (GET /me, AUT-02) with its scopes. */
export type SessionClaims = components['schemas']['SessionClaims'];

export const sessionApi = {
  me(): Promise<SessionClaims> {
    return fetchAPI('/me');
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
  /** The business calendar of the policy (TKT-02); null lets the calendar chain decide (WP-292). */
  calendar_id?: string | null;
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
  /** The business calendar of the due times (TKT-02); null for elapsed time or the standard calendar. */
  calendar_id?: string | null;
  /** Business minutes until response_due_at; negative once overdue, null once responded. */
  response_remaining_minutes?: number | null;
  /** Business minutes until resolution_due_at; negative once overdue, null once resolved. */
  resolution_remaining_minutes?: number | null;
  /** paused: the moment lies outside business hours or on a holiday of the calendar. */
  paused?: boolean;
  created_at: string;
  updated_at: string;
}

export interface SLAPolicyRequest {
  client_id?: string;
  name: string;
  priority: string;
  response_target_minutes: number;
  resolution_target_minutes: number;
  /** business_calendar applies business hours (switch of the form, WP-815). */
  business_calendar?: boolean;
  /**
   * calendar_id names a business calendar without client or of the policy's
   * client (TKT-02); null or absent lets the calendar chain decide (WP-292).
   */
  calendar_id?: string | null;
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

// --- Business calendars (TKT-02, E-38, WP-768) ---

// The business calendars come from the OpenAPI contract (K27). The list
// takes client_id, but follows no scope switcher: the calendar choice of a
// policy needs the org-wide calendars (client_id null) too, so
// SCOPED_LISTS stays unchanged (WP-815).
export type BusinessCalendar = components['schemas']['BusinessCalendar'];
export type BusinessCalendarWindow = components['schemas']['BusinessCalendarWindow'];
export type BusinessCalendarHoliday = components['schemas']['BusinessCalendarHoliday'];
export type CreateBusinessCalendarRequest = components['schemas']['CreateBusinessCalendarRequest'];
export type UpdateBusinessCalendarRequest = components['schemas']['UpdateBusinessCalendarRequest'];

export interface BusinessCalendarListParams {
  limit?: number;
  cursor?: string;
  client_id?: string;
}

export const businessCalendarApi = {
  /** One page of the calendars sorted by name; the next one over next_cursor (API-02). */
  list(params: BusinessCalendarListParams = {}): Promise<PaginatedResponse<BusinessCalendar>> {
    return fetchAPI(`/business-calendars${buildQuery(params)}`);
  },
  /** listAll reads every calendar visible under the scope (calendar choice of a policy). */
  listAll(): Promise<BusinessCalendar[]> {
    return fetchAllPages('/business-calendars');
  },
  get(id: string): Promise<BusinessCalendar> {
    return fetchAPI(`/business-calendars/${id}`);
  },
  create(data: CreateBusinessCalendarRequest): Promise<BusinessCalendar> {
    return fetchAPI('/business-calendars', { method: 'POST', body: JSON.stringify(data) });
  },
  /** PATCH sends the changed fields; client_id cannot change (400 on client_id). */
  update(id: string, data: UpdateBusinessCalendarRequest): Promise<BusinessCalendar> {
    return fetchAPI(`/business-calendars/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/business-calendars/${id}`, { method: 'DELETE' });
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

/** The kinds of a credential (CreateCredentialRequest.kind in api/openapi.yaml). */
export type CredentialKind =
  | 'snmp_v2c'
  | 'snmp_v3'
  | 'ssh_password'
  | 'ssh_key'
  | 'redfish'
  | 'ipmi'
  | 'wmi'
  | 'api_token'
  | 'nut'
  | 'nas';

/**
 * A new credential. The secret is write-only: the server encrypts it and no
 * response returns it (SEC-01, COL-02). Without client_id it is
 * organization-wide.
 */
export interface CredentialInput {
  name: string;
  kind: CredentialKind;
  client_id?: string;
  secret: Record<string, string>;
}

export const credentialApi = {
  list(params: ListParams = {}): Promise<PaginatedResponse<Credential>> {
    return fetchAPI(`/credentials${buildQuery(params)}`);
  },
  create(input: CredentialInput): Promise<Credential> {
    return fetchAPI('/credentials', { method: 'POST', body: JSON.stringify(input) });
  },
  remove(id: string): Promise<void> {
    return fetchAPI(`/credentials/${encodeURIComponent(id)}`, { method: 'DELETE' });
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
  /** Lifecycle state of the assets of a datev export (E-120); datev only. */
  lifecycle_state?: string;
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
  /**
   * Signed URL of the error report of a completed, unexpired datev job: the
   * asset rows the file leaves out and why (EXP-01, WP-310).
   */
  error_report_url?: string;
  /** Fields of the file and the visible fields it leaves out (EXP-01). */
  manifest?: {
    format: string;
    fields: string[];
    omitted: { field: string; reason: 'layout' | 'field_permission' }[];
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
  /** Where the row comes from (PKG-01, WP-880). */
  origin: 'plan' | 'addon' | 'manual';
  updated_at: string;
}

/** GET /entitlements: the rows and the leading plan of the organization. */
export interface EntitlementListResponse extends PaginatedResponse<Entitlement> {
  /** organization.plan, the leading plan (WP-880). */
  plan: string;
  /** Time of the last plan change; null while the plan of the creation applies. */
  plan_changed_at: string | null;
}

/** The use of one quota (PKG-05, WP-885). */
export interface QuotaUsage {
  quota: string;
  /** Current use; null when the caller may not see the counts (F-414). */
  used: number | null;
  /** The limit; null is unlimited. */
  limit: number | null;
  /** The limit is the plan default or an override. */
  origin: 'plan' | 'override';
}

/** GET /entitlements/usage. */
export interface QuotaUsageResponse {
  data: QuotaUsage[];
  plan: string;
}

/** One plan of the plan matrix (E-151, E-153). */
export interface PlanInfo {
  plan: string;
  features: string[];
  /** Default limits; a quota without entry is unlimited. */
  quota_defaults: Record<string, number>;
}

/** A bookable add-on (CH14). */
export interface AddonInfo {
  key: string;
  phase: 'A';
  description_key: string;
}

/** GET /entitlements/plans: the plan matrix and the add-ons. */
export interface PlanMatrixResponse {
  data: PlanInfo[];
  addons: AddonInfo[];
}

export const entitlementApi = {
  list(params: ListParams = {}): Promise<EntitlementListResponse> {
    return fetchAPI(`/entitlements${buildQuery(params)}`);
  },
  usage(): Promise<QuotaUsageResponse> {
    return fetchAPI('/entitlements/usage');
  },
  plans(): Promise<PlanMatrixResponse> {
    return fetchAPI('/entitlements/plans');
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

/**
 * Consumable is a quantity item of the warehouse (E-40, WP-283) as the
 * OpenAPI types describe it: its stock lies per bin (GET
 * /consumables/{id}/stock); stock_level is the read-only total over all bins.
 */
export type Consumable = components['schemas']['Consumable'];
export type CreateConsumableRequest = components['schemas']['CreateConsumableRequest'];
export type UpdateConsumableRequest = components['schemas']['UpdateConsumableRequest'];
/** The availability of a consumable in one bin: on_hand, reserved, available (WP-759). */
export type ConsumableAvailability = components['schemas']['ConsumableAvailability'];
export type ConsumableAvailabilityList =
  components['schemas']['ConsumableAvailabilityListResponse'];

export const consumableApi = {
  list(params: ListParams & { low_stock?: boolean } = {}): Promise<PaginatedResponse<Consumable>> {
    return fetchAPI(`/consumables${buildQuery(params)}`);
  },
  /** get reads one consumable (GET /consumables/{id}), e.g. the object of a scanned URN (WP-807). */
  get(id: string): Promise<Consumable> {
    return fetchAPI(`/consumables/${id}`);
  },
  create(data: CreateConsumableRequest): Promise<Consumable> {
    return fetchAPI('/consumables', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: UpdateConsumableRequest): Promise<Consumable> {
    return fetchAPI(`/consumables/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/consumables/${id}`, { method: 'DELETE' });
  },
  /**
   * availability reads on_hand, reserved and available of a consumable per
   * bin, page by page over next_cursor (GET /consumables/{id}/availability,
   * WP-759). It succeeds the deprecated GET /inventory/availability.
   */
  availability(id: string, cursor?: string): Promise<ConsumableAvailabilityList> {
    return fetchAPI(`/consumables/${id}/availability${buildQuery({ limit: 200, cursor })}`);
  },
};

/**
 * Order is an internal order with its state machine of nine states, the
 * supplier record (supplier_id, supplier_name, E-41), the target bin of the
 * goods receipt and its positions (WP-286, WP-760) as the OpenAPI types
 * describe it (K27). The deprecated free-text supplier is no longer sent.
 */
export type Order = components['schemas']['Order'];
/** OrderItem is a position: a consumable (consumable_id) or an asset description. */
export type OrderItem = components['schemas']['OrderItem'];
export type OrderStatus = Order['status'];
export type CreateOrderRequest = Omit<components['schemas']['CreateOrderRequest'], 'supplier'>;
export type UpdateOrderRequest = Omit<components['schemas']['UpdateOrderRequest'], 'supplier'>;
export type CreateOrderItemRequest = components['schemas']['CreateOrderItemRequest'];
/** ReceiveOrderRequest is the goods receipt: items[{item_id, quantity, location_id, asset_ids}]. */
export type ReceiveOrderRequest = components['schemas']['ReceiveOrderRequest'];

export const orderApi = {
  list(params: ListParams & { status?: string } = {}): Promise<PaginatedResponse<Order>> {
    return fetchAPI(`/orders${buildQuery(params)}`);
  },
  get(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}`);
  },
  create(data: CreateOrderRequest): Promise<Order> {
    return fetchAPI('/orders', { method: 'POST', body: JSON.stringify(data) });
  },
  /** update changes an order in status draft (PATCH /orders/{id}; 409 otherwise). */
  update(id: string, data: UpdateOrderRequest): Promise<Order> {
    return fetchAPI(`/orders/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  addItem(id: string, data: CreateOrderItemRequest): Promise<Order> {
    return fetchAPI(`/orders/${id}/items`, { method: 'POST', body: JSON.stringify(data) });
  },
  submit(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/submit`, { method: 'POST', body: '{}' });
  },
  /** approve and reject need order:approve and another user than the requester (403). */
  approve(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/approve`, { method: 'POST', body: '{}' });
  },
  reject(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/reject`, { method: 'POST', body: '{}' });
  },
  place(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/place`, { method: 'POST', body: '{}' });
  },
  cancel(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/cancel`, { method: 'POST', body: '{}' });
  },
  close(id: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/close`, { method: 'POST', body: '{}' });
  },
  /**
   * receive books a goods receipt (POST /orders/{id}/receive, WP-760). The
   * idempotencyKey is created once per dialog, so a repeated send of the
   * same receipt is answered from the stored result.
   */
  receive(id: string, data: ReceiveOrderRequest, idempotencyKey: string): Promise<Order> {
    return fetchAPI(`/orders/${id}/receive`, {
      method: 'POST',
      body: JSON.stringify(data),
      headers: { 'Idempotency-Key': idempotencyKey },
    });
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

/**
 * A revision-safe disposal record after E-55 (WP-751) from the schema
 * (K27): append-only, with the DIN 66399 fields, the evidence document and
 * record_hash set by the database.
 */
export type DisposalRecord = components['schemas']['DisposalRecord'];

/** The payload of POST /disposal-records; the required fields depend on the method. */
export type CreateDisposalRecordRequest = components['schemas']['CreateDisposalRecordRequest'];

export type DisposalMethod = CreateDisposalRecordRequest['method'];

export interface DisposalListParams extends ListParams {
  method?: string;
  asset_id?: string;
  ci_id?: string;
}

export const disposalApi = {
  list(params: DisposalListParams = {}): Promise<PaginatedResponse<DisposalRecord>> {
    return fetchAPI(`/disposal-records${buildQuery(params)}`);
  },
  get(id: string): Promise<DisposalRecord> {
    return fetchAPI(`/disposal-records/${id}`);
  },
  /** 400 names the missing or invalid field, 422 a refused lifecycle source state. */
  create(data: CreateDisposalRecordRequest): Promise<DisposalRecord> {
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

// The SVG label of an asset (INV-03, WP-849): Code128 and QR of the
// inventory number or the asset URN.
export function assetLabelURL(assetId: string): string {
  return `${API_BASE}/assets/${assetId}/label?format=svg`;
}

// The printable labels (INV-03, WP-763, E-52, E-135): the PDF of one asset
// label and the label batches of an inventory area as job labels.batch.

export type LabelTemplate = components['schemas']['LabelBatchRequest']['template'];
export type LabelBatchRequest = components['schemas']['LabelBatchRequest'];
export type LabelBatchResult = components['schemas']['LabelBatchResult'];
export type LabelBatchDownload = components['schemas']['LabelBatchDownload'];

/** LabelBatchJob is the 202 of POST /labels/batch: the job that renders the batch. */
export interface LabelBatchJob {
  job_id: string;
  status: string;
  kind: string;
}

export const labelDownloadApi = {
  /** assetLabel loads the PDF label of an asset in a template (GET /assets/{id}/label). */
  assetLabel(assetId: string, template: 'asset_50x25' | 'a4_24'): Promise<Blob> {
    return fetchBlob(`/assets/${assetId}/label${buildQuery({ format: 'pdf', template })}`);
  },
  /** batch starts a label batch job (POST /labels/batch, 202). */
  batch(data: LabelBatchRequest): Promise<LabelBatchJob> {
    return fetchAPI('/labels/batch', { method: 'POST', body: JSON.stringify(data) });
  },
  /** batchDownload names the signed, short-lived URL of a finished batch (E-135). */
  batchDownload(jobId: string): Promise<LabelBatchDownload> {
    return fetchAPI(`/labels/batch/${jobId}/download`);
  },
};

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
