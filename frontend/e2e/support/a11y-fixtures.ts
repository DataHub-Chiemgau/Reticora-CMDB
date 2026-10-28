import { CI_TYPES, list } from './backend';
import type { Handler, Reply } from './backend';

// Data of the accessibility check (UI-18/NFR-07, WP-522): every list the
// pages read holds at least one row, so axe checks the rendered rows, badges
// and links instead of empty states only. The records follow the shapes of
// api/openapi.yaml and src/api/client.ts. Each record carries a name the spec
// waits for (A11Y_MARKERS), so a page that renders no row fails visibly.

const ORG = '00000000-0000-0000-0000-0000000000aa';
const USER = '00000000-0000-0000-0000-000000000001';
const CLIENT = 'c0000000-0000-4000-8000-000000000001';
const SITE = '51000000-0000-4000-8000-000000000001';
const BUILDING = 'b1000000-0000-4000-8000-000000000001';
const ROOM = 'a1000000-0000-4000-8000-000000000001';
const RACK = 'ac000000-0000-4000-8000-000000000001';
const COLLECTOR = 'c1000000-0000-4000-8000-000000000001';
const ASSET = 'a5000000-0000-4000-8000-000000000001';
const ROLE = 'e0000000-0000-4000-8000-000000000001';
const SERVER_TYPE = CI_TYPES[0]!.id;
const AT = '2026-10-14T08:00:00Z';
const LATER = '2026-12-31T08:00:00Z';

/** The CI whose detail page /cmdb/:id the spec opens. */
export const CI_ID = 'c1a00000-0000-4000-8000-000000000001';
const CI_PEER = 'c1a00000-0000-4000-8000-000000000002';

/** Texts of the fixture rows, one per page, that must be visible. */
export const A11Y_MARKERS = {
  ci: 'a11y-srv-01',
  ciPeer: 'a11y-sw-01',
  asset: 'a11y-notebook-01',
  assignment: 'a11y-assignee',
  document: 'a11y-betriebshandbuch',
  stocktake: 'a11y-inventur-2026',
  ticket: 'a11y-ticket-drucker',
  collector: 'a11y-collector-01',
  site: 'a11y-site-hamburg',
  building: 'a11y-building-a',
  room: 'a11y-room-101',
  rack: 'a11y-rack-01',
  location: 'a11y-location-site',
  savedView: 'a11y-saved-view',
  ciType: 'a11y-org-type',
  consumable: 'a11y-toner',
  order: 'a11y-order-title',
  maintenance: 'a11y-maintenance-window',
  disposal: 'a11y-certificate-77',
  key: 'a11y-key-main',
  training: 'a11y-training-fire',
  desk: 'a11y-desk-01',
  agent: 'a11y-agent-host',
  finding: 'a11y-finding-openssl',
  review: 'a11y-review-device',
  job: 'export.csv',
  user: 'a11y-user-anna',
  permission: 'a11y permission description',
  sla: 'a11y-sla-gold',
  form: 'a11y-form-onboarding',
  workflow: 'a11y-workflow-escalation',
  compliance: 'a11y-rule-firmware',
  iga: 'a11y-connector-scim',
  conversation: 'a11y-conversation',
  webhook: 'a11y-webhook-erp',
  alert: 'a11y-alert-cpu',
  audit: 'ci.update',
  quantityItem: 'a11y-cable',
  notification: 'a11y-notification',
} as const;

const M = A11Y_MARKERS;

const ci = {
  id: CI_ID,
  organization_id: ORG,
  client_id: CLIENT,
  site_id: SITE,
  room_id: ROOM,
  location_id: ROOM,
  version: 3,
  ci_type_id: SERVER_TYPE,
  name: M.ci,
  status: 'active',
  health: 'ok',
  manufacturer: 'Dell',
  model: 'R750',
  serial_number: 'SN-0001',
  management_ip: '10.0.0.10',
  firmware_version: '2.1.0',
  attributes: { cpu_cores: 32 },
  discovery_source: 'snmp',
  first_seen_at: AT,
  last_seen_at: AT,
  created_at: AT,
  updated_at: AT,
};

