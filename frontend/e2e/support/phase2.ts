import type { Page } from '@playwright/test';
import { AT, CI_TYPES, ORG_ID, list, problem } from './backend';
import type { Handler, Reply } from './backend';

// The Phase 2 test data of the browser flows (TST-03 [P2], WP-324): typed
// fixtures for the strict mock of support/backend.ts, whose paths and
// answers follow api/openapi.yaml (the mock checks each of them, WP-530).
// They hold CI types with inheritance, a template, layouts and rules, an
// asset with contract and supplier, a consumable with bins and stock, an
// order, a stocktake, a document with versions, tickets with SLA and
// business calendar, an import profile, tags and relationship types. A spec
// takes the handlers it needs and installs them with mockBackend ahead of
// the DEFAULTS of support/backend.ts.
//
// Two additions to support/backend.ts: signInWith stores a session whose
// user carries rights (flows that read user.permissions, e.g.
// metamodel:publish), and mePermissions answers GET /me/permissions
// (useEffectivePermissions) ahead of the DEFAULTS, which only grant
// ci:read, discovery:read and review:resolve. Pages without a rights check
// keep using signIn.

export const USER_ID = '00000000-0000-0000-0000-000000000001';

const fixed =
  (body: unknown, status = 200) =>
  (): Reply => ({ status, body });

/** signInWith stores a session like signIn whose user holds the given rights. */
export async function signInWith(page: Page, permissions: string[]) {
  await page.addInitScript((rights) => {
    window.sessionStorage.setItem(
      'reticora-auth-session',
      JSON.stringify({
        token: 'header.payload.signature',
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        user: {
          sub: '00000000-0000-0000-0000-000000000001',
          name: 'Admin',
          org_id: '00000000-0000-0000-0000-0000000000aa',
          permissions: rights,
        },
      }),
    );
  }, permissions);
}

/** mePermissions answers GET /me/permissions with the given effective rights. */
export function mePermissions(permissions: string[]): Handler {
  return {
    path: '/me/permissions',
    reply: fixed({ user_id: USER_ID, permissions }),
  };
}

// --- CI types with inheritance, template, layouts and rules (MET-03, MET-16, MET-45, MET-50)

const SERVER_TYPE = CI_TYPES.find((type) => type.key === 'server')!;

function p2Type(id: string, key: string, de: string, en: string, extra: Record<string, unknown>) {
  return {
    ...SERVER_TYPE,
    id,
    organization_id: ORG_ID,
    key,
    name: key,
    display_name: { de, en },
    is_builtin: false,
    is_system: false,
    ...extra,
  };
}

/** NETWORK_TYPE is an organization type with two subtypes (parent_type_id, WP-248). */
export const NETWORK_TYPE = p2Type(
  '5e1b0c3a-7d2f-4a10-9c01-0000000000b1',
  'p2_network_device',
  'Netzwerkgerät',
  'Network device',
  { category: 'network', icon: 'network' },
);
export const ACCESS_SWITCH_TYPE = p2Type(
  '5e1b0c3a-7d2f-4a10-9c01-0000000000b2',
  'p2_access_switch',
  'Access-Switch',
  'Access switch',
  { category: 'network', icon: 'router', parent_type_id: NETWORK_TYPE.id },
);
export const CORE_SWITCH_TYPE = p2Type(
  '5e1b0c3a-7d2f-4a10-9c01-0000000000b3',
  'p2_core_switch',
  'Core-Switch',
  'Core switch',
  { category: 'network', icon: 'router', parent_type_id: NETWORK_TYPE.id },
);
/** P2_TYPES are the types of GET /ci-types in the Phase 2 tenant. */
export const P2_TYPES = [...CI_TYPES, NETWORK_TYPE, ACCESS_SWITCH_TYPE, CORE_SWITCH_TYPE];

