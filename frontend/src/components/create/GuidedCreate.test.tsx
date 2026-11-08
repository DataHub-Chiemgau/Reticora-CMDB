/**
 * GuidedCreate (MET-51, UI-12, WP-317): the guided creation of a CI with
 * type tiles, manufacturer and model with suggestions from the stock and
 * the fields of the resolved form (GET /ci-types/{id}/form, WP-719).
 *
 * - Tiles: grouped by ci_type.category with the labels of create.categories,
 *   a raw category as its own group, types without one under "Weitere";
 *   the icon of ci_type.icon or the generic device icon (E-117); subtypes
 *   fold under their parent (aria-expanded).
 * - Manufacturer and model: suggestions from GET /cis of the type
 *   (filter[...][like], fields=manufacturer,model), deduplicated; the
 *   models narrowed to the chosen manufacturer. Logical types skip the step.
 * - Variant: the published templates of the type; none without the module
 *   metamodel (E-105).
 * - Fields: conditional fields and catalog rules are recomputed on input;
 *   typed columns go into the request as columns, the rest in attributes;
 *   references use the pickers (ci_ref, location, user).
 * - type-inactive (WP-249) returns to the tiles; no metamodel terms in any
 *   step (UI-12).
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/create/GuidedCreate.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import type { FieldDefinition } from '../../lib/fieldmeta';
import { useAuthStore } from '../../auth/authStore';
import { renderWithProviders } from '../../test/utils';
import { CIFormModal } from '../../pages/CIFormModal';
import { GuidedCreate, type CreateType, type GuidedCreateDraft } from './GuidedCreate';
import { TYPE_CATEGORIES, groupTypes } from './TypeTiles';

const SWITCH = '5e1b0c3a-7d2f-4a10-9c01-000000000002';
const SERVER = '5e1b0c3a-7d2f-4a10-9c01-000000000001';
const EDGE_SWITCH = '5e1b0c3a-7d2f-4a10-9c01-0000000000b1';
const ORG_TYPE = '5e1b0c3a-7d2f-4a10-9c01-0000000000a1';
const CUSTOM = '5e1b0c3a-7d2f-4a10-9c01-0000000000a2';
const SERVICE = '5e1b0c3a-7d2f-4a10-9c01-0000000000c1';
const TEMPLATE_A = '7e000000-0000-4000-8000-0000000000a1';
const TEMPLATE_B = '7e000000-0000-4000-8000-0000000000a2';
const LOCATION = '10c00000-0000-4000-8000-000000000001';
const HOST_CI = '11111111-1111-4111-8111-000000000001';
const USER = '00000000-0000-4000-8000-0000000000f1';
const TEAM = '00000000-0000-4000-8000-0000000000e1';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function apiType(
  id: string,
  key: string,
  de: string,
  extra: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    id,
    key,
    name: key,
    display_name: { de, en: de },
    is_builtin: true,
    is_system: true,
    is_active: true,
    is_logical: false,
    is_observable: true,
    schema_version: 1,
    version: 1,
    capabilities: {},
    allowed_relationship_types: [],
    ui_schema: {},
    ...extra,
  };
}

const TYPES = [
  apiType(SWITCH, 'switch', 'Switch', { category: 'network', icon: 'network' }),
  apiType(EDGE_SWITCH, 'edge_switch', 'Edge-Switch', {
    category: 'network',
    parent_type_id: SWITCH,
  }),
  apiType(SERVER, 'server', 'Server', { category: 'hardware', icon: 'server' }),
  apiType(ORG_TYPE, 'org_box', 'Org-Box', { icon: 'not-a-lucide-icon' }),
  apiType(CUSTOM, 'lab_gear', 'Laborgerät', { category: 'lab' }),
  apiType(SERVICE, 'business_service', 'Geschäftsdienst', {
    category: 'service',
    icon: 'briefcase',
    is_logical: true,
  }),
];

interface SectionFixture {
  key: string;
  title?: Record<string, string>;
  visible?: boolean;
  fields: Array<FieldDefinition & { state?: Record<string, unknown> }>;
}

function resolvedForm(
  typeId: string,
  sections: SectionFixture[],
  rules: unknown[] = [],
  extra: Record<string, unknown> = {},
) {
  return {
    ci_type_id: typeId,
    source: 'default',
    sections: sections.map((section) => ({
      key: section.key,
      title: section.title ?? { de: section.key, en: section.key },
      visible: section.visible ?? true,
      fields: section.fields.map(({ state, ...def }) => ({
        name: def.name,
        width: 12,
        definition: { required: false, sort_order: 0, ...def },
        state: { visible: true, required: !!def.required, read_only: false, ...state },
      })),
    })),
    rules,
    ...extra,
  };
}

/** The server form of the switch: typed columns, a conditional field and a rule. */
const SWITCH_FORM = resolvedForm(
  SWITCH,
  [
    {
      key: 'identity',
      fields: [
        { name: 'manufacturer', label: 'Manufacturer', data_type: 'text' },
        { name: 'model', label: 'Model', data_type: 'text' },
        { name: 'serial_number', label: 'Seriennummer', data_type: 'text' },
        {
          name: 'mount',
          label: 'Montage',
          data_type: 'enum',
          enum_values: ['rack', 'desk'],
        },
        {
          name: 'rack_unit',
          label: 'Höheneinheit',
          data_type: 'integer',
          conditional: { visible_when: [{ field: 'mount', op: 'eq', value: 'rack' }] },
          state: { visible: false },
        },
      ],
    },
    {
      key: 'management',
      fields: [
        { name: 'management_ip', label: 'Management-IP', data_type: 'ip' },
        { name: 'snmp_community', label: 'SNMP-Community', data_type: 'text' },
        { name: 'port_count', label: 'Ports', data_type: 'integer', default_value: '48' },
      ],
    },
    {
      key: 'Eigene Gruppe',
      fields: [{ name: 'cost_center', label: 'Kostenstelle', data_type: 'text' }],
    },
  ],
  [
    // Ports become required for a rack-mounted switch (catalog rule, WP-257).
    {
      key: 'ports-for-rack',
      attribute_key: 'port_count',
      kind: 'required',
      condition: { op: 'eq', field: 'mount', value: 'rack' },
    },
  ],
);

