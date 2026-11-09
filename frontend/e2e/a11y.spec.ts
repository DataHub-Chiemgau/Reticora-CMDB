import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  A11Y_HANDLERS,
  A11Y_MARKERS as M,
  CI_ID,
  DRAFT_HANDLERS,
  DRAFT_ID,
  FORM_BUILDER_HANDLERS,
  FORM_TYPE_ID,
  RULE_HANDLERS,
  TEMPLATE_ID,
  TEMPLATE_PERMISSIONS,
  TOPOLOGY_TOO_LARGE,
} from './support/a11y-fixtures';
import { AT, EMPTY_LIST, ORG_ID, list, mockBackend, signIn } from './support/backend';
import type { Handler } from './support/backend';

// UI-18/NFR-07 (WP-193, WP-522, WP-631): every route of the router
// (src/App.tsx) passes an automated WCAG 2.1 AA check (axe-core) in every
// browser project of playwright.config.ts, in both UI languages of E-99
// (de-DE and en-US) and in the light and the dark theme, also with the
// light theme chosen on a dark OS (colour scheme of the browser). The
// backend is mocked with data (support/a11y-fixtures.ts): every list holds at
// least one row, and the check waits for that row, so axe sees rows, badges
// and links instead of an empty page. The empty states of the list pages of
// F-172 are checked as well.

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

type Theme = 'light' | 'dark';

// The theme the user chose (stored like the theme toggle) and the colour
// scheme the OS reports (prefers-color-scheme). The chosen theme alone
// decides: before, the light theme on a dark OS kept the dark canvas of the
// OS under the dark text of the light theme (1:1 on every route).
interface ThemeCase {
  theme: Theme;
  os: Theme;
}
const THEMES: ThemeCase[] = [
  { theme: 'light', os: 'light' },
  { theme: 'dark', os: 'light' },
  { theme: 'light', os: 'dark' },
];

/** themeName names a case in the test title: the theme, and the OS if it differs. */
function themeName({ theme, os }: ThemeCase): string {
  return os === theme || os === 'light' ? theme : `${theme}, ${os} OS`;
}

// The UI languages of E-99 (WP-523). The interface follows the browser
// language; each check runs once per language, because the labels change the
// layout on a phone (F-357: the tables scrolled sideways in de-DE only).
type Language = 'de-DE' | 'en-US';
const LANGUAGES: Language[] = ['de-DE', 'en-US'];

interface A11yRoute {
  /** The path pattern exactly as src/App.tsx declares it. */
  path: string;
  /** The URL the check opens. */
  url: string;
  /** False for the public pages that run without a session. */
  signedIn?: boolean;
  /** Handlers ahead of the fixtures, e.g. an error answer. */
  handlers?: Handler[];
  /** What must be visible before axe runs: the fixture row of the page. */
  ready: (page: Page) => Locator;
  /** Steps that bring the page into its data view (selections). */
  prepare?: (page: Page) => Promise<void>;
  /** The answer of the GraphQL BFF (POST /bff/graphql) for a page that reads it. */
  bff?: unknown;
}

// Every route passes without exception (WP-531, F-172/F-173). The findings
// of this check outside WP-522 are fixed: F-355 grey secondary text and F-361
// red delete actions by WP-630 (shared classes text-muted and text-danger,
// src/index.css; source scan src/test/darkVariant.test.ts), F-356 site markers
// inside the map image and F-357 tables that scroll sideways on a phone
// without taking the focus by WP-631 (audit/g1-reaudit/FINDINGS.md). A new
// violation is a new finding with its own WP (plan convention 16), not a mark.

const text = (value: string) => (page: Page) => page.getByText(value, { exact: false }).first();

// WP-323: a system and an organization type of the relationship type
// administration, the organization type with an attribute schema.
const relationshipType = (key: string, extra: Record<string, unknown>) => ({
  id: `a11y-rt-${key}`,
  key,
  display_name: key,
  reverse_name: key,
  directed: true,
  category: 'logical',
  impact_direction: 'source_depends_on_target',
  redundant: false,
  allowed_source_types: [],
  allowed_target_types: [],
  cardinality: 'many_to_many',
  is_system: true,
  metadata_schema: [],
  created_at: AT,
  updated_at: AT,
  ...extra,
});
// WP-321: the document of the fixtures with a clean and an infected
// version, a link to the fixture CI and the storage usage of the
// organization near its limit (warning from 90 %).
const DOCUMENT_ID = 'da000000-0000-4000-8000-000000000001';
const documentVersion = (version: number, scan: string) => ({
  id: `da100000-0000-4000-8000-00000000000${version}`,
  organization_id: ORG_ID,
  document_id: DOCUMENT_ID,
  version,
  size: 102400 * version,
  mime_type: 'application/pdf',
  checksum_sha256: `${version}`.repeat(64),
  scan_status: scan,
  ...(scan === 'pending_scan' ? {} : { scanned_at: AT }),
  created_at: AT,
});
// WP-809: the detail page of an asset with a primary CI, a configuration
// and a standalone child, a document (BFF aggregate asset(id)), a holder, an
// active reservation and a warranty that ends in two weeks.
const DETAIL_ASSET_ID = 'a5000000-0000-4000-8000-000000000009';
const DETAIL_ASSET_CI = 'a5100000-0000-4000-8000-000000000001';
const inDays = (days: number) =>
  new Date(Date.now() + days * 86_400_000).toISOString().slice(0, 10);