/** P2_TYPE_HANDLERS answer GET /ci-types and GET /ci-types/{id} with the hierarchy. */
export const P2_TYPE_HANDLERS: Handler[] = [
  { path: '/ci-types', reply: fixed(list(P2_TYPES)) },
  {
    path: /^\/ci-types\/[0-9a-f-]{36}$/,
    reply: (_request, url) => {
      const type = P2_TYPES.find((t) => url.pathname.endsWith(`/ci-types/${t.id}`));
      return type ? { body: type } : { status: 404 };
    },
  },
];

/** The template (variant) of the access switch with a published version (WP-251, WP-314). */
export const ACCESS_TEMPLATE = {
  id: '7e000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  key: 'p2_access_24',
  ci_type_id: ACCESS_SWITCH_TYPE.id,
  display_name: { de: 'Access 24 Port', en: 'Access 24 port' },
  description: 'Access switch with 24 ports',
  is_system: false,
  is_active: true,
  current_version: 1,
  created_at: AT,
  updated_at: AT,
};

/** resolvedField is a field of a resolved form (GET /ci-types/{id}/form). */
export function resolvedField(
  name: string,
  label: string,
  data_type: string,
  extra: Record<string, unknown> = {},
  state: Record<string, unknown> = {},
  width = 12,
) {
  return {
    name,
    width,
    definition: { name, label, data_type, required: false, sort_order: 0, ...extra },
    state: { visible: true, required: false, read_only: false, ...state },
  };
}

/**
 * accessSwitchForm is the resolved form of the access switch: the
 * inherited field of the network device and, with the template, its
 * default for the port count.
 */
export function accessSwitchForm(templateId: string | null) {
  return {
    ci_type_id: ACCESS_SWITCH_TYPE.id,
    source: 'default',
    sections: [
      {
        key: 'identity',
        title: { de: 'identity', en: 'identity' },
        visible: true,
        fields: [
          resolvedField('manufacturer', 'Manufacturer', 'text'),
          resolvedField('model', 'Model', 'text'),
          resolvedField('p2_uplink', 'Uplink', 'text'),
          resolvedField('p2_ports', 'Ports', 'integer', templateId ? { default_value: '24' } : {}),
        ],
      },
    ],
    rules: [],
  };
}

/** SERVICE_ROLE is the role a layout and the preview are made for. */
export const SERVICE_ROLE = {
  id: 'e0000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  name: 'Servicedesk',
  description: 'First level',
  is_system: false,
  permissions: ['ci:read', 'ci:write'],
  created_at: AT,
  updated_at: AT,
};