const ciPeer = {
  ...ci,
  id: CI_PEER,
  ci_type_id: CI_TYPES[1]!.id,
  name: M.ciPeer,
  status: 'maintenance',
  management_ip: '10.0.0.2',
};

const graph = {
  nodes: [
    { id: CI_ID, name: M.ci, ci_type: 'server', status: 'active', site_id: SITE },
    { id: CI_PEER, name: M.ciPeer, ci_type: 'switch', status: 'maintenance', site_id: SITE },
  ],
  edges: [{ id: 'e-1', source_ci_id: CI_ID, target_ci_id: CI_PEER, rel_type: 'connected_to' }],
};

const relationship = {
  id: 'f1000000-0000-4000-8000-000000000001',
  organization_id: ORG,
  source_ci_id: CI_ID,
  target_ci_id: CI_PEER,
  rel_type: 'connected_to',
  attributes: {},
  source: 'discovery',
  confidence: 0.9,
  verification_state: 'verified',
  created_at: AT,
  updated_at: AT,
};

const site = {
  id: SITE,
  organization_id: ORG,
  client_id: CLIENT,
  name: M.site,
  address: 'Hafenstraße 1, Hamburg',
  geo_lat: 53.54,
  geo_lon: 9.98,
  timezone: 'Europe/Berlin',
  created_at: AT,
  updated_at: AT,
};

const location = {
  id: SITE,
  organization_id: ORG,
  client_id: CLIENT,
  site_id: SITE,
  kind: 'site',
  name: M.location,
  path: SITE.replace(/-/g, '_'),
  created_at: AT,
  updated_at: AT,
  children: [
    {
      id: BUILDING,
      organization_id: ORG,
      client_id: CLIENT,
      site_id: SITE,
      parent_id: SITE,
      kind: 'building',
      name: M.building,
      path: `${SITE}.${BUILDING}`.replace(/-/g, '_'),
      created_at: AT,
      updated_at: AT,
      children: [],
    },
  ],
};

const asset = {
  id: ASSET,
  organization_id: ORG,
  client_id: CLIENT,
  ci_id: CI_ID,
  asset_tag: 'AT-0001',
  name: M.asset,
  category: 'hardware',
  status: 'assigned',
  supplier: 'Bechtle',
  location: 'Hamburg',
  serial_number: 'SN-A-1',
  custom_fields: {},
  created_at: AT,
  updated_at: AT,
};

const collector = {
  id: COLLECTOR,
  organization_id: ORG,
  client_id: CLIENT,
  site_id: SITE,
  name: M.collector,
  version: '1.4.0',
  status: 'active',
  last_heartbeat_at: AT,
  cert_not_after: LATER,
  config: {},
  created_at: AT,
  updated_at: AT,
};

const job = {
  id: 'd1000000-0000-4000-8000-000000000001',
  kind: M.job,
  status: 'running',
  progress: 40,
  attempts: 1,
  max_attempts: 3,
  created_by: USER,
  created_at: AT,
  started_at: AT,
};

const role = {
  id: ROLE,
  organization_id: ORG,
  name: 'a11y-role-auditor',
  description: 'Read-only auditor',
  is_system: false,
  is_builtin: false,
  permissions: ['ci:read'],
  created_at: AT,
  updated_at: AT,
};

const scopeSuggestion = {
  id: 'd2000000-0000-4000-8000-000000000001',
  kind: 'scope_suggested',
  status: 'open',
  payload: { collector_id: COLLECTOR, collector_name: M.collector, cidr: '10.0.1.0/24' },
  escalated: false,
  created_at: AT,
};