const ASSET_DETAIL_HANDLERS: Handler[] = [
  {
    path: `/assets/${DETAIL_ASSET_ID}`,
    reply: () => ({
      body: {
        id: DETAIL_ASSET_ID,
        organization_id: ORG_ID,
        inventory_no: 'AT-0009',
        name: M.asset,
        category: 'hardware',
        manufacturer: 'Lenovo',
        model: 'T14',
        serial_number: 'SN-A-9',
        lifecycle_state: 'deployed',
        warranty_until: inDays(14),
        primary_ci_id: DETAIL_ASSET_CI,
        active_reservation: {
          id: 'a5200000-0000-4000-8000-000000000001',
          reserved_until: inDays(30),
          project_ref: 'PRJ-a11y',
        },
        custom_fields: {},
        version: 2,
        created_at: AT,
        updated_at: AT,
      },
    }),
  },
  {
    path: `/assets/${DETAIL_ASSET_ID}/lifecycle-transitions`,
    reply: () => ({
      body: {
        model: { id: 'a5300000-0000-4000-8000-000000000001', key: 'physical_asset', name: 'Asset' },
        current_state: 'deployed',
        transitions: [{ to_state: 'repair', key: 'repair', label: 'Repair', required_fields: [] }],
      },
    }),
  },
];
const assetDetailCI = (id: string, name: string, extra: Record<string, unknown>) => ({
  id,
  name,
  status: 'active',
  lifecycleState: 'deployed',
  serialNumber: 'SN-A-9',
  locationId: null,
  assetId: null,
  parentCiId: null,
  childMode: null,
  ...extra,
});
const ASSET_DETAIL_BFF = {
  data: {
    asset: {
      id: DETAIL_ASSET_ID,
      primaryCiId: DETAIL_ASSET_CI,
      cis: [assetDetailCI(DETAIL_ASSET_CI, M.ci, { assetId: DETAIL_ASSET_ID })],
      children: [
        assetDetailCI('a5100000-0000-4000-8000-000000000002', 'a11y-ssd', {
          parentCiId: DETAIL_ASSET_CI,
          childMode: 'configuration',
        }),
        assetDetailCI('a5100000-0000-4000-8000-000000000003', 'a11y-monitor', {
          parentCiId: DETAIL_ASSET_CI,
          assetId: 'a5000000-0000-4000-8000-000000000010',
          childMode: 'standalone',
        }),
      ],
      documents: [
        {
          id: DOCUMENT_ID,
          title: M.document,
          description: null,
          fileName: 'a11y.pdf',
          fileSize: 102400,
          mimeType: 'application/pdf',
          version: 1,
          category: 'manual',
          tags: [],
          uploadedBy: null,
          scanStatus: 'clean',
          createdAt: AT,
          updatedAt: AT,
        },
      ],
    },
  },
};
// WP-320: the detail page of the fixture consumable with its stock and
// availability in one bin and the rights that show the booking and
// reservation actions.
const CONSUMABLE_ID = 'e1000000-0000-4000-8000-000000000001';
const CONSUMABLE_BIN = 'e1100000-0000-4000-8000-000000000001';
const CONSUMABLE_HANDLERS: Handler[] = [
  {
    path: `/consumables/${CONSUMABLE_ID}`,
    reply: () => ({
      body: {
        id: CONSUMABLE_ID,
        organization_id: ORG_ID,
        name: M.consumable,
        sku: 'TN-1',
        barcode: '4006381333931',
        category: 'printer',
        unit: 'pcs',
        stock_level: 2,
        min_level: 4,
        created_at: AT,
        updated_at: AT,
      },
    }),
  },
  {
    path: `/consumables/${CONSUMABLE_ID}/stock`,
    reply: () => ({
      body: list([
        {
          consumable_id: CONSUMABLE_ID,
          location_id: CONSUMABLE_BIN,
          on_hand: 2,
          reserved: 1,
          updated_at: AT,
        },
      ]),
    }),
  },
  {
    path: `/consumables/${CONSUMABLE_ID}/availability`,
    reply: () => ({
      body: list([{ location_id: CONSUMABLE_BIN, on_hand: 2, reserved: 1, available: 1 }]),
    }),
  },
  {
    path: '/me/permissions',
    reply: () => ({
      body: {
        user_id: '00000000-0000-0000-0000-000000000001',
        permissions: ['stock:read', 'stock:write', 'reservation:write'],
      },
    }),
  },
];
const DOCUMENT_HANDLERS: Handler[] = [
  {
    path: '/documents/usage',
    reply: () => ({ body: { storage_bytes: 950 * 1024 * 1024, limit_mb: 1024 } }),
  },
  {
    path: `/documents/${DOCUMENT_ID}`,
    reply: () => ({
      body: {
        id: DOCUMENT_ID,
        organization_id: ORG_ID,
        title: M.document,
        description: 'a11y Betrieb',
        file_name: 'handbuch.pdf',
        file_size: 204800,
        mime_type: 'application/pdf',
        storage_key: `org/${ORG_ID}/documents/${DOCUMENT_ID}/2`,
        version: 2,
        category: 'manual',
        tags: ['betrieb'],
        uploaded_by: '00000000-0000-0000-0000-000000000001',
        created_at: AT,
        updated_at: AT,
        scan_status: 'clean',
        scanned_at: AT,
      },
    }),
  },
  {
    path: `/documents/${DOCUMENT_ID}/versions`,
    reply: () => ({ body: list([documentVersion(2, 'clean'), documentVersion(1, 'infected')]) }),
  },
  {
    path: `/documents/${DOCUMENT_ID}/links`,
    reply: () => ({
      body: list([
        {
          id: 'da200000-0000-4000-8000-000000000001',
          organization_id: ORG_ID,
          document_id: DOCUMENT_ID,
          entity_type: 'ci',
          entity_id: CI_ID,
          created_at: AT,
        },
      ]),
    }),
  },
];

// WP-814: the fixture ticket with its primary CI, a link to that CI, an
// external and a system comment, a clean attachment and a breached SLA.
const TICKET_ID = 'dc000000-0000-4000-8000-000000000001';
const TICKET_USER = '00000000-0000-0000-0000-000000000001';
const ticketComment = (id: number, extra: Record<string, unknown>) => ({
  id: `dc100000-0000-4000-8000-00000000000${id}`,
  organization_id: ORG_ID,
  ticket_id: TICKET_ID,
  content: `a11y Kommentar ${id}`,
  created_at: AT,
  updated_at: AT,
  ...extra,
});
const TICKET_HANDLERS: Handler[] = [
  {
    path: `/tickets/${TICKET_ID}`,
    reply: () => ({
      body: {
        id: TICKET_ID,
        organization_id: ORG_ID,
        ticket_number: 42,
        title: M.ticket,
        description: 'a11y Papierstau',
        status: 'open',
        priority: 'high',
        category: 'incident',
        reporter_id: TICKET_USER,
        assignee_id: TICKET_USER,
        related_ci_id: CI_ID,
        due_date: AT,
        tags: [],
        created_at: AT,
        updated_at: AT,
      },
    }),
  },
  {
    path: `/tickets/${TICKET_ID}/comments`,
    reply: () => ({
      body: list([
        ticketComment(1, { author_id: TICKET_USER, author_type: 'user', is_internal: false }),
        ticketComment(2, { author_id: null, author_type: 'system', is_internal: true }),
      ]),
    }),
  },
  {
    path: `/tickets/${TICKET_ID}/links`,
    reply: () => ({
      body: list([
        {
          id: 'dc200000-0000-4000-8000-000000000001',
          organization_id: ORG_ID,
          ticket_id: TICKET_ID,
          entity_type: 'ci',
          entity_id: CI_ID,
          created_at: AT,
        },
      ]),
    }),
  },
  {
    path: `/tickets/${TICKET_ID}/attachments`,
    reply: () => ({
      body: list([
        {
          id: DOCUMENT_ID,
          organization_id: ORG_ID,
          title: M.document,
          file_name: 'handbuch.pdf',
          file_size: 204800,
          mime_type: 'application/pdf',
          storage_key: `org/${ORG_ID}/documents/${DOCUMENT_ID}/2`,
          version: 2,
          category: 'manual',
          tags: [],
          uploaded_by: TICKET_USER,
          created_at: AT,
          updated_at: AT,
          scan_status: 'clean',
          scanned_at: AT,
        },
      ]),
    }),
  },
  {
    path: `/tickets/${TICKET_ID}/sla`,
    reply: () => ({
      body: {
        id: 'dc300000-0000-4000-8000-000000000001',
        organization_id: ORG_ID,
        ticket_id: TICKET_ID,
        sla_id: 'dc400000-0000-4000-8000-000000000001',
        response_due_at: AT,
        resolution_due_at: AT,
        response_breached: true,
        resolution_breached: false,
        calendar_id: null,
        response_remaining_minutes: -90,
        resolution_remaining_minutes: 125,
        paused: false,
        created_at: AT,
        updated_at: AT,
      },
    }),
  },
];

