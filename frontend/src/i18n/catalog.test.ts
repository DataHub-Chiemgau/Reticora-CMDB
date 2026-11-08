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
import { FIELD_TYPES } from '../pages/CITypeAdminPage';
import { TEMPLATE_VERSION_STATUSES } from '../pages/TemplatesPage';
import { DIFF_CHANGES } from '../components/metamodel/MetamodelDiff';
import { DRAFT_ITEM_KINDS, DRAFT_OPS } from '../stores/metamodelDraft';
import { DRAFT_STATUSES } from '../pages/MetamodelDraftsPage';
import { JOB_STATUSES } from '../hooks/useJobs';
import { DISCOVERY_PLUGINS } from '../pages/DiscoveryPage';
import { ASSET_CATEGORIES, ASSET_STATUSES } from '../pages/AssetListPage';
import { ASSIGNMENT_STATUSES } from '../pages/AssignmentListPage';
import { MOVEMENT_TYPES } from '../pages/InventoryPage';
import { DOCUMENT_CATEGORIES } from '../pages/DocumentListPage';
import { LINK_ENTITY_TYPES } from '../pages/DocumentDetailPage';
import { TICKET_CATEGORIES, TICKET_STATUSES } from '../pages/TicketListPage';
import { NODE_KINDS, SCAN_STATUSES, relationshipChangeKinds } from '../api/client';
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
  FILTER_ENTITY_KINDS,
  FILTER_FIELDS,
  FILTER_MODE,
  FILTER_NODE_KINDS,
  GROUP_JUNCTIONS,
  NODE_ISSUES,
  RANGE_OF,
  RELATED_DIRECTIONS,
  RULE_KINDS,
  VALIDATION_ISSUES,
  VALIDATION_NODE_KINDS,
} from '../rules/builderModes';
import { filterOps } from '../rules/filter';
import { compareOps } from '../rules/rules';
import { RULE_TARGETS } from '../pages/RulesPage';
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
  // backend/migrations/000009_phase2_assets.up.sql asset_status_check; the
  // constraint was dropped in 000055 ("asset.status keeps the legacy
  // vocabulary"), the default stays in_stock (internal/asset/handler.go).
  assetStatuses: ['in_stock', 'assigned', 'maintenance', 'retired', 'disposed', 'lost'],
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
  ['asset.statuses', BACKEND_ENUMS.assetStatuses],
  ['asset.categories', BACKEND_ENUMS.assetCategories],
  ['assignment.statuses', BACKEND_ENUMS.assignmentStatuses],
  // assignment_type of the projection (000257): assignment or transfer.
  ['assignment.types', ['assignment', 'transfer']],
  ['document.categories', BACKEND_ENUMS.documentCategories],
  ['ticket.statuses', BACKEND_ENUMS.ticketStatuses],
  ['ticket.categories', BACKEND_ENUMS.ticketCategories],
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
  ['discovery.scopes.plugin', DISCOVERY_PLUGINS],
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
    expect([...ASSET_STATUSES]).toEqual(BACKEND_ENUMS.assetStatuses);
    expect([...ASSET_CATEGORIES]).toEqual(BACKEND_ENUMS.assetCategories);
    expect([...ASSIGNMENT_STATUSES]).toEqual(BACKEND_ENUMS.assignmentStatuses);
    expect([...DOCUMENT_CATEGORIES]).toEqual(BACKEND_ENUMS.documentCategories);
    expect([...TICKET_STATUSES]).toEqual(BACKEND_ENUMS.ticketStatuses);
    expect([...TICKET_CATEGORIES]).toEqual(BACKEND_ENUMS.ticketCategories);
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

  it('offers exactly the movement types of the OpenAPI types (WP-806)', () => {
    expect([...MOVEMENT_TYPES]).toEqual(specEnum('MovementType'));
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

  it('offers exactly the node kinds of the OpenAPI types (WP-818)', () => {
    expect([...NODE_KINDS]).toEqual(specEnum('TopologyNode', 'node_kind'));
  });

  it('labels in_stock as "Auf Lager" in de and "In stock" in en', () => {
    expect(lookup(deDE, 'asset.statuses.in_stock')).toBe('Auf Lager');
    expect(lookup(enUS, 'asset.statuses.in_stock')).toBe('In stock');
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
    // WP-806: the inventory page (the stock flow dialogs are flowSources).
    '../pages/InventoryPage.tsx',
    ...Object.keys(metamodelSources),
    ...Object.keys(flowSources),
    ...Object.keys(ruleSources),
    // WP-321: the document pages, the upload and the document components.
    '../pages/DocumentListPage.tsx',
    '../pages/DocumentDetailPage.tsx',
    ...Object.keys(documentSources),
  ])('%s has no literal option labels', (file) => {
    const source =
      pageSources[file] ??
      metamodelSources[file] ??
      flowSources[file] ??
      cardSources[file] ??
      ruleSources[file] ??
      documentSources[file];
    expect(source, file).toBeDefined();
    expect(source).not.toMatch(/label:\s*['"][A-Za-zÄÖÜäöüß]{2,}/);
    expect(source).not.toMatch(/'(technik|einkauf)'/);
  });
});