const reviewItem = {
  id: 'd2000000-0000-4000-8000-000000000002',
  kind: 'unclassified_device',
  status: 'open',
  payload: { name: M.review, ip: '10.0.0.77', hostname: M.review },
  candidate_ci_ids: [CI_ID],
  escalated: false,
  created_at: AT,
};

const compliance = {
  overall: { passed: 8, failed: 2, not_applicable: 1, score: 80 },
  by_ci_type: [{ ci_type_id: SERVER_TYPE, passed: 8, failed: 2, not_applicable: 1, score: 80 }],
  results: [
    {
      id: 'd3000000-0000-4000-8000-000000000001',
      organization_id: ORG,
      rule_id: 'd3000000-0000-4000-8000-000000000002',
      ci_id: CI_ID,
      ci_type_id: SERVER_TYPE,
      status: 'fail',
      details: 'firmware below 2.2',
      evaluated_at: AT,
      created_at: AT,
      updated_at: AT,
    },
  ],
};

const fixed = (body: unknown) => (): Reply => ({ body });

/**
 * A11Y_HANDLERS answer every read of the pages with data. Paths are matched
 * below /api/v1 without the query (support/backend.ts).
 */
export const A11Y_HANDLERS: Handler[] = [
  // Application shell: organization, scope, navigation and notifications.
  {
    path: '/org',
    reply: fixed({
      id: ORG,
      name: 'a11y Org',
      slug: 'a11y-org',
      plan: 'enterprise',
      settings: {},
      timezone: 'Europe/Berlin',
      currency: 'EUR',
      locale: 'de-DE',
      ai_opt_in: true,
      created_at: AT,
      updated_at: AT,
    }),
  },
  {
    path: '/clients',
    reply: fixed(
      list([
        {
          id: CLIENT,
          organization_id: ORG,
          name: 'a11y Client',
          slug: 'a11y-client',
          external_ref: null,
          timezone: null,
          settings: {},
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  { path: '/sites', reply: fixed(list([site])) },
  {
    path: '/notifications',
    reply: fixed(
      list([
        {
          id: 'd4000000-0000-4000-8000-000000000001',
          category: 'job',
          subject: M.notification,
          body: 'Export finished',
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/modules',
    reply: fixed({ data: [{ key: 'inventory', phase: 'P2', nav: ['assets'], enabled: true }] }),
  },
  {
    path: '/me/permissions',
    reply: fixed({
      user_id: USER,
      permissions: [
        'ci:read',
        'ci:write',
        'collector:manage',
        'discovery:manage',
        'discovery:read',
        'review:resolve',
        'saved_view:write',
        'ticket:write',
        'user:manage',
        'user:read',
        'webhook:manage',
        'webhook:read',
      ],
    }),
  },
  // CMDB: CI list, detail and its sections, topology.
  { path: '/cis', reply: fixed({ ...list([ci, ciPeer]), total: 2 }) },
  { path: `/cis/${CI_ID}`, reply: fixed(ci) },
  { path: `/cis/${CI_PEER}`, reply: fixed(ciPeer) },
  { path: /^\/cis\/[^/]+\/relationships$/, reply: fixed(list([relationship])) },
  { path: /^\/cis\/[^/]+\/parent$/, reply: () => ({ status: 404, body: { status: 404 } }) },
  {
    path: /^\/cis\/[^/]+\/fields$/,
    reply: fixed({
      data: [
        {
          id: 'd5000000-0000-4000-8000-000000000001',
          ci_id: CI_ID,
          field_name: 'firmware_version',
          discovered_value: '2.1.0',
          discovered_source: 'snmp',
          discovered_at: AT,
          protected: false,
          effective_value: '2.1.0',
          diverged: false,
        },
      ],
    }),
  },
  {
    path: /^\/cis\/[^/]+\/provenance$/,
    reply: fixed({
      data: [
        {
          field: 'firmware_version',
          value: '2.1.0',
          source: 'snmp',
          rank: 1,
          reason: 'source_rank',
          observed_at: AT,
          diverged: false,
        },
      ],
    }),
  },
  { path: /^\/cis\/[^/]+\/field-definitions$/, reply: fixed({ data: [] }) },
  {
    path: /^\/cis\/[^/]+\/metrics$/,
    reply: fixed({
      ci_id: CI_ID,
      from: AT,
      series: [
        {
          name: 'cpu_usage',
          points: [
            { timestamp: AT, value: 12 },
            { timestamp: '2026-10-14T08:05:00Z', value: 18 },
          ],
        },
      ],
    }),
  },
  {
    path: /^\/cis\/[^/]+\/blast-radius$/,
    reply: fixed({
      ci_id: CI_ID,
      affected_cis: [],
      affected_count: 0,
      heuristic_count: 0,
    }),
  },
  {
    path: /^\/history\/[^/]+\/[^/]+$/,
    reply: fixed({
      ...list([
        {
          id: 'd6000000-0000-4000-8000-000000000001',
          entity_type: 'ci',
          entity_id: CI_ID,
          actor_id: USER,
          change_type: 'updated',
          change_kind: 'updated',
          field_name: 'status',
          old_value: 'planned',
          new_value: 'active',
          field_changes: [{ field: 'status', old: 'planned', new: 'active' }],
          change_source: 'ui',
          created_at: AT,
        },
      ]),
      audit_included: false,
    }),
  },
  { path: /^\/topology\/cis\/[^/]+\/neighbors$/, reply: fixed(graph) },
  { path: '/topology', reply: fixed(graph) },
  { path: '/relationship-types', reply: fixed(list([])) },
  // Locations, room plan and racks.
  { path: '/locations/tree', reply: fixed({ data: [location] }) },
  {
    path: '/buildings',
    reply: fixed(
      list([
        {
          id: BUILDING,
          organization_id: ORG,
          site_id: SITE,
          name: M.building,
          floors: 2,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/rooms',
    reply: fixed(
      list([
        {
          id: ROOM,
          organization_id: ORG,
          building_id: BUILDING,
          name: M.room,
          floor: 1,
          layout: { [CI_ID]: { x: 0.3, y: 0.4 } },
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: `/rooms/${ROOM}`,
    reply: fixed({
      id: ROOM,
      organization_id: ORG,
      building_id: BUILDING,
      name: M.room,
      floor: 1,
      layout: { [CI_ID]: { x: 0.3, y: 0.4 } },
      created_at: AT,
      updated_at: AT,
    }),
  },
  {
    path: '/racks',
    reply: fixed(
      list([
        {
          id: RACK,
          organization_id: ORG,
          room_id: ROOM,
          name: M.rack,
          height_units: 42,
          width_mm: 600,
          depth_mm: 1000,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: `/racks/${RACK}/layout`,
    reply: fixed({
      rack: {
        id: RACK,
        organization_id: ORG,
        room_id: ROOM,
        name: M.rack,
        height_units: 42,
        width_mm: 600,
        depth_mm: 1000,
        created_at: AT,
        updated_at: AT,
      },
      mounts: [
        {
          id: 'd7000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          rack_id: RACK,
          ci_id: CI_ID,
          position_u: 10,
          height_u: 2,
          face: 'front',
          ci_name: M.ci,
          ci_type: 'server',
          created_at: AT,
          updated_at: AT,
        },
      ],
      free: { front: [1, 2, 3], rear: [1, 2, 3] },
    }),
  },
  // Discovery, reviews and jobs.
  { path: '/collectors', reply: fixed(list([collector])) },
  {
    path: '/discovery-scopes',
    reply: fixed(
      list([
        {
          id: 'd8000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          client_id: CLIENT,
          site_id: SITE,
          collector_id: COLLECTOR,
          name: 'a11y-scope-lan',
          kind: 'cidr',
          target: '10.0.0.0/24',
          schedule: '@hourly',
          credential_ids: [],
          plugins: ['sweep', 'snmp'],
          enabled: true,
          last_run_at: AT,
          health: { state: 'ok' },
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/review-items',
    reply: (_request, url) =>
      url.searchParams.get('kind') === 'scope_suggested'
        ? { body: list([scopeSuggestion]) }
        : { body: list([reviewItem]) },
  },
  { path: '/jobs', reply: fixed(list([job])) },
  { path: '/credentials', reply: fixed(list([])) },
  { path: '/vrfs', reply: fixed(list([])) },
  // Assets and the modules around them.
  { path: '/assets', reply: fixed(list([asset])) },
  {
    path: '/assignments',
    reply: fixed(
      list([
        {
          id: 'd9000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          asset_id: ASSET,
          assigned_to: M.assignment,
          assigned_by: USER,
          assignment_type: 'user',
          status: 'active',
          assigned_at: AT,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/documents',
    reply: fixed(
      list([
        {
          id: 'da000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          title: M.document,
          file_name: 'handbuch.pdf',
          file_size: 204800,
          mime_type: 'application/pdf',
          storage_key: 'docs/handbuch.pdf',
          version: 1,
          category: 'manual',
          tags: ['betrieb'],
          uploaded_by: USER,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/stocktakes',
    reply: fixed(
      list([
        {
          id: 'db000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          title: M.stocktake,
          status: 'in_progress',
          scope: 'full',
          total_expected: 10,
          total_scanned: 7,
          total_missing: 1,
          total_surplus: 0,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/tickets',
    reply: fixed(
      list([
        {
          id: 'dc000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          ticket_number: 42,
          title: M.ticket,
          status: 'open',
          priority: 'high',
          category: 'incident',
          reporter_id: USER,
          tags: [],
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/stock-movements',
    reply: fixed(
      list([
        {
          id: 'dd000000-0000-4000-8000-000000000001',
          item_kind: 'asset',
          asset_id: ASSET,
          movement_type: 'transfer',
          from_location_id: SITE,
          to_location_id: BUILDING,
          actor_id: USER,
          reason: 'a11y-move',
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/inventory/items',
    reply: fixed(
      list([
        {
          id: 'de000000-0000-4000-8000-000000000001',
          sku: 'CBL-1',
          name: M.quantityItem,
          category: 'cable',
          unit: 'pcs',
          stock_level: 3,
          min_level: 5,
        },
      ]),
    ),
  },
  {
    path: '/reservations',
    reply: fixed(
      list([
        {
          id: 'df000000-0000-4000-8000-000000000001',
          item_kind: 'asset',
          asset_id: ASSET,
          quantity: 1,
          state: 'active',
          reserved_from: AT,
          reserved_until: LATER,
          project_ref: 'a11y-project',
        },
      ]),
    ),
  },
  {
    path: '/inventory/availability',
    reply: fixed({
      data: [
        {
          item_kind: 'asset',
          item_id: ASSET,
          total: 1,
          available: 0,
          reserved: 1,
          assigned: 0,
          repair: 0,
          unavailable: 0,
        },
      ],
    }),
  },
  {
    path: '/consumables',
    reply: fixed(
      list([
        {
          id: 'e1000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          name: M.consumable,
          sku: 'TN-1',
          category: 'printer',
          unit: 'pcs',
          stock_level: 2,
          min_level: 4,
          location: 'Lager',
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/orders',
    reply: fixed(
      list([
        {
          id: 'e2000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          order_number: 'PO-1001',
          title: M.order,
          status: 'submitted',
          supplier: 'Bechtle',
          total_cost: 1200,
          currency: 'EUR',
          items: [
            {
              id: 'e2000000-0000-4000-8000-000000000002',
              description: 'Notebook',
              quantity: 1,
              unit_price: 1200,
            },
          ],
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/maintenance-windows',
    reply: fixed(
      list([
        {
          id: 'e3000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          title: M.maintenance,
          description: 'Firmware',
          starts_at: AT,
          ends_at: LATER,
          status: 'scheduled',
          ci_ids: [CI_ID],
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/disposal-records',
    reply: fixed(
      list([
        {
          id: 'e4000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          asset_id: ASSET,
          method: 'shredding',
          certificate_ref: M.disposal,
          data_carrier: 'hdd',
          performed_by: USER,
          performed_at: AT,
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/keys',
    reply: fixed(
      list([
        {
          id: 'e5000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          name: M.key,
          key_type: 'physical',
          identifier: 'K-1',
          status: 'available',
          location: 'Empfang',
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/trainings',
    reply: fixed(
      list([
        {
          id: 'e6000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          title: M.training,
          category: 'safety',
          validity_months: 12,
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/desks',
    reply: fixed(
      list([
        {
          id: 'e7000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          room_id: ROOM,
          name: M.desk,
          status: 'available',
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/agents',
    reply: fixed(
      list([
        {
          id: 'e8000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          agent_id: 'agent-1',
          hostname: M.agent,
          version: '1.0.0',
          os: 'linux',
          arch: 'amd64',
          ci_id: CI_ID,
          status: 'active',
          last_heartbeat: AT,
          policy: { interval_seconds: 300, metrics_enabled: true, inventory_enabled: true },
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/security/findings',
    reply: fixed(
      list([
        {
          id: 'e9000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          ci_id: CI_ID,
          kind: 'vulnerability',
          severity: 'high',
          title: M.finding,
          package_name: 'openssl',
          installed_version: '3.0.1',
          fixed_version: '3.0.7',
          reference: 'CVE-2022-3602',
          status: 'open',
          detected_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/security/findings/summary',
    reply: fixed({ by_severity: { high: 1 }, open_total: 1 }),
  },
  // Saved views and CI types.
  {
    path: '/saved-views',
    reply: fixed(
      list([
        {
          id: 'ea000000-0000-4000-8000-000000000001',
          owner_id: USER,
          name: M.savedView,
          entity_kind: 'ci',
          filter_spec: { status: 'active' },
          shared: true,
          shared_with_roles: [],
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/saved-views/presets',
    reply: fixed({
      data: [
        {
          id: 'preset-active',
          name: 'a11y-preset-active',
          entity_kind: 'ci',
          filter_spec: { status: 'active' },
          shared: true,
          created_at: AT,
        },
      ],
    }),
  },
  {
    path: '/ci-types/templates',
    reply: fixed({
      data: [{ ...CI_TYPES[0], id: 'tpl-server', key: 'tpl_server', name: 'a11y-template' }],
    }),
  },
  {
    path: '/ci-types',
    reply: fixed(
      list(
        CI_TYPES.map((type) =>
          type.is_system
            ? type
            : { ...type, name: M.ciType, display_name: { de: M.ciType, en: M.ciType } },
        ),
      ),
    ),
  },
  { path: '/attribute-definitions', reply: fixed({ data: [] }) },
  // Administration: users, teams, roles, permissions.
  {
    path: '/users',
    reply: fixed(
      list([
        {
          id: USER,
          organization_id: ORG,
          email: 'anna@example.com',
          display_name: M.user,
          status: 'active',
          last_login_at: AT,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/teams',
    reply: fixed(
      list([
        {
          id: 'eb000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          name: 'a11y-team-ops',
          member_count: 1,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  { path: '/roles', reply: fixed(list([role])) },
  {
    path: '/permissions',
    reply: fixed(
      list([
        { key: 'ci:read', resource: 'ci', action: 'read', description: M.permission },
        { key: 'ci:write', resource: 'ci', action: 'write', description: 'Write CIs' },
      ]),
    ),
  },
  { path: /^\/roles\/[^/]+\/permissions$/, reply: fixed(list([])) },
  {
    path: '/invitations',
    reply: fixed(
      list([
        {
          id: 'ec000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          email: 'new@example.com',
          role_id: ROLE,
          scope_type: 'org',
          invited_by: USER,
          status: 'pending',
          expires_at: LATER,
          created_at: AT,
        },
      ]),
    ),
  },
  // Service management: SLAs, forms, workflows, compliance, IGA.
  {
    path: '/slas',
    reply: fixed(
      list([
        {
          id: 'ed000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          name: M.sla,
          priority: 'high',
          response_target_minutes: 30,
          resolution_target_minutes: 240,
          business_calendar: true,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/slas/breaches',
    reply: fixed(
      list([
        {
          id: 'ed000000-0000-4000-8000-000000000002',
          organization_id: ORG,
          ticket_id: 'dc000000-0000-4000-8000-000000000001',
          sla_id: 'ed000000-0000-4000-8000-000000000001',
          response_due_at: AT,
          resolution_due_at: LATER,
          response_breached: true,
          resolution_breached: false,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/forms',
    reply: fixed(
      list([
        {
          id: 'ee000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          name: M.form,
          description: 'New employee',
          schema: {
            type: 'object',
            properties: { name: { type: 'string', title: 'Name' } },
            required: ['name'],
          },
          ui_hints: {},
          active: true,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  { path: '/form-submissions', reply: fixed(list([])) },
  {
    path: '/workflows',
    reply: fixed(
      list([
        {
          id: 'ef000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          name: M.workflow,
          description: 'Escalate breached tickets',
          trigger: { event: 'sla.breached' },
          conditions: [],
          actions: [{ type: 'notify' }],
          active: true,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/workflow-runs',
    reply: fixed(
      list([
        {
          id: 'ef000000-0000-4000-8000-000000000002',
          organization_id: ORG,
          workflow_id: 'ef000000-0000-4000-8000-000000000001',
          status: 'waiting_approval',
          trigger: 'manual',
          context: {},
          started_at: AT,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/compliance/rules',
    reply: fixed(
      list([
        {
          id: 'd3000000-0000-4000-8000-000000000002',
          organization_id: ORG,
          ci_type_id: SERVER_TYPE,
          name: M.compliance,
          description: 'Firmware current',
          severity: 'high',
          category: 'patching',
          expression: { field: 'firmware_version', op: 'gte', value: '2.2' },
          remediation_hint: 'Update firmware',
          active: true,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  { path: '/compliance/results', reply: fixed(list(compliance.results)) },
  { path: '/compliance/score', reply: fixed(compliance) },
  {
    path: '/compliance/report',
    reply: fixed({
      generated_at: AT,
      standard: 'iso27001',
      audit_integrity: { intact: true, checked: 120 },
      compliance_score: compliance.overall,
      failures_by_severity: { high: 2 },
      findings: [
        {
          rule_id: 'd3000000-0000-4000-8000-000000000002',
          rule_name: M.compliance,
          severity: 'high',
          category: 'patching',
          remediation_hint: 'Update firmware',
          affected_cis: [CI_ID],
        },
      ],
      capabilities: [{ feature_key: 'mfa', enabled: true }],
    }),
  },
  {
    path: '/privacy/retention',
    reply: fixed({
      id: 'f0000000-0000-4000-8000-000000000001',
      organization_id: ORG,
      retention_days: 365,
      mode: 'anonymize',
      created_at: AT,
      updated_at: AT,
    }),
  },
  {
    path: '/iga/connectors',
    reply: fixed(
      list([
        {
          id: 'f2000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          name: M.iga,
          type: 'scim',
          base_url: 'https://idp.example.com/scim/v2',
          capabilities: { users: true, groups: true },
          status: 'active',
          last_sync_at: AT,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/iga/tasks',
    reply: fixed(
      list([
        {
          id: 'f2000000-0000-4000-8000-000000000002',
          connector_id: 'f2000000-0000-4000-8000-000000000001',
          user_id: USER,
          action: 'add_group_member',
          status: 'failed',
          attempts: 3,
          max_attempts: 3,
          next_run_at: AT,
          error: 'group not found',
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/iga/access-requests',
    reply: fixed(
      list([
        {
          id: 'f2000000-0000-4000-8000-000000000003',
          requester_id: USER,
          subject_user_id: USER,
          connector_id: 'f2000000-0000-4000-8000-000000000001',
          entitlement: 'group:admins',
          reason: 'on call',
          status: 'pending',
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/iga/access-reviews',
    reply: fixed(
      list([
        {
          id: 'f2000000-0000-4000-8000-000000000004',
          name: 'a11y-review-q4',
          status: 'open',
          due_at: LATER,
          created_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/iga/drift',
    reply: fixed(
      list([
        {
          id: 'f2000000-0000-4000-8000-000000000005',
          connector_id: 'f2000000-0000-4000-8000-000000000001',
          external_id: 'ext-1',
          user_id: USER,
          drift_type: 'orphaned_account',
          severity: 'high',
          status: 'open',
          created_at: AT,
        },
      ]),
    ),
  },
  // Assistant, webhooks, export, monitoring, audit.
  {
    path: '/ai/conversations',
    reply: fixed(
      list([
        {
          id: 'f3000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          user_id: USER,
          title: M.conversation,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/webhooks',
    reply: fixed(
      list([
        {
          id: 'f4000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          name: M.webhook,
          url: 'https://erp.example.com/hook',
          events: ['ci.created'],
          is_active: true,
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/webhooks/dead-letters',
    reply: fixed(
      list([
        {
          id: 'f4000000-0000-4000-8000-000000000002',
          delivery_id: 'f4000000-0000-4000-8000-000000000003',
          organization_id: ORG,
          subscription_id: 'f4000000-0000-4000-8000-000000000001',
          event: 'ci.updated',
          attempts: 8,
          last_status_code: 503,
          last_error: 'service unavailable',
          first_attempt_at: AT,
          dead_at: AT,
        },
      ]),
    ),
  },
  { path: /^\/webhooks\/[^/]+\/deliveries$/, reply: fixed(list([])) },
  {
    path: '/exports',
    reply: fixed(
      list([
        {
          id: 'f5000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          initiated_by: USER,
          format: 'csv',
          status: 'completed',
          filters: {},
          row_count: 2,
          file_size_bytes: 512,
          started_at: AT,
          completed_at: AT,
          expires_at: LATER,
          download_url: '/api/v1/exports/f5000000-0000-4000-8000-000000000001/download',
          created_at: AT,
          updated_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/monitoring/alerts',
    reply: fixed(
      list([
        {
          id: 'f6000000-0000-4000-8000-000000000001',
          org_id: ORG,
          name: M.alert,
          metric_name: 'cpu_usage',
          condition: 'gt',
          threshold: 90,
          duration: '5m',
          severity: 'critical',
          enabled: true,
          last_fired_at: AT,
        },
      ]),
    ),
  },
  {
    path: '/monitoring/metrics',
    reply: fixed(list([{ timestamp: AT, value: 12 }])),
  },
  {
    path: '/audit-log',
    reply: fixed(
      list([
        {
          id: 'f7000000-0000-4000-8000-000000000001',
          organization_id: ORG,
          seq: 1,
          timestamp: AT,
          actor_id: USER,
          actor_type: 'user',
          action: M.audit,
          resource_type: 'ci',
          resource_id: CI_ID,
          changes: { status: ['planned', 'active'] },
          source: 'ui',
          hash: 'a'.repeat(64),
        },
      ]),
    ),
  },
];

/**
 * TOPOLOGY_TOO_LARGE is the server's 422 for a topology of more than 2,000
 * CIs without a filter: the page then shows its start dialog.
 */
export const TOPOLOGY_TOO_LARGE: Handler = {
  path: '/topology',
  reply: () => ({
    status: 422,
    body: {
      type: 'https://reticora.io/problems/422',
      title: 'Unprocessable Entity',
      status: 422,
      detail: 'topology too large: choose a site or CI type',
    },
  }),
};