interface Backend {
  forms?: Record<string, unknown>;
  templates?: Record<string, unknown[]> | 'forbidden';
  modules?: Array<{ key: string; enabled: boolean }>;
  /** The CIs GET /cis answers for the suggestions and the picker. */
  cis?: Array<Record<string, unknown>>;
  post?: (body: Record<string, unknown>) => Response;
  /** The teams GET /teams answers (owner picker, WP-805). */
  teams?: Array<Record<string, unknown>>;
}

function stubBackend({
  forms = {},
  templates = {},
  modules,
  cis = [],
  post,
  teams = [],
}: Backend = {}) {
  const requests: URL[] = [];
  const posted: Array<Record<string, unknown>> = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const method = (init?.method ?? 'GET').toUpperCase();
      requests.push(url);
      if (method === 'POST' && path === '/cis') {
        const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
        posted.push(body);
        return post
          ? post(body)
          : json(
              {
                id: '99999999-9999-4999-8999-999999999999',
                organization_id: 'org-1',
                version: 1,
                status: 'active',
                attributes: {},
                created_at: '2026-01-01T00:00:00Z',
                updated_at: '2026-01-01T00:00:00Z',
                ...body,
              },
              201,
            );
      }
      if (path === '/modules') {
        return json({
          data: (modules ?? []).map((m) => ({ ...m, phase: 'P2', nav: [] })),
        });
      }
      if (path === '/ci-types') {
        return json({ data: TYPES, total: TYPES.length, limit: 200, offset: 0, has_more: false });
      }
      const form = /^\/ci-types\/([^/]+)\/form$/.exec(path);
      if (form) {
        const body =
          forms[`${form[1]}:${url.searchParams.get('template_id') ?? ''}`] ??
          forms[form[1]!] ??
          resolvedForm(form[1]!, []);
        return json(body);
      }
      if (path === '/templates') {
        if (templates === 'forbidden') return json({ detail: 'forbidden' }, 403);
        const data = templates[url.searchParams.get('ci_type_id') ?? ''] ?? [];
        return json({ data, total: data.length, limit: 200, offset: 0, has_more: false });
      }
      if (path === '/cis') {
        return json({ data: cis, total: cis.length, limit: 50, offset: 0, has_more: false });
      }
      if (path === '/locations/tree') {
        return json({
          data: [
            {
              id: LOCATION,
              organization_id: 'org-1',
              kind: 'site',
              name: 'RZ Nord',
              path: LOCATION,
              created_at: '2026-01-01T00:00:00Z',
              updated_at: '2026-01-01T00:00:00Z',
              children: [],
            },
          ],
        });
      }
      if (path === '/users') {
        return json({
          data: [{ id: USER, display_name: 'Erika Muster', email: 'erika@example.test' }],
          total: 1,
          limit: 20,
          offset: 0,
          has_more: false,
        });
      }
      if (path === '/teams') {
        return json({ data: teams, total: teams.length, limit: 20, offset: 0, has_more: false });
      }
      if (path === '/entitlements') return json({ data: [] });
      return json({ detail: `unexpected request: ${method} ${path}` }, 404);
    }),
  );
  return { requests, posted };
}

