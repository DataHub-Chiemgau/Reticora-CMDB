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
import { DISCOVERY_PLUGINS } from '../pages/DiscoveryPage';
import { ASSET_CATEGORIES, ASSET_STATUSES } from '../pages/AssetListPage';
import { ASSIGNMENT_STATUSES } from '../pages/AssignmentListPage';
import { DOCUMENT_CATEGORIES } from '../pages/DocumentListPage';
import { TICKET_CATEGORIES, TICKET_STATUSES } from '../pages/TicketListPage';
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
  // backend/migrations/000013_phase2_tickets.up.sql ticket_status_check.
  ticketStatuses: ['open', 'in_progress', 'waiting', 'resolved', 'closed'],
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
];

const enumerations: Array<[string, readonly string[]]> = [
  ['dashboard.roles', DASHBOARD_ROLES],
  ['stocktake.statuses', STOCKTAKE_STATUSES],
  ['stocktake.scopes', STOCKTAKE_SCOPES],
  ['citypes.dataTypes', FIELD_TYPES],
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
      'received',
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
  ])('%s has no literal option labels', (file) => {
    const source = pageSources[file];
    expect(source, file).toBeDefined();
    expect(source).not.toMatch(/label:\s*['"][A-Za-zÄÖÜäöüß]{2,}/);
    expect(source).not.toMatch(/'(technik|einkauf)'/);
  });
});
