/**
 * GLO-10/UI-18: enumeration values are English keys and every value the UI
 * shows has a label in both catalogs; the pages render labels from the
 * catalog, not as literals.
 */
import { describe, expect, it } from 'vitest';
import deDE from './de-DE.json';
import enUS from './en-US.json';
import { DASHBOARD_ROLES, parseDashboardRole } from '../stores/dashboard';
import { STOCKTAKE_SCOPES, STOCKTAKE_STATUSES } from '../pages/StocktakeListPage';
import { SCAN_METHODS, SCAN_RESULTS } from '../pages/StocktakeDetailPage';
import { LABEL_KINDS, LABEL_TEMPLATES } from '../components/flows/LabelPrintDialog';
import { FIELD_TYPES } from '../pages/CITypeAdminPage';
import { TEMPLATE_VERSION_STATUSES } from '../pages/TemplatesPage';
import { DIFF_CHANGES } from '../components/metamodel/MetamodelDiff';
import { DRAFT_ITEM_KINDS, DRAFT_OPS } from '../stores/metamodelDraft';
import { DRAFT_STATUSES } from '../pages/MetamodelDraftsPage';
import { JOB_STATUSES } from '../hooks/useJobs';
import { DISCOVERY_PLUGINS } from '../pages/DiscoveryPage';
import { ASSET_CATEGORIES, ASSET_LIFECYCLE_STATES } from '../pages/AssetListPage';
import { ASSIGNMENT_STATUSES } from '../pages/AssignmentListPage';
import { MOVEMENT_TYPES } from '../pages/InventoryPage';
import { DOCUMENT_CATEGORIES } from '../pages/DocumentListPage';
import { LINK_ENTITY_TYPES } from '../pages/DocumentDetailPage';
import { TICKET_CATEGORIES, TICKET_PRIORITIES, TICKET_STATUSES } from '../pages/TicketListPage';
import { TICKET_TRANSITIONS } from '../pages/TicketDetailPage';
import { NODE_KINDS, SCAN_STATUSES, relationshipChangeKinds } from '../api/client';
import { CONSUMABLE_BOOKING_TYPES } from '../api/cmdb';
import {
  CARDINALITIES,
  IMPACT_DIRECTIONS,
  RELATIONSHIP_ATTRIBUTE_TYPES,
  RELATIONSHIP_CATEGORIES,
} from '../pages/RelationshipTypesPage';
import {
  CONDITION_COLUMNS,
  CONDITION_JUNCTIONS,
  CONDITION_MODE,
  EXPRESSION_MODE,
  EXPR_ISSUES,
  EXPR_NODE_KINDS,
  FILTER_ENTITY_KINDS,
  FILTER_FIELDS,
  FILTER_MODE,
  FILTER_NODE_KINDS,
  GROUP_JUNCTIONS,
  LITERAL_TYPES,
  NODE_ISSUES,
  RANGE_OF,
  RELATED_DIRECTIONS,
  RULE_KINDS,
  VALIDATION_ISSUES,
  VALIDATION_NODE_KINDS,
} from '../rules/builderModes';
import { filterOps } from '../rules/filter';
import { compareOps } from '../rules/rules';
import { EXPR_ASSET_FIELDS, EXPR_OPS, EXPR_RESULT_TYPES } from '../rules/expr';
import { RULE_TARGETS } from '../pages/RulesPage';
import { IMPORT_STEPS } from '../pages/ImportPage';
import {
  IMPORT_ENTITIES,
  IMPORT_FILE_FORMATS,
  IMPORT_MODES,
  IMPORT_TARGETS,
  IMPORT_TRANSFORMS,
} from '../components/import/MappingEditor';
import { TYPE_CATEGORIES } from '../components/create/TypeTiles';
import { FORM_GROUPS } from '../components/create/GuidedCreate';
import spec from '../api/generated/schema.d.ts?raw';

type Catalog = Record<string, unknown>;

function lookup(catalog: Catalog, key: string): unknown {
  return key
    .split('.')
    .reduce<unknown>((node, part) => (node as Catalog | undefined)?.[part], catalog);
}

