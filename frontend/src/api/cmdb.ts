/**
 * cmdb — API surface for the enterprise CMDB + asset/inventory extension
 * (spec §20). Kept in a dedicated module so the extension is additive to the
 * existing client.ts. All calls flow through the same fetchAPI (auth, RFC
 * 7807 errors, idempotency).
 */
import {
  ApiError,
  buildQuery as qs,
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
  listAll(params: { include_inactive?: boolean; category?: string; search?: string } = {}) {
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
  consumable_id?: string;
  quantity_item_id?: string;
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
      movement_type: 'receipt' | 'retrieval' | 'correction' | 'transfer';
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
  /** The stock of a consumable per bin (GET /consumables/{id}/stock, WP-283). */
  stockLevels(consumableId: string) {
    return fetchAPI<ConsumableStockList>(`/consumables/${consumableId}/stock${qs({ limit: 200 })}`);
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

/**
 * A row of the org-wide projection `GET /inventory/availability`; a
 * consumable sums its stock rows per bin (WP-284). The path stays an alias
 * (E-38); the successor per bin is `GET /consumables/{id}/availability`.
 */
export interface Availability {
  item_kind: 'asset' | 'consumable';
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

// ─── Composition (§5, §14) ───────────────────────────────────────────────────

/**
 * The parent link of a child CI in the format of the former composition
 * handler, as the deprecated `GET /cis/{id}/parent` still answers it from
 * `ci.parent_ci_id` (CIParentLegacy, WP-742): `id` and `child_ci_id` are the
 * child CI. `role` is no longer sent; it stays optional for the detail card.
 */
export interface Composition {
  id: string;
  organization_id: string;
  parent_asset_id: string;
  child_ci_id: string;
  role?: string;
  configuration_only: boolean;
  independently_serialized: boolean;
  independently_assignable: boolean;
  independently_locatable: boolean;
  independently_lifecycle_managed: boolean;
  created_at: string;
  updated_at: string;
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
  inherited_fields: string[];
}

export const compositionApi = {
  /** Resolves the parent asset of a CI. Rejects with a 404 ApiError when the CI is standalone. */
  parentOfCI(ciId: string) {
    return fetchAPI<CIParent>(`/cis/${ciId}/parent`);
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