// WP-813: the stocktake of the fixtures in progress, with an area, a frozen
// asset and consumable, one scan per kind and a difference of each kind.
// The area and bins lie outside the fixture tree, so the page names them by
// id; the page reads the names of asset and consumable one by one.
const STOCKTAKE_ID = 'db000000-0000-4000-8000-000000000001';
const STOCKTAKE_AREA = 'db100000-0000-4000-8000-000000000001';
const STOCKTAKE_BIN = 'db100000-0000-4000-8000-000000000002';
const STOCKTAKE_ASSET = 'db200000-0000-4000-8000-000000000001';
const STOCKTAKE_CONSUMABLE = 'db300000-0000-4000-8000-000000000001';
const STOCKTAKE_USER = '00000000-0000-0000-0000-000000000001';
const STOCKTAKE_HANDLERS: Handler[] = [
  {
    path: `/stocktakes/${STOCKTAKE_ID}`,
    reply: () => ({
      body: {
        id: STOCKTAKE_ID,
        organization_id: ORG_ID,
        title: M.stocktake,
        status: 'in_progress',
        scope: 'full',
        location_id: STOCKTAKE_AREA,
        started_at: AT,
        total_expected: 2,
        total_scanned: 2,
        total_missing: 0,
        total_surplus: 0,
        created_at: AT,
        updated_at: AT,
      },
    }),
  },
  {
    path: `/stocktakes/${STOCKTAKE_ID}/items`,
    reply: () => ({
      body: list([
        {
          id: 'db400000-0000-4000-8000-000000000001',
          stocktake_id: STOCKTAKE_ID,
          item_kind: 'asset',
          asset_id: STOCKTAKE_ASSET,
          location_id: STOCKTAKE_AREA,
          expected_quantity: 1,
          created_at: AT,
        },
        {
          id: 'db400000-0000-4000-8000-000000000002',
          stocktake_id: STOCKTAKE_ID,
          item_kind: 'consumable',
          consumable_id: STOCKTAKE_CONSUMABLE,
          location_id: STOCKTAKE_BIN,
          expected_quantity: 5,
          created_at: AT,
        },
      ]),
    }),
  },
  {
    path: `/stocktakes/${STOCKTAKE_ID}/scans`,
    reply: () => ({
      body: list([
        {
          id: 'db500000-0000-4000-8000-000000000001',
          organization_id: ORG_ID,
          stocktake_id: STOCKTAKE_ID,
          asset_id: STOCKTAKE_ASSET,
          location_id: STOCKTAKE_BIN,
          scanned_by: STOCKTAKE_USER,
          scan_method: 'barcode',
          scan_result: 'found',
          code: 'AT-0002',
          scanned_at: AT,
        },
        {
          id: 'db500000-0000-4000-8000-000000000002',
          organization_id: ORG_ID,
          stocktake_id: STOCKTAKE_ID,
          consumable_id: STOCKTAKE_CONSUMABLE,
          quantity: 3,
          location_id: STOCKTAKE_BIN,
          scanned_by: STOCKTAKE_USER,
          scan_method: 'manual',
          scan_result: 'found',
          scanned_at: AT,
        },
      ]),
    }),
  },
  {
    path: `/stocktakes/${STOCKTAKE_ID}/differences`,
    reply: () => ({
      body: list([
        {
          item_kind: 'asset',
          asset_id: STOCKTAKE_ASSET,
          classification: 'found',
          expected_location_id: STOCKTAKE_AREA,
          found_location_id: STOCKTAKE_BIN,
          expected_quantity: 1,
          counted_quantity: 1,
          asset: { id: STOCKTAKE_ASSET, name: 'a11y Scanner' },
        },
        {
          item_kind: 'consumable',
          consumable_id: STOCKTAKE_CONSUMABLE,
          classification: 'found',
          expected_location_id: STOCKTAKE_BIN,
          found_location_id: STOCKTAKE_BIN,
          expected_quantity: 5,
          counted_quantity: 3,
          consumable: { id: STOCKTAKE_CONSUMABLE, name: 'a11y Toner' },
        },
      ]),
    }),
  },
  {
    path: `/assets/${STOCKTAKE_ASSET}`,
    reply: () => ({
      body: {
        id: STOCKTAKE_ASSET,
        organization_id: ORG_ID,
        inventory_no: 'AT-0002',
        name: 'a11y Scanner',
        category: 'hardware',
        status: 'in_stock',
        custom_fields: {},
        created_at: AT,
        updated_at: AT,
      },
    }),
  },
  {
    path: `/consumables/${STOCKTAKE_CONSUMABLE}`,
    reply: () => ({
      body: {
        id: STOCKTAKE_CONSUMABLE,
        organization_id: ORG_ID,
        name: 'a11y Toner',
        sku: 'TN-2',
        category: 'printer',
        unit: 'pcs',
        stock_level: 3,
        min_level: 1,
        created_at: AT,
        updated_at: AT,
      },
    }),
  },
];

const RELATIONSHIP_TYPES: Handler = {
  path: '/relationship-types',
  reply: () => ({
    body: list([
      relationshipType('a11y_depends_on', {
        display_name: 'a11y depends on',
        reverse_name: 'a11y required by',
      }),
      relationshipType('a11y_backs_up', {
        organization_id: ORG_ID,
        is_system: false,
        display_name: 'a11y backs up',
        reverse_name: 'a11y backed up by',
        category: 'hosting',
        cardinality: 'one_to_many',
        metadata_schema: [
          {
            key: 'a11y_schedule',
            display_name: { de: 'Zeitplan', en: 'Schedule' },
            data_type: 'text',
            required: true,
          },
        ],
      }),
    ]),
  }),
};

// WP-817: a system tag and an own tag with a color in the tag catalog.
const TAG_HANDLERS: Handler[] = [
  {
    path: '/tags',
    reply: () => ({
      body: list(
        [
          { key: 'archived', is_system: true, display_name: { de: 'Archiviert', en: 'Archived' } },
          {
            key: 'a11y-backup',
            is_system: false,
            display_name: { de: 'a11y Sicherung', en: 'a11y backup' },
            color: '#1f6feb',
          },
        ].map((tag, i) => ({
          id: `fa000000-0000-4000-8000-00000000000${i + 1}`,
          organization_id: ORG_ID,
          color: null,
          created_at: AT,
          updated_at: AT,
          ...tag,
        })),
      ),
    }),
  },
];