const pageSources = import.meta.glob('../pages/*.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

// WP-314: the components of the metamodel administration, without tests.
// WP-318: the pickers and the flow dialogs.
const flowSources = import.meta.glob(
  [
    '../components/pickers/**/*.tsx',
    '../components/flows/**/*.tsx',
    '!../components/pickers/**/*.test.tsx',
    '!../components/flows/**/*.test.tsx',
  ],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;

// WP-315: the components of the click builder, without tests.
const ruleSources = import.meta.glob(
  ['../components/rules/**/*.tsx', '!../components/rules/**/*.test.tsx'],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;

const metamodelSources = import.meta.glob(
  ['../components/metamodel/**/*.tsx', '!../components/metamodel/**/*.test.tsx'],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;

// WP-323: the edge dialog of the CI detail page.
const cardSources = import.meta.glob('../components/cmdb/CIRelationshipsCard.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

// WP-321: the upload component and the document components.
const documentSources = import.meta.glob(
  [
    '../components/upload/**/*.tsx',
    '../components/documents/**/*.tsx',
    '!../components/upload/**/*.test.tsx',
    '!../components/documents/**/*.test.tsx',
  ],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;

// WP-322: the components of the import assistant, without tests.
const importSources = import.meta.glob(
  ['../components/import/**/*.tsx', '!../components/import/**/*.test.tsx'],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;

// WP-317: the guided creation, without tests.
const createSources = import.meta.glob(
  ['../components/create/**/*.tsx', '!../components/create/**/*.test.tsx'],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;

const COLLECTOR_STATUSES = ['pending', 'active', 'offline', 'disabled', 'paused'];

// specEnum reads the string enum of a schema (or of one of its properties)
// from the types generated from api/openapi.yaml (kept in sync by
// generate:api:check; Vite does not serve files outside frontend/).
function specEnum(schema: string, property?: string): string[] {
  const lines = spec.split('\n');
  const start = lines.findIndex((l) => new RegExp(`^ {4}${schema}:`).test(l));
  if (start < 0) throw new Error(`schema ${schema} not in the OpenAPI types`);
  let text = lines[start] ?? '';
  if (property) {
    const end = lines.findIndex((l, i) => i > start && /^ {4}};/.test(l));
    const body = lines.slice(start + 1, end);
    const at = body.findIndex((l) => new RegExp(`^ {6}${property}\\??:`).test(l));
    if (at < 0) throw new Error(`${schema}.${property} not in the OpenAPI types`);
    text = body.slice(at).join('\n');
  } else {
    text = lines.slice(start).join('\n');
  }
  const union = text.slice(text.indexOf(':') + 1, text.indexOf(';'));
  const values = [...union.matchAll(/'([^']*)'/g)].map((m) => m[1] ?? '');
  if (values.length === 0 || union.replace(/'[^']*'/g, '').replace(/[\s|]/g, '') !== '') {
    throw new Error(`${schema}${property ? '.' + property : ''} is not a string enum`);
  }
  return values;
}

// Status and category values the OpenAPI document types as plain string;
// the value lists come from the backend module (CHECK constraint or
// constants) named next to each.
const BACKEND_ENUMS = {
  // The states of the system lifecycle model physical_asset
  // (backend/migrations/000055_cmdb_extensions.up.sql); the asset list
  // shows lifecycle_state instead of the legacy status (E-120, WP-319).
  assetLifecycleStates: [
    'ordered',
    'received',
    'in_stock',
    'reserved',
    'preparing',
    'deployed',
    'repair',
    'retired',
    'disposed',
  ],
  // backend/migrations/000009_phase2_assets.up.sql asset_category_check.
  assetCategories: ['hardware', 'software', 'license', 'cloud_resource', 'accessory', 'other'],
  // backend/migrations/000257_assignment_projection.up.sql: the status of
  // the view assignment_projection over the ledger (WP-747); the table
  // assignment with its status check is gone (000266), cancelled no longer
  // occurs (WP-318).
  assignmentStatuses: ['active', 'returned', 'transferred', 'overdue'],
  // backend/migrations/000011_phase2_documents.up.sql document_category_check.
  documentCategories: [
    'general',
    'contract',
    'invoice',
    'manual',
    'diagram',
    'certificate',
    'policy',
    'other',
  ],
  // backend/migrations/000214_ticket_states_number.up.sql ticket_status_check.
  ticketStatuses: ['new', 'open', 'in_progress', 'waiting', 'resolved', 'closed'],
  // backend/migrations/000013_phase2_tickets.up.sql ticket_category_check.
  ticketCategories: ['incident', 'request', 'problem', 'change', 'task'],
  // backend/migrations/000027_workflows_forms.up.sql workflow_step_status_check
  // (internal/workflow/model.go Status* constants).
  workflowStepStatuses: [
    'pending',
    'running',
    'waiting_approval',
    'succeeded',
    'failed',
    'cancelled',
  ],
  // backend/internal/user/pg_repository.go (CASE WHEN is_active THEN 'active'
  // ELSE 'inactive') and internal/user/repository.go (anonymized).
  userStatuses: ['active', 'inactive', 'anonymized'],
  // backend/migrations/000029_iga.up.sql: iga_connector, iga_provisioning_task
  // (internal/iga/model.go TaskStatus* constants), iga_access_request,
  // iga_access_review status checks and iga_drift_finding severity check.
  igaConnectorStatuses: ['active', 'inactive', 'error'],
  igaTaskStatuses: ['pending', 'running', 'succeeded', 'failed', 'cancelled'],
  igaRequestStatuses: ['pending', 'approved', 'rejected', 'cancelled'],
  igaReviewStatuses: ['draft', 'active', 'completed', 'cancelled'],
  igaSeverities: ['low', 'medium', 'high', 'critical'],
};

// WP-525: the enums the inventory, ticket, order and room plan pages show.
const pageEnumerations: Array<[string, readonly string[]]> = [
  ['lifecycle.states', BACKEND_ENUMS.assetLifecycleStates],
  ['asset.categories', BACKEND_ENUMS.assetCategories],
  ['assignment.statuses', BACKEND_ENUMS.assignmentStatuses],
  // assignment_type of the projection (000257): assignment or transfer.
  ['assignment.types', ['assignment', 'transfer']],
  ['document.categories', BACKEND_ENUMS.documentCategories],
  ['ticket.statuses', BACKEND_ENUMS.ticketStatuses],
  ['ticket.categories', BACKEND_ENUMS.ticketCategories],
  // WP-814: the ticket priorities (WP-290) and the targets of a ticket link (WP-764).
  ['ticket.priorities', TICKET_PRIORITIES],
  ['ticket.priorities', specEnum('TicketPriority')],
  ['ticket.links.types', specEnum('TicketLink', 'entity_type')],
  ['orders.statuses', specEnum('Order', 'status')],
  ['keys.statuses', specEnum('KeyItem', 'status')],
  ['ci.statuses', specEnum('CIStatus')],
  ['workflows.statuses', specEnum('WorkflowRun', 'status')],
  ['workflows.statuses', BACKEND_ENUMS.workflowStepStatuses],
  ['userMgmt.statuses', BACKEND_ENUMS.userStatuses],
  // WP-527: agent, maintenance, findings, desk, compliance, security and IGA
  // pages.
  ['agents.statuses', specEnum('Agent', 'status')],
  ['maintenance.statuses', specEnum('MaintenanceWindow', 'status')],
  ['findings.severities', specEnum('PatchFinding', 'severity')],
  ['desks.statuses', specEnum('Desk', 'status')],
  ['compliance.resultStatuses', specEnum('ComplianceResult', 'status')],
  ['compliance.severities', specEnum('ComplianceRule', 'severity')],
  ['compliance.severities', specEnum('SecurityFinding', 'severity')],
  ['iga.connectorStatuses', BACKEND_ENUMS.igaConnectorStatuses],
  ['iga.taskStatuses', BACKEND_ENUMS.igaTaskStatuses],
  ['iga.requestStatuses', BACKEND_ENUMS.igaRequestStatuses],
  ['iga.reviewStatuses', BACKEND_ENUMS.igaReviewStatuses],
  ['iga.severities', BACKEND_ENUMS.igaSeverities],
];

const enumerations: Array<[string, readonly string[]]> = [
  // WP-818: the kinds of a topology node (IMP-11) and the relationship
  // categories of summary.by_category in the impact panel.
  ['topology.nodeKinds', NODE_KINDS],
  ['topology.nodeKinds', specEnum('TopologyNode', 'node_kind')],
  ['impact.categories', specEnum('RelationshipTypeCategory')],
  ['dashboard.roles', DASHBOARD_ROLES],
  ['stocktake.statuses', STOCKTAKE_STATUSES],
  ['stocktake.scopes', STOCKTAKE_SCOPES],
  // WP-813: the scan results and methods of the stocktake (StockScan of
  // WP-287) and the templates and kinds of the labels (WP-763).
  ['stocktake.results', SCAN_RESULTS],
  ['stocktake.results', specEnum('StockScan', 'scan_result')],
  ['stocktake.scanMethods', SCAN_METHODS],
  ['stocktake.scanMethods', specEnum('StockScan', 'scan_method')],
  ['stocktake.statuses', specEnum('Stocktake', 'status')],
  ['labels.templates', LABEL_TEMPLATES],
  ['labels.templates', specEnum('LabelBatchRequest', 'template')],
  ['labels.kinds', LABEL_KINDS],
  ['labels.kinds', specEnum('LabelBatchRequest', 'kind')],
  ['citypes.dataTypes', FIELD_TYPES],
  // WP-314: template versions, the changes of MetamodelDiff, the job states
  // and the index state none of WP-313.
  ['templates.versionStatuses', TEMPLATE_VERSION_STATUSES],
  ['templates.versionStatuses', specEnum('TemplateVersion', 'status')],
  ['templates.diff.changes', DIFF_CHANGES],
  ['jobs.statuses', JOB_STATUSES],
  ['citypes.indexStatus', ['none']],
  // WP-798: kinds and operations of metamodel draft items and the draft states.
  ['metamodelDrafts.itemKinds', DRAFT_ITEM_KINDS],
  ['metamodelDrafts.itemKinds', specEnum('MetamodelDraftItem', 'kind')],
  ['metamodelDrafts.ops', DRAFT_OPS],
  ['metamodelDrafts.ops', specEnum('MetamodelDraftItem', 'op')],
  ['metamodelDrafts.statuses', DRAFT_STATUSES],
  ['metamodelDrafts.statuses', specEnum('MetamodelDraft', 'status')],
  // WP-799: the job kind of the draft test (nested, JobCenterPage) and the
  // reasons of stale items of a refused publication (metamodel.StaleItem).
  ['jobs.kinds.metamodel', ['test']],
  ['metamodelDrafts.page.staleReasons', ['changed', 'missing']],
  // WP-323: category, impact direction and cardinality of a relationship
  // type, the kinds of a change of an edge and the parts of a history row.
  ['relationshipTypes.categories', RELATIONSHIP_CATEGORIES],
  ['relationshipTypes.categories', specEnum('RelationshipTypeCategory')],
  ['relationshipTypes.impactDirections', IMPACT_DIRECTIONS],
  ['relationshipTypes.impactDirections', specEnum('RelationshipImpactDirection')],
  ['relationshipTypes.cardinalities', CARDINALITIES],
  ['relationshipTypes.cardinalities', specEnum('RelationshipTypeCardinality')],
  ['relationship.changeKinds', relationshipChangeKinds],
  ['relationship.changeKinds', specEnum('RelationshipChange', 'change_kind')],
  ['relationship.changeSources', specEnum('RelationshipChange', 'source')],
  ['relationship.actorTypes', specEnum('RelationshipChange', 'actor_type')],
  // WP-315: the operators, junctions, node kinds, directions and issues of
  // the click builder (filter mode) and the fields of its whitelist.
  ['rules.ops', FILTER_MODE.ops],
  ['rules.ops', filterOps],
  ['rules.ops', specEnum('FilterExpr', 'op')],
  ['rules.junctions', GROUP_JUNCTIONS],
  ['rules.junctionHints', GROUP_JUNCTIONS],
  ['rules.nodeKinds', FILTER_NODE_KINDS],
  ['rules.directions', RELATED_DIRECTIONS],
  ['rules.directions', specEnum('FilterRelated', 'direction')],
  ['rules.issues', NODE_ISSUES],
  ['rules.entityKinds', FILTER_ENTITY_KINDS],
  ['rules.entityKinds', specEnum('SavedView', 'entity_kind')],
  // WP-801: the rule kinds and targets of the rule catalog, the operators
  // and junctions of the condition mode, the node kinds, range measures and
  // issues of the validation mode and the typed CI columns of a condition.
  ['ruleCatalog.kinds', RULE_KINDS],
  ['ruleCatalog.kinds', specEnum('MetamodelRuleKind')],
  ['ruleCatalog.targets', RULE_TARGETS],
  ['ruleCatalog.filters', RULE_TARGETS],
  ['rules.conditionOps', CONDITION_MODE.ops],
  ['rules.conditionOps', compareOps],
  ['rules.conditionJunctions', CONDITION_JUNCTIONS],
  ['rules.conditionJunctionHints', CONDITION_JUNCTIONS],
  ['rules.validationKinds', VALIDATION_NODE_KINDS],
  ['rules.validation.add', VALIDATION_NODE_KINDS],
  ['rules.rangeOf', RANGE_OF],
  ['rules.issues', VALIDATION_ISSUES],
  // WP-802: the operators of the expression mode ($defs/expr), the kinds
  // of a literal, the result types, the issues and the asset fields an
  // expression reads.
  ['rules.exprOps', EXPRESSION_MODE.nodeKinds],
  ['rules.exprOps', EXPR_NODE_KINDS],
  ['rules.exprOps', EXPR_OPS],
  ['rules.literalTypes', LITERAL_TYPES],
  ['rules.resultTypes', EXPR_RESULT_TYPES],
  ['rules.issues', EXPR_ISSUES],
  ['savedviews.fields', Object.keys(EXPR_ASSET_FIELDS)],
  ['savedviews.fields', CONDITION_COLUMNS],
  ['savedviews.fields', Object.keys(FILTER_FIELDS.ci)],
  ['savedviews.fields', Object.keys(FILTER_FIELDS.asset)],
  // WP-321: the scan states of a document version (WP-733) and the link targets (WP-267).
  ['document.scanStatuses', SCAN_STATUSES],
  ['document.scanStatuses', specEnum('DocumentVersion', 'scan_status')],
  ['document.linkTypes', LINK_ENTITY_TYPES],
  ['discovery.collectorStatus', COLLECTOR_STATUSES],
  // WP-806: the movement types of the ledger (STK-04, WP-274).
  ['movement.types', MOVEMENT_TYPES],
  ['movement.types', specEnum('MovementType')],
  // WP-320: the quantity bookings of a consumable (WP-756).
  ['consumables.movementTypes', CONSUMABLE_BOOKING_TYPES],
  ['discovery.scopes.plugin', DISCOVERY_PLUGINS],
  // WP-322: the entities, modes and transformations of an import profile,
  // the formats and steps of the assistant, the job kind import.run (nested
  // like metamodel.test) and the core targets of each entity, labelled as
  // fields like the filter whitelist.
  ['import.entities', IMPORT_ENTITIES],
  ['import.entities', specEnum('ImportProfile', 'entity')],
  ['import.modes', IMPORT_MODES],
  ['import.modes', specEnum('ImportProfile', 'mode')],
  ['import.transforms', IMPORT_TRANSFORMS],
  ['import.transforms', specEnum('ImportTransform', 'op')],
  ['import.formats', IMPORT_FILE_FORMATS],
  ['import.formats', specEnum('ImportPreview', 'format')],
  ['import.steps', IMPORT_STEPS],
  ['jobs.kinds.import', ['run']],
  // WP-317: the tile categories (citype.Templates() and other) and the
  // field groups of the guided creation.
  ['create.categories', TYPE_CATEGORIES],
  ['create.groups', FORM_GROUPS],
  ...Object.values(IMPORT_TARGETS).map((targets): [string, readonly string[]] => [
    'savedviews.fields',
    targets,
  ]),
  ...pageEnumerations,
];

describe('i18n catalog', () => {
  it.each(enumerations)('labels every value of %s in both languages', (prefix, values) => {
    for (const value of values) {
      for (const [lang, catalog] of [
        ['de-DE', deDE],
        ['en-US', enUS],
      ] as const) {
        const label = lookup(catalog, `${prefix}.${value}`);
        expect(typeof label === 'string' && label.length > 0, `${lang} ${prefix}.${value}`).toBe(
          true,
        );
      }
    }
  });

  it('keeps enumeration values as lower-case English keys', () => {
    for (const [, values] of enumerations) {
      for (const value of values) {
        expect(value).toMatch(/^[a-z][a-z0-9_]*$/);
      }
    }
    expect(DASHBOARD_ROLES).toEqual(['technician', 'procurement', 'management']);
  });

  it('reads the status enums from the OpenAPI types', () => {
    expect(specEnum('Order', 'status')).toEqual([
      'draft',
      'submitted',
      'approved',
      'rejected',
      'ordered',
      'partially_received',
      'received',
      'closed',
      'cancelled',
    ]);
    expect(specEnum('KeyItem', 'status')).toEqual(['available', 'issued', 'lost', 'retired']);
    expect(specEnum('CIStatus')).toEqual([
      'active',
      'inactive',
      'maintenance',
      'decommissioned',
      'unknown',
    ]);
    expect(specEnum('WorkflowRun', 'status')).toEqual(BACKEND_ENUMS.workflowStepStatuses);
    expect(specEnum('Agent', 'status')).toEqual(['online', 'offline', 'disabled']);
    expect(specEnum('MaintenanceWindow', 'status')).toEqual([
      'scheduled',
      'in_progress',
      'completed',
      'cancelled',
    ]);
    expect(specEnum('PatchFinding', 'severity')).toEqual(['low', 'medium', 'high', 'critical']);
    expect(specEnum('Desk', 'status')).toEqual(['available', 'occupied', 'maintenance']);
    expect(specEnum('ComplianceResult', 'status')).toEqual(['pass', 'fail', 'not_applicable']);
    // Plain strings in the specification: their values come from the backend.
    for (const [schema, property] of [
      ['Asset', 'status'],
      ['Asset', 'category'],
      ['Assignment', 'status'],
      ['Document', 'category'],
      ['Ticket', 'status'],
      ['Ticket', 'category'],
      ['WorkflowStep', 'status'],
      ['User', 'status'],
      // Free tenant text (compliance_rule.category has no CHECK constraint):
      // shown through t('compliance.categories.<value>', value), labelled
      // where the catalog knows the value, otherwise as stored.
      ['ComplianceRule', 'category'],
      ['SecurityFinding', 'category'],
    ] as const) {
      expect(() => specEnum(schema, property), `${schema}.${property}`).toThrow(
        /not a string enum/,
      );
    }
  });

  it('offers exactly the backend values in the page filters', () => {
    expect([...ASSET_LIFECYCLE_STATES]).toEqual(BACKEND_ENUMS.assetLifecycleStates);
    expect([...ASSET_CATEGORIES]).toEqual(BACKEND_ENUMS.assetCategories);
    expect([...ASSIGNMENT_STATUSES]).toEqual(BACKEND_ENUMS.assignmentStatuses);
    expect([...DOCUMENT_CATEGORIES]).toEqual(BACKEND_ENUMS.documentCategories);
    expect([...TICKET_STATUSES]).toEqual(BACKEND_ENUMS.ticketStatuses);
    expect([...TICKET_CATEGORIES]).toEqual(BACKEND_ENUMS.ticketCategories);
    expect([...TICKET_PRIORITIES]).toEqual(specEnum('TicketPriority'));
  });

  it('labels the ticket priorities and mirrors the state machine (WP-814)', () => {
    expect(lookup(deDE, 'ticket.priorities.critical')).toBe('Kritisch');
    expect(lookup(enUS, 'ticket.priorities.critical')).toBe('Critical');
    // backend/internal/ticket/states.go: every state has its next states,
    // each of them a state of the machine.
    expect(Object.keys(TICKET_TRANSITIONS)).toEqual(BACKEND_ENUMS.ticketStatuses);
    for (const next of Object.values(TICKET_TRANSITIONS)) {
      for (const status of next) expect(BACKEND_ENUMS.ticketStatuses).toContain(status);
    }
  });

  it('lists the pickers and the flow dialogs (WP-318)', () => {
    expect(Object.keys(flowSources)).toEqual(
      expect.arrayContaining([
        '../components/pickers/ObjectPicker.tsx',
        '../components/pickers/LocationPicker.tsx',
        '../components/pickers/UserTeamPicker.tsx',
        '../components/flows/AssignDialogs.tsx',
        '../components/flows/StockFlowDialogs.tsx',
      ]),
    );
    expect(Object.keys(flowSources).some((f) => f.endsWith('.test.tsx'))).toBe(false);
  });

  it('lists the components of the guided creation (WP-317)', () => {
    expect(Object.keys(createSources)).toEqual(
      expect.arrayContaining([
        '../components/create/GuidedCreate.tsx',
        '../components/create/TypeTiles.tsx',
      ]),
    );
    expect(Object.keys(createSources).some((f) => f.endsWith('.test.tsx'))).toBe(false);
  });

  it('offers exactly the movement types of the OpenAPI types (WP-806)', () => {
    expect([...MOVEMENT_TYPES]).toEqual(specEnum('MovementType'));
    // The quantity bookings are movement types of the ledger (WP-320).
    expect(specEnum('MovementType')).toEqual(expect.arrayContaining([...CONSUMABLE_BOOKING_TYPES]));
    // The former key inventory.movementTypes is gone: one label per type.
    expect(lookup(deDE, 'inventory.movementTypes')).toBeUndefined();
    expect(lookup(enUS, 'inventory.movementTypes')).toBeUndefined();
  });

  it('offers exactly the scan states of the OpenAPI types and lists the document components (WP-321)', () => {
    expect([...SCAN_STATUSES]).toEqual(specEnum('DocumentVersion', 'scan_status'));
    expect(Object.keys(documentSources)).toEqual(
      expect.arrayContaining([
        '../components/upload/FileUpload.tsx',
        '../components/documents/LinkedDocuments.tsx',
      ]),
    );
    expect(Object.keys(documentSources).some((f) => f.endsWith('.test.tsx'))).toBe(false);
  });

  it('offers exactly the template version states of the OpenAPI types (WP-314)', () => {
    expect([...TEMPLATE_VERSION_STATUSES]).toEqual(specEnum('TemplateVersion', 'status'));
    expect(Object.keys(metamodelSources)).toContain('../components/metamodel/MetamodelDiff.tsx');
    expect(Object.keys(metamodelSources).some((f) => f.endsWith('.test.tsx'))).toBe(false);
  });

  it('offers exactly the draft item kinds, operations and states of the OpenAPI types (WP-798)', () => {
    expect([...DRAFT_ITEM_KINDS]).toEqual(specEnum('MetamodelDraftItem', 'kind'));
    expect([...DRAFT_OPS]).toEqual(specEnum('MetamodelDraftItem', 'op'));
    expect([...DRAFT_STATUSES]).toEqual(specEnum('MetamodelDraft', 'status'));
  });

  it('offers exactly the relationship enums of the OpenAPI types (WP-323)', () => {
    expect([...RELATIONSHIP_CATEGORIES]).toEqual(specEnum('RelationshipTypeCategory'));
    expect([...IMPACT_DIRECTIONS]).toEqual(specEnum('RelationshipImpactDirection'));
    expect([...CARDINALITIES].sort()).toEqual(specEnum('RelationshipTypeCardinality').sort());
    expect([...relationshipChangeKinds]).toEqual(specEnum('RelationshipChange', 'change_kind'));
    // The attribute types are the values of FIELD_TYPES that WP-295 accepts:
    // no json, file or reference type.
    expect([...RELATIONSHIP_ATTRIBUTE_TYPES]).toEqual(
      specEnum('RelationshipAttributeDefinition', 'data_type'),
    );
    expect(FIELD_TYPES).toEqual(expect.arrayContaining([...RELATIONSHIP_ATTRIBUTE_TYPES]));
  });

  it('labels the relationship type page in the navigation (WP-323)', () => {
    expect(lookup(deDE, 'nav.relationship-types')).toBe('Beziehungstypen');
    expect(lookup(enUS, 'nav.relationship-types')).toBe('Relationship types');
  });

  it('labels the rule catalog in the navigation and the palette (WP-801)', () => {
    expect(lookup(deDE, 'nav.rules')).toBe('Regelkatalog');
    expect(lookup(enUS, 'nav.rules')).toBe('Rule catalog');
    expect(lookup(deDE, 'commandPalette.commands.rules')).toBe('Regelkatalog öffnen');
    expect(lookup(enUS, 'commandPalette.commands.rules')).toBe('Open rule catalog');
    expect([...RULE_KINDS]).toEqual(specEnum('MetamodelRuleKind'));
  });

  it('offers exactly the import enums of the OpenAPI types and labels the page (WP-322)', () => {
    expect([...IMPORT_ENTITIES]).toEqual(specEnum('ImportProfile', 'entity'));
    expect([...IMPORT_MODES]).toEqual(specEnum('ImportProfile', 'mode'));
    expect([...IMPORT_TRANSFORMS]).toEqual(specEnum('ImportTransform', 'op'));
    expect([...IMPORT_FILE_FORMATS]).toEqual(specEnum('ImportPreview', 'format'));
    expect(lookup(deDE, 'nav.import')).toBe('Import');
    expect(lookup(enUS, 'nav.import')).toBe('Import');
    expect(lookup(deDE, 'commandPalette.commands.import')).toBe('Import-Assistent öffnen');
    expect(lookup(enUS, 'commandPalette.commands.import')).toBe('Open import assistant');
    expect(lookup(deDE, 'jobs.kinds.import.run')).toBe('Import-Lauf');
    expect(lookup(enUS, 'jobs.kinds.import.run')).toBe('Import run');
    expect(Object.keys(importSources)).toEqual(['../components/import/MappingEditor.tsx']);
    // The row errors of GET /imports/{id}/errors (finding of WP-322): the
    // columns follow the report (Zeile;Feld;Code;Meldung).
    const lines = spec.split('\n');
    const start = lines.findIndex((l) => /^ {4}ImportError:/.test(l));
    const end = lines.findIndex((l, i) => i > start && /^ {4}};/.test(l));
    const props = lines
      .slice(start + 1, end)
      .map((l) => /^ {6}(\w+)\??:/.exec(l)?.[1])
      .filter(Boolean);
    expect(props).toEqual(['line', 'field', 'code', 'message']);
    expect(
      ['line', 'field', 'code', 'message'].map((k) => lookup(deDE, `import.report.errorList.${k}`)),
    ).toEqual(['Zeile', 'Feld', 'Code', 'Meldung']);
    expect(
      ['line', 'field', 'code', 'message'].map((k) => lookup(enUS, `import.report.errorList.${k}`)),
    ).toEqual(['Line', 'Field', 'Code', 'Message']);
    expect(lookup(deDE, 'import.report.errorList.next')).toBe('Weiter');
    expect(lookup(enUS, 'import.report.errorList.next')).toBe('Next');
  });

  it('offers exactly the operators, directions and entity kinds of FilterExpr (WP-315)', () => {
    expect([...FILTER_MODE.ops].sort()).toEqual([...specEnum('FilterExpr', 'op')].sort());
    expect([...RELATED_DIRECTIONS]).toEqual(specEnum('FilterRelated', 'direction'));
    expect([...FILTER_ENTITY_KINDS]).toEqual(specEnum('SavedView', 'entity_kind'));
    expect(Object.keys(ruleSources)).toEqual(
      expect.arrayContaining([
        '../components/rules/RuleBuilder.tsx',
        '../components/rules/AttributePicker.tsx',
        '../components/rules/ValueEditor.tsx',
      ]),
    );
    expect(Object.keys(ruleSources).some((f) => f.endsWith('.test.tsx'))).toBe(false);
  });

  it('offers exactly the operators of $defs/expr in the expression mode (WP-802)', () => {
    const ops = [
      ...specEnum('expr_field', 'op'),
      ...specEnum('expr_literal', 'op'),
      ...specEnum('expr_arithmetic', 'op'),
      ...specEnum('expr_today', 'op'),
      ...specEnum('expr_days_between', 'op'),
      ...specEnum('expr_concat', 'op'),
      ...specEnum('expr_case', 'op'),
      ...specEnum('expr_coalesce', 'op'),
    ];
    expect([...EXPR_OPS].sort()).toEqual(ops.sort());
    expect(lookup(deDE, 'rules.exprOps.days_between')).toBe('Tage zwischen');
    expect(lookup(enUS, 'rules.exprOps.days_between')).toBe('Days between');
    expect(Object.keys(ruleSources)).toContain('../components/rules/ComputedEditor.tsx');
  });

  it('offers exactly the node kinds of the OpenAPI types (WP-818)', () => {
    expect([...NODE_KINDS]).toEqual(specEnum('TopologyNode', 'node_kind'));
  });

  it('labels in_stock as "Auf Lager" in de and "In stock" in en', () => {
    expect(lookup(deDE, 'lifecycle.states.in_stock')).toBe('Auf Lager');
    expect(lookup(enUS, 'lifecycle.states.in_stock')).toBe('In stock');
  });

  it('maps the stored German dashboard roles onto the English values', () => {
    expect(parseDashboardRole('technik')).toBe('technician');
    expect(parseDashboardRole('einkauf')).toBe('procurement');
    expect(parseDashboardRole('management')).toBe('management');
    expect(parseDashboardRole(null)).toBe('technician');
    expect(parseDashboardRole('unknown')).toBe('technician');
  });

  it.each([
    '../pages/DashboardPage.tsx',
    '../pages/DiscoveryPage.tsx',
    '../pages/StocktakeListPage.tsx',
    // WP-813: the stocktake detail (the label dialog is a flowSource).
    '../pages/StocktakeDetailPage.tsx',
    '../pages/CITypeAdminPage.tsx',
    '../pages/TopologyPage.tsx',
    '../pages/TemplatesPage.tsx',
    // WP-799: the work area of the metamodel drafts.
    '../pages/MetamodelDraftsPage.tsx',
    // WP-318: the assignment page, the pickers and the flow dialogs.
    '../pages/AssignmentListPage.tsx',
    // WP-323: the relationship type administration and the edge dialog.
    '../pages/RelationshipTypesPage.tsx',
    '../components/cmdb/CIRelationshipsCard.tsx',
    // WP-315: the saved views page and the click builder.
    '../pages/SavedViewsPage.tsx',
    // WP-801: the rule catalog.
    '../pages/RulesPage.tsx',
    // WP-316: the form builder (LayoutEditor is a metamodelSource).
    '../pages/FormBuilderPage.tsx',
    // WP-806: the inventory page (the stock flow dialogs are flowSources).
    '../pages/InventoryPage.tsx',
    // WP-320: the list and the detail page of the quantity items.
    '../pages/ConsumableListPage.tsx',
    '../pages/ConsumableDetailPage.tsx',
    ...Object.keys(metamodelSources),
    ...Object.keys(flowSources),
    ...Object.keys(ruleSources),
    // WP-321: the document pages, the upload and the document components.
    '../pages/DocumentListPage.tsx',
    '../pages/DocumentDetailPage.tsx',
    ...Object.keys(documentSources),
    // WP-814: the ticket pages.
    '../pages/TicketListPage.tsx',
    '../pages/TicketDetailPage.tsx',
    // WP-322: the import assistant and its mapping editor.
    '../pages/ImportPage.tsx',
    ...Object.keys(importSources),
    // WP-317: the guided creation.
    ...Object.keys(createSources),
    // WP-319: the asset list with the guided asset creation.
    '../pages/AssetListPage.tsx',
  ])('%s has no literal option labels', (file) => {
    const source =
      pageSources[file] ??
      metamodelSources[file] ??
      flowSources[file] ??
      cardSources[file] ??
      ruleSources[file] ??
      documentSources[file] ??
      importSources[file] ??
      createSources[file];
    expect(source, file).toBeDefined();
    expect(source).not.toMatch(/label:\s*['"][A-Za-zÄÖÜäöüß]{2,}/);
    expect(source).not.toMatch(/'(technik|einkauf)'/);
  });
});