function attributeDefinition(
  id: string,
  key: string,
  label: string,
  extra: Record<string, unknown> = {},
) {
  return {
    id,
    organization_id: ORG_ID,
    ci_type_id: ACCESS_SWITCH_TYPE.id,
    scope: 'type',
    key,
    display_name: { de: label, en: label },
    data_type: 'text',
    required: false,
    multi_value: false,
    searchable: false,
    unique: false,
    unique_scope: 'org',
    inheritable: true,
    order: 0,
    is_system: false,
    applies_to: 'all',
    applies_to_categories: [],
    applies_to_type_ids: [],
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

/**
 * The attributes of the access switch (WP-313): two own, one inherited from
 * the network device and one global attribute.
 */
export const ACCESS_ATTRIBUTES = [
  attributeDefinition('ad000000-0000-4000-8000-0000000000b1', 'p2_ports', 'Ports', {
    data_type: 'integer',
    order: 1,
  }),
  attributeDefinition('ad000000-0000-4000-8000-0000000000b2', 'p2_uplink', 'Uplink', {
    order: 2,
  }),
];
export const NETWORK_ATTRIBUTES = [
  attributeDefinition('ad000000-0000-4000-8000-0000000000b3', 'p2_mgmt_vlan', 'Management-VLAN', {
    ci_type_id: NETWORK_TYPE.id,
    data_type: 'integer',
  }),
];
export const GLOBAL_ATTRIBUTES = [
  attributeDefinition('ad000000-0000-4000-8000-0000000000b4', 'p2_cost_center', 'Kostenstelle', {
    ci_type_id: undefined,
    scope: 'global',
  }),
];

/** The catalog rule of the access switch that a section may show itself with (WP-801). */
export const RACK_RULE = {
  id: 'f1000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  key: 'p2_rack_mounted',
  attribute_key: 'p2_uplink',
  kind: 'visible',
  ci_type_id: ACCESS_SWITCH_TYPE.id,
  condition: { op: 'eq', field: 'p2_ports', value: 48 },
  ast: null,
  message: null,
  is_system: false,
  is_active: true,
  version: 1,
  created_at: AT,
  updated_at: AT,
};

/** NETWORK_LAYOUT is the layout of the parent type that the access switch inherits. */
export const NETWORK_LAYOUT = {
  id: '1a000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  key: 'p2_network_device',
  ci_type_id: NETWORK_TYPE.id,
  priority: 0,
  version: 3,
  is_active: true,
  layout: {
    sections: [
      {
        key: 'p2_main',
        title: { de: 'Allgemein', en: 'General' },
        visible_when: [],
        fields: [
          { name: 'p2_mgmt_vlan', width: 6 },
          { name: 'p2_uplink', width: 6 },
        ],
      },
    ],
  },
  created_at: AT,
  updated_at: AT,
};

/**
 * accessResolvedForm is the resolved form of the access switch: without a
 * role the layout of the parent; as the service desk role with the template
 * (preview, WP-803) the template's default for the ports.
 */
export function accessResolvedForm(url: URL) {
  const templateId = url.searchParams.get('template_id');
  const roleId = url.searchParams.get('role_id');
  return {
    ci_type_id: ACCESS_SWITCH_TYPE.id,
    layout_id: NETWORK_LAYOUT.id,
    ...(roleId ? { role_id: roleId } : {}),
    ...(templateId ? { template_id: templateId } : {}),
    source: 'layout',
    sections: [
      {
        key: 'p2_main',
        title: { de: 'Allgemein', en: 'General' },
        visible: true,
        fields: [
          resolvedField('p2_mgmt_vlan', 'Management-VLAN', 'integer', {}, {}, 6),
          resolvedField('p2_uplink', 'Uplink', 'text', {}, roleId ? { read_only: true } : {}, 6),
        ],
      },
      {
        key: '_required',
        title: { de: 'Pflichtfelder', en: 'Required fields' },
        visible: true,
        fields: [
          resolvedField(
            'p2_ports',
            'Ports',
            'integer',
            templateId ? { default_value: '24', required: true } : { required: true },
            { required: true },
          ),
        ],
      },
    ],
    rules: [],
  };
}

/**
 * FORM_BUILDER_READS answer the reads of the form builder of the access
 * switch: the types, the template, the roles, the attributes, the rules,
 * the inherited layout and the resolved form. The layouts of the access
 * switch itself come from the spec (they change with its writes).
 */
export const FORM_BUILDER_READS: Handler[] = [
  ...P2_TYPE_HANDLERS,
  { path: '/templates', reply: fixed(list([ACCESS_TEMPLATE])) },
  { path: `/templates/${ACCESS_TEMPLATE.id}`, reply: fixed(ACCESS_TEMPLATE) },
  { path: '/roles', reply: fixed(list([SERVICE_ROLE])) },
  {
    path: '/attribute-definitions',
    reply: (_request, url) => {
      const applicable = url.searchParams.get('applicable_to');
      const typeId = url.searchParams.get('ci_type_id');
      let data: unknown[] = [];
      if (applicable === ACCESS_SWITCH_TYPE.id) {
        data = [...ACCESS_ATTRIBUTES, ...NETWORK_ATTRIBUTES, ...GLOBAL_ATTRIBUTES];
      } else if (typeId === ACCESS_SWITCH_TYPE.id) data = ACCESS_ATTRIBUTES;
      else if (typeId === NETWORK_TYPE.id) data = NETWORK_ATTRIBUTES;
      return { body: { data } };
    },
  },
  { path: '/attribute-sets', reply: fixed({ data: [] }) },
  {
    path: /^\/ci-types\/[0-9a-f-]{36}\/attribute-sets$/,
    reply: (_request, url) => ({
      body: { ci_type_id: url.pathname.split('/').at(-2), attribute_sets: [] },
    }),
  },
  { path: '/rules', reply: fixed({ data: [RACK_RULE] }) },
  { path: `/layouts/${NETWORK_LAYOUT.id}`, reply: fixed(NETWORK_LAYOUT) },
  {
    path: `/ci-types/${ACCESS_SWITCH_TYPE.id}/form`,
    reply: (_request, url) => ({ body: accessResolvedForm(url) }),
  },
];

// --- Locations: a warehouse with two bins (LOC-01, STK-02, WP-283)

export const CLIENT_ID = 'c0000000-0000-4000-8000-0000000000b1';
export const SITE_ID = '51000000-0000-4000-8000-0000000000b1';
export const WAREHOUSE_ID = '1c000000-0000-4000-8000-0000000000b1';
export const BIN_A = '1c000000-0000-4000-8000-0000000000b2';
export const BIN_B = '1c000000-0000-4000-8000-0000000000b3';

function location(
  id: string,
  kind: string,
  name: string,
  path: string,
  parent: string | undefined,
  extra: Record<string, unknown> = {},
) {
  return {
    id,
    organization_id: ORG_ID,
    client_id: CLIENT_ID,
    site_id: SITE_ID,
    ...(parent ? { parent_id: parent } : {}),
    kind,
    name,
    path,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

export const LOCATIONS = [
  location(SITE_ID, 'site', 'Standort Nord', 'Standort Nord', undefined),
  location(WAREHOUSE_ID, 'warehouse', 'Lager Nord', 'Standort Nord/Lager Nord', SITE_ID),
  location(BIN_A, 'bin', 'Fach A-01', 'Standort Nord/Lager Nord/Fach A-01', WAREHOUSE_ID, {
    barcode: 'BIN-A01',
  }),
  location(BIN_B, 'bin', 'Fach A-02', 'Standort Nord/Lager Nord/Fach A-02', WAREHOUSE_ID, {
    barcode: 'BIN-A02',
  }),
];

/** LOCATION_TREE is the answer of GET /locations/tree: site → warehouse → bins. */
export const LOCATION_TREE = [
  {
    ...LOCATIONS[0]!,
    children: [
      {
        ...LOCATIONS[1]!,
        children: [
          { ...LOCATIONS[2]!, children: [] },
          { ...LOCATIONS[3]!, children: [] },
        ],
      },
    ],
  },
];

// --- Assets with contract and supplier (AST-01, CON-01, WP-808)

export const SUPPLIER = {
  id: 'fd000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  client_id: null,
  name: 'Contoso Systemhaus GmbH',
  external_ref: 'K-4711',
  contact_id: null,
  notes: null,
  version: 1,
  created_at: AT,
  updated_at: AT,
};

export const CONTRACT = {
  id: 'fc000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  client_id: null,
  supplier_id: SUPPLIER.id,
  number: 'WV-2026-17',
  kind: 'maintenance',
  start_date: '2026-01-01',
  end_date: '2027-12-31',
  notice_period_days: 90,
  value: 1200,
  currency: 'EUR',
  status: 'active',
  notes: null,
  version: 1,
  created_at: AT,
  updated_at: AT,
};

/** P2_ASSET is a notebook under the maintenance contract of the supplier. */
export const P2_ASSET = {
  id: 'a5000000-0000-4000-8000-0000000000c1',
  organization_id: ORG_ID,
  inventory_no: 'INV-1001',
  name: 'Notebook 1001',
  category: 'hardware',
  status: 'active',
  manufacturer: 'Contoso',
  model: 'NB-14',
  serial_number: 'SN-1001',
  barcode: 'AST-1001',
  supplier_id: SUPPLIER.id,
  contract_id: CONTRACT.id,
  lifecycle_state: 'in_stock',
  custom_fields: {},
  tags: [],
  version: 1,
  created_at: AT,
  updated_at: AT,
};

/**
 * assetListHandlers answer the asset list (GET /assets) with P2_ASSET and
 * the assets in created, the saved views, the contract and the supplier.
 */
export function assetListHandlers(created: Record<string, unknown>[] = []): Handler[] {
  return [
    { path: '/assets', reply: () => ({ body: list([P2_ASSET, ...created]) }) },
    { path: `/assets/${P2_ASSET.id}`, reply: fixed(P2_ASSET) },
    { path: '/saved-views', reply: fixed(list([])) },
    { path: '/contracts', reply: fixed(list([CONTRACT])) },
    { path: `/contracts/${CONTRACT.id}`, reply: fixed(CONTRACT) },
    { path: '/suppliers', reply: fixed(list([SUPPLIER])) },
    { path: `/suppliers/${SUPPLIER.id}`, reply: fixed(SUPPLIER) },
    { path: '/locations/tree', reply: () => ({ body: { data: LOCATION_TREE } }) },
  ];
}

// --- Import profile (IMP-02, WP-322)

/** IMPORT_PROFILE is a stored mapping profile of the asset import. */
export const IMPORT_PROFILE = {
  id: '7b000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  name: 'Asset-Import',
  description: 'Inventarliste der Buchhaltung',
  entity: 'asset',
  format: 'csv',
  mode: 'both',
  sheet: null,
  mapping: {
    columns: {
      Inventarnummer: { target: 'inventory_no', transforms: [{ op: 'trim' }] },
      Bezeichnung: { target: 'name', transforms: [] },
    },
  },
  identity_keys: ['inventory_no'],
  created_by: USER_ID,
  created_at: AT,
  updated_at: AT,
};

// --- Consumable with bins and stock, movements and reservations (STK-01..06, AST-06)

/** TONER is a consumable that lies in both bins of the warehouse. */
export const TONER = {
  id: 'e1000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  client_id: CLIENT_ID,
  name: 'Toner schwarz',
  sku: 'TN-1',
  barcode: 'EAN-4006381333931',
  supplier_id: SUPPLIER.id,
  category: 'printer',
  unit: 'Stück',
  stock_level: 14,
  min_level: 5,
  created_at: AT,
  updated_at: AT,
};

/** RESERVER is the person an asset is reserved for. */
export const RESERVER = {
  id: '00000000-0000-4000-8000-0000000000b2',
  organization_id: ORG_ID,
  email: 'erika.muster@example.com',
  display_name: 'Erika Muster',
  status: 'active',
  created_at: AT,
  updated_at: AT,
};

/** LABELS are the codes GET /labels/lookup knows: bin labels, the consumable and the asset. */
const LABELS: Record<string, { kind: string; id: string; name: string }> = {
  'BIN-A01': { kind: 'location', id: BIN_A, name: 'Fach A-01' },
  'BIN-A02': { kind: 'location', id: BIN_B, name: 'Fach A-02' },
  [TONER.barcode]: { kind: 'consumable', id: TONER.id, name: TONER.name },
  [P2_ASSET.barcode]: { kind: 'asset', id: P2_ASSET.id, name: P2_ASSET.name },
};

export interface StockState {
  handlers: Handler[];
  /** The bodies of POST /stock-movements in order. */
  bookings: Record<string, unknown>[];
  /** The reservations created by POST /assets/{id}/reserve. */
  reservations: Record<string, unknown>[];
  /** The requests of POST /assets/{id}/reserve with their asset. */
  reserveRequests: { assetId: string; body: unknown }[];
}

/**
 * stockHandlers answer the warehouse flows like a server: the pickers
 * (consumables, assets, users, the location tree), the stock per bin, the
 * journal and the reservations. A booking changes the stock of its bins and
 * appears in the journal; a reservation appears in the list of active ones.
 * The toner lies with 10 (2 reserved) in bin A-01 and 4 in bin A-02.
 */
export function stockHandlers(): StockState {
  const levels = new Map<string, { on_hand: number; reserved: number }>([
    [BIN_A, { on_hand: 10, reserved: 2 }],
    [BIN_B, { on_hand: 4, reserved: 0 }],
  ]);
  const movements: Record<string, unknown>[] = [];
  const state: StockState = { handlers: [], bookings: [], reservations: [], reserveRequests: [] };
  const id = (prefix: string, n: number) =>
    `${prefix}-0000-4000-8000-${String(n).padStart(12, '0')}`;

  state.handlers = [
    { path: '/consumables', reply: () => ({ body: list([TONER]) }) },
    { path: `/consumables/${TONER.id}`, reply: fixed(TONER) },
    {
      path: `/consumables/${TONER.id}/stock`,
      reply: () => ({
        body: list(
          [...levels.entries()].map(([location_id, level]) => ({
            consumable_id: TONER.id,
            location_id,
            client_id: CLIENT_ID,
            site_id: SITE_ID,
            ...level,
            updated_at: AT,
          })),
        ),
      }),
    },
    { path: '/assets', reply: () => ({ body: list([P2_ASSET]) }) },
    { path: `/assets/${P2_ASSET.id}`, reply: fixed(P2_ASSET) },
    { path: '/users', reply: () => ({ body: list([RESERVER]) }) },
    { path: '/teams', reply: () => ({ body: list([]) }) },
    // The asset is held by nobody (the scan result asks for its holder).
    { path: '/assignments', reply: () => ({ body: list([]) }) },
    // The objects the command palette finds besides its commands: none.
    { path: '/search', reply: () => ({ body: list([]) }) },
    { path: '/locations/tree', reply: () => ({ body: { data: LOCATION_TREE } }) },
    {
      path: /^\/locations\/[0-9a-f-]{36}$/,
      reply: (_request, url) => {
        const found = LOCATIONS.find((l) => url.pathname.endsWith(`/locations/${l.id}`));
        return found ? { body: found } : { status: 404 };
      },
    },
    {
      path: '/labels/lookup',
      reply: (_request, url) => {
        const code = url.searchParams.get('code') ?? '';
        const hit = LABELS[code];
        return hit
          ? { body: hit }
          : { status: 404, body: problem(404, `no label with the code ${code}`) };
      },
    },
    {
      path: '/stock-movements',
      reply: () => ({ body: list([...movements].reverse()) }),
    },
    {
      method: 'POST',
      path: '/stock-movements',
      reply: (request) => {
        const body = request.postDataJSON() as Record<string, unknown>;
        state.bookings.push(body);
        const quantity = Number(body.quantity ?? 0);
        const change = (bin: unknown, delta: number) => {
          const level = levels.get(String(bin)) ?? { on_hand: 0, reserved: 0 };
          levels.set(String(bin), { ...level, on_hand: level.on_hand + delta });
        };
        if (body.from_location_id) change(body.from_location_id, -quantity);
        if (body.to_location_id) change(body.to_location_id, quantity);
        const movement = {
          id: id('3e000000', movements.length + 1),
          organization_id: ORG_ID,
          item_kind: body.asset_id ? 'asset' : 'consumable',
          ...body,
          actor_id: USER_ID,
          occurred_at: AT,
          created_at: AT,
        };
        movements.push(movement);
        return { status: 201, body: movement };
      },
    },
    {
      path: '/reservations',
      reply: () => ({ body: list(state.reservations) }),
    },
    {
      method: 'POST',
      path: /^\/assets\/[0-9a-f-]{36}\/reserve$/,
      reply: (request, url) => {
        const assetId = url.pathname.split('/').at(-2)!;
        const body = request.postDataJSON() as Record<string, unknown>;
        state.reserveRequests.push({ assetId, body });
        const reservation = {
          id: id('3f000000', state.reservations.length + 1),
          organization_id: ORG_ID,
          item_kind: 'asset',
          asset_id: assetId,
          quantity: 1,
          state: 'active',
          reserved_from: AT,
          ...body,
          created_by: USER_ID,
          created_at: AT,
          updated_at: AT,
        };
        state.reservations.push(reservation);
        return { status: 201, body: reservation };
      },
    },
  ];
  return state;
}

// --- Tickets with SLA and business calendar (TKT-01..05, WP-292, WP-814, WP-815)

/** BUSINESS_CALENDAR is the default calendar: weekdays 8–17, two holidays. */
export const BUSINESS_CALENDAR = {
  id: 'ca000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  client_id: null,
  name: 'Servicezeiten Nord',
  timezone: 'Europe/Berlin',
  weekly_hours: [1, 2, 3, 4, 5].map((weekday) => ({ weekday, start: '08:00', end: '17:00' })),
  holidays: [
    { date: '2026-12-24', name: 'Heiligabend' },
    { date: '2026-12-25', name: '1. Weihnachtstag' },
  ],
  is_default: true,
  created_at: AT,
  updated_at: AT,
};

/** SLA_POLICY is the policy of high tickets, counted in the business calendar. */
export const SLA_POLICY = {
  id: '5a000000-0000-4000-8000-0000000000b1',
  organization_id: ORG_ID,
  name: 'Hoch – Servicezeiten',
  priority: 'high',
  response_target_minutes: 120,
  resolution_target_minutes: 480,
  business_calendar: true,
  calendar_id: BUSINESS_CALENDAR.id,
  created_at: AT,
  updated_at: AT,
};

function ticket(id: string, ticket_number: number, title: string, extra: Record<string, unknown>) {
  return {
    id,
    organization_id: ORG_ID,
    ticket_number,
    title,
    description: '',
    status: 'open',
    priority: 'high',
    category: 'incident',
    reporter_id: USER_ID,
    tags: [],
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

/** RUNNING_TICKET has an SLA within its deadlines. */
export const RUNNING_TICKET = ticket(
  'dc000000-0000-4000-8000-0000000000b1',
  57,
  'Drucker Etage 3 offline',
  {},
);
/** BREACHED_TICKET has missed the resolution deadline of its SLA. */
export const BREACHED_TICKET = ticket(
  'dc000000-0000-4000-8000-0000000000b2',
  58,
  'Switch Halle 2 ausgefallen',
  { status: 'in_progress', assignee_id: USER_ID },
);

/**
 * ticketSLA is the SLA state of a ticket (GET /tickets/{id}/sla): the
 * remaining business minutes, negative after the deadline.
 */
export function ticketSLA(
  ticketId: string,
  response: number | null,
  resolution: number | null,
  extra: Record<string, unknown> = {},
) {
  return {
    id: `5b000000-0000-4000-8000-${ticketId.slice(-12)}`,
    organization_id: ORG_ID,
    ticket_id: ticketId,
    sla_id: SLA_POLICY.id,
    response_due_at: '2026-10-14T10:00:00Z',
    resolution_due_at: '2026-10-14T16:00:00Z',
    response_breached: response !== null && response < 0,
    resolution_breached: resolution !== null && resolution < 0,
    calendar_id: BUSINESS_CALENDAR.id,
    response_remaining_minutes: response,
    resolution_remaining_minutes: resolution,
    paused: false,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

/** The reads of a ticket detail page besides ticket and SLA: no comments, links or attachments. */
export function ticketDetailHandlers(ticketId: string): Handler[] {
  return [
    { path: `/tickets/${ticketId}/comments`, reply: fixed(list([])) },
    { path: `/tickets/${ticketId}/links`, reply: fixed(list([])) },
    { path: `/tickets/${ticketId}/attachments`, reply: fixed(list([])) },
  ];
}

/** SLA_HANDLERS answer the SLA page: the policy, the calendar and the breaches. */
export const SLA_HANDLERS: Handler[] = [
  { path: '/slas', reply: fixed(list([SLA_POLICY])) },
  { path: '/business-calendars', reply: fixed(list([BUSINESS_CALENDAR])) },
  {
    path: '/slas/breaches',
    reply: fixed(list([ticketSLA(BREACHED_TICKET.id, null, -15, { first_response_at: AT })])),
  },
];
