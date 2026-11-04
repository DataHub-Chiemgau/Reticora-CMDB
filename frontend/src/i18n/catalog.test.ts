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
import { DOCUMENT_CATEGORIES } from '../pages/DocumentListPage';
import { TICKET_CATEGORIES, TICKET_STATUSES } from '../pages/TicketListPage';
import { NODE_KINDS } from '../api/client';
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
const metamodelSources = import.meta.glob(
  ['../components/metamodel/**/*.tsx', '!../components/metamodel/**/*.test.tsx'],
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
  // backend/migrations/000010_phase2_assignments.up.sql assignment_status_check.
  assignmentStatuses: ['active', 'returned', 'transferred', 'overdue', 'cancelled'],
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
  ['discovery.collectorStatus', COLLECTOR_STATUSES],
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
    ...Object.keys(metamodelSources),
  ])('%s has no literal option labels', (file) => {
    const source = pageSources[file] ?? metamodelSources[file];
    expect(source, file).toBeDefined();
    expect(source).not.toMatch(/label:\s*['"][A-Za-zÄÖÜäöüß]{2,}/);
    expect(source).not.toMatch(/'(technik|einkauf)'/);
  });
});