function createTypes(): CreateType[] {
  return TYPES.map((ty) => ({
    id: ty.id as string,
    label: (ty.display_name as { de: string }).de,
    category: ty.category as string | undefined,
    icon: ty.icon as string | undefined,
    parentId: ty.parent_type_id as string | undefined,
    isLogical: ty.is_logical as boolean,
  }));
}

const statusOptions = [
  { value: 'active', label: 'Aktiv' },
  { value: 'inactive', label: 'Inaktiv' },
];

function submitSpy() {
  return vi.fn<(draft: GuidedCreateDraft) => Promise<void>>(async () => {});
}

function renderGuided(onSubmit = submitSpy()) {
  renderWithProviders(
    <GuidedCreate
      resource="ci"
      typeSource={{
        types: createTypes(),
        loading: false,
        failed: false,
        retrying: false,
        onRetry: () => {},
      }}
      statusOptions={statusOptions}
      onSubmit={onSubmit}
      onCancel={() => {}}
    />,
  );
  return onSubmit;
}

function tile(name: string) {
  return screen.getByRole('button', { name });
}

function next() {
  fireEvent.click(screen.getByRole('button', { name: 'Weiter' }));
}

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
});

afterEach(async () => {
  vi.unstubAllGlobals();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('TypeTiles', () => {
  it('groups by category with the catalog labels, raw categories and "Weitere"', () => {
    stubBackend();
    renderGuided();
    const groups = screen
      .getAllByRole('heading', { level: 4 })
      .map((heading) => heading.textContent);
    expect(groups).toEqual(['Hardware', 'Netzwerk', 'Dienste', 'lab', 'Weitere']);
    const other = screen.getByRole('region', { name: 'Weitere' });
    expect(within(other).getByRole('button', { name: 'Org-Box' })).toBeInTheDocument();
    expect(
      within(screen.getByRole('region', { name: 'lab' })).getByText('Laborgerät'),
    ).toBeTruthy();
  });

  it('takes the icon of the type and the generic device icon otherwise', () => {
    stubBackend();
    renderGuided();
    const icon = (name: string) =>
      screen.getByRole('button', { name }).querySelector('svg')?.getAttribute('class') ?? '';
    expect(icon('Server')).toMatch(/lucide-server/);
    expect(icon('Switch')).toMatch(/lucide-network/);
    // An unknown icon name and no icon fall back to the generic device icon.
    expect(icon('Org-Box')).toMatch(/lucide-cpu/);
    expect(icon('Laborgerät')).toMatch(/lucide-cpu/);
  });

  it('folds subtypes under their parent (aria-expanded)', () => {
    stubBackend();
    renderGuided();
    expect(screen.queryByRole('button', { name: 'Edge-Switch' })).not.toBeInTheDocument();
    const toggle = screen.getByRole('button', { name: 'Untertypen von Switch anzeigen' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    const children = screen.getByRole('list', { name: 'Untertypen von Switch' });
    fireEvent.click(within(children).getByRole('button', { name: 'Edge-Switch' }));
    expect(screen.getByRole('button', { name: 'Edge-Switch' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  });

  it('exports the categories of citype.Templates() and other', () => {
    expect(TYPE_CATEGORIES).toHaveLength(13);
    expect(TYPE_CATEGORIES[TYPE_CATEGORIES.length - 1]).toBe('other');
    const groups = groupTypes([
      { id: 'a', label: 'A' },
      { id: 'b', label: 'B', category: 'cloud' },
      { id: 'c', label: 'C', category: 'hardware', parentId: 'missing' },
    ]);
    expect(groups.map((g) => g.category)).toEqual(['hardware', 'cloud', 'other']);
  });
});

describe('GuidedCreate steps', () => {
  it('marks the current step, moves the focus to its heading and keeps inputs on back', async () => {
    stubBackend({ forms: { [SWITCH]: SWITCH_FORM } });
    renderGuided();
    const steps = screen.getByRole('list', { name: 'Schritte der Anlage' });
    expect(
      within(steps)
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual(['1Typ', '2Hersteller und Modell', '3Angaben']);
    expect(within(steps).getByText('Typ').closest('li')).toHaveAttribute('aria-current', 'step');
    expect(screen.getByRole('button', { name: 'Weiter' })).toBeDisabled();

    fireEvent.click(tile('Switch'));
    next();
    expect(screen.getByRole('heading', { name: 'Hersteller und Modell' })).toHaveFocus();
    expect(within(steps).getByText('Hersteller und Modell').closest('li')).toHaveAttribute(
      'aria-current',
      'step',
    );
    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'Contoso' } });
    next();
    fireEvent.change(await screen.findByLabelText('Name *'), { target: { value: 'sw-01' } });
    fireEvent.change(await screen.findByLabelText('Kostenstelle'), { target: { value: 'IT-1' } });

    fireEvent.click(screen.getByRole('button', { name: 'Zurück' }));
    expect(screen.getByLabelText('Hersteller')).toHaveValue('Contoso');
    fireEvent.click(screen.getByRole('button', { name: 'Zurück' }));
    expect(screen.getByRole('button', { name: 'Switch' })).toHaveAttribute('aria-pressed', 'true');
    next();
    next();
    expect(await screen.findByLabelText('Name *')).toHaveValue('sw-01');
    expect(screen.getByLabelText('Kostenstelle')).toHaveValue('IT-1');
  });

  it('skips manufacturer and model for a logical type', async () => {
    stubBackend();
    renderGuided();
    fireEvent.click(tile('Geschäftsdienst'));
    const steps = screen.getByRole('list', { name: 'Schritte der Anlage' });
    expect(within(steps).queryByText('Hersteller und Modell')).not.toBeInTheDocument();
    next();
    expect(screen.getByRole('heading', { name: 'Angaben' })).toHaveFocus();
    expect(screen.queryByLabelText('Hersteller')).not.toBeInTheDocument();
  });

  it('a type change drops the values of the type, not the master fields', async () => {
    const submit = submitSpy();
    stubBackend({
      forms: {
        [SWITCH]: SWITCH_FORM,
        [SERVER]: resolvedForm(SERVER, [
          { key: 'hardware', fields: [{ name: 'cpu', label: 'CPU', data_type: 'text' }] },
        ]),
      },
    });
    renderGuided(submit);
    fireEvent.click(tile('Switch'));
    next();
    next();
    fireEvent.change(await screen.findByLabelText('Name *'), { target: { value: 'dev-01' } });
    fireEvent.change(await screen.findByLabelText('Kostenstelle'), { target: { value: 'IT-1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Zurück' }));
    fireEvent.click(screen.getByRole('button', { name: 'Zurück' }));
    fireEvent.click(tile('Server'));
    next();
    next();
    expect(await screen.findByLabelText('CPU')).toBeInTheDocument();
    expect(screen.getByLabelText('Name *')).toHaveValue('dev-01');
    fireEvent.click(screen.getByRole('button', { name: 'Anlegen' }));
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    expect(submit.mock.calls[0]![0]).toMatchObject({
      typeId: SERVER,
      name: 'dev-01',
      attributes: {},
    });
  });
});

describe('GuidedCreate manufacturer and model', () => {
  it('suggests the deduplicated stored values, models of the chosen manufacturer', async () => {
    const { requests } = stubBackend({
      cis: [
        { id: 'c1', manufacturer: 'Contoso', model: 'CX-24' },
        { id: 'c2', manufacturer: 'Contoso', model: 'CX-48' },
        { id: 'c3', manufacturer: 'Fabrikam', model: 'CX-24' },
      ],
    });
    renderGuided();
    fireEvent.click(tile('Switch'));
    next();

    const manufacturers = await screen.findByRole('list', { name: 'Vorschläge für Hersteller' });
    expect(
      within(manufacturers)
        .getAllByRole('button')
        .map((b) => b.textContent),
    ).toEqual(['Contoso', 'Fabrikam']);
    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'Con' } });
    await waitFor(() =>
      expect(
        requests.some((u) => u.searchParams.get('filter[manufacturer][like]') === 'Con*'),
      ).toBe(true),
    );
    const query = requests.find(
      (u) => u.searchParams.get('filter[manufacturer][like]') === 'Con*',
    )!;
    expect(query.searchParams.get('filter[ci_type_id]')).toBe(SWITCH);
    expect(query.searchParams.get('fields')).toBe('manufacturer,model');
    expect(query.searchParams.get('limit')).toBe('50');

    fireEvent.click(
      within(screen.getByRole('list', { name: 'Vorschläge für Hersteller' })).getByRole('button', {
        name: 'Contoso',
      }),
    );
    expect(screen.getByLabelText('Hersteller')).toHaveValue('Contoso');
    await waitFor(() =>
      expect(
        requests.some(
          (u) =>
            u.searchParams.get('filter[manufacturer]') === 'Contoso' &&
            u.searchParams.get('filter[model][like]') === '*',
        ),
      ).toBe(true),
    );
    // The stub answers all rows; the client dedupes CX-24.
    const models = await screen.findByRole('list', { name: 'Vorschläge für Modell' });
    expect(
      within(models)
        .getAllByRole('button')
        .map((b) => b.textContent),
    ).toEqual(['CX-24', 'CX-48']);
  });
});

describe('GuidedCreate variants', () => {
  const templates = {
    [SWITCH]: [
      {
        id: TEMPLATE_A,
        key: 'access',
        ci_type_id: SWITCH,
        display_name: { de: 'Access-Switch', en: 'Access switch' },
        is_system: false,
        is_active: true,
        current_version: 1,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
      },
      {
        id: TEMPLATE_B,
        key: 'core',
        ci_type_id: SWITCH,
        display_name: { de: 'Core-Switch', en: 'Core switch' },
        is_system: false,
        is_active: true,
        current_version: 2,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
      },
      {
        id: '7e000000-0000-4000-8000-0000000000a3',
        key: 'draft_only',
        ci_type_id: SWITCH,
        display_name: { de: 'Entwurf', en: 'Draft' },
        is_system: false,
        is_active: true,
        current_version: null,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
      },
    ],
  };

  it('offers the published variants and resolves the form with the chosen one', async () => {
    const submit = submitSpy();
    const { requests } = stubBackend({
      templates,
      forms: {
        [`${SWITCH}:${TEMPLATE_B}`]: resolvedForm(SWITCH, [
          {
            key: 'identity',
            fields: [
              { name: 'uplinks', label: 'Uplinks', data_type: 'integer', default_value: '4' },
            ],
          },
        ]),
      },
    });
    renderGuided(submit);
    fireEvent.click(tile('Switch'));
    next();
    next();
    const variant = await screen.findByLabelText('Ausführung *');
    expect(Array.from((variant as HTMLSelectElement).options).map((o) => o.textContent)).toEqual([
      'Bitte auswählen',
      'Access-Switch',
      'Core-Switch',
    ]);
    fireEvent.change(variant, { target: { value: TEMPLATE_B } });
    // The default of the variant is prefilled and marked.
    expect(await screen.findByLabelText('Uplinks (vorbelegt)')).toHaveValue(4);
    expect(
      requests.some(
        (u) =>
          u.pathname.endsWith(`/ci-types/${SWITCH}/form`) &&
          u.searchParams.get('template_id') === TEMPLATE_B,
      ),
    ).toBe(true);
    fireEvent.change(screen.getByLabelText('Name *'), { target: { value: 'core-01' } });
    fireEvent.click(screen.getByRole('button', { name: 'Anlegen' }));
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    expect(submit.mock.calls[0]![0]).toMatchObject({
      templateId: TEMPLATE_B,
      attributes: { uplinks: 4 },
    });
  });

  it('shows no variant without the module metamodel', async () => {
    const { requests } = stubBackend({
      templates,
      modules: [{ key: 'metamodel', enabled: false }],
    });
    renderGuided();
    // The module list has answered before the type is chosen.
    await waitFor(() => expect(requests.some((u) => u.pathname.endsWith('/modules'))).toBe(true));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    fireEvent.click(tile('Switch'));
    next();
    next();
    expect(await screen.findByLabelText('Name *')).toBeInTheDocument();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(screen.queryByLabelText('Ausführung *')).not.toBeInTheDocument();
    expect(requests.some((u) => u.pathname.endsWith('/templates'))).toBe(false);
  });

  it('shows no variant when the templates answer 403', async () => {
    stubBackend({ templates: 'forbidden' });
    renderGuided();
    fireEvent.click(tile('Switch'));
    next();
    next();
    expect(await screen.findByLabelText('Name *')).toBeInTheDocument();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(screen.queryByLabelText('Ausführung *')).not.toBeInTheDocument();
  });
});

describe('GuidedCreate fields of the resolved form', () => {
  it('shows a conditional field and makes a field required on input', async () => {
    stubBackend({ forms: { [SWITCH]: SWITCH_FORM } });
    renderGuided();
    fireEvent.click(tile('Switch'));
    next();
    next();
    expect(await screen.findByLabelText('Montage')).toBeInTheDocument();
    expect(screen.queryByLabelText(/Höheneinheit/)).not.toBeInTheDocument();
    expect(screen.getByLabelText('Ports (vorbelegt)')).toHaveValue(48);

    fireEvent.change(screen.getByLabelText('Montage'), { target: { value: 'rack' } });
    expect(await screen.findByLabelText('Höheneinheit')).toBeInTheDocument();
    // The catalog rule makes the ports required while the switch is racked.
    expect(screen.getByLabelText('Ports (vorbelegt) *')).toBeRequired();
  });

  it('labels the sections from the catalog groups and never shows technical keys', async () => {
    stubBackend({
      forms: {
        [SWITCH]: resolvedForm(SWITCH, [
          { key: 'identity', fields: [{ name: 'a', label: 'A', data_type: 'text' }] },
          { key: 'Eigene Gruppe', fields: [{ name: 'b', label: 'B', data_type: 'text' }] },
          {
            key: '_required',
            title: { de: 'Weitere Pflichtfelder', en: 'Further required fields' },
            fields: [{ name: 'c', label: 'C', data_type: 'text', required: true }],
          },
          {
            key: 'hidden',
            title: { de: 'Versteckt', en: 'Hidden' },
            visible: false,
            fields: [{ name: 'd', label: 'D', data_type: 'text' }],
          },
        ]),
      },
    });
    renderGuided();
    fireEvent.click(tile('Switch'));
    next();
    next();
    await screen.findByLabelText('A');
    // The sections are the layout of DynamicForm (WP-803), titled by legends.
    const titles = Array.from(document.querySelectorAll('legend')).map((l) => l.textContent);
    expect(titles).toEqual(['Identität', 'Eigene Gruppe', 'Weitere Pflichtfelder']);
    expect(screen.queryByLabelText('D')).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain('_required');
  });

  it('sends typed columns as columns and the rest in attributes (CIFormModal)', async () => {
    const { posted } = stubBackend({ forms: { [SWITCH]: SWITCH_FORM } });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch' }));
    next();
    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'Contoso' } });
    fireEvent.change(screen.getByLabelText('Modell'), { target: { value: 'CX-24' } });
    next();
    fireEvent.change(await screen.findByLabelText('Name *'), { target: { value: 'sw-02' } });
    // Manufacturer and model of the form appear once, in their own step.
    expect(screen.queryByLabelText('Manufacturer')).not.toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText('Seriennummer'), { target: { value: 'SN-1' } });
    fireEvent.change(screen.getByLabelText('Management-IP'), { target: { value: '10.0.0.2' } });
    fireEvent.change(screen.getByLabelText('SNMP-Community'), { target: { value: 'public' } });
    fireEvent.click(await screen.findByRole('treeitem', { name: /RZ Nord/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Anlegen' }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({
      name: 'sw-02',
      ci_type_id: SWITCH,
      status: 'active',
      manufacturer: 'Contoso',
      model: 'CX-24',
      serial_number: 'SN-1',
      management_ip: '10.0.0.2',
      location_id: LOCATION,
      attributes: { snmp_community: 'public', port_count: 48 },
      discovery_source: 'manual',
    });
  });

  it('chooses ci_ref, location and user values with the pickers', async () => {
    const submit = submitSpy();
    stubBackend({
      cis: [{ id: HOST_CI, name: 'host-01', ci_type_id: SERVER, status: 'active' }],
      forms: {
        [SWITCH]: resolvedForm(SWITCH, [
          {
            key: 'assignment',
            fields: [
              { name: 'host', label: 'Host', data_type: 'ci_ref', reference_target: 'ci' },
              { name: 'room', label: 'Raum', data_type: 'location' },
              {
                name: 'owner',
                label: 'Betreuer',
                data_type: 'reference',
                reference_target: 'user',
              },
            ],
          },
        ]),
      },
    });
    renderGuided(submit);
    fireEvent.click(tile('Switch'));
    next();
    next();
    // No free id inputs: every reference is a search or a tree.
    const host = await screen.findByRole('combobox', { name: 'Host' });
    fireEvent.change(host, { target: { value: 'host' } });
    fireEvent.click(await screen.findByRole('option', { name: /host-01/ }, { timeout: 2000 }));
    expect(screen.getByRole('tree', { name: 'Raum' })).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole('tree', { name: 'Raum' })).getByRole('treeitem'));
    const owner = screen.getByRole('combobox', { name: 'Betreuer' });
    fireEvent.change(owner, { target: { value: 'erika' } });
    fireEvent.click(await screen.findByRole('option', { name: /Erika Muster/ }, { timeout: 2000 }));
    expect(screen.getByText('Erika Muster')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Name *'), { target: { value: 'sw-03' } });
    fireEvent.click(screen.getByRole('button', { name: 'Anlegen' }));
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    expect(submit.mock.calls[0]![0]).toMatchObject({
      attributes: { host: HOST_CI, room: LOCATION, owner: USER },
    });
  });

  it('shows 422 violations at the field of the fields step', async () => {
    stubBackend({
      forms: { [SWITCH]: SWITCH_FORM },
      post: () =>
        json(
          {
            type: 'https://reticora.io/problems/422',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'invalid',
            violations: [
              { field: 'attributes.snmp_community', detail: 'too short' },
              { field: 'name', detail: 'already taken' },
            ],
          },
          422,
        ),
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch' }));
    next();
    next();
    fireEvent.change(await screen.findByLabelText('Name *'), { target: { value: 'sw-04' } });
    fireEvent.click(screen.getByRole('button', { name: 'Anlegen' }));
    await waitFor(() =>
      expect(screen.getByLabelText('SNMP-Community')).toHaveAccessibleDescription('too short'),
    );
    expect(screen.getByLabelText('Name *')).toHaveAccessibleDescription('already taken');
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Bitte korrigieren Sie die markierten Felder.',
    );
  });

  it('returns to the tiles with a hint when the type was deactivated (type-inactive)', async () => {
    const { requests } = stubBackend({
      forms: { [SWITCH]: SWITCH_FORM },
      post: () =>
        json(
          {
            type: 'https://reticora.io/problems/422',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'the CI type is deactivated',
            violations: [{ field: 'ci_type_id', detail: 'the CI type is deactivated' }],
          },
          422,
        ),
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch' }));
    next();
    next();
    fireEvent.change(await screen.findByLabelText('Name *'), { target: { value: 'sw-05' } });
    const typeReads = () => requests.filter((u) => u.pathname.endsWith('/ci-types')).length;
    const before = typeReads();
    fireEvent.click(screen.getByRole('button', { name: 'Anlegen' }));

    expect(await screen.findByText(/inzwischen deaktiviert/)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Typ' })).toHaveFocus();
    expect(screen.getByRole('button', { name: 'Switch' })).toHaveAttribute('aria-pressed', 'false');
    expect(screen.getByRole('button', { name: 'Weiter' })).toBeDisabled();
    await waitFor(() => expect(typeReads()).toBeGreaterThan(before));
  });
});

describe('GuidedCreate owner (CI-07, WP-805)', () => {
  const teams = [{ id: TEAM, name: 'Netzbetrieb', member_count: 3 }];

  /** Opens the creation of a switch and chooses user and team as owner. */
  async function chooseOwner() {
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch' }));
    next();
    next();
    fireEvent.change(await screen.findByLabelText('Name *'), { target: { value: 'sw-06' } });
    expect(screen.getByRole('group', { name: 'Verantwortlich' })).toBeInTheDocument();
    fireEvent.change(screen.getByRole('combobox', { name: 'Verantwortlicher Nutzer' }), {
      target: { value: 'erika' },
    });
    fireEvent.click(await screen.findByRole('option', { name: /Erika Muster/ }, { timeout: 2000 }));
    fireEvent.change(screen.getByRole('combobox', { name: 'Verantwortliches Team' }), {
      target: { value: 'netz' },
    });
    fireEvent.click(await screen.findByRole('option', { name: /Netzbetrieb/ }, { timeout: 2000 }));
    fireEvent.click(screen.getByRole('button', { name: 'Anlegen' }));
  }

  it('sends the chosen user and team as owner_user_id and owner_team_id', async () => {
    const { posted } = stubBackend({ forms: { [SWITCH]: SWITCH_FORM }, teams });
    await chooseOwner();

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ owner_user_id: USER, owner_team_id: TEAM });
  });

  it('sends no owner when none is chosen', async () => {
    const { posted } = stubBackend({ forms: { [SWITCH]: SWITCH_FORM } });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch' }));
    next();
    next();
    fireEvent.change(await screen.findByLabelText('Name *'), { target: { value: 'sw-07' } });
    fireEvent.click(screen.getByRole('button', { name: 'Anlegen' }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).not.toHaveProperty('owner_user_id');
    expect(posted[0]).not.toHaveProperty('owner_team_id');
  });

  it('shows a 403 of the team scope at the team field (TEN-05, E-11)', async () => {
    stubBackend({
      forms: { [SWITCH]: SWITCH_FORM },
      teams,
      post: () =>
        json(
          {
            type: 'about:blank',
            title: 'Forbidden',
            status: 403,
            detail: 'the team is outside the team scope',
          },
          403,
        ),
    });
    await chooseOwner();

    const group = screen.getByRole('group', { name: 'Verantwortlich' });
    expect(
      await within(group).findByText('Dieses Team liegt außerhalb Ihres Team-Bereichs.'),
    ).toBeInTheDocument();
    // The server's words stay in the banner: a 403 without fields may be a missing right.
    expect(screen.getByText('the team is outside the team scope')).toBeInTheDocument();
  });

  it('shows 422 violations of the owner at their fields', async () => {
    stubBackend({
      forms: { [SWITCH]: SWITCH_FORM },
      teams,
      post: () =>
        json(
          {
            type: 'https://reticora.io/problems/422',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'invalid',
            violations: [
              { field: 'owner_user_id', detail: 'unknown user' },
              { field: 'owner_team_id', detail: 'unknown team' },
            ],
          },
          422,
        ),
    });
    await chooseOwner();

    const group = screen.getByRole('group', { name: 'Verantwortlich' });
    expect(await within(group).findByText('unknown user')).toBeInTheDocument();
    expect(within(group).getByText('unknown team')).toBeInTheDocument();
    expect(screen.getByText('Bitte korrigieren Sie die markierten Felder.')).toBeInTheDocument();
    expect(screen.queryByText(/owner_user_id/)).not.toBeInTheDocument();
  });
});

describe('GuidedCreate terms (UI-12)', () => {
  const FORBIDDEN =
    /attribute[ -]?set|attribut-?set|template|vorlage|layout|metamodell|metamodel|instanzfeld|instance field|_required|_default|_instance/i;

  it.each(['de-DE', 'en-US'])('shows no metamodel terms in any step (%s)', async (language) => {
    await act(async () => {
      await i18n.changeLanguage(language);
    });
    stubBackend({
      forms: {
        [SWITCH]: resolvedForm(SWITCH, [
          { key: 'management', fields: [{ name: 'snmp', label: 'SNMP', data_type: 'text' }] },
          {
            key: '_required',
            title: { de: 'Weitere Pflichtfelder', en: 'Further required fields' },
            fields: [
              { name: 'asset_ref_field', label: 'Inventar', data_type: 'text', required: true },
            ],
          },
        ]),
      },
      templates: {
        [SWITCH]: [
          {
            id: TEMPLATE_A,
            key: 'access',
            ci_type_id: SWITCH,
            display_name: { de: 'Access', en: 'Access' },
            is_system: false,
            is_active: true,
            current_version: 1,
            created_at: '2026-01-01T00:00:00Z',
            updated_at: '2026-01-01T00:00:00Z',
          },
          {
            id: TEMPLATE_B,
            key: 'core',
            ci_type_id: SWITCH,
            display_name: { de: 'Core', en: 'Core' },
            is_system: false,
            is_active: true,
            current_version: 1,
            created_at: '2026-01-01T00:00:00Z',
            updated_at: '2026-01-01T00:00:00Z',
          },
        ],
      },
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);
    const dialogText = () => screen.getByRole('dialog').textContent ?? '';
    const switchLabel = await screen.findByRole('button', { name: 'Switch' });
    expect(dialogText()).not.toMatch(FORBIDDEN);
    fireEvent.click(switchLabel);
    fireEvent.click(screen.getByRole('button', { name: language === 'de-DE' ? 'Weiter' : 'Next' }));
    expect(dialogText()).not.toMatch(FORBIDDEN);
    fireEvent.click(screen.getByRole('button', { name: language === 'de-DE' ? 'Weiter' : 'Next' }));
    await screen.findByLabelText('SNMP');
    await screen.findByLabelText(language === 'de-DE' ? 'Ausführung *' : 'Variant *');
    expect(dialogText()).not.toMatch(FORBIDDEN);
  });
});
