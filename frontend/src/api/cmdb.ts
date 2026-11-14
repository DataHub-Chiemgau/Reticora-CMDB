/**
 * cmdb — API surface for the enterprise CMDB + asset/inventory extension
 * (spec §20). Kept in a dedicated module so the extension is additive to the
 * existing client.ts. All calls flow through the same fetchAPI (auth, RFC
 * 7807 errors, idempotency).
 */
import {
  API_MAX_LIMIT,
  ApiError,
  assetApi,
  buildQuery as qs,
  consumableApi,
  fetchAllPages,
  fetchAPI,
  type CI,
  type PaginatedResponse,
} from './client';
import type { FieldDefinition, ValidationRules } from '../lib/fieldmeta';
import type { components } from './generated/schema';

// ─── Metamodel drafts (MET-53, WP-260, WP-720, WP-798) ──────────────────────

export type MetamodelDraft = components['schemas']['MetamodelDraft'];
/** A typed operation of a draft: what a write with ?draft answers (202). */
export type MetamodelDraftItem = components['schemas']['MetamodelDraftItem'];

/**
 * Drafted is the answer of a metamodel write: the resource, or the item of
 * the active draft when withDraft (client.ts) captured the write (202).
 */
export type Drafted<T> = T | MetamodelDraftItem;

/**
 * isDraftItem tells a captured write (202 with the draft item) from the
 * resource: an item carries kind, op, route and schema_versions. A caller
 * that would go on with the id of the resource stops there; the id of an
 * item names no resource (WP-798).
 */
export function isDraftItem(value: unknown): value is MetamodelDraftItem {
  if (typeof value !== 'object' || value === null) return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v.kind === 'string' &&
    typeof v.op === 'string' &&
    typeof v.route === 'string' &&
    typeof v.schema_versions === 'object' &&
    v.schema_versions !== null
  );
}

export const metamodelDraftApi = {
  /** Every draft of a status (cursor pages, API-02), newest first, without items. */
  async list(status: MetamodelDraft['status']): Promise<MetamodelDraft[]> {
    return fetchAllPages<MetamodelDraft>('/metamodel/drafts', { status });
  },
  get(id: string): Promise<MetamodelDraft> {
    return fetchAPI(`/metamodel/drafts/${id}`);
  },
  create(name: string): Promise<MetamodelDraft> {
    return fetchAPI('/metamodel/drafts', { method: 'POST', body: JSON.stringify({ name }) });
  },
  /** Every item of a draft, all pages over next_cursor (API-02). */
  async items(id: string): Promise<MetamodelDraftItem[]> {
    return fetchAllPages<MetamodelDraftItem>(`/metamodel/drafts/${id}/items`);
  },
  /** Every draft of every status, all pages over next_cursor (WP-799). */
  async listAll(): Promise<MetamodelDraft[]> {
    return fetchAllPages<MetamodelDraft>('/metamodel/drafts');
  },
  /**
   * Discards a draft (status discarded); with the version read, If-Match
   * refuses a change in between with 412 (API-07).
   */
  discard(id: string, version?: number): Promise<MetamodelDraft> {
    return fetchAPI(`/metamodel/drafts/${id}`, {
      method: 'DELETE',
      headers: ifMatch(version),
    });
  },
  /** Removes an item; If-Match names the draft version the page read (412 otherwise). */
  removeItem(id: string, itemId: string, version: number): Promise<MetamodelDraft> {
    return fetchAPI(`/metamodel/drafts/${id}/items/${itemId}`, {
      method: 'DELETE',
      headers: ifMatch(version),
    });
  },
  /** Starts the test run (job metamodel.test, WP-721); needs metamodel:publish. */
  test(id: string): Promise<MetamodelTestStart> {
    return fetchAPI(`/metamodel/drafts/${id}/test`, { method: 'POST' });
  },
  /** One page of the test report with its violations; needs metamodel:publish. */
  testReport(id: string, cursor?: string): Promise<MetamodelTestReportPage> {
    return fetchAPI(`/metamodel/drafts/${id}/test${qs({ limit: 50, cursor })}`);
  },
  /** Publishes a tested draft (WP-261); needs metamodel:publish. */
  publish(id: string): Promise<MetamodelPublication> {
    return fetchAPI(`/metamodel/drafts/${id}/publish`, { method: 'POST' });
  },
  /**
   * The current state of the target of an item, for the diff of the patch
   * list: the resource the item changes or deletes, the field of a type or
   * the global field of that name. null: the target does not exist (a new
   * part or a vanished target); undefined: no route reads it (a template
   * version by its id).
   */
  async target(item: MetamodelDraftItem): Promise<Record<string, unknown> | null | undefined> {
    const id = item.target_id ?? '';
    const key = item.target_key ?? '';
    const path = TARGET_PATHS[item.kind];
    try {
      if (item.kind === 'ci_type_field') {
        if (!id || !key) return null;
        const fields = await fetchAPI<{ data: FieldDefinition[] }>(`/ci-types/${id}/fields`);
        return asRecord(fields.data.find((f) => f.name === key));
      }
      if (item.kind === 'global_field') {
        if (!key) return null;
        const fields = await fetchAPI<{ data: FieldDefinition[] }>('/ci-fields/global');
        return asRecord(fields.data.find((f) => f.name === key));
      }
      if (!path) return undefined;
      if (!id) return null;
      return await fetchAPI<Record<string, unknown>>(`${path}/${id}`);
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return null;
      throw error;
    }
  },
};

/** The routes that read the target of an item by its id (template_version has none). */
const TARGET_PATHS: Partial<Record<MetamodelDraftItem['kind'], string>> = {
  ci_type: '/ci-types',
  attribute_definition: '/attribute-definitions',
  attribute_set: '/attribute-sets',
  template: '/templates',
  rule: '/rules',
  layout: '/layouts',
};

function asRecord(value: object | undefined): Record<string, unknown> | null {
  return value ? (value as Record<string, unknown>) : null;
}

function ifMatch(version: number | undefined): HeadersInit | undefined {
  return version !== undefined ? { 'If-Match': `"${version}"` } : undefined;
}

/** The answer of POST /metamodel/drafts/{id}/test: the job of the run. */
export interface MetamodelTestStart {
  job_id: string;
  status: string;
  kind: 'metamodel.test';
}
/** The counters of a test run without its violations (WP-721). */
export type MetamodelTestReport = components['schemas']['MetamodelTestReport'];
export type MetamodelTestReportPage = components['schemas']['MetamodelTestReportPage'];
export type MetamodelTestViolation = components['schemas']['MetamodelTestViolation'];
/** The answer of a publication: the draft, counts per kind, the new schema versions. */
export type MetamodelPublication = components['schemas']['MetamodelPublication'];

/** An item of a draft whose target changed or vanished since its capture (409 of publish). */
export interface StaleDraftItem {
  item_id: string;
  kind: string;
  op: string;
  target_id?: string | null;
  target_key?: string | null;
  reason: string;
}

/**
 * staleItemsOf reads the stale items of a refused publication (409 with
 * stale_items, WP-261); empty for every other failure.
 */
export function staleItemsOf(error: unknown): StaleDraftItem[] {
  if (!(error instanceof ApiError) || error.status !== 409) return [];
  const raw = error.problem.stale_items;
  if (!Array.isArray(raw)) return [];
  return raw.filter(
    (entry): entry is StaleDraftItem =>
      typeof entry === 'object' &&
      entry !== null &&
      typeof (entry as Record<string, unknown>).item_id === 'string',
  );
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
  /** Parent in the type chain (MET-06, WP-248); absent for a root. */
  parent_type_id?: string;
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
  /** Parent type of the organization or a system type (MET-06, WP-248). */
  parent_type_id?: string;
  lifecycle_definition_id?: string;
  capabilities?: Record<string, unknown>;
  allowed_relationship_types?: string[];
  ui_schema?: Record<string, unknown>;
  fields?: FieldDefinition[];
}

/**
 * The changes of PATCH /ci-types/{id}: the key is immutable; an empty
 * parent_type_id makes the type a root (WP-248).
 */
export type CITypeUpdateRequest = Partial<Omit<CITypeCreateRequest, 'key' | 'fields'>>;