// WP-808: two contracts of a supplier, one whose notice deadline is in 7 days
// and one whose deadline has passed (the expiry overview shows both), and
// the supplier with its contact person.
const CONTRACT_ID = 'fc000000-0000-4000-8000-000000000001';
const SUPPLIER_ID = 'fd000000-0000-4000-8000-000000000001';
const CONTACT_ID = 'fe000000-0000-4000-8000-000000000001';
/** day is today plus the given days as YYYY-MM-DD. */
const day = (offset: number) =>
  new Date(Date.now() + offset * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
const contractRow = (n: number, extra: Record<string, unknown>) => ({
  id: `fc000000-0000-4000-8000-00000000000${n}`,
  organization_id: ORG_ID,
  client_id: null,
  supplier_id: SUPPLIER_ID,
  number: `a11y-V-${n}`,
  kind: 'maintenance',
  start_date: '2026-01-01',
  notice_period_days: 30,
  value: 1200.5,
  currency: 'EUR',
  status: 'active',
  notes: null,
  version: 1,
  created_at: AT,
  updated_at: AT,
  ...extra,
});
const CONTRACTS = [
  contractRow(1, { end_date: day(37) }),
  contractRow(2, { end_date: day(20), kind: 'license' }),
];
const SUPPLIER = {
  id: SUPPLIER_ID,
  organization_id: ORG_ID,
  client_id: null,
  name: 'a11y Wartung GmbH',
  external_ref: 'K-100',
  contact_id: CONTACT_ID,
  notes: null,
  version: 1,
  created_at: AT,
  updated_at: AT,
};
const CONTRACT_HANDLERS: Handler[] = [
  { path: `/contracts/${CONTRACT_ID}`, reply: () => ({ body: CONTRACTS[0] }) },
  { path: '/contracts', reply: () => ({ body: list(CONTRACTS) }) },
  { path: '/suppliers', reply: () => ({ body: list([SUPPLIER]) }) },
  {
    path: '/contacts',
    reply: () => ({
      body: list([
        {
          id: CONTACT_ID,
          organization_id: ORG_ID,
          display_name: 'a11y Erika Muster',
          is_24_7: false,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    }),
  },
];

// WP-322: an uploaded CSV file whose scan is clean and its column preview;
// the check opens the mapping step of the import assistant.
const IMPORT_DOCUMENT = {
  id: 'fb000000-0000-4000-8000-000000000001',
  organization_id: ORG_ID,
  title: 'a11y-import',
  file_name: 'a11y-import.csv',
  file_size: 32,
  mime_type: 'text/csv',
  storage_key: '',
  version: 1,
  category: 'general',
  tags: [],
  uploaded_by: '00000000-0000-0000-0000-000000000001',
  created_at: AT,
  updated_at: AT,
  scan_status: 'clean',
};
const IMPORT_HANDLERS: Handler[] = [
  {
    path: '/me/permissions',
    reply: () => ({
      body: {
        user_id: '00000000-0000-0000-0000-000000000001',
        permissions: ['ci:read', 'import:run', 'import:manage'],
      },
    }),
  },
  { method: 'POST', path: '/documents', reply: () => ({ status: 201, body: IMPORT_DOCUMENT }) },
  {
    method: 'PUT',
    path: `/documents/${IMPORT_DOCUMENT.id}/content`,
    reply: () => ({ body: IMPORT_DOCUMENT }),
  },
  { path: `/documents/${IMPORT_DOCUMENT.id}`, reply: () => ({ body: IMPORT_DOCUMENT }) },
  {
    method: 'POST',
    path: '/imports/preview',
    reply: () => ({
      body: {
        format: 'csv',
        sheets: [],
        columns: ['Hostname', 'Seriennummer'],
        sample_rows: [['a11y-import-srv', 'SN-1']],
      },
    }),
  },
];

// The report of a dry run with its row errors (GET /imports/{id}/errors,
// finding of WP-322): a saved profile, the finished job, its projection and
// one page of errors with a next page.
const IMPORT_JOB_ID = '7c000000-0000-4000-8000-0000000000a1';
const IMPORT_REPORT_HANDLERS: Handler[] = [
  ...IMPORT_HANDLERS,
  {
    path: '/import-profiles',
    reply: () => ({
      body: list([
        {
          id: '7b000000-0000-4000-8000-0000000000a1',
          organization_id: ORG_ID,
          name: 'a11y-import-profile',
          description: null,
          entity: 'ci',
          format: 'csv',
          mode: 'both',
          sheet: null,
          mapping: { columns: { Hostname: { target: 'name', transforms: [] } }, defaults: {} },
          identity_keys: ['name'],
          created_by: '00000000-0000-0000-0000-000000000001',
          created_at: AT,
          updated_at: AT,
        },
      ]),
    }),
  },
  {
    method: 'POST',
    path: '/imports',
    reply: () => ({
      status: 202,
      body: { job_id: IMPORT_JOB_ID, status: 'queued', kind: 'import.run' },
    }),
  },
  {
    path: `/jobs/${IMPORT_JOB_ID}`,
    reply: () => ({
      body: {
        id: IMPORT_JOB_ID,
        organization_id: ORG_ID,
        kind: 'import.run',
        status: 'succeeded',
        progress: 100,
        result: { dry_run: true, total: 2, created: 1, updated: 0, skipped: 0, failed: 1 },
        attempts: 1,
        max_attempts: 1,
        created_at: AT,
        updated_at: AT,
        cancellable: false,
      },
    }),
  },
  {
    path: `/imports/${IMPORT_JOB_ID}`,
    reply: () => ({
      body: {
        id: IMPORT_JOB_ID,
        kind: 'import.run',
        status: 'succeeded',
        progress: 100,
        source: 'document',
        document_id: IMPORT_DOCUMENT.id,
        document_version: 1,
        row_count: null,
        profile_id: '7b000000-0000-4000-8000-0000000000a1',
        mode: 'both',
        dry_run: true,
        sheet: null,
        rows_total: 2,
        rows_failed: 1,
        error_code: null,
        error: null,
        cancellable: false,
        created_at: AT,
        started_at: AT,
        finished_at: AT,
      },
    }),
  },
  {
    path: `/imports/${IMPORT_JOB_ID}/errors`,
    reply: () => ({
      body: {
        data: [
          {
            line: null,
            field: 'Seriennummer',
            code: 'column_missing',
            message: 'a11y-import-file',
          },
          { line: 3, field: 'name', code: 'invalid_value', message: 'a11y-import-error' },
        ],
        total: 3,
        limit: 50,
        offset: 0,
        has_more: true,
        next_cursor: 'a11y-next',
      },
    }),
  },
];

// One entry per route of src/App.tsx; the parity test below fails when a
// route is added there without an entry here.
const ROUTES: A11yRoute[] = [
  {
    path: '/login',
    url: '/login',
    signedIn: false,
    ready: (page) => page.getByRole('button', { name: /sign in|log in|anmelden/i }),
  },
  {
    path: '/auth/callback',
    url: '/auth/callback?code=a11y&state=unknown',
    signedIn: false,
    ready: (page) => page.locator('section h1'),
  },
  {
    path: '/onboarding',
    url: '/onboarding',
    signedIn: false,
    ready: (page) => page.locator('#main-content form, #main-content h1').first(),
  },
  {
    path: '/invitations/accept',
    url: '/invitations/accept?token=a11y-token',
    signedIn: false,
    ready: (page) => page.locator('#main-content form'),
  },
  { path: '/', url: '/', ready: text(`${80} %`) },
  { path: '/dashboard', url: '/dashboard', ready: text(`${80} %`) },
  { path: '/cmdb', url: '/cmdb', ready: text(M.ci) },
  {
    path: '/cmdb/:id',
    url: `/cmdb/${CI_ID}`,
    // The advanced sections (fields, provenance, lifecycle, impact, metrics,
    // history) are opened and the blast radius and metrics loaded, so their
    // content is checked too.
    prepare: async (page) => {
      const sections = page.locator('#main-content details:not([open]) > summary');
      await expect(page.getByRole('heading', { name: M.ci })).toBeVisible();
      while ((await sections.count()) > 0) await sections.first().click();
      await page.getByRole('button', { name: /blast radius/i }).click();
      await page.getByRole('button', { name: /metriken laden|load metrics/i }).click();
    },
    ready: (page) => page.getByRole('heading', { name: M.ci }),
  },
  {
    path: '/topology',
    url: '/topology',
    // More than 2,000 CIs without a filter: the start dialog (422).
    handlers: [TOPOLOGY_TOO_LARGE],
    ready: (page) => page.locator('#main-content form'),
  },
  {
    path: '/map',
    url: '/map',
    // The marker of the site opens its details.
    prepare: (page) => page.getByRole('button', { name: M.site }).click(),
    ready: (page) => page.getByRole('heading', { name: M.site }),
  },
  {
    path: '/roomplan',
    url: '/roomplan',
    ready: (page) => page.locator('#main-content [aria-label]', { hasText: M.ci }).first(),
    prepare: async (page) => {
      const selects = page.locator('#main-content select');
      await expect(selects.first().locator('option', { hasText: M.building })).toHaveCount(1);
      await selects.first().selectOption({ label: M.building });
      await expect(selects.nth(1).locator('option', { hasText: M.room })).toHaveCount(1);
      await selects.nth(1).selectOption({ label: M.room });
    },
  },
  { path: '/racks', url: '/racks', ready: text(M.ci) },
  { path: '/discovery', url: '/discovery', ready: text(M.collector) },
  { path: '/assets', url: '/assets', ready: text(M.asset) },
  {
    // WP-809: master data, CIs, children, documents, holder, reservation and warranty.
    path: '/assets/:id',
    url: `/assets/${DETAIL_ASSET_ID}`,
    handlers: ASSET_DETAIL_HANDLERS,
    bff: ASSET_DETAIL_BFF,
    prepare: async (page) => {
      await expect(page.locator('#main-content').getByText('a11y-monitor')).toBeVisible();
      await expect(page.locator('#main-content').getByText(M.document)).toBeVisible();
    },
    ready: (page) => page.getByRole('heading', { name: M.asset, level: 1 }),
  },
  { path: '/inventory', url: '/inventory', ready: text('a11y-move') },
  { path: '/locations', url: '/locations', ready: text(M.location) },
  { path: '/saved-views', url: '/saved-views', ready: text(M.savedView) },
  {
    // WP-817: the tag catalog, read-only without tag:manage.
    path: '/tags',
    url: '/tags',
    handlers: TAG_HANDLERS,
    ready: (page) => page.locator('#main-content tr').filter({ hasText: 'a11y-backup' }),
  },
  {
    path: '/ci-types',
    url: '/ci-types',
    // WP-313: the type tree with parent and child; the child is selected, so
    // its attribute catalog (own, inherited, global; index states none,
    // queued and failed; the sensitive mark) is checked too. WP-797: the
    // child carries a set of two attributes, whose editor is open.
    prepare: async (page) => {
      await page.getByRole('treeitem', { name: M.ciTypeChild }).click();
      await expect(page.getByRole('treeitem', { name: M.ciTypeChild })).toHaveAttribute(
        'aria-selected',
        'true',
      );
      await page.locator('#attribute-sets button[aria-expanded]').first().click();
      const editor = page.locator('#attribute-sets [role="region"]');
      await expect(editor.getByText('a11y_set_vlan')).toBeVisible();
      await expect(editor.getByText('a11y_set_speed')).toBeVisible();
    },
    ready: (page) => page.locator('#main-content li').filter({ hasText: 'a11y_inventory' }),
  },
  {
    // WP-323: a system type (read-only) and an organization type with its
    // edit and delete actions.
    path: '/relationship-types',
    url: '/relationship-types',
    handlers: [RELATIONSHIP_TYPES],
    ready: (page) => page.locator('#main-content tr').filter({ hasText: 'a11y backs up' }),
  },
  {
    // WP-314: own and system template, with the rights to edit and publish.
    path: '/ci-types/templates',
    url: '/ci-types/templates',
    handlers: [TEMPLATE_PERMISSIONS],
    ready: (page) => page.getByRole('link', { name: M.systemTemplate }),
  },
  {
    // WP-314: the draft in the editor with its overrides and the comparison
    // of the two versions.
    path: '/ci-types/templates/:id',
    url: `/ci-types/templates/${TEMPLATE_ID}`,
    handlers: [TEMPLATE_PERMISSIONS],
    prepare: async (page) => {
      await expect(page.locator('[data-item-key="a11y_serial"]')).toBeVisible();
    },
    ready: (page) => page.locator('#main-content table'),
  },
  {
    // WP-799: a tested and a published draft, with the rights to test and publish.
    path: '/ci-types/drafts',
    url: '/ci-types/drafts',
    handlers: DRAFT_HANDLERS,
    ready: (page) => page.getByRole('link', { name: M.draft, exact: true }),
  },
  {
    // WP-799: the patch list with the diff per item and the test report.
    path: '/ci-types/drafts/:id',
    url: `/ci-types/drafts/${DRAFT_ID}`,
    handlers: DRAFT_HANDLERS,
    prepare: async (page) => {
      await expect(
        page.locator('#main-content').getByText('a11y changed description'),
      ).toBeVisible();
      await expect(page.locator('#main-content').getByText('a11y_template').first()).toBeVisible();
    },
    ready: (page) => page.locator('#main-content').getByText('a11y value required'),
  },
  {
    // WP-316: the form builder of the child type without a role: the copy of
    // the resolved form, inherited from the layout of the parent, with the
    // field palette, a section with its fields and widths and the catalog
    // rules for visible_when.
    path: '/ci-types/:id/form-builder',
    url: `/ci-types/${FORM_TYPE_ID}/form-builder`,
    handlers: [TEMPLATE_PERMISSIONS, ...RULE_HANDLERS, ...FORM_BUILDER_HANDLERS],
    prepare: async (page) => {
      await expect(page.locator('[data-section-key="a11y_main"]')).toBeVisible();
      await expect(page.locator('[data-field-name="a11y_serial"]')).toBeVisible();
    },
    ready: (page) => page.locator('[data-palette-field="a11y_cost_center"]'),
  },
  {
    // WP-801: the rule catalog with an own rule (condition, edit and delete),
    // a validation of a template version and a read-only system rule.
    path: '/rules',
    url: '/rules',
    handlers: [TEMPLATE_PERMISSIONS, ...RULE_HANDLERS],
    ready: (page) => page.locator('#main-content tr').filter({ hasText: M.rule }),
  },
  { path: '/assignments', url: '/assignments', ready: text(M.assignment) },
  {
    // WP-808: the contract list with the expiry overview (a deadline in 7
    // days, a passed one), the contract with its documents and the suppliers.
    path: '/contracts',
    url: '/contracts',
    handlers: CONTRACT_HANDLERS,
    ready: (page) => page.locator('#main-content tr').filter({ hasText: 'a11y-V-1' }),
  },
  {
    path: '/contracts/:id',
    url: `/contracts/${CONTRACT_ID}`,
    handlers: CONTRACT_HANDLERS,
    prepare: async (page) => {
      await expect(page.locator('#main-content').getByText('a11y Wartung GmbH')).toBeVisible();
    },
    ready: (page) => page.locator('#main-content').getByRole('link', { name: M.document }),
  },
  {
    path: '/suppliers',
    url: '/suppliers',
    handlers: CONTRACT_HANDLERS,
    prepare: async (page) => {
      await expect(page.locator('#main-content').getByText('a11y Erika Muster')).toBeVisible();
    },
    ready: (page) => page.locator('#main-content tr').filter({ hasText: 'a11y Wartung GmbH' }),
  },
  {
    path: '/documents',
    url: '/documents',
    handlers: DOCUMENT_HANDLERS,
    ready: text(M.document),
  },
  {
    // WP-321: metadata, versions with scan status and download, the links.
    path: '/documents/:id',
    url: `/documents/${DOCUMENT_ID}`,
    handlers: DOCUMENT_HANDLERS,
    prepare: async (page) => {
      await expect(page.locator('#main-content table').getByText('v1')).toBeVisible();
    },
    ready: (page) => page.locator('#main-content').getByRole('link', { name: M.ci }),
  },
  { path: '/stocktake', url: '/stocktake', ready: text(M.stocktake) },
  {
    path: '/stocktake/:id',
    url: `/stocktake/${STOCKTAKE_ID}`,
    handlers: STOCKTAKE_HANDLERS,
    prepare: async (page) => {
      await expect(page.locator('#main-content').getByText('a11y Toner').first()).toBeVisible();
    },
    ready: (page) => page.locator('#main-content h1').getByText(M.stocktake),
  },
  { path: '/consumables', url: '/consumables', ready: text(M.consumable) },
  {
    path: '/consumables/:id',
    url: `/consumables/${CONSUMABLE_ID}`,
    handlers: CONSUMABLE_HANDLERS,
    ready: (page) => page.getByRole('heading', { name: M.consumable }),
  },
  { path: '/orders', url: '/orders', ready: text(M.order) },
  { path: '/maintenance', url: '/maintenance', ready: text(M.maintenance) },
  { path: '/disposal', url: '/disposal', ready: text(M.disposal) },
  { path: '/keys', url: '/keys', ready: text(M.key) },
  { path: '/trainings', url: '/trainings', ready: text(M.training) },
  { path: '/desks', url: '/desks', ready: text(M.desk) },
  { path: '/agents', url: '/agents', ready: text(M.agent) },
  { path: '/findings', url: '/findings', ready: text(M.finding) },
  { path: '/reviews', url: '/reviews', ready: text(M.review) },
  { path: '/jobs', url: '/jobs', ready: text(M.job) },
  { path: '/tickets', url: '/tickets', ready: text(M.ticket) },
  {
    // WP-814: head, status changes, assignment, links, comments, attachments and SLA.
    path: '/tickets/:id',
    url: `/tickets/${TICKET_ID}`,
    handlers: TICKET_HANDLERS,
    prepare: async (page) => {
      await expect(page.locator('#main-content').getByText('a11y Kommentar 2')).toBeVisible();
      await expect(page.locator('#main-content').getByText(M.document)).toBeVisible();
    },
    ready: (page) =>
      page.locator('#main-content').getByRole('list').getByRole('link', { name: M.ci }).first(),
  },
  {
    path: '/users',
    url: '/users',
    ready: text(M.user),
  },
  {
    path: '/permissions',
    url: '/permissions',
    ready: text(M.permission),
  },
  { path: '/slas', url: '/slas', ready: text(M.sla) },
  { path: '/forms', url: '/forms', ready: text(M.form) },
  { path: '/workflows', url: '/workflows', ready: text(M.workflow) },
  { path: '/compliance', url: '/compliance', ready: text(M.compliance) },
  { path: '/iga', url: '/iga', ready: text(M.iga) },
  { path: '/assistant', url: '/assistant', ready: text(M.conversation) },
  { path: '/webhooks', url: '/webhooks', ready: text(M.webhook) },
  {
    path: '/export',
    url: '/export',
    ready: (page) => page.locator('#main-content tbody td', { hasText: 'csv' }).first(),
  },
  {
    // WP-322: the import assistant after the upload, with preview and mapping.
    path: '/imports',
    url: '/imports',
    handlers: IMPORT_HANDLERS,
    prepare: async (page) => {
      await page.locator('#main-content input[type="file"]').setInputFiles({
        name: 'a11y-import.csv',
        mimeType: 'text/csv',
        buffer: Buffer.from('Hostname;Seriennummer\na11y-import-srv;SN-1\n'),
      });
      await page.getByRole('button', { name: /Weiter zur Zuordnung|Continue to mapping/ }).click();
    },
    ready: (page) => page.locator('#main-content td', { hasText: 'a11y-import-srv' }),
  },
  { path: '/monitoring', url: '/monitoring', ready: text(M.alert) },
  { path: '/audit', url: '/audit', ready: text(M.audit) },
  { path: '/security', url: '/security', ready: text(M.compliance) },
  // Unknown paths fall back to the CI list.
  { path: '*', url: '/a11y-unknown-route', ready: text(M.ci) },
];

// The list pages of F-172 whose empty state had too little contrast: they are
// checked with an empty list as well.
const EMPTY_PAGES: { url: string; endpoint: string; handlers?: Handler[] }[] = [
  { url: '/assets', endpoint: '/assets' },
  { url: '/stocktake', endpoint: '/stocktakes' },
  { url: '/assignments', endpoint: '/assignments' },
  { url: '/documents', endpoint: '/documents', handlers: DOCUMENT_HANDLERS },
  { url: '/tickets', endpoint: '/tickets' },
];

/** routerPaths reads the path of every <Route> of src/App.tsx. */
function routerPaths(): string[] {
  const app = readFileSync(fileURLToPath(new URL('../src/App.tsx', import.meta.url)), 'utf8');
  return [...app.matchAll(/<Route\b[^>]*?\bpath="([^"]+)"/g)].map((match) => match[1]!);
}

/** storeTheme stores the theme the way the theme toggle does (stores/theme.ts). */
async function storeTheme(page: Page, theme: Theme) {
  await page.addInitScript((mode) => {
    window.localStorage.setItem('reticora-theme-mode', mode);
  }, theme);
}

// pageDiagnostics collects what the browser reports while a page loads:
// uncaught errors, console errors and failed requests. When the page does
// not appear, the assertion names them, so a failure in CI shows its cause
// in the job log. pageErrors holds the uncaught errors alone.
function pageDiagnostics(page: Page): { describe: () => string; pageErrors: string[] } {
  const lines: string[] = [];
  const pageErrors: string[] = [];
  page.on('pageerror', (err) => {
    pageErrors.push(err.message);
    lines.push(`pageerror: ${err.message}`);
  });
  page.on('console', (msg) => {
    if (msg.type() === 'error') lines.push(`console.error: ${msg.text()}`);
  });
  page.on('requestfailed', (req) =>
    lines.push(`requestfailed: ${req.url()} ${req.failure()?.errorText ?? ''}`),
  );
  const describe = () =>
    lines.length ? lines.join('\n') : 'no page errors, console errors or failed requests';
  return { describe, pageErrors };
}

async function expectVisible(page: Page, locator: Locator, diagnostics: () => string) {
  try {
    await expect(locator).toBeVisible();
  } catch (err) {
    const state = await page
      .evaluate(
        () =>
          `${document.readyState}, #root children: ${document.getElementById('root')?.childElementCount ?? 'no #root'}`,
      )
      .catch((e: unknown) => `unavailable (${String(e)})`);
    throw new Error(
      `${(err as Error).message}\nurl: ${page.url()}\ndocument: ${state}\n${diagnostics()}`,
    );
  }
}

/** expectLanguage checks that the interface runs in the browser language. */
async function expectLanguage(page: Page, language: Language) {
  await expect(page.locator('html')).toHaveAttribute('lang', language);
}

/**
 * expectTheme checks that the chosen theme is in effect whatever the OS
 * prefers: the class of the dark: variants, data-theme (which switches off the
 * pre-JS OS fallback of src/index.css), the colour scheme of native controls
 * and the canvas colour (the light canvas, or the dark one: both are near-neutral
 * and clearly on their side of the scale).
 */
async function expectTheme(page: Page, theme: Theme) {
  const html = page.locator('html');
  if (theme === 'dark') {
    await expect(html).toHaveClass(/\bdark\b/);
  } else {
    await expect(html).not.toHaveClass(/\bdark\b/);
  }
  await expect(html).toHaveAttribute('data-theme', theme);
  await expect(html).toHaveCSS('color-scheme', theme);
  const canvas = await html.evaluate((el) => getComputedStyle(el).backgroundColor);
  const channels = (canvas.match(/\d+(\.\d+)?/g) ?? []).slice(0, 3).map(Number);
  expect(channels, `html background ${canvas}`).toHaveLength(3);
  for (const channel of channels) {
    if (theme === 'dark') {
      expect(channel, `html background ${canvas}`).toBeLessThan(40);
    } else {
      expect(channel, `html background ${canvas}`).toBeGreaterThan(235);
    }
  }
}

// Each violation names its elements with their markup, so a failure in CI
// shows which element of the page breaks the rule. include limits the check
// to one part of the page, e.g. an open dialog (Radix hides the page behind
// it with aria-hidden).
async function expectNoViolations(page: Page, include?: string) {
  const builder = new AxeBuilder({ page }).withTags(WCAG_TAGS);
  if (include) builder.include(include);
  const results = await builder.analyze();
  const summary = results.violations.map(
    (v) =>
      `${v.id} (${v.impact}): ${v.nodes
        .map((n) => `${n.target.join(' ')} ${n.html.slice(0, 160)}`)
        .join(', ')}`,
  );
  expect(summary, 'WCAG 2.1 AA violations').toEqual([]);
}

/**
 * checkPage opens a page with data in a theme on an OS colour scheme and runs
 * axe on it (on the part named by include, if given).
 */
async function checkPage(
  page: Page,
  route: Pick<A11yRoute, 'url' | 'signedIn' | 'handlers' | 'ready' | 'prepare' | 'bff'> & {
    include?: string;
  },
  { theme, os }: ThemeCase,
  language: Language,
) {
  const diagnostics = pageDiagnostics(page);
  await mockBackend(page, [...(route.handlers ?? []), ...A11Y_HANDLERS]);
  if (route.bff !== undefined) {
    const bff = route.bff;
    await page.route('**/bff/graphql', (request) =>
      request.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(bff) }),
    );
  }
  if (route.signedIn !== false) await signIn(page);
  await storeTheme(page, theme);
  await page.emulateMedia({ colorScheme: os });
  await page.goto(route.url);
  await expectVisible(page, page.locator('#main-content'), diagnostics.describe);
  await route.prepare?.(page);
  await expectVisible(page, route.ready(page), diagnostics.describe);
  await page.waitForLoadState('networkidle');
  // A page that throws once its data arrives unmounts the app; axe then
  // checks an empty document and finds nothing (WP-606). The page must still
  // stand after the requests, without an uncaught error.
  await expectVisible(page, route.ready(page), diagnostics.describe);
  expect(diagnostics.pageErrors, `uncaught errors on ${route.url}`).toEqual([]);
  await expectLanguage(page, language);
  await expectTheme(page, theme);
  await expectNoViolations(page, route.include);
}

// WP-806 (UI-15): the flows need the modules inventory and stocktake and
// the features stock and assets_basic (E-44); the handlers say so
// explicitly and go ahead of A11Y_HANDLERS.
const FLOW_HANDLERS: Handler[] = [
  {
    path: '/modules',
    reply: () => ({
      body: {
        data: [
          { key: 'inventory', phase: 'P2', nav: ['assets'], enabled: true },
          { key: 'stocktake', phase: 'P2', nav: ['stocktake'], enabled: true },
        ],
      },
    }),
  },
  {
    path: '/entitlements',
    reply: () => ({
      body: list(
        ['assets_basic', 'stock'].map((feature_key) => ({
          organization_id: ORG_ID,
          feature_key,
          plan: 'standard',
          enabled: true,
          limits: {},
          source: 'manual',
        })),
      ),
    }),
  },
];

// WP-807 (UI-15, INV-03): the scan dialog and the signature field need the
// features barcode and signature (E-44) besides those of the flows; the
// entitlements say so ahead of A11Y_HANDLERS.
const SCAN_HANDLERS: Handler[] = [
  FLOW_HANDLERS[0]!,
  {
    path: '/entitlements',
    reply: () => ({
      body: list(
        ['assets_basic', 'stock', 'barcode', 'signature'].map((feature_key) => ({
          organization_id: ORG_ID,
          feature_key,
          plan: 'standard',
          enabled: true,
          limits: {},
          source: 'manual',
        })),
      ),
    }),
  },
];

/**
 * settled waits until exactly one dialog is open and every animation of the
 * page has ended, so axe does not measure the contrast of a dialog that is
 * still fading in (or of one fading out behind it).
 */
async function settled(page: Page) {
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await page.waitForFunction(() =>
    document.getAnimations().every((a) => a.playState !== 'running'),
  );
}

/** openFlow opens a flow dialog of a page by its button and waits for its first picker. */
const openFlow = (button: RegExp) => async (page: Page) => {
  await page.getByRole('button', { name: button }).click();
  await expect(page.getByRole('dialog').getByRole('combobox').first()).toBeVisible();
};

interface A11yDialog {
  name: string;
  /** The page the dialog opens on. */
  url: string;
  /** Handlers ahead of the fixtures, e.g. the features of the flow. */
  handlers?: Handler[];
  /** Opens the dialog and brings it into the state that is checked. */
  prepare: (page: Page) => Promise<void>;
  /** An element whose text colour must not be the browser default black. */
  plainText?: (page: Page) => Locator;
}

// The open dialogs on the data of the a11y fixtures, each on its page
// (WP-324). Radix portals a dialog under <body>, outside the App shell that
// gives the pages their text colour: the unclassed checkbox labels of the
// field editor were black on the dark dialog (1.18:1). The page behind an
// open dialog is hidden (aria-hidden), so axe checks the dialog, once its
// animations have ended.
const DIALOGS: A11yDialog[] = [
  {
    // The CI type administration: organisation type M.ciType with the child
    // M.ciTypeChild, one template.
    name: 'CI type field editor',
    url: '/ci-types',
    prepare: async (page) => {
      await expect(page.getByText(M.ciType).first()).toBeVisible();
      await page.getByRole('treeitem', { name: M.ciType }).click();
      await page.getByRole('button', { name: /^(feld hinzufügen|add field)$/i }).click();
      await page
        .getByRole('dialog')
        .getByRole('checkbox', { name: /^(eindeutig|unique)$/i })
        .check();
      await expect(
        page.getByRole('dialog').getByRole('combobox', { name: /^(eindeutig je|unique per)$/i }),
      ).toBeVisible();
    },
    plainText: (page) =>
      page
        .getByRole('dialog')
        .locator('label')
        .filter({ has: page.getByRole('checkbox', { name: /^(eindeutig|unique)$/i }) }),
  },
  {
    name: 'CI type templates',
    url: '/ci-types',
    prepare: async (page) => {
      await expect(page.getByText(M.ciType).first()).toBeVisible();
      await page.getByRole('button', { name: /^(aus typvorlage|from type template)$/i }).click();
      await expect(page.getByRole('dialog').getByRole('button', { name: /server/i })).toBeVisible();
    },
    plainText: (page) =>
      page
        .getByRole('dialog')
        .getByRole('button', { name: /server/i })
        .locator('p')
        .first(),
  },
  {
    // WP-317: the guided creation with the subtypes of a tile unfolded.
    name: 'guided create',
    url: '/cmdb',
    prepare: async (page) => {
      await expect(page.getByText(M.ci).first()).toBeVisible();
      await page
        .getByRole('button', { name: /^(CI anlegen|Create CI)$/ })
        .first()
        .click();
      const dialog = page.getByRole('dialog');
      const fold = dialog.getByRole('button', { name: /^(Untertypen von|Show subtypes of) / });
      await fold.first().click();
      await expect(
        dialog.getByRole('list', { name: /^(Untertypen von|Subtypes of) / }).first(),
      ).toBeVisible();
    },
  },
  // WP-806 (UI-15): the flow dialogs of the inventory page and the assign
  // dialog of WP-318 with their pickers.
  {
    name: 'inventory receive',
    url: '/inventory',
    handlers: FLOW_HANDLERS,
    prepare: openFlow(/^(empfangen|receive)$/i),
  },
  {
    name: 'inventory move',
    url: '/inventory',
    handlers: FLOW_HANDLERS,
    prepare: openFlow(/^(verschieben|move)$/i),
  },
  {
    name: 'inventory reserve',
    url: '/inventory',
    handlers: FLOW_HANDLERS,
    prepare: openFlow(/^(reservieren|reserve)$/i),
  },
  {
    name: 'assignments assign',
    url: '/assignments',
    handlers: FLOW_HANDLERS,
    prepare: openFlow(/^(zuweisen|assign)$/i),
  },
  // WP-807 (UI-15, INV-03): the scan dialog in manual entry and the assign
  // dialog with the signature field.
  {
    name: 'scan (manual entry)',
    url: '/assignments',
    handlers: SCAN_HANDLERS,
    prepare: async (page) => {
      // No camera: the dialog reads codes from the text field only.
      await page.evaluate(() =>
        Object.defineProperty(navigator, 'mediaDevices', { value: undefined, configurable: true }),
      );
      await expect(page.getByRole('button', { name: /^(zuweisen|assign)$/i })).toBeVisible();
      await page.keyboard.press('Control+k');
      await page.getByRole('combobox').fill('Scan');
      await page.getByRole('option', { name: /^(scannen|scan)/i }).click();
      await expect(
        page.getByRole('dialog').getByLabel(/^(code \(handscanner|code \(handheld)/i),
      ).toBeVisible();
    },
  },
  {
    name: 'assignments assign with signature',
    url: '/assignments',
    handlers: SCAN_HANDLERS,
    prepare: async (page) => {
      await page.getByRole('button', { name: /^(zuweisen|assign)$/i }).click();
      await expect(
        page.getByRole('dialog').getByRole('img', { name: /^(unterschrift|signature)/i }),
      ).toBeVisible();
    },
  },
  {
    // WP-323: a new edge of the CI with the attribute form of its type.
    name: 'relationship with attributes',
    url: `/cmdb/${CI_ID}`,
    handlers: [RELATIONSHIP_TYPES],
    prepare: async (page) => {
      await expect(page.getByRole('heading', { name: M.ci })).toBeVisible();
      await page.getByRole('button', { name: /^(Beziehung hinzufügen|Add relationship)$/ }).click();
      const dialog = page.getByRole('dialog');
      await dialog
        .getByRole('combobox', { name: /^(Beziehungstyp|Relationship type)$/ })
        .selectOption({ label: 'a11y backs up' });
      await expect(dialog.getByLabel(/^(Zeitplan|Schedule)/)).toBeVisible();
    },
  },
  {
    // WP-800: the migration of the CIs of a template version.
    name: 'template migration',
    url: '/ci-types/templates',
    handlers: [TEMPLATE_PERMISSIONS],
    prepare: async (page) => {
      await page
        .getByRole('button', { name: /^(Template migrieren|Migrate template): / })
        .first()
        .click();
      await expect(
        page
          .getByRole('dialog')
          .getByRole('combobox', { name: /^(Ausgangsversion|Source version)/ }),
      ).toBeVisible();
    },
  },
];

// The open states of the P2 pages (WP-324): the bulk bar of a selection
// (WP-816), the impact panel of the failure simulation (WP-818) and the
// opened section of the business calendars (WP-815).
const OPEN_STATES: (Pick<A11yRoute, 'url' | 'handlers' | 'ready' | 'prepare'> & {
  name: string;
})[] = [
  {
    name: '/cmdb with selected rows',
    url: '/cmdb',
    prepare: async (page) => {
      await page
        .getByRole('checkbox', { name: new RegExp(`^(${M.ci} auswählen|Select ${M.ci})$`) })
        .check();
    },
    ready: (page) => page.getByRole('toolbar', { name: /^(Massenbearbeitung|Bulk edit)$/ }),
  },
  {
    name: '/topology with impact panel',
    url: `/topology?simulate=${CI_ID}`,
    ready: (page) =>
      page.getByRole('list', {
        name: /^(Single Points of Failure der Services|Single points of failure of the services)/,
      }),
  },
  {
    name: '/slas with business calendars open',
    url: '/slas',
    prepare: async (page) => {
      const toggle = page.getByRole('button', { name: /^(Business-Kalender|Business calendars)/ });
      await toggle.click();
      await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    },
    ready: (page) => page.locator('#main-content td', { hasText: 'a11y Bürozeiten' }),
  },
];

test.describe('accessibility (WCAG 2.1 AA)', () => {
  test('every route of the router has a check', () => {
    const covered = ROUTES.map((route) => route.path).sort();
    expect(covered, 'routes of src/App.tsx without an entry in ROUTES (or stale entries)').toEqual(
      [...new Set(routerPaths())].sort(),
    );
  });

  // The theme toggle cycles relative to the OS theme (system -> opposite of
  // the OS -> OS theme -> system): every click away from system changes the
  // page, a light theme stored on a dark OS returns to following the OS, and
  // the toggle and the page follow an OS change at runtime (no stale label).
  test.describe('theme toggle', () => {
    test.use({ locale: 'de-DE' });

    test('cycles relative to the OS theme', async ({ page }) => {
      await mockBackend(page, A11Y_HANDLERS);
      await signIn(page);
      await storeTheme(page, 'light');
      await page.emulateMedia({ colorScheme: 'dark' });
      await page.goto('/dashboard');
      const toggle = page.getByRole('button', { name: /^Dunkelmodus umschalten/ });
      const named = (current: string, next: string) =>
        expect(toggle).toHaveAccessibleName(
          `Dunkelmodus umschalten – aktuell: ${current}, als Nächstes: ${next}`,
        );

      // Dark OS: light -> dark (the OS theme) -> system.
      await named('Hell', 'Dunkel');
      await expectTheme(page, 'light');
      await toggle.click();
      await named('Dunkel', 'System');
      await expectTheme(page, 'dark');
      await toggle.click();
      await named('System (Dunkel)', 'Hell');
      await expectTheme(page, 'dark');
      // The OS turns light: the page and the next step follow it.
      await page.emulateMedia({ colorScheme: 'light' });
      await named('System (Hell)', 'Dunkel');
      await expectTheme(page, 'light');
      expect(await page.evaluate(() => localStorage.getItem('reticora-theme-mode'))).toBe('system');
      // Light OS: system -> dark -> light, each click changes the page; only
      // the return to system keeps it.
      await toggle.click();
      await named('Dunkel', 'Hell');
      await expectTheme(page, 'dark');
      await toggle.click();
      await named('Hell', 'System');
      await expectTheme(page, 'light');
      // An explicit theme stays on an OS change, its next step follows the OS.
      await page.emulateMedia({ colorScheme: 'dark' });
      await named('Hell', 'Dunkel');
      await expectTheme(page, 'light');
      await page.emulateMedia({ colorScheme: 'light' });
      await named('Hell', 'System');
      await toggle.click();
      await named('System (Hell)', 'Dunkel');
      await expectTheme(page, 'light');
      expect(await page.evaluate(() => localStorage.getItem('reticora-theme-mode'))).toBe('system');
    });
  });

  // Each check runs once per UI language (E-99): the browser language picks
  // it (src/i18n), and checkPage asserts the lang attribute it sets.
  for (const language of LANGUAGES) {
    test.describe(language, () => {
      test.use({ locale: language });

      for (const route of ROUTES) {
        for (const theme of THEMES) {
          test(`page ${route.path} (${themeName(theme)})`, async ({ page }) => {
            await checkPage(page, route, theme, language);
          });
        }
      }

      for (const dialog of DIALOGS) {
        for (const theme of THEMES) {
          test(`dialog ${dialog.name} (${themeName(theme)})`, async ({ page }) => {
            await checkPage(
              page,
              {
                url: dialog.url,
                handlers: dialog.handlers,
                prepare: async (p) => {
                  await dialog.prepare(p);
                  await settled(p);
                },
                ready: (p) => p.getByRole('dialog'),
                include: '[role="dialog"]',
              },
              theme,
              language,
            );
            if (dialog.plainText) {
              await expect(dialog.plainText(page)).not.toHaveCSS('color', 'rgb(0, 0, 0)');
            }
          });
        }
      }

      for (const state of OPEN_STATES) {
        for (const theme of THEMES) {
          test(`page ${state.name} (${themeName(theme)})`, async ({ page }) => {
            await checkPage(page, state, theme, language);
          });
        }
      }

      // The report of an import dry run with its row errors page by page
      // (GET /imports/{id}/errors, finding of WP-322).
      for (const theme of THEMES) {
        test(`page /imports report with row errors (${themeName(theme)})`, async ({ page }) => {
          await checkPage(
            page,
            {
              url: '/imports',
              handlers: IMPORT_REPORT_HANDLERS,
              prepare: async (p) => {
                await p.locator('#main-content input[type="file"]').setInputFiles({
                  name: 'a11y-import.csv',
                  mimeType: 'text/csv',
                  buffer: Buffer.from('Hostname;Seriennummer\na11y-import-srv;SN-1\n'),
                });
                await p
                  .getByRole('button', { name: /Weiter zur Zuordnung|Continue to mapping/ })
                  .click();
                await p
                  .getByLabel(/^(Profil|Profile)$/)
                  .selectOption('7b000000-0000-4000-8000-0000000000a1');
                await p
                  .getByRole('button', { name: /Weiter zum Prüfen|Continue to check/ })
                  .click();
                await p.getByRole('button', { name: /Dry Run starten|Start dry run/ }).click();
              },
              ready: (p) => p.locator('#main-content td', { hasText: 'a11y-import-error' }),
            },
            theme,
            language,
          );
        });
      }

      // WP-524 (E-87): the CI detail of a CI outside the client chosen in the
      // scope control, with the notice, the counter of the hidden relationships
      // and, after "show all", the outside badge.
      for (const theme of THEMES) {
        test(`page /cmdb/:id outside the chosen scope (${themeName(theme)})`, async ({ page }) => {
          const otherClient = 'c7000000-0000-4000-8000-0000000000aa';
          await page.addInitScript((clientId) => {
            window.localStorage.setItem('reticora-scope', JSON.stringify({ clientId, siteId: '' }));
          }, otherClient);
          const clients = [{ id: otherClient, name: 'a11y Other Client', slug: 'a11y-other' }];
          await checkPage(
            page,
            {
              url: `/cmdb/${CI_ID}`,
              handlers: [
                {
                  path: '/clients',
                  reply: () => ({
                    body: { data: clients, total: 1, limit: 200, offset: 0, has_more: false },
                  }),
                },
              ],
              prepare: async (p) => {
                await p.getByRole('button', { name: /alle anzeigen|show all/i }).click();
              },
              ready: (p) => p.getByRole('status', { name: /arbeitsbereich|scope/i }),
            },
            theme,
            language,
          );
        });
      }

      for (const { url, endpoint, handlers = [] } of EMPTY_PAGES) {
        for (const theme of THEMES) {
          test(`empty state ${url} (${themeName(theme)})`, async ({ page }) => {
            await checkPage(
              page,
              {
                url,
                handlers: [{ path: endpoint, reply: () => ({ body: EMPTY_LIST }) }, ...handlers],
                ready: (p) => p.locator('#main-content td[colspan]'),
              },
              theme,
              language,
            );
          });
        }
      }
    });
  }
});