export const ciTypeApi = {
  list(
    params: {
      include_inactive?: boolean;
      category?: string;
      search?: string;
      /** client_id leaves out the types hidden for the client (WP-858). */
      client_id?: string;
      limit?: number;
      offset?: number;
    } = {},
  ) {
    return fetchAPI<PaginatedResponse<CIType>>(`/ci-types${qs(params)}`);
  },
  /**
   * listAll reads every page of GET /ci-types (pages of 200 with offset while
   * has_more holds, F-362), so a selection offers all active types, not only
   * the first page (MET-51, WP-804).
   */
  listAll(
    params: {
      include_inactive?: boolean;
      category?: string;
      search?: string;
      client_id?: string;
    } = {},
  ) {
    return fetchAllPages<CIType>('/ci-types', params);
  },
  templates(): Promise<{ data: CIType[] }> {
    return fetchAPI('/ci-types/templates');
  },
  get(id: string): Promise<CIType> {
    return fetchAPI(`/ci-types/${id}`);
  },
  create(data: CITypeCreateRequest): Promise<Drafted<CIType>> {
    return fetchAPI('/ci-types', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: CITypeUpdateRequest): Promise<Drafted<CIType>> {
    return fetchAPI(`/ci-types/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  clone(id: string, data: { key?: string; name?: string }): Promise<Drafted<CIType>> {
    return fetchAPI(`/ci-types/${id}/clone`, { method: 'POST', body: JSON.stringify(data) });
  },
  deactivate(id: string): Promise<Drafted<CIType>> {
    return fetchAPI(`/ci-types/${id}/deactivate`, { method: 'POST', body: '{}' });
  },
  activate(id: string): Promise<Drafted<CIType>> {
    return fetchAPI(`/ci-types/${id}/activate`, { method: 'POST', body: '{}' });
  },
  versions(id: string): Promise<{ data: CIType[] }> {
    return fetchAPI(`/ci-types/${id}/versions`);
  },
  listFields(id: string): Promise<{ data: FieldDefinition[] }> {
    return fetchAPI(`/ci-types/${id}/fields`);
  },
  /**
   * The resolved form of the type (WP-719) for a target and a role: the
   * template replaces the templates of the type, preview resolves as the
   * role (citype:manage). Without role_id the server resolves with the
   * roles of the caller (WP-316). draft resolves the state of a metamodel
   * draft (E-113, WP-803).
   */
  form(id: string, params: ResolvedFormParams = {}): Promise<ResolvedForm> {
    return fetchAPI(`/ci-types/${id}/form${qs(params)}`);
  },
};

// ─── Value suggestions of the guided creation (MET-51, WP-317) ───────────────

/** SUGGESTION_LIMIT is the page of CIs a suggestion request reads. */
export const SUGGESTION_LIMIT = 50;

/** The request of value suggestions for manufacturer or model of one CI type. */
export interface CIValueSuggestionQuery {
  ciTypeId: string;
  field: 'manufacturer' | 'model';
  /** input is the typed prefix; empty suggests every stored value. */
  input: string;
  /** manufacturer narrows the model suggestions to the chosen manufacturer. */
  manufacturer?: string;
}

/**
 * likePrefix is the prefix filter of an input: * is the only wildcard of
 * like (API-02) and cannot be escaped, so a typed * is dropped.
 */
function likePrefix(input: string) {
  return `${input.split('*').join('')}*`;
}

export const ciValueApi = {
  /**
   * suggestions reads the stored values of manufacturer or model of the
   * CIs of one type: GET /cis with the type, a prefix filter and the sparse
   * fieldset manufacturer,model (limit 50), deduplicated by value in the
   * order of the answer. There is no distinct endpoint; /cis is a scoped
   * list, so the suggestions follow the chosen scope.
   */
  async suggestions({ ciTypeId, field, input, manufacturer }: CIValueSuggestionQuery) {
    const params: Record<string, string | number> = {
      'filter[ci_type_id]': ciTypeId,
      fields: 'manufacturer,model',
      limit: SUGGESTION_LIMIT,
    };
    if (field === 'manufacturer') {
      params['filter[manufacturer][like]'] = likePrefix(input.trim());
    } else {
      if (manufacturer?.trim()) params['filter[manufacturer]'] = manufacturer.trim();
      params['filter[model][like]'] = likePrefix(input.trim());
    }
    const page = await fetchAPI<PaginatedResponse<CI>>(`/cis${qs(params)}`);
    const seen = new Set<string>();
    const values: string[] = [];
    for (const row of page.data ?? []) {
      const value = row[field]?.trim();
      if (!value || seen.has(value)) continue;
      seen.add(value);
      values.push(value);
    }
    return values;
  },
};

/** The parameters of GET /ci-types/{id}/form (WP-719). */
export interface ResolvedFormParams {
  template_id?: string;
  role_id?: string;
  preview?: boolean;
  /** The id of a metamodel draft: the form as the draft would make it (E-113). */
  draft?: string;
}

// ─── Form layouts (MET-50, WP-259, WP-316) ───────────────────────────────────

/** The layout of a form for one CI type or template and optionally one role. */
export type FormLayout = components['schemas']['FormLayout'];
export type FormLayoutDocument = components['schemas']['FormLayoutDocument'];
export type FormLayoutSection = components['schemas']['FormLayoutSection'];
export type FormLayoutField = components['schemas']['FormLayoutField'];
export type FormLayoutCreate = components['schemas']['FormLayoutCreate'];
export type FormLayoutPatch = components['schemas']['FormLayoutPatch'];
/** A resolved form (WP-719): sections with fields, definitions and states. */
export type ResolvedForm = components['schemas']['ResolvedForm'];
export type ResolvedFormSection = components['schemas']['ResolvedFormSection'];

/** The filters of GET /layouts: exactly one target, optionally a role. */
export interface LayoutFilter {
  ci_type_id?: string;
  template_id?: string;
  role_id?: string;
}

export const layoutApi = {
  /** Every layout of the filter, all pages over next_cursor (API-02). */
  async list(filter: LayoutFilter): Promise<{ data: FormLayout[] }> {
    return { data: await fetchAllPages<FormLayout>('/layouts', filter) };
  },
  get(id: string): Promise<FormLayout> {
    return fetchAPI(`/layouts/${id}`);
  },
  /** A new layout; with an active draft the answer is the draft item (202). */
  create(data: FormLayoutCreate): Promise<Drafted<FormLayout>> {
    return fetchAPI('/layouts', { method: 'POST', body: JSON.stringify(data) });
  },
  /**
   * Changes a layout; version (form_layout.version) is sent as If-Match, so
   * a change in between is refused with 412 (API-07).
   */
  update(id: string, data: FormLayoutPatch, version?: number): Promise<Drafted<FormLayout>> {
    return fetchAPI(`/layouts/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
      headers: ifMatch(version),
    });
  },
  /** 204, or the draft item (202) while a draft is active. */
  remove(id: string, version?: number): Promise<MetamodelDraftItem | undefined> {
    return fetchAPI(`/layouts/${id}`, { method: 'DELETE', headers: ifMatch(version) });
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
export type AttributeDefinitionCreate = components['schemas']['AttributeDefinitionCreate'];
export type AttributeDefinitionPatch = components['schemas']['AttributeDefinitionPatch'];
/** The index status of a definition (DB-04, WP-796). */
export type AttributeIndexStatus = components['schemas']['AttributeIndexStatus'];
/** The applicability of a global attribute of the organization (MET-13, WP-255). */
export type AttributeAppliesTo = components['schemas']['AttributeAppliesTo'];

/** The filters of GET /attribute-definitions (MET-10, WP-255). */
export interface AttributeDefinitionFilter {
  ci_type_id?: string;
  attribute_set_id?: string;
  scope?: 'type' | 'global' | 'set';
  /** A CI type: its definitions and the global definitions that apply to it. */
  applicable_to?: string;
}

export const attributeDefinitionApi = {
  // Every definition of the filter: the list pages at 200 (API-02, F-215),
  // so the catalog follows next_cursor instead of reading one page.
  async list(params: AttributeDefinitionFilter): Promise<{ data: AttributeDefinition[] }> {
    return { data: await fetchAllPages<AttributeDefinition>('/attribute-definitions', params) };
  },
  get(id: string): Promise<AttributeDefinition> {
    return fetchAPI(`/attribute-definitions/${id}`);
  },
  /** One POST with the index flags (MET-15); without ci_type_id a global definition. */
  create(data: AttributeDefinitionCreate): Promise<Drafted<AttributeDefinition>> {
    return fetchAPI('/attribute-definitions', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: AttributeDefinitionPatch): Promise<Drafted<AttributeDefinition>> {
    return fetchAPI(`/attribute-definitions/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
  },
  delete(id: string): Promise<void> {
    return fetchAPI(`/attribute-definitions/${id}`, { method: 'DELETE' });
  },
  /** Queues an index job of the definition (DB-04, WP-796); answers its status. */
  reindex(id: string): Promise<AttributeIndexStatus> {
    return fetchAPI(`/attribute-definitions/${id}/reindex`, { method: 'POST', body: '{}' });
  },
};

// ─── Attribute sets (MET-16, WP-250, WP-797) ─────────────────────────────────

/** A named group of attribute definitions (scope set); a system set is read-only. */
export type AttributeSet = components['schemas']['AttributeSet'];
export type AttributeSetCreate = components['schemas']['AttributeSetCreate'];
export type AttributeSetPatch = components['schemas']['AttributeSetPatch'];
/** The ordered sets of a CI type (GET/PUT /ci-types/{id}/attribute-sets). */
export type CITypeAttributeSets = components['schemas']['CITypeAttributeSets'];
export type AttributeSetAssignment = CITypeAttributeSets['attribute_sets'][number];

export const attributeSetApi = {
  /** Every set of the organization and the system sets (cursor pages, API-02). */
  async list(): Promise<{ data: AttributeSet[] }> {
    return { data: await fetchAllPages<AttributeSet>('/attribute-sets') };
  },
  create(data: AttributeSetCreate): Promise<Drafted<AttributeSet>> {
    return fetchAPI('/attribute-sets', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: AttributeSetPatch): Promise<Drafted<AttributeSet>> {
    return fetchAPI(`/attribute-sets/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** A set assigned to a CI type is 409. */
  delete(id: string): Promise<void> {
    return fetchAPI(`/attribute-sets/${id}`, { method: 'DELETE' });
  },
  ofType(ciTypeId: string): Promise<CITypeAttributeSets> {
    return fetchAPI(`/ci-types/${ciTypeId}/attribute-sets`);
  },
  /** Replaces the ordered list of the type as a whole. */
  replaceOfType(
    ciTypeId: string,
    sets: AttributeSetAssignment[],
  ): Promise<Drafted<CITypeAttributeSets>> {
    return fetchAPI(`/ci-types/${ciTypeId}/attribute-sets`, {
      method: 'PUT',
      body: JSON.stringify({ ci_type_id: ciTypeId, attribute_sets: sets }),
    });
  },
};

// ─── Templates (MET-16, WP-251, WP-314) ──────────────────────────────────────

/**
 * A versioned, ordered bundle of attribute definitions of one CI type
 * (MET-16); no type template of GET /ci-types/templates. A system template
 * (is_system) is read-only.
 */
export type Template = components['schemas']['Template'];
export type TemplateCreate = components['schemas']['TemplateCreate'];
export type TemplatePatch = components['schemas']['TemplatePatch'];
export type TemplateClone = components['schemas']['TemplateClone'];
export type TemplateVersion = components['schemas']['TemplateVersion'];
export type TemplateVersionItem = components['schemas']['TemplateVersionItem'];
export type TemplateVersionItemsUpdate = components['schemas']['TemplateVersionItemsUpdate'];

export const templateApi = {
  /** Every template of the filter: all pages of 200 over next_cursor (API-02). */
  async list(params: { ci_type_id?: string } = {}): Promise<{ data: Template[] }> {
    return { data: await fetchAllPages<Template>('/templates', params) };
  },
  get(id: string): Promise<Template> {
    return fetchAPI(`/templates/${id}`);
  },
  /** Version 1 is created as an empty draft. */
  create(data: TemplateCreate): Promise<Drafted<Template>> {
    return fetchAPI('/templates', { method: 'POST', body: JSON.stringify(data) });
  },
  /** Display name, description and is_active; key and ci_type_id are immutable. */
  update(id: string, data: TemplatePatch): Promise<Drafted<Template>> {
    return fetchAPI(`/templates/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** Copies a template, a system template as well, under a new key. */
  clone(id: string, data: TemplateClone): Promise<Drafted<Template>> {
    return fetchAPI(`/templates/${id}/clone`, { method: 'POST', body: JSON.stringify(data) });
  },
  /** Every version, newest first, each with its items. */
  async versions(id: string): Promise<{ data: TemplateVersion[] }> {
    return { data: await fetchAllPages<TemplateVersion>(`/templates/${id}/versions`) };
  },
  /** The next version as a draft copy of the current one. */
  createVersion(id: string): Promise<Drafted<TemplateVersion>> {
    return fetchAPI(`/templates/${id}/versions`, { method: 'POST' });
  },
  /** Replaces the items of a draft version as a whole. */
  saveVersion(
    id: string,
    version: number,
    data: TemplateVersionItemsUpdate,
  ): Promise<Drafted<TemplateVersion>> {
    return fetchAPI(`/templates/${id}/versions/${version}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    });
  },
  /**
   * Publishes a draft version directly (MET-04, MET-53); with an active
   * metamodel draft the publication is captured as template_version/publish.
   * Needs metamodel:publish.
   */
  publishVersion(id: string, version: number): Promise<Drafted<TemplateVersion>> {
    return fetchAPI(`/templates/${id}/versions/${version}/publish`, { method: 'POST' });
  },
};

// ─── Rule catalog (MET-45, WP-716, WP-801) ───────────────────────────────────

/**
 * A named rule of the rule catalog for one attribute of exactly one target
 * (CI type, attribute set or template version); a system rule
 * (is_system, no organization_id) is read-only.
 */
export type MetamodelRule = components['schemas']['MetamodelRule'];
export type MetamodelRuleCreate = components['schemas']['MetamodelRuleCreate'];
export type MetamodelRulePatch = components['schemas']['MetamodelRulePatch'];
export type MetamodelRuleKind = components['schemas']['MetamodelRuleKind'];
export type MetamodelRuleListResponse = components['schemas']['MetamodelRuleListResponse'];

/** The filters of GET /rules; the catalog is organization-wide (no client or site). */
export interface RuleFilter {
  ci_type_id?: string;
  attribute_set_id?: string;
  template_version_id?: string;
  attribute_key?: string;
  kind?: MetamodelRuleKind;
}

export const ruleApi = {
  /** One page of the catalog, newest first; next_cursor names the next one (API-02). */
  list(filter: RuleFilter, cursor?: string): Promise<MetamodelRuleListResponse> {
    return fetchAPI(`/rules${qs({ ...filter, limit: 50, cursor })}`);
  },
  get(id: string): Promise<MetamodelRule> {
    return fetchAPI(`/rules/${id}`);
  },
  /**
   * Creates a rule; with an active draft withDraft adds ?draft and the
   * answer is the draft item (202), which names no rule: the caller does
   * not ask for it by an id.
   */
  create(data: MetamodelRuleCreate): Promise<Drafted<MetamodelRule>> {
    return fetchAPI('/rules', { method: 'POST', body: JSON.stringify(data) });
  },
  /** attribute_key, kind, condition, ast, message and is_active; null removes. */
  update(id: string, data: MetamodelRulePatch): Promise<Drafted<MetamodelRule>> {
    return fetchAPI(`/rules/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** 204, or the draft item (202) while a draft is active. */
  delete(id: string): Promise<MetamodelDraftItem | undefined> {
    return fetchAPI(`/rules/${id}`, { method: 'DELETE' });
  },
};

// ─── Templates at the CI, template migration, metamodel transfer (WP-800) ──

/** A template assigned to a CI with the version it uses and its position (WP-710). */
export type CITemplate = components['schemas']['CITemplate'];
/** An entry of the ordered list PUT /cis/{id}/templates sends. */
export type CITemplateRef = components['schemas']['CITemplateRef'];
export type TemplateMigrationRequest = components['schemas']['TemplateMigrationRequest'];
/** The result of a job template.migrate (GET /jobs/{id}/result, WP-712). */
export type TemplateMigrationReport = components['schemas']['TemplateMigrationReport'];
export type MetamodelImportResult = components['schemas']['MetamodelImportResult'];
export type MetamodelImportReport = components['schemas']['MetamodelImportReport'];

/** The answer of POST /templates/{id}/migrations: the job template.migrate (202). */
export interface TemplateMigrationStart {
  job_id: string;
  status: string;
  kind: 'template.migrate';
}

/** The most CIs a migration names by id (API-08). */
export const MIGRATION_MAX_IDS = 500;

export const ciTemplateApi = {
  /** The templates of a CI in position order; complete, without cursor. */
  list(ciId: string): Promise<{ data: CITemplate[] }> {
    return fetchAPI(`/cis/${ciId}/templates`);
  },
  /**
   * Replaces the ordered list (the position is the index) with If-Match of
   * the CI version the page shows (API-07, 412 otherwise); needs ci:write.
   */
  replace(ciId: string, templates: CITemplateRef[], version: number | undefined): Promise<CI> {
    return fetchAPI(`/cis/${ciId}/templates`, {
      method: 'PUT',
      headers: ifMatch(version),
      body: JSON.stringify({ templates }),
    });
  },
};

export const templateMigrationApi = {
  /** Queues the job template.migrate (no draft capture, DRAFT_EXEMPT); citype:manage. */
  start(templateId: string, request: TemplateMigrationRequest): Promise<TemplateMigrationStart> {
    return fetchAPI(`/templates/${templateId}/migrations`, {
      method: 'POST',
      body: JSON.stringify(request),
    });
  },
};

export const metamodelTransferApi = {
  /**
   * The metamodel export format 2 (WP-262) over fetchAPI, so with the bearer
   * header of the session: a plain link would carry no token.
   */
  async export(): Promise<Blob> {
    const data = await fetchAPI<unknown>('/ci-types/export?format_version=2');
    return new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
  },
  /**
   * Sends a metamodel export as a draft (WP-722): 201 with the draft,
   * 200 with report.unchanged and draft_id null; citype:manage.
   */
  import(document: string): Promise<MetamodelImportResult> {
    return fetchAPI('/metamodel/import', { method: 'POST', body: document });
  },
};

// ─── Sensitive fields (RBA-07, E-43, WP-726) ─────────────────────────────────

export type SensitiveField = components['schemas']['SensitiveField'];

export const sensitiveFieldApi = {
  /** Every mark of an object kind (cursor pages); field_permission:manage, else 403. */
  async list(params: { object_kind?: string } = {}): Promise<{ data: SensitiveField[] }> {
    return {
      data: await fetchAllPages<SensitiveField>('/field-permissions/sensitive-fields', params),
    };
  },
};

// ─── Global + instance field definitions (§2) ────────────────────────────────

/**
 * A global field of GET /ci-fields/global (MET-13): an organization-wide
 * field applies to every CI type of the organization, the system types
 * included; a system field (is_system) is read-only.
 */
export type GlobalFieldDefinition = FieldDefinition & {
  id?: string;
  scope?: 'global' | 'type';
  is_system?: boolean;
};

export const ciFieldApi = {
  listGlobal(): Promise<{ data: GlobalFieldDefinition[] }> {
    return fetchAPI('/ci-fields/global');
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

/**
 * Relationship type metadata (REL-01, CH27) with the attribute schema of its
 * edges (REL-07, WP-295); derived from the OpenAPI schema (K27), so it
 * carries organization_id (empty for a system type) and metadata_schema.
 */
export type RelationshipType = components['schemas']['RelationshipTypeDefinition'];
/** One definition of the attribute schema of a relationship type (WP-295). */
export type RelationshipAttributeDefinition =
  components['schemas']['RelationshipAttributeDefinition'];
/** The body of POST and PATCH /relationship-types. */
export type RelationshipTypeUpsert = components['schemas']['RelationshipTypeUpsert'];
export type RelationshipTypeCategory = components['schemas']['RelationshipTypeCategory'];
export type RelationshipImpactDirection = components['schemas']['RelationshipImpactDirection'];
export type RelationshipTypeCardinality = components['schemas']['RelationshipTypeCardinality'];

/**
 * validationRulesOf turns the validation of an attribute definition (the
 * rule AST of MET-45 the server returns, or the legacy form) into the
 * ValidationRules of a form field, as fieldmeta.ValidationFromRule does on
 * the server: pattern, range of value (min, max) and of length
 * (min_length, max_length) and enum (options). required is a flag of the
 * definition and not repeated here.
 */
export function validationRulesOf(validation: unknown): ValidationRules | undefined {
  if (typeof validation !== 'object' || validation === null) return undefined;
  const v = validation as Record<string, unknown>;
  if (typeof v.op !== 'string') {
    // The legacy form is already the structure of a form field.
    const legacy = v as ValidationRules;
    return Object.keys(legacy).length > 0 ? legacy : undefined;
  }
  const out: ValidationRules = {};
  const patterns: string[] = [];
  const visit = (rule: Record<string, unknown>) => {
    switch (rule.op) {
      case 'all':
        for (const child of (rule.rules as unknown[]) ?? []) {
          if (typeof child === 'object' && child !== null) visit(child as Record<string, unknown>);
        }
        return;
      case 'pattern':
        if (typeof rule.pattern === 'string') patterns.push(rule.pattern);
        return;
      case 'range': {
        const length = rule.of === 'length';
        if (typeof rule.min === 'number') out[length ? 'min_length' : 'min'] = rule.min;
        if (typeof rule.max === 'number') out[length ? 'max_length' : 'max'] = rule.max;
        return;
      }
      case 'enum':
        if (Array.isArray(rule.values)) out.options = rule.values.map(String);
        return;
      default:
    }
  };
  visit(v);
  if (patterns.length > 0) out.pattern = patterns[0];
  if (patterns.length > 1) out.patterns = patterns.slice(1);
  return Object.keys(out).length > 0 ? out : undefined;
}

/**
 * relationshipAttributeFields maps the attribute schema of a relationship
 * type onto the field definitions of DynamicForm (WP-323): key → name,
 * display_name in the UI language → label, default → default_value; the
 * validation as ValidationRules, conditional as it is.
 */
export function relationshipAttributeFields(
  schema: readonly RelationshipAttributeDefinition[] | undefined,
  language: string,
): FieldDefinition[] {
  return (schema ?? []).map((def, index) => ({
    name: def.key,
    label: localizedText(def.display_name, language) || def.key,
    data_type: def.data_type,
    required: def.required ?? false,
    default_value:
      def.default === undefined || def.default === null
        ? undefined
        : typeof def.default === 'string'
          ? def.default
          : JSON.stringify(def.default),
    enum_values: def.enum_values,
    validation: validationRulesOf(def.validation),
    conditional: def.conditional as FieldDefinition['conditional'],
    sort_order: index,
  }));
}

/**
 * relationshipAttributeDefaults are the values a new edge starts with: the
 * default of each definition that has one (the server does not set it).
 */
export function relationshipAttributeDefaults(
  schema: readonly RelationshipAttributeDefinition[] | undefined,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const def of schema ?? []) {
    if (def.default !== undefined && def.default !== null) out[def.key] = def.default;
  }
  return out;
}

export const relationshipTypeApi = {
  list(params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<RelationshipType>>(`/relationship-types${qs(params)}`);
  },
  get(key: string): Promise<RelationshipType> {
    return fetchAPI(`/relationship-types/${encodeURIComponent(key)}`);
  },
  create(
    data: RelationshipTypeUpsert & { key: string; display_name: string; reverse_name: string },
  ): Promise<RelationshipType> {
    return fetchAPI('/relationship-types', { method: 'POST', body: JSON.stringify(data) });
  },
  update(key: string, data: RelationshipTypeUpsert): Promise<RelationshipType> {
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
  guard?: LifecycleGuard;
}

// The guard of a transition and the allowed transitions of an entity
// (LCY-02, WP-723); the types come from the OpenAPI specification.
export type LifecycleGuard = components['schemas']['LifecycleGuard'];
export type LifecycleTransitionOptions = components['schemas']['LifecycleTransitionOptions'];

export interface LifecycleDefinition {
  id: string;
  key: string;
  name: string;
  applies_to: string;
  is_system: boolean;
  description?: string;
  version: number;
  states?: LifecycleState[];
  transitions?: LifecycleTransition[];
}

// Lifecycle models live under /lifecycle-models (E-38); /lifecycle-definitions
// is the deprecated alias (WP-723).
export const lifecycleApi = {
  list(params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<LifecycleDefinition>>(`/lifecycle-models${qs(params)}`);
  },
  get(id: string): Promise<LifecycleDefinition> {
    return fetchAPI(`/lifecycle-models/${id}`);
  },
  create(data: {
    key: string;
    name: string;
    applies_to?: string;
    description?: string;
    states: unknown[];
    transitions?: unknown[];
  }) {
    return fetchAPI('/lifecycle-models', { method: 'POST', body: JSON.stringify(data) });
  },
  // The transitions the effective model of the entity allows from its
  // current state (GET /{cis|assets}/{id}/lifecycle-transitions).
  options(entityType: 'assets' | 'cis', id: string): Promise<LifecycleTransitionOptions> {
    return fetchAPI(`/${entityType}/${id}/lifecycle-transitions`);
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

/** A client (customer) of the organization (LOC-02). */
export type Client = components['schemas']['Client'];
/** The body of POST /clients (WP-855). */
export type ClientCreateRequest = components['schemas']['CreateClientRequest'];
/** The body of PATCH /clients/{id} (WP-855). */
export type ClientUpdateRequest = components['schemas']['UpdateClientRequest'];

/**
 * clientApi reads and writes the clients (customers) of the organization:
 * reading needs location:read, creating, changing and deleting user:manage
 * (WP-855).
 */
export const clientApi = {
  /** One page of 200 clients; `offset` reads the following pages (F-340). */
  list({ offset = 0 }: { offset?: number } = {}) {
    return fetchAPI<components['schemas']['ClientListResponse']>(
      `/clients?limit=200${offset > 0 ? `&offset=${offset}` : ''}`,
    );
  },
  get(id: string) {
    return fetchAPI<Client>(`/clients/${encodeURIComponent(id)}`);
  },
  /** create adds a client; a taken name or slug is 409 naming the field. */
  create(data: ClientCreateRequest) {
    return fetchAPI<Client>('/clients', { method: 'POST', body: JSON.stringify(data) });
  },
  /** update changes the sent fields of a client (PATCH). */
  update(id: string, data: ClientUpdateRequest) {
    return fetchAPI<Client>(`/clients/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
  },
  /** remove deletes a client; the server deletes its sites with it. */
  remove(id: string) {
    return fetchAPI<void>(`/clients/${encodeURIComponent(id)}`, { method: 'DELETE' });
  },
  /** hiddenCITypes reads the CI types the client hides (WP-858, location:read). */
  hiddenCITypes(id: string) {
    return fetchAPI<HiddenCITypes>(`/clients/${encodeURIComponent(id)}/hidden-ci-types`);
  },
  /**
   * setHiddenCITypes replaces the CI types the client hides (WP-858,
   * user:manage); an unknown type is 422 on ci_type_ids.
   */
  setHiddenCITypes(id: string, ciTypeIds: string[]) {
    return fetchAPI<HiddenCITypes>(`/clients/${encodeURIComponent(id)}/hidden-ci-types`, {
      method: 'PUT',
      body: JSON.stringify({ ci_type_ids: ciTypeIds } satisfies HiddenCITypes),
    });
  },
};

/**
 * HiddenCITypes are the CI types a client hides (MET-05, WP-858): the type
 * lists for the client leave them out and a CI of the client cannot be
 * created with one; they stay active, existing CIs stay.
 */
export type HiddenCITypes = components['schemas']['HiddenCITypes'];

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

// ─── Scanned codes (INV-03, E-52, WP-762, WP-807) ────────────────────────────

/** LabelLookupResult is the object GET /labels/lookup names: kind, id and name. */
export type LabelLookupResult = components['schemas']['LabelLookupResult'];

/** ScanKind is the kind of object a label names (E-52). */
export type ScanKind = LabelLookupResult['kind'];

/**
 * ScannedObject is the object a scanned code resolves to: kind, id and
 * name, the inventory number of an asset or the SKU of a consumable as
 * code, and for a location its node (kind and ltree path of ids).
 */
export interface ScannedObject {
  kind: ScanKind;
  id: string;
  name: string;
  code?: string;
  location?: Location;
}

const OBJECT_URN =
  /^urn:reticora:(asset|consumable|location):([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/i;

/**
 * parseObjectUrn splits the URN of a label,
 * urn:reticora:<asset|consumable|location>:<uuid> (E-52, WP-289), into kind
 * and id; any other code gives undefined.
 */
export function parseObjectUrn(code: string): { kind: ScanKind; id: string } | undefined {
  const match = OBJECT_URN.exec(code.trim());
  if (!match) return undefined;
  return { kind: match[1]!.toLowerCase() as ScanKind, id: match[2]!.toLowerCase() };
}

export const labelApi = {
  /**
   * lookup resolves a code that is no object URN: a barcode or an
   * inventory number (GET /labels/lookup?code=, WP-762). An unknown code
   * and an object outside the caller's scope, rights or plan answer 404
   * alike.
   */
  lookup(code: string) {
    return fetchAPI<LabelLookupResult>(`/labels/lookup${qs({ code })}`);
  },
};

/**
 * resolveScannedCode turns a scanned code into its object. A URN is split
 * locally and the object read through GET /assets/{id}, /consumables/{id}
 * or /locations/{id}, without a lookup. Every other code goes to GET
 * /labels/lookup; a location found there is read once more for its kind
 * and path (a flow takes only bins).
 */
export async function resolveScannedCode(code: string): Promise<ScannedObject> {
  const urn = parseObjectUrn(code);
  if (urn?.kind === 'asset') {
    const asset = await assetApi.get(urn.id);
    return {
      kind: 'asset',
      id: asset.id,
      name: asset.name,
      code: asset.inventory_no,
    };
  }
  if (urn?.kind === 'consumable') {
    const item = await consumableApi.get(urn.id);
    return { kind: 'consumable', id: item.id, name: item.name, code: item.sku };
  }
  let locationId = urn?.id;
  if (!locationId) {
    const hit = await labelApi.lookup(code.trim());
    if (hit.kind !== 'location') return { kind: hit.kind, id: hit.id, name: hit.name };
    locationId = hit.id;
  }
  const location = await locationApi.get(locationId);
  return { kind: 'location', id: location.id, name: location.name, location };
}

// ─── Movements and stock of consumables (§9, §6) ─────────────────────────────

/**
 * CONSUMABLE_BOOKING_TYPES are the quantity bookings of a consumable over
 * POST /stock-movements (WP-756), labelled by consumables.movementTypes.*:
 * receipt into a bin (to_location_id), retrieval from a bin
 * (from_location_id), correction of one bin (to_location_id raises,
 * from_location_id lowers) and transfer between two different bins.
 */
export const CONSUMABLE_BOOKING_TYPES = ['receipt', 'retrieval', 'correction', 'transfer'] as const;
export type ConsumableBookingType = (typeof CONSUMABLE_BOOKING_TYPES)[number];

export interface StockMovement {
  id: string;
  item_kind: string;
  asset_id?: string;
  consumable_id?: string;
  movement_type: string;
  from_location_id?: string;
  to_location_id?: string;
  quantity?: number;
  actor_id?: string;
  reason?: string;
  /** When the movement happened; the list sorts newest first on it (WP-274). */
  occurred_at: string;
  created_at: string;
}

/**
 * StockBooking is the body of POST /stock-movements as the flows send it:
 * a quantity booking of a consumable in bins, or the transfer of a serial
 * asset to a location (E-142). Other movement types of an asset arise only
 * in their domain paths (assign, reserve, dispose).
 */
export type StockBooking =
  | {
      consumable_id: string;
      movement_type: ConsumableBookingType;
      from_location_id?: string;
      to_location_id?: string;
      quantity: number;
      reason?: string;
    }
  | {
      asset_id: string;
      movement_type: 'transfer';
      to_location_id: string;
      reason?: string;
    };

/** The stock of a consumable in one bin: on_hand and reserved (STK-02, WP-283). */
export type ConsumableStock = components['schemas']['ConsumableStock'];
export type ConsumableStockList = components['schemas']['ConsumableStockListResponse'];

export const inventoryApi = {
  listMovements(
    params: {
      asset_id?: string;
      consumable_id?: string;
      movement_type?: string;
      location_id?: string;
      /** RFC 3339 bounds of occurred_at, inclusive. */
      occurred_from?: string;
      occurred_to?: string;
      limit?: number;
      offset?: number;
    } = {},
  ) {
    return fetchAPI<PaginatedResponse<StockMovement>>(`/stock-movements${qs(params)}`);
  },
  /**
   * book records a physical movement through POST /stock-movements (WP-274,
   * WP-756, E-38): a consumable books receipt, retrieval, correction or
   * transfer with quantity on its stock per bin; a serial asset only moves
   * by transfer to to_location_id (E-142). Movements are never changed or
   * deleted; a correction is a counter-booking (Convention 14).
   */
  book(data: StockBooking) {
    return fetchAPI<StockMovement>('/stock-movements', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  /**
   * The stock of a consumable per bin (GET /consumables/{id}/stock, WP-283);
   * cursor is the next_cursor of the page before.
   */
  stockLevels(consumableId: string, cursor?: string) {
    return fetchAPI<ConsumableStockList>(
      `/consumables/${consumableId}/stock${qs({ limit: 200, cursor })}`,
    );
  },
};

// ─── Reservations (§10) ──────────────────────────────────────────────────────

export interface Reservation {
  id: string;
  item_kind: 'asset' | 'consumable';
  asset_id?: string;
  /** The reserved consumable and its bin (item_kind consumable, WP-284). */
  consumable_id?: string;
  location_id?: string;
  quantity: number;
  state: string;
  reserved_from: string;
  reserved_until?: string;
  assignee_id?: string;
  project_ref?: string;
  expires_at?: string;
  reason?: string;
}

export type ReserveAssetRequest = components['schemas']['ReserveAssetRequest'];
export type ConvertReservationRequest = components['schemas']['ConvertReservationRequest'];

export const reservationApi = {
  list(params: { state?: string; limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<Reservation>>(`/reservations${qs(params)}`);
  },
  /**
   * create reserves a quantity of a consumable in a bin (WP-759). Assets are
   * reserved only through reserve (POST /assets/{id}/reserve, WP-277).
   */
  create(data: {
    item_kind?: 'consumable';
    consumable_id: string;
    /** The bin; without it the only bin of the consumable (WP-284). */
    location_id?: string;
    quantity?: number;
    reserved_until?: string;
    expires_at?: string;
    assignee_id?: string;
    project_ref?: string;
    reason?: string;
  }) {
    return fetchAPI<Reservation>('/reservations', { method: 'POST', body: JSON.stringify(data) });
  },
  /**
   * reserve reserves an asset for a user (assignee_id) or a project
   * (project_ref) until reserved_until (POST /assets/{id}/reserve, AST-06,
   * WP-277); 409 asset-reserved or asset-assigned when it is taken.
   */
  reserve(assetId: string, data: ReserveAssetRequest) {
    return fetchAPI<Reservation>(`/assets/${assetId}/reserve`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  /**
   * convert turns an active asset reservation into an assignment (POST
   * /reservations/{id}/convert, WP-277): to assignee_id or team_id, else to
   * the user of the reservation. 409 when it is no longer active.
   */
  convert(id: string, data: ConvertReservationRequest = {}) {
    return fetchAPI<components['schemas']['ConvertReservationResponse']>(
      `/reservations/${id}/convert`,
      { method: 'POST', body: JSON.stringify(data) },
    );
  },
  release(id: string): Promise<Reservation> {
    return fetchAPI(`/reservations/${id}/release`, { method: 'POST', body: '{}' });
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
  // The state at a point in time (AUD-08): GET /cis/{id}/as-of and
  // /assets/{id}/as-of (WP-793, WP-795); the former /state paths are
  // deprecated aliases.
  stateAt(entityType: 'assets' | 'cis', id: string, at?: string): Promise<StateSnapshot> {
    return fetchAPI(`/${entityType}/${id}/as-of${qs({ at })}`);
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
 * WP-297 adds affected_services, service_spofs with spof_truncated (the SPOF
 * of the services, IMP-08) and the counts of blast_radius; the ImpactPanel
 * shows them (WP-818). single_points_of_failure are the direct dependents
 * without an alternative, not the SPOF of IMP-08.
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

/**
 * A system view (VIE-02, WP-307): a code preset with a stable key, a name
 * per language and no row in saved_view. It is read-only; a caller clones it
 * with savedViewApi.clonePreset.
 */
export interface SavedViewPreset {
  key: string;
  /** English name. */
  name: string;
  display_name: { de: string; en: string };
  entity_kind: string;
  filter_spec: Record<string, unknown>;
  sort?: SavedViewSortKey[];
  columns?: string[];
  /** Always true. */
  system: boolean;
}

/** presetName is the name of a system view in the UI language, else English. */
export function presetName(preset: SavedViewPreset, language: string): string {
  const lang = language.toLowerCase().startsWith('de') ? 'de' : 'en';
  return preset.display_name?.[lang] || preset.display_name?.en || preset.name || preset.key;
}

export const savedViewApi = {
  list(params: { limit?: number; offset?: number } = {}) {
    return fetchAPI<PaginatedResponse<SavedView>>(`/saved-views${qs(params)}`);
  },
  presets(): Promise<{ data: SavedViewPreset[] }> {
    return fetchAPI('/saved-views/presets');
  },
  /**
   * clonePreset creates an own view from a system view (POST /saved-views
   * with from_preset, VIE-02): the server takes over its entity kind,
   * filter, sort and columns; the clone is not shared.
   */
  clonePreset(key: string, name: string): Promise<SavedView> {
    return fetchAPI('/saved-views', {
      method: 'POST',
      body: JSON.stringify({ name, from_preset: key }),
    });
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

// ─── Tags (CI-08, WP-753, WP-817) ────────────────────────────────────────────

/** A catalog tag of the organization; CI.tags lists its key. */
export type Tag = components['schemas']['Tag'];
/** The name of a tag in de and/or en. */
export type TagDisplayName = components['schemas']['TagDisplayName'];

/** The changeable fields of a tag; color null removes the color. */
export interface TagWrite {
  display_name?: TagDisplayName;
  color?: string | null;
}

/** tagLabel is the name of a tag in the UI language, else the other one, else its key. */
export function tagLabel(tag: Pick<Tag, 'key' | 'display_name'>, language: string): string {
  const lang = language.toLowerCase().startsWith('de') ? 'de' : 'en';
  return tag.display_name?.[lang] || tag.display_name?.en || tag.display_name?.de || tag.key;
}

/**
 * tagApi is the tag catalog (WP-753): org-wide, core without a module gate
 * (E-105), read with ci:read and written with tag:manage (E-106). The list
 * pages by the signed keyset cursor (next_cursor, API-02).
 */
export const tagApi = {
  list(params: { limit?: number; cursor?: string } = {}): Promise<PaginatedResponse<Tag>> {
    return fetchAPI(`/tags${qs(params)}`);
  },
  /** all reads the whole catalog, page by page over next_cursor. */
  all(): Promise<Tag[]> {
    return fetchAllPages<Tag>('/tags');
  },
  create(data: { key: string } & TagWrite): Promise<Tag> {
    return fetchAPI('/tags', { method: 'POST', body: JSON.stringify(data) });
  },
  /** update changes name and color; the key is unchangeable (422). */
  update(id: string, data: TagWrite): Promise<Tag> {
    return fetchAPI(`/tags/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** delete removes a tag no CI carries; an assigned or system tag is 409. */
  delete(id: string): Promise<void> {
    return fetchAPI(`/tags/${id}`, { method: 'DELETE' });
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

// ─── IPAM sections and subnet tree (IPM-01, IPM-03, IPM-10, IPM-20, WP-915) ─

export type IPAMSection = components['schemas']['IPAMSection'];
export type IPAMSectionCreate = components['schemas']['CreateIPAMSectionRequest'];
export type IPAMSectionUpdate = components['schemas']['UpdateIPAMSectionRequest'];
export type IPAMSectionPermission = components['schemas']['IPAMSectionPermission'];
export type IPAMSectionLevel = IPAMSectionPermission['level'];
export type Subnet = components['schemas']['Subnet'];
export type SubnetUsage = components['schemas']['SubnetUsage'];

/**
 * SubnetQuery are the filters of GET /subnets the IPAM page sets (WP-909):
 * the section, one level of the tree (parent_id root or the id of the
 * parent), VRF, client, site, the text q and contains (the chain of subnets
 * that contain an address, narrowest first).
 */
export interface SubnetQuery {
  section_id?: string;
  parent_id?: string;
  vrf_id?: string;
  client_id?: string;
  site_id?: string;
  q?: string;
  contains?: string;
  limit?: number;
  offset?: number;
}

export const ipamApi = {
  listSections(
    params: { limit?: number; offset?: number } = {},
  ): Promise<PaginatedResponse<IPAMSection>> {
    return fetchAPI(`/ipam-sections${qs(params)}`);
  },
  createSection(data: IPAMSectionCreate): Promise<IPAMSection> {
    return fetchAPI('/ipam-sections', { method: 'POST', body: JSON.stringify(data) });
  },
  updateSection(id: string, data: IPAMSectionUpdate): Promise<IPAMSection> {
    return fetchAPI(`/ipam-sections/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** deleteSection: the default section and a section with subnets are 409. */
  deleteSection(id: string): Promise<void> {
    return fetchAPI(`/ipam-sections/${id}`, { method: 'DELETE' });
  },
  listPermissions(sectionId: string): Promise<PaginatedResponse<IPAMSectionPermission>> {
    return fetchAPI(`/ipam-sections/${sectionId}/permissions${qs({ limit: API_MAX_LIMIT })}`);
  },
  createPermission(
    sectionId: string,
    data: { team_id: string; level: IPAMSectionLevel },
  ): Promise<IPAMSectionPermission> {
    return fetchAPI(`/ipam-sections/${sectionId}/permissions`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  updatePermission(
    sectionId: string,
    id: string,
    level: IPAMSectionLevel,
  ): Promise<IPAMSectionPermission> {
    return fetchAPI(`/ipam-sections/${sectionId}/permissions/${id}`, {
      method: 'PATCH',
      body: JSON.stringify({ level }),
    });
  },
  deletePermission(sectionId: string, id: string): Promise<void> {
    return fetchAPI(`/ipam-sections/${sectionId}/permissions/${id}`, { method: 'DELETE' });
  },
  /** listSubnets reads one page (at most API_MAX_LIMIT) of GET /subnets. */
  listSubnets(params: SubnetQuery): Promise<PaginatedResponse<Subnet>> {
    return fetchAPI(`/subnets${qs({ limit: API_MAX_LIMIT, ...params })}`);
  },
  subnetUsage(id: string): Promise<SubnetUsage> {
    return fetchAPI(`/subnets/${id}/usage`);
  },
  // ─── Subnet detail (IPM-03, IPM-05, IPM-07, IPM-19, WP-916) ───────────────
  getSubnet(id: string): Promise<Subnet> {
    return fetchAPI(`/subnets/${id}`);
  },
  createSubnet(data: SubnetCreate): Promise<Subnet> {
    return fetchAPI('/subnets', { method: 'POST', body: JSON.stringify(data) });
  },
  updateSubnet(id: string, data: SubnetUpdate): Promise<Subnet> {
    return fetchAPI(`/subnets/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  deleteSubnet(id: string): Promise<void> {
    return fetchAPI(`/subnets/${id}`, { method: 'DELETE' });
  },
  /** subnetAddresses reads one page (at most API_MAX_LIMIT) of the addresses of a subnet. */
  subnetAddresses(
    id: string,
    params: { limit?: number; offset?: number } = {},
  ): Promise<PaginatedResponse<IPAddress>> {
    return fetchAPI(`/subnets/${id}/addresses${qs({ limit: API_MAX_LIMIT, ...params })}`);
  },
  /** allSubnetAddresses reads every address of a subnet page by page (the address map). */
  allSubnetAddresses(id: string): Promise<IPAddress[]> {
    return fetchAllPages(`/subnets/${id}/addresses`);
  },
  /** allocateAddress stores the next free host address (409 subnet-exhausted when full). */
  allocateAddress(id: string, data: IPAllocate): Promise<IPAddress> {
    return fetchAPI(`/subnets/${id}/allocate`, { method: 'POST', body: JSON.stringify(data) });
  },
  /** allocateSubnet stores the next free child of the prefix length. */
  allocateSubnet(id: string, data: SubnetAllocate): Promise<Subnet> {
    return fetchAPI(`/subnets/${id}/allocate-subnet`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  /** createReservation stores an address of type reserved (NET-09). */
  createReservation(id: string, data: IPReservationCreate): Promise<IPAddress> {
    return fetchAPI(`/subnets/${id}/reservations`, {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
  /** deleteReservation releases a reservation of the subnet. */
  deleteReservation(id: string, ipId: string): Promise<void> {
    return fetchAPI(`/subnets/${id}/reservations/${ipId}`, { method: 'DELETE' });
  },
  /** getIPAddress reads one address (a search hit ?address=<id> of the subnet detail). */
  getIPAddress(id: string): Promise<IPAddress> {
    return fetchAPI(`/ips/${id}`);
  },
  // ─── Address dialog (IPM-13, IPM-14, IPM-19, WP-917) ──────────────────────
  /** createIPAddress stores an address (origin manual); 422 names ci_id, role or vrf_id. */
  createIPAddress(data: IPAddressCreate): Promise<IPAddress> {
    return fetchAPI('/ips', { method: 'POST', body: JSON.stringify(data) });
  },
  /** updateIPAddress changes the fields sent; ci_id or interface_id "" clears them. */
  updateIPAddress(id: string, data: IPAddressUpdate): Promise<IPAddress> {
    return fetchAPI(`/ips/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** ipHistory reads one page of the change history of an address (IPM-08). */
  ipHistory(
    id: string,
    params: { limit?: number; offset?: number } = {},
  ): Promise<PaginatedResponse<IPAMChange>> {
    return fetchAPI(`/ips/${id}/history${qs({ limit: IPAM_HISTORY_PAGE, ...params })}`);
  },
  /** subnetHistory reads one page of the change history of a subnet (IPM-08). */
  subnetHistory(
    id: string,
    params: { limit?: number; offset?: number } = {},
  ): Promise<PaginatedResponse<IPAMChange>> {
    return fetchAPI(`/subnets/${id}/history${qs({ limit: IPAM_HISTORY_PAGE, ...params })}`);
  },
  /** ciInterfaces reads the network interfaces of a CI (NET-01), at most API_MAX_LIMIT. */
  ciInterfaces(ciId: string): Promise<PaginatedResponse<NetworkInterface>> {
    return fetchAPI(`/cis/${ciId}/interfaces${qs({ limit: API_MAX_LIMIT })}`);
  },
  // ─── Network section of the CI detail (IPM-13, WP-919) ────────────────────
  /** ciAddresses reads every address bound to a CI (GET /cis/{id}/addresses, IPM-13) page by page. */
  ciAddresses(ciId: string): Promise<CIAddress[]> {
    return fetchAllPages(`/cis/${ciId}/addresses`);
  },
  /** deleteIPAddress removes an address; the network section releases a failed assignment with it. */
  deleteIPAddress(id: string): Promise<void> {
    return fetchAPI(`/ips/${id}`, { method: 'DELETE' });
  },
  getVRF(id: string): Promise<IPAMVRF> {
    return fetchAPI(`/vrfs/${id}`);
  },
  getVLAN(id: string): Promise<VLAN> {
    return fetchAPI(`/vlans/${id}`);
  },
  /** listVLANs reads one page (at most API_MAX_LIMIT) of the VLAN catalog. */
  listVLANs(
    params: { site_id?: string; limit?: number; offset?: number } = {},
  ): Promise<PaginatedResponse<VLAN>> {
    return fetchAPI(`/vlans${qs({ limit: API_MAX_LIMIT, ...params })}`);
  },
  // ─── VLANs with domains, VRFs, nameserver sets (IPM-05–07, WP-918) ──────
  createVLAN(data: VLANCreate): Promise<VLAN> {
    return fetchAPI('/vlans', { method: 'POST', body: JSON.stringify(data) });
  },
  updateVLAN(id: string, data: VLANUpdate): Promise<VLAN> {
    return fetchAPI(`/vlans/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  deleteVLAN(id: string): Promise<void> {
    return fetchAPI(`/vlans/${id}`, { method: 'DELETE' });
  },
  /** listVLANDomains reads one page (at most API_MAX_LIMIT) of the VLAN domains. */
  listVLANDomains(
    params: { limit?: number; offset?: number } = {},
  ): Promise<PaginatedResponse<VLANDomain>> {
    return fetchAPI(`/vlan-domains${qs({ limit: API_MAX_LIMIT, ...params })}`);
  },
  /** listDomainVLANs reads one page of the VLANs of a domain (the domain filter). */
  listDomainVLANs(
    domainId: string,
    params: { limit?: number; offset?: number } = {},
  ): Promise<PaginatedResponse<VLAN>> {
    return fetchAPI(`/vlan-domains/${domainId}/vlans${qs({ limit: API_MAX_LIMIT, ...params })}`);
  },
  createVLANDomain(data: VLANDomainCreate): Promise<VLANDomain> {
    return fetchAPI('/vlan-domains', { method: 'POST', body: JSON.stringify(data) });
  },
  updateVLANDomain(id: string, data: VLANDomainUpdate): Promise<VLANDomain> {
    return fetchAPI(`/vlan-domains/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  deleteVLANDomain(id: string): Promise<void> {
    return fetchAPI(`/vlan-domains/${id}`, { method: 'DELETE' });
  },
  /** listVRFs reads one page (at most API_MAX_LIMIT) of the VRFs with RD, RT and sections. */
  listVRFs(params: { limit?: number; offset?: number } = {}): Promise<PaginatedResponse<IPAMVRF>> {
    return fetchAPI(`/vrfs${qs({ limit: API_MAX_LIMIT, ...params })}`);
  },
  createVRF(data: VRFCreate): Promise<IPAMVRF> {
    return fetchAPI('/vrfs', { method: 'POST', body: JSON.stringify(data) });
  },
  updateVRF(id: string, data: VRFUpdate): Promise<IPAMVRF> {
    return fetchAPI(`/vrfs/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** deleteVRF: the default VRF and a VRF with subnets are refused (409). */
  deleteVRF(id: string): Promise<void> {
    return fetchAPI(`/vrfs/${id}`, { method: 'DELETE' });
  },
  createNameserverSet(data: NameserverSetCreate): Promise<NameserverSet> {
    return fetchAPI('/nameserver-sets', { method: 'POST', body: JSON.stringify(data) });
  },
  updateNameserverSet(id: string, data: NameserverSetUpdate): Promise<NameserverSet> {
    return fetchAPI(`/nameserver-sets/${id}`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** deleteNameserverSet: a set subnets use is refused (409). */
  deleteNameserverSet(id: string): Promise<void> {
    return fetchAPI(`/nameserver-sets/${id}`, { method: 'DELETE' });
  },
  /** listNameserverSets reads one page (at most API_MAX_LIMIT) of the nameserver sets. */
  listNameserverSets(
    params: { limit?: number; offset?: number } = {},
  ): Promise<PaginatedResponse<NameserverSet>> {
    return fetchAPI(`/nameserver-sets${qs({ limit: API_MAX_LIMIT, ...params })}`);
  },
};

export type IPAddress = components['schemas']['IPAddress'];
export type IPAddressState = components['schemas']['IPAddressState'];
export type IPAddressType = components['schemas']['IPAddressType'];
export type IPAddressRole = components['schemas']['IPAddressRole'];
export type IPAddressOrigin = components['schemas']['IPAddressOrigin'];
export type IPAddressCreate = components['schemas']['CreateIPAddressRequest'];
export type IPAddressUpdate = components['schemas']['UpdateIPAddressRequest'];
/** IPAMChange is one row of the IPAM change history (IPM-08). */
export type IPAMChange = components['schemas']['IPAMChange'];
export type NetworkInterface = components['schemas']['NetworkInterface'];
/** CIAddress is an address of a CI with interface name and subnet CIDR (IPM-13). */
export type CIAddress = components['schemas']['CIAddress'];
/** IPAM_HISTORY_PAGE is the page size of the history list of an address or subnet. */
export const IPAM_HISTORY_PAGE = 25;
export type IPAllocate = components['schemas']['IPAllocateRequest'];
export type SubnetAllocate = components['schemas']['SubnetAllocateRequest'];
export type IPReservationCreate = components['schemas']['IPReservationCreateRequest'];
export type SubnetCreate = components['schemas']['CreateSubnetRequest'];
export type SubnetUpdate = components['schemas']['UpdateSubnetRequest'];
export type VLAN = components['schemas']['VLAN'];
/** IPAMVRF is a VRF with route distinguisher, route targets and sections (IPM-07). */
export type IPAMVRF = components['schemas']['VRF'];
export type NameserverSet = components['schemas']['NameserverSet'];
export type NameserverSetCreate = components['schemas']['CreateNameserverSetRequest'];
export type NameserverSetUpdate = components['schemas']['UpdateNameserverSetRequest'];
export type VLANCreate = components['schemas']['CreateVLANRequest'];
export type VLANUpdate = components['schemas']['UpdateVLANRequest'];
export type VLANDomain = components['schemas']['VLANDomain'];
export type VLANDomainCreate = components['schemas']['CreateVLANDomainRequest'];
export type VLANDomainUpdate = components['schemas']['UpdateVLANDomainRequest'];
export type VRFCreate = components['schemas']['CreateVRFRequest'];
export type VRFUpdate = components['schemas']['UpdateVRFRequest'];

/** IP_ADDRESS_STATES are the states of an address (IPM-04), labels ipam.states.*. */
export const IP_ADDRESS_STATES = [
  'active',
  'offline',
  'unknown',
] as const satisfies readonly IPAddressState[];
/** IP_ADDRESS_TYPES are the types of an address (NET-03), labels ipam.types.*. */
export const IP_ADDRESS_TYPES = [
  'discovered',
  'reserved',
  'static',
  'dhcp',
] as const satisfies readonly IPAddressType[];
/** IP_ADDRESS_ROLES are the roles at a device (IPM-13), labels ipam.roles.*. */
export const IP_ADDRESS_ROLES = [
  'primary',
  'secondary',
  'vip',
  'anycast',
  'loopback',
  'management',
] as const satisfies readonly IPAddressRole[];
/** IP_ADDRESS_ORIGINS are the origins of an address row (IPM-14), labels ipam.origins.*. */
export const IP_ADDRESS_ORIGINS = [
  'manual',
  'discovery',
  'scan',
  'import',
] as const satisfies readonly IPAddressOrigin[];
/** IPAM_CHANGE_SOURCES are the sources of a history row (IPM-08), labels ipam.history.sources.*. */
export const IPAM_CHANGE_SOURCES = [
  'manual',
  'discovery',
  'scan',
  'import',
  'allocation',
] as const satisfies readonly IPAMChange['source'][];

/** PROBLEM_SUBNET_EXHAUSTED is the problem type of a full subnet (IPM-03). */
export const PROBLEM_SUBNET_EXHAUSTED = 'urn:reticora:problem:subnet-exhausted';

// ─── Subnet scan and PTR check (IPM-11, IPM-12, WP-1065) ────────────────────

export type SubnetScan = components['schemas']['SubnetScan'];
export type SubnetScanUpdate = components['schemas']['SubnetScanUpdate'];
export type CollectorGroup = components['schemas']['CollectorGroup'];
export type PTRMismatch = components['schemas']['PTRMismatch'];
export type IPAMPTRMode = components['schemas']['IPAMPTRMode'];
export type PTRStatus = PTRMismatch['ptr_status'];

/** PTR_MODES are the DNS reverse check modes of a section (IPM-12), labels ipam.ptr.modes.*. */
export const PTR_MODES = ['off', 'report', 'adopt'] as const satisfies readonly IPAMPTRMode[];
/** PTR_STATUSES are the kinds of a PTR deviation (IPM-12), labels ipam.ptr.statuses.*. */
export const PTR_STATUSES = ['mismatch', 'missing'] as const satisfies readonly PTRStatus[];
/** SCAN_TARGET_STATES are the target counters of a scan run, labels ipam.scan.targetStates.*. */
export const SCAN_TARGET_STATES = [
  'active',
  'offline',
  'unknown',
  'none',
] as const satisfies readonly (keyof SubnetScan['target_states'])[];

/** The scan of a subnet, its collector groups and the PTR deviations (Konvention 27). */
export const ipamScanApi = {
  getScan(subnetId: string): Promise<SubnetScan> {
    return fetchAPI(`/subnets/${subnetId}/scan`);
  },
  updateScan(subnetId: string, data: SubnetScanUpdate): Promise<SubnetScan> {
    return fetchAPI(`/subnets/${subnetId}/scan`, { method: 'PATCH', body: JSON.stringify(data) });
  },
  /** Every collector group in the caller's scope (monitoring:read, WP-947). */
  collectorGroups(): Promise<CollectorGroup[]> {
    return fetchAllPages<CollectorGroup>('/collector-groups');
  },
  /** The hosts the scan found (origin scan, type discovered) of a subnet, one page. */
  newHosts(subnetId: string): Promise<PaginatedResponse<IPAddress>> {
    return fetchAPI(
      `/ips${qs({ subnet_id: subnetId, origin: 'scan', type: 'discovered', limit: API_MAX_LIMIT })}`,
    );
  },
  /** One page of the PTR deviations (cursor pages, API-02). */
  ptrMismatches(params: {
    section_id?: string;
    subnet_id?: string;
    status?: PTRStatus;
    cursor?: string;
    limit?: number;
  }): Promise<PaginatedResponse<PTRMismatch>> {
    return fetchAPI(`/ipam/ptr-mismatches${qs({ limit: 50, ...params })}`);
  },
};

// ─── Credentials at the CI (CRD-01, CRD-04, CRD-06, CRD-07, WP-923) ─────────

export type CredentialBinding = components['schemas']['CredentialBinding'];
export type CredentialBindingCreate = components['schemas']['CredentialBindingCreate'];
export type CredentialBindingUpdate = components['schemas']['CredentialBindingUpdate'];
/** One entry of PUT /cis/{id}/credentials (replaces every CI binding). */
export type CICredentialBindingEntry =
  components['schemas']['CICredentialBindingsReplace']['bindings'][number];
export type EffectiveCredential = components['schemas']['EffectiveCredential'];
export type EffectiveCredentialSet = components['schemas']['EffectiveCredentialSet'];
export type CredentialOrigin = components['schemas']['CredentialOrigin'];
export type CredentialUsage = components['schemas']['CredentialUsage'];
export type CredentialNamespace = CredentialBinding['namespace'];
export type CredentialErrorClass = NonNullable<CredentialUsage['last_error_class']>;
export type VaultConnection = components['schemas']['VaultConnection'];
export type CredentialVaultRef = components['schemas']['CredentialVaultRef'];
type MonitoringGroupRef = components['schemas']['MonitoringGroup'];

/** CREDENTIAL_ORIGINS are the levels of CRD-04, labels credentials.binding.origins.*. */
export const CREDENTIAL_ORIGINS = [
  'probe',
  'ci',
  'device_group',
  'subnet',
  'scope',
  'client',
  'org',
] as const satisfies readonly CredentialOrigin[];

/** CREDENTIAL_ERROR_CLASSES are the error classes of CRD-06, labels credentials.binding.errorClasses.*. */
export const CREDENTIAL_ERROR_CLASSES = [
  'auth_failed',
  'timeout',
  'unreachable',
  'tls',
  'vault_unreachable',
  'vault_denied',
  'vault_not_found',
  'field_missing',
] as const satisfies readonly CredentialErrorClass[];

/** The bindings of credentials to targets (/credential-bindings, WP-890). */
export const credentialBindingApi = {
  list(
    params: {
      credential_id?: string;
      target_kind?: CredentialBinding['target_kind'];
      target_id?: string;
      limit?: number;
      cursor?: string;
    } = {},
  ): Promise<PaginatedResponse<CredentialBinding>> {
    return fetchAPI(`/credential-bindings${qs({ limit: API_MAX_LIMIT, ...params })}`);
  },
  get(id: string): Promise<CredentialBinding> {
    return fetchAPI(`/credential-bindings/${encodeURIComponent(id)}`);
  },
  create(data: CredentialBindingCreate): Promise<CredentialBinding> {
    return fetchAPI('/credential-bindings', { method: 'POST', body: JSON.stringify(data) });
  },
  update(id: string, data: CredentialBindingUpdate): Promise<CredentialBinding> {
    return fetchAPI(`/credential-bindings/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
  },
  remove(id: string): Promise<void> {
    return fetchAPI(`/credential-bindings/${encodeURIComponent(id)}`, { method: 'DELETE' });
  },
};

/** The bindings with target ci of a CI (GET/PUT /cis/{id}/credentials; writing needs credential:bind). */
export const ciCredentials = {
  list(ciId: string): Promise<{ data: CredentialBinding[] }> {
    return fetchAPI(`/cis/${encodeURIComponent(ciId)}/credentials`);
  },
  /** replace sets every binding of the CI at once; a refused entry keeps the former ones. */
  replace(
    ciId: string,
    bindings: CICredentialBindingEntry[],
  ): Promise<{ data: CredentialBinding[] }> {
    return fetchAPI(`/cis/${encodeURIComponent(ciId)}/credentials`, {
      method: 'PUT',
      body: JSON.stringify({ bindings }),
    });
  },
};

/** The effective credential per namespace with origin and overridden candidates (CRD-04, WP-891). */
export function ciEffectiveCredentials(ciId: string): Promise<EffectiveCredentialSet> {
  return fetchAPI(`/cis/${encodeURIComponent(ciId)}/credentials/effective`);
}

/** The usage of a credential at one CI per namespace (CRD-06, WP-898). */
export function credentialUsage(
  credentialId: string,
  ciId: string,
): Promise<PaginatedResponse<CredentialUsage>> {
  return fetchAPI(
    `/credentials/${encodeURIComponent(credentialId)}/usage${qs({ ci_id: ciId, limit: API_MAX_LIMIT })}`,
  );
}

/** The vault connections a vault reference can use (credential:read, WP-893). */
export const vaultConnectionApi = {
  list(): Promise<VaultConnection[]> {
    return fetchAllPages<VaultConnection>('/vault-connections');
  },
};

/**
 * credentialOriginTarget names the target of a binding the resolution
 * reports: the group name of a device_group, the CIDR of a subnet.
 */
export async function credentialOriginTarget(bindingId: string): Promise<string | undefined> {
  const binding = await credentialBindingApi.get(bindingId);
  if (binding.target_kind === 'device_group') {
    const group = await fetchAPI<MonitoringGroupRef>(
      `/monitoring/groups/${encodeURIComponent(binding.target_id)}`,
    );
    return group.name;
  }
  if (binding.target_kind === 'subnet') {
    const subnet = await fetchAPI<{ cidr: string }>(
      `/subnets/${encodeURIComponent(binding.target_id)}`,
    );
    return subnet.cidr;
  }
  return undefined;
}
