/**
 * CIFormModal: the CI types come from the metamodel API, the chosen type's
 * attributes are captured, and the API's 422 rejections land on the offending
 * field.
 *
 * - CI-03, MET-04 (WP-397, F-016): the type options are the active types of
 *   GET /ci-types (system and organization types, also ones created at
 *   runtime) with the type id as value and display_name in the UI language
 *   as label; POST /cis carries the chosen id. Before WP-397 the dialog
 *   offered eight hard-coded type keys and posted the key ('server'), which
 *   the API refuses. The cases of CIFormModal.auditdefect.tsx and
 *   CITypeLabels.auditdefect.tsx (WP-201, WP-194) moved here unchanged.
 * - CI-01, MET-11: after the type choice the dialog renders the attributes of
 *   the type schema (GET /ci-types/{id}) and sends them in attributes.
 * - CI-04, API-07 (WP-477, F-081, F-228): editing sends only the members
 *   whose value differs from the loaded CI, a cleared optional field as null
 *   (JSON merge patch), and no request at all without a change. Before
 *   WP-477 the dialog sent every form value and dropped cleared fields
 *   (optionalValue('') → undefined): the save reported success, the value
 *   stayed, and a stale editor got 409 for a field it had not touched.
 * - DB-05, AST-04 (WP-577, F-238): a CI linked to an asset takes serial
 *   number and location from the asset; the dialog shows both disabled with
 *   the hint "Vom Asset geführt" and never sends them. Before WP-577 the
 *   serial number was editable and the save failed with 422.
 * - Server-side validation: the backend validates CI attributes against the
 *   CI type's field metadata and answers with per-field `violations`; before
 *   the regression test the client discarded that array, so the user saw one
 *   opaque sentence and had no idea which field to fix.
 *
 * - MET-51 (WP-317): a CI is created through the guided creation
 *   (GuidedCreate): type tiles, manufacturer and model, then the fields of
 *   the resolved form GET /ci-types/{id}/form; editing stays one step.
 *   The cases of the type select moved to the tiles.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/CIFormModal.test.tsx
 */
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { I18nextProvider } from 'react-i18next';
import i18n from '../i18n';
import type { CI } from '../api/client';
import type { FieldDefinition } from '../lib/fieldmeta';
import { renderWithProviders } from '../test/utils';
import { CIFormModal } from './CIFormModal';

const SYSTEM_TYPE_ID = '11111111-1111-4111-8111-111111111111';
const ORG_TYPE_ID = '22222222-2222-4222-8222-222222222222';
const SERVER_TYPE_ID = '7a0c1e2d-0000-4000-8000-0000000000a1';
const RACK_TYPE_ID = '7a0c1e2d-0000-4000-8000-0000000000a2';
const CI_ID = '7a0c1e2d-0000-4000-8000-0000000000c1';

interface TypeFixture {
  id: string;
  key: string;
  name: string;
  display_name: { de?: string; en?: string };
  is_builtin: boolean;
  is_system: boolean;
  is_active: boolean;
  is_logical: boolean;
  is_observable: boolean;
  schema_version: number;
  version: number;
  fields?: FieldDefinition[];
}

function ciType(id: string, key: string, name: string, system: boolean): TypeFixture {
  return {
    id,
    key,
    name,
    display_name: { de: name, en: name },
    is_builtin: system,
    is_system: system,
    is_active: true,
    is_logical: false,
    is_observable: true,
    schema_version: 1,
    version: 1,
  };
}

const serverType: TypeFixture = {
  ...ciType(SERVER_TYPE_ID, 'server', 'server', true),
  display_name: { de: 'Server (physisch)', en: 'Server (physical)' },
};

/** An organization type with attributes of several data types (MET-11). */
const rackType: TypeFixture = {
  ...ciType(RACK_TYPE_ID, 'audit-rack-unit', 'Rack-Einheit', false),
  fields: [
    { name: 'rack_units', label: 'Höheneinheiten', data_type: 'integer', required: true },
    {
      name: 'airflow',
      label: 'Luftstrom',
      data_type: 'enum',
      enum_values: ['front_to_back', 'back_to_front'],
    },
    { name: 'dual_psu', label: 'Redundantes Netzteil', data_type: 'boolean' },
  ],
};

const storedCI: CI = {
  id: CI_ID,
  organization_id: 'org-1',
  ci_type_id: RACK_TYPE_ID,
  name: 'rack-unit-01',
  status: 'active',
  version: 3,
  attributes: { rack_units: 2, airflow: 'front_to_back', discovered_only: 'keep' },
  tags: [],
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-02T00:00:00Z',
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function violations(...items: Array<{ field: string; detail: string }>) {
  return json(
    {
      type: 'https://reticora.io/problems/422',
      title: 'Unprocessable Entity',
      status: 422,
      detail: items.map((v) => `${v.field}: ${v.detail}`).join('; '),
      violations: items,
    },
    422,
  );
}

interface BackendOptions {
  /** Answer of GET /ci-types (the active types). */
  types?: TypeFixture[];
  /**
   * The active types split into pages: GET /ci-types answers the page at the
   * requested offset with has_more until the last one (overrides types).
   */
  pages?: TypeFixture[][];
  /** Further types GET /ci-types/{id} knows (deactivated ones). */
  hidden?: TypeFixture[];
  /** Answer of POST /cis; the default stores the body and answers 201. */
  post?: (body: Record<string, unknown>) => Response;
  /** Answer of GET /locations/tree; empty without it. */
  tree?: unknown[];
  /** Answer of GET /ci-fields/global (organization-wide fields, MET-13). */
  globals?: FieldDefinition[];
  /** Users and teams of GET /users, /teams and /{id} (owner, WP-805). */
  users?: Array<{ id: string; display_name: string }>;
  teams?: Array<{ id: string; name: string }>;
}

/** Answers by exact API path; records the requests and the POST/PATCH bodies. */
function stubBackend({
  pages,
  types = pages ? pages.flat() : [serverType],
  hidden = [],
  post,
  tree,
  globals = [],
  users = [],
  teams = [],
}: BackendOptions = {}) {
  const posted: Array<Record<string, unknown>> = [];
  const patched: Array<Record<string, unknown>> = [];
  const requests: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input.toString();
    const url = new URL(raw, 'http://localhost');
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const method = (init?.method ?? 'GET').toUpperCase();
    requests.push(`${method} ${path}${url.search}`);
    if (method === 'GET' && path === '/ci-types' && pages) {
      const offset = Number(url.searchParams.get('offset') ?? 0);
      let start = 0;
      const index = pages.findIndex((page) => {
        const found = start === offset;
        start += page.length;
        return found;
      });
      const page = pages[index] ?? [];
      return json({
        data: page,
        total: types.length,
        limit: 200,
        offset,
        has_more: index >= 0 && index < pages.length - 1,
      });
    }
    if (method === 'GET' && path === '/ci-types') {
      return json({ data: types, total: types.length, limit: 200, offset: 0, has_more: false });
    }
    const known = [...types, ...hidden].find((ty) => path === `/ci-types/${ty.id}`);
    if (method === 'GET' && known) {
      return json(known);
    }
    if (method === 'GET' && path === '/ci-fields/global') {
      return json({ data: globals });
    }
    if (method === 'GET' && (path === '/users' || path === '/teams')) {
      const data = path === '/users' ? users : teams;
      return json({ data, total: data.length, limit: 20, offset: 0, has_more: false });
    }
    const person =
      users.find((user) => path === `/users/${user.id}`) ??
      teams.find((team) => path === `/teams/${team.id}`);
    if (method === 'GET' && person) {
      return json(person);
    }
    if (method === 'GET' && path === '/locations/tree') {
      return json({ data: tree ?? [] });
    }
    // The guided creation (WP-317): the resolved form of a type (its fields
    // in the standard section), no variants and no stored values.
    const formOf = [...types, ...hidden].find((ty) => path === `/ci-types/${ty.id}/form`);
    if (method === 'GET' && formOf) {
      return json({
        ci_type_id: formOf.id,
        source: 'default',
        sections: [
          {
            key: '_default',
            title: { de: 'Allgemein', en: 'General' },
            visible: true,
            fields: (formOf.fields ?? []).map((def) => ({
              name: def.name,
              width: 12,
              definition: { required: false, sort_order: 0, ...def },
              state: { visible: true, required: !!def.required, read_only: false },
            })),
          },
        ],
        rules: [],
      });
    }
    if (method === 'GET' && (path === '/templates' || path === '/cis')) {
      return json({ data: [], total: 0, limit: 50, offset: 0, has_more: false });
    }
    if (method === 'POST' && path === '/cis') {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
      posted.push(body);
      return post ? post(body) : json({ ...storedCI, id: CI_ID, version: 1, ...body }, 201);
    }
    if (method === 'PATCH' && path === `/cis/${CI_ID}`) {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
      patched.push(body);
      return json({ ...storedCI, ...body, version: storedCI.version! + 1 });
    }
    return json({ detail: `unexpected request: ${method} ${path}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { posted, patched, requests };
}

function typeSelect(): HTMLSelectElement {
  return screen.getByLabelText(/typ|type/i) as HTMLSelectElement;
}

function submitButton() {
  return screen.getByRole('button', { name: /anlegen|create|speichern|save/i });
}

afterEach(async () => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

/** The guided creation (WP-317): the button of a type tile. */
async function typeTile(name: string | RegExp) {
  return screen.findByRole('button', { name }, { timeout: 2000 });
}

function tileNames() {
  return screen
    .getAllByRole('button', { pressed: false })
    .concat(screen.queryAllByRole('button', { pressed: true }))
    .map((button) => button.textContent ?? '');
}

/** Chooses a type tile and goes on to the fields step of the guided creation. */
async function chooseTile(name: string | RegExp) {
  fireEvent.click(await typeTile(name));
  fireEvent.click(screen.getByRole('button', { name: /^(Weiter|Next)$/ }));
  // A physical type asks for manufacturer and model first.
  const next = screen.queryByRole('button', { name: /^(Weiter|Next)$/ });
  if (next) fireEvent.click(next);
  return screen.findByLabelText(/^Name \*$/);
}

function createButton() {
  return screen.getByRole('button', { name: /^(Anlegen|Create)$/ });
}

describe('CIFormModal CI types come from the metamodel API (guided creation, WP-317)', () => {
  it('offers the org type created at runtime as a tile (MET-04)', async () => {
    stubBackend({
      types: [
        ciType(SYSTEM_TYPE_ID, 'server', 'Server', true),
        ciType(ORG_TYPE_ID, 'audit-custom-pdu', 'Audit Custom PDU', false),
      ],
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    expect(await typeTile('Audit Custom PDU')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Server' })).toBeInTheDocument();
  });

  it('posts the id of the chosen tile as ci_type_id (CI-03)', async () => {
    const { posted } = stubBackend({
      types: [
        ciType(SYSTEM_TYPE_ID, 'server', 'Server', true),
        ciType(ORG_TYPE_ID, 'audit-custom-pdu', 'Audit Custom PDU', false),
      ],
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    const name = await chooseTile('Audit Custom PDU');
    fireEvent.change(name, { target: { value: 'srv-audit-01' } });
    fireEvent.click(createButton());

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({
      name: 'srv-audit-01',
      ci_type_id: ORG_TYPE_ID,
      status: 'active',
      discovery_source: 'manual',
    });
  });

  it('requests the active types only while open and reads them again at every opening', async () => {
    const types = [ciType(SYSTEM_TYPE_ID, 'server', 'Server', true)];
    const { requests } = stubBackend({ types });
    // The query defaults of the app (main.tsx): a cached answer counts as
    // fresh for 30 s, which would hide a type created in the meantime.
    const queryClient = new QueryClient({
      defaultOptions: { queries: { staleTime: 30_000, retry: false }, mutations: { retry: false } },
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <MemoryRouter>{children}</MemoryRouter>
        </QueryClientProvider>
      </I18nextProvider>
    );
    const view = render(<CIFormModal open={false} onOpenChange={() => {}} />, { wrapper });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(requests, 'no type request while the dialog is closed').toEqual([]);

    view.rerender(<CIFormModal open onOpenChange={() => {}} />);
    await typeTile('Server');
    expect(requests.filter((r) => r.startsWith('GET /ci-types?'))).toEqual([
      'GET /ci-types?limit=200',
    ]);

    // An organization type is created elsewhere while the SPA stays loaded.
    types.push(ciType(ORG_TYPE_ID, 'audit-runtime-type', 'Laufzeittyp', false));
    view.rerender(<CIFormModal open={false} onOpenChange={() => {}} />);
    view.rerender(<CIFormModal open onOpenChange={() => {}} />);
    expect(await typeTile('Laufzeittyp')).toBeInTheDocument();
  });

  it('labels the tiles in the UI language and falls back to the key', async () => {
    stubBackend({
      types: [serverType, { ...ciType(ORG_TYPE_ID, 'org_only_key', 'x', false), display_name: {} }],
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await typeTile('Server (physisch)');
    expect(tileNames()).toEqual(expect.arrayContaining(['org_only_key', 'Server (physisch)']));
  });

  it('does not go on or post without a type choice', async () => {
    const { posted } = stubBackend();
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);
    await typeTile('Server (physisch)');

    const next = screen.getByRole('button', { name: 'Weiter' });
    expect(next).toBeDisabled();
    fireEvent.submit(next.closest('form')!);
    expect(screen.queryByLabelText('Name *')).not.toBeInTheDocument();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(posted, 'no POST /cis without a type').toHaveLength(0);
  });

  it('preselects the stored type in edit mode, also a deactivated one', async () => {
    stubBackend({ types: [serverType], hidden: [{ ...rackType, is_active: false }] });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} ci={storedCI} />);

    await waitFor(() => expect(typeSelect().value).toBe(RACK_TYPE_ID));
    expect(typeSelect()).toBeDisabled();
    expect(typeSelect().selectedOptions[0]?.textContent).toBe('Rack-Einheit');
    // Editing stays one step: no tiles, no step list.
    expect(screen.queryByRole('list', { name: 'Schritte der Anlage' })).not.toBeInTheDocument();
  });
});

describe('CI type labels and ids from the metamodel (WP-201 / WP-194)', () => {
  it('posts the CI type UUID from GET /ci-types when the user picks "Server" (WP-201 P1)', async () => {
    const { posted } = stubBackend();
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    // The user picks the server type, whatever the tile is labelled today.
    const name = await chooseTile(/server/i);
    fireEvent.change(name, { target: { value: 'srv-label-01' } });
    fireEvent.click(createButton());

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(
      posted[0]?.ci_type_id,
      'POST /cis ci_type_id must be the type id from GET /ci-types (the API rejects a type key with 400)',
    ).toBe(SERVER_TYPE_ID);
  });

  it('labels the tiles with display_name in the UI language (WP-194 P2)', async () => {
    stubBackend();
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await typeTile('Server (physisch)');
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(await typeTile('Server (physical)')).toBeInTheDocument();
  });
});

describe('CIFormModal type attributes', () => {
  it('renders the fields of the resolved form and posts them in attributes', async () => {
    const { posted, requests } = stubBackend({ types: [serverType, rackType] });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    const name = await chooseTile('Rack-Einheit');
    fireEvent.change(name, { target: { value: 'rack-unit-02' } });
    const units = await screen.findByLabelText('Höheneinheiten *');
    expect(requests).toContain(`GET /ci-types/${RACK_TYPE_ID}/form`);
    expect(screen.getByRole('group', { name: 'Allgemein' })).toBeInTheDocument();
    fireEvent.change(units, { target: { value: '4' } });
    fireEvent.change(screen.getByLabelText('Luftstrom'), { target: { value: 'back_to_front' } });
    fireEvent.click(screen.getByLabelText('Redundantes Netzteil'));
    fireEvent.click(createButton());

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({
      name: 'rack-unit-02',
      ci_type_id: RACK_TYPE_ID,
      discovery_source: 'manual',
      attributes: { rack_units: 4, airflow: 'back_to_front', dual_psu: true },
    });
  });

  it('sends no attributes of a type the user switched away from', async () => {
    const { posted } = stubBackend({ types: [serverType, rackType] });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    fireEvent.change(await chooseTile('Rack-Einheit'), { target: { value: 'srv-switched' } });
    fireEvent.change(await screen.findByLabelText('Höheneinheiten *'), { target: { value: '4' } });
    fireEvent.click(screen.getByRole('button', { name: 'Zurück' }));
    fireEvent.click(screen.getByRole('button', { name: 'Zurück' }));
    await chooseTile('Server (physisch)');
    expect(screen.queryByLabelText('Höheneinheiten *')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Name *')).toHaveValue('srv-switched');
    fireEvent.click(createButton());

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]?.ci_type_id).toBe(SERVER_TYPE_ID);
    expect(posted[0]).not.toHaveProperty('attributes');
  });

  it('checks the fields before posting and names the invalid field', async () => {
    const { posted } = stubBackend({ types: [rackType] });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    fireEvent.change(await chooseTile('Rack-Einheit'), { target: { value: 'rack-unit-03' } });
    const units = await screen.findByLabelText('Höheneinheiten *');
    fireEvent.click(createButton());
    expect(await screen.findByText('Höheneinheiten ist erforderlich')).toBeInTheDocument();
    expect(units).toHaveAttribute('aria-invalid', 'true');
    // A fraction is no integer.
    fireEvent.change(units, { target: { value: '1.5' } });
    fireEvent.click(createButton());
    expect(await screen.findByText('Höheneinheiten muss eine ganze Zahl sein')).toBeInTheDocument();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(posted, 'no POST /cis with an invalid attribute').toHaveLength(0);
  });

  it('shows the stored attributes in edit mode and patches only the changed ones', async () => {
    const { patched } = stubBackend({ types: [rackType] });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} ci={storedCI} />);

    const units = await screen.findByLabelText('Höheneinheiten *');
    expect(units).toHaveValue(2);
    expect(screen.getByLabelText('Luftstrom')).toHaveValue('front_to_back');
    fireEvent.change(units, { target: { value: '3' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]?.attributes).toEqual({ rack_units: 3 });
    expect(patched[0]).not.toHaveProperty('ci_type_id');
  });
});

describe('CIFormModal edit sends a merge patch of the changed fields (CI-04, API-07)', () => {
  const editedCI: CI = {
    ...storedCI,
    ci_type_id: SERVER_TYPE_ID,
    name: 'srv-edit-01',
    status: 'active',
    version: 7,
    manufacturer: 'Old Vendor',
    model: 'M-0',
    serial_number: 'SN-1',
    management_ip: '10.1.1.1',
    firmware_version: '9.9.9',
    attributes: {},
  };

  async function openEditor(ci: CI = editedCI) {
    const onOpenChange = vi.fn();
    const backend = stubBackend({ types: [serverType, rackType] });
    renderWithProviders(<CIFormModal open onOpenChange={onOpenChange} ci={ci} />);
    await waitFor(() => expect(typeSelect().value).toBe(ci.ci_type_id));
    return { ...backend, onOpenChange };
  }

  function patchCalls() {
    const fetchMock = vi.mocked(fetch);
    return fetchMock.mock.calls.filter(
      ([, init]) => (init?.method ?? 'GET').toUpperCase() === 'PATCH',
    );
  }

  it('sends only the changed manufacturer, with If-Match on the loaded version', async () => {
    const { patched, onOpenChange } = await openEditor();

    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'New Vendor' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ manufacturer: 'New Vendor' });
    const headers = new Headers(patchCalls()[0]?.[1]?.headers);
    expect(headers.get('If-Match')).toBe('"7"');
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it('sends a cleared firmware version as null (F-081)', async () => {
    const { patched } = await openEditor();

    expect(screen.getByLabelText('Firmware-Version')).toHaveValue('9.9.9');
    fireEvent.change(screen.getByLabelText('Firmware-Version'), { target: { value: '' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ firmware_version: null });
  });

  it('sends several changes and clearings together, untouched fields stay out', async () => {
    const { patched } = await openEditor();

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'srv-edit-02' } });
    fireEvent.change(screen.getByLabelText('Status'), { target: { value: 'maintenance' } });
    fireEvent.change(screen.getByLabelText('Modell'), { target: { value: '  ' } });
    fireEvent.change(screen.getByLabelText('Management-IP'), { target: { value: '10.1.1.1 ' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ name: 'srv-edit-02', status: 'maintenance', model: null });
  });

  it('sends no request and closes when nothing changed', async () => {
    const { patched, onOpenChange } = await openEditor();

    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'Changed' } });
    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'Old Vendor' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(patched, 'no PATCH without a change').toHaveLength(0);
    expect(patchCalls()).toHaveLength(0);
  });

  it('sends a cleared type attribute as null and leaves the others out', async () => {
    const { patched } = await openEditor(storedCI);

    const airflow = await screen.findByLabelText('Luftstrom');
    expect(airflow).toHaveValue('front_to_back');
    fireEvent.change(airflow, { target: { value: '' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ attributes: { airflow: null } });
  });
});

describe('CIFormModal server-side validation', () => {
  /** Chooses the server tile, fills the name and submits the creation. */
  async function fillAndSubmit() {
    fireEvent.change(await chooseTile('Server (physisch)'), { target: { value: 'srv-01' } });
    fireEvent.click(createButton());
  }

  it('renders a 422 violation on the matching input and unmapped ones in the banner', async () => {
    stubBackend({
      post: () =>
        violations(
          { field: 'name', detail: 'must match the required format' },
          { field: 'ram_gb', detail: 'field is required' },
        ),
    });

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await fillAndSubmit();

    // The violation whose field matches a form input is shown inline.
    await waitFor(() => {
      expect(screen.getByLabelText('Name *')).toHaveAccessibleDescription(
        'must match the required format',
      );
    });

    // `ram_gb` is a field the form does not show, so it has no input in this
    // modal and must still be reported rather than silently dropped.
    expect(screen.getByRole('alert')).toHaveTextContent('ram_gb: field is required');
  });

  it('shows the plain detail when the failure has no violations', async () => {
    stubBackend({ post: () => json({ status: 409, detail: 'name already in use' }, 409) });

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await fillAndSubmit();

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('name already in use');
    });
  });

  it('reports a 422 violation of ci_type_id in the banner', async () => {
    stubBackend({ post: () => violations({ field: 'ci_type_id', detail: 'CI type not found' }) });

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await fillAndSubmit();

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('ci_type_id: CI type not found'),
    );
  });

  it('shows a 422 violation of a type attribute at its field', async () => {
    stubBackend({
      types: [rackType],
      post: () => violations({ field: 'rack_units', detail: 'must be at most 60' }),
    });

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    fireEvent.change(await chooseTile('Rack-Einheit'), { target: { value: 'rack-unit-04' } });
    const units = await screen.findByLabelText('Höheneinheiten *');
    fireEvent.change(units, { target: { value: '61' } });
    fireEvent.click(createButton());

    await waitFor(() => expect(units).toHaveAttribute('aria-invalid', 'true'));
    expect(units).toHaveAccessibleDescription('must be at most 60');
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Bitte korrigieren Sie die markierten Felder.');
    expect(within(alert).queryByText(/rack_units/)).not.toBeInTheDocument();
  });

  it('offers the types of all pages of GET /ci-types (MET-51, WP-804)', async () => {
    const { requests, posted } = stubBackend({
      pages: [
        [ciType(SYSTEM_TYPE_ID, 'server', 'Server', true)],
        [ciType(ORG_TYPE_ID, 'audit-second-page', 'Typ auf Seite zwei', false)],
      ],
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await typeTile('Typ auf Seite zwei');
    expect(screen.getByRole('button', { name: 'Server' })).toBeInTheDocument();
    expect(requests.filter((r) => r.startsWith('GET /ci-types?'))).toEqual([
      'GET /ci-types?limit=200',
      'GET /ci-types?limit=200&offset=1',
    ]);

    fireEvent.change(await chooseTile('Typ auf Seite zwei'), {
      target: { value: 'srv-page-two' },
    });
    fireEvent.click(createButton());
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]?.ci_type_id).toBe(ORG_TYPE_ID);
  });

  it('offers a retry and blocks going on while the types cannot be loaded (WP-804)', async () => {
    let failing = true;
    const { posted } = stubBackend({
      types: [ciType(SYSTEM_TYPE_ID, 'server', 'Server', true)],
    });
    const backend = globalThis.fetch;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const raw = typeof input === 'string' ? input : input.toString();
        if (failing && new URL(raw, 'http://localhost').pathname.endsWith('/ci-types')) {
          return json({ detail: 'unavailable' }, 503);
        }
        return backend(input, init);
      }),
    );
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    const retry = await screen.findByRole('button', { name: 'Erneut versuchen' });
    expect(screen.getByText('Die CI-Typen konnten nicht geladen werden.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Weiter' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Anlegen' })).not.toBeInTheDocument();

    failing = false;
    fireEvent.click(retry);
    fireEvent.change(await chooseTile('Server'), { target: { value: 'srv-retried' } });
    expect(
      screen.queryByText('Die CI-Typen konnten nicht geladen werden.'),
    ).not.toBeInTheDocument();
    fireEvent.click(createButton());
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]?.ci_type_id).toBe(SYSTEM_TYPE_ID);
  });
});

describe('CIFormModal for a CI linked to an asset (DB-05, AST-04, F-238)', () => {
  const SITE = '10c00000-0000-4000-8000-000000000001';
  const ROOM = '10c00000-0000-4000-8000-000000000003';
  const ASSET = 'a55e7000-0000-4000-8000-000000000001';
  const node = (
    id: string,
    kind: string,
    name: string,
    parent: string | undefined,
    children: unknown[],
  ) => ({
    id,
    organization_id: 'org-1',
    client_id: 'client-1',
    site_id: SITE,
    ...(parent ? { parent_id: parent } : {}),
    kind,
    name,
    path: id,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    children,
  });
  const tree = [
    node(SITE, 'site', 'RZ Nord', undefined, [node(ROOM, 'room', 'Raum 1.01', SITE, [])]),
  ];

  const linkedCI: CI = {
    ...storedCI,
    ci_type_id: SERVER_TYPE_ID,
    name: 'srv-linked-01',
    version: 4,
    manufacturer: 'Old Vendor',
    serial_number: 'SN-OBSERVED',
    location_id: ROOM,
    site_id: SITE,
    room_id: ROOM,
    asset_id: ASSET,
    asset: {
      id: ASSET,
      asset_tag: 'INV-0042',
      name: 'srv-linked-01 hardware',
      status: 'assigned',
      serial_number: 'SN-ASSET-42',
      location_id: ROOM,
    },
    attributes: {},
  };

  async function openEditor(ci: CI) {
    const onOpenChange = vi.fn();
    const backend = stubBackend({ types: [serverType], tree });
    renderWithProviders(<CIFormModal open onOpenChange={onOpenChange} ci={ci} />);
    await waitFor(() => expect(typeSelect().value).toBe(ci.ci_type_id));
    return { ...backend, onOpenChange };
  }

  it('shows serial number and location disabled with the hint and sends neither', async () => {
    const { patched, requests } = await openEditor(linkedCI);

    const serial = screen.getByLabelText('Seriennummer');
    expect(serial).toBeDisabled();
    expect(serial).toHaveValue('SN-ASSET-42');
    expect(serial).toHaveAccessibleDescription('Vom Asset geführt');
    const location = screen.getByLabelText('Standort');
    expect(location).toBeDisabled();
    await waitFor(() => expect(location).toHaveValue('RZ Nord › Raum 1.01'));
    expect(location).toHaveAccessibleDescription('Vom Asset geführt');
    expect(screen.getAllByText('Vom Asset geführt')).toHaveLength(2);
    expect(requests).toContain('GET /locations/tree');

    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'New Vendor' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ manufacturer: 'New Vendor' });
    expect(patched[0]).not.toHaveProperty('serial_number');
    expect(patched[0]).not.toHaveProperty('location_id');
  });

  it('labels the hint in English', async () => {
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    await openEditor(linkedCI);

    expect(screen.getByLabelText('Serial Number')).toHaveAccessibleDescription(
      'Managed by the asset',
    );
    expect(screen.getByLabelText('Location')).toBeDisabled();
  });

  it('keeps the serial number editable and shows no location field without an asset', async () => {
    const { patched, requests } = await openEditor({
      ...linkedCI,
      asset_id: undefined,
      asset: undefined,
    });

    const serial = screen.getByLabelText('Seriennummer');
    expect(serial).toBeEnabled();
    expect(serial).toHaveValue('SN-OBSERVED');
    expect(screen.queryByLabelText('Standort')).toBeNull();
    expect(screen.queryByText('Vom Asset geführt')).toBeNull();
    expect(requests).not.toContain('GET /locations/tree');

    fireEvent.change(serial, { target: { value: 'SN-NEW' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ serial_number: 'SN-NEW' });
  });
});

describe('CIFormModal owner (CI-07, WP-805)', () => {
  const USER_A = '0a000000-0000-4000-8000-0000000000a1';
  const USER_B = '0a000000-0000-4000-8000-0000000000b1';
  const TEAM_A = '0e000000-0000-4000-8000-0000000000a1';
  const users = [
    { id: USER_A, display_name: 'Erika Muster' },
    { id: USER_B, display_name: 'Max Beispiel' },
  ];
  const teams = [{ id: TEAM_A, name: 'Netzbetrieb' }];
  const ownedCI: CI = {
    ...storedCI,
    ci_type_id: SERVER_TYPE_ID,
    name: 'srv-owned-01',
    version: 5,
    owner_user_id: USER_A,
    owner_team_id: TEAM_A,
    attributes: {},
  };

  async function openEditor(ci: CI) {
    const backend = stubBackend({ types: [serverType], users, teams });
    renderWithProviders(<CIFormModal open onOpenChange={vi.fn()} ci={ci} />);
    await waitFor(() => expect(typeSelect().value).toBe(ci.ci_type_id));
    return backend;
  }

  function patchHeaders() {
    const call = vi
      .mocked(fetch)
      .mock.calls.find(([, init]) => (init?.method ?? 'GET').toUpperCase() === 'PATCH');
    return new Headers(call?.[1]?.headers);
  }

  async function pickUser(query: string, name: RegExp) {
    fireEvent.change(screen.getByRole('combobox', { name: 'Verantwortlicher Nutzer' }), {
      target: { value: query },
    });
    fireEvent.click(await screen.findByRole('option', { name }, { timeout: 2000 }));
  }

  it('shows the stored owner by name and changes the user with If-Match', async () => {
    const { patched } = await openEditor(ownedCI);
    const group = screen.getByRole('group', { name: 'Verantwortlich' });
    expect(await within(group).findByText('Erika Muster')).toBeInTheDocument();
    expect(await within(group).findByText('Netzbetrieb')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Erika Muster entfernen' }));
    await pickUser('max', /Max Beispiel/);
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ owner_user_id: USER_B });
    expect(patchHeaders().get('If-Match')).toBe('"5"');
  });

  it('sends "" when the owner is removed', async () => {
    const { patched } = await openEditor(ownedCI);
    fireEvent.click(await screen.findByRole('button', { name: 'Netzbetrieb entfernen' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Erika Muster entfernen' }));
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ owner_user_id: '', owner_team_id: '' });
  });

  it('sends no owner when it stays unchanged', async () => {
    const { patched } = await openEditor(ownedCI);
    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'Vendor' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ manufacturer: 'Vendor' });
  });

  it('sets an owner on a CI without one', async () => {
    const { patched } = await openEditor({
      ...ownedCI,
      owner_user_id: undefined,
      owner_team_id: undefined,
    });
    fireEvent.change(screen.getByRole('combobox', { name: 'Verantwortliches Team' }), {
      target: { value: 'netz' },
    });
    fireEvent.click(await screen.findByRole('option', { name: /Netzbetrieb/ }, { timeout: 2000 }));
    fireEvent.click(submitButton());

    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ owner_team_id: TEAM_A });
  });

  it('shows the owner of a CI with an asset read-only with the hint and never sends it', async () => {
    const ASSET = 'a55e7000-0000-4000-8000-000000000002';
    const { patched } = await openEditor({
      ...ownedCI,
      asset_id: ASSET,
      asset: {
        id: ASSET,
        asset_tag: 'INV-0043',
        name: 'hardware',
        status: 'assigned',
        owner_user_id: USER_A,
        owner_team_id: TEAM_A,
      },
    });

    const user = screen.getByLabelText('Verantwortlicher Nutzer');
    expect(user).toBeDisabled();
    await waitFor(() => expect(user).toHaveValue('Erika Muster'));
    expect(user).toHaveAccessibleDescription('vom Asset übernommen');
    const team = screen.getByLabelText('Verantwortliches Team');
    expect(team).toBeDisabled();
    await waitFor(() => expect(team).toHaveValue('Netzbetrieb'));
    expect(screen.queryByRole('combobox', { name: 'Verantwortlicher Nutzer' })).toBeNull();

    fireEvent.change(screen.getByLabelText('Hersteller'), { target: { value: 'Vendor' } });
    fireEvent.click(submitButton());
    await waitFor(() => expect(patched).toHaveLength(1));
    expect(patched[0]).toEqual({ manufacturer: 'Vendor' });
  });

  it('shows a 422 of the owner at its field', async () => {
    await openEditor(ownedCI);
    const fetchMock = vi.mocked(fetch);
    const original = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input, init) =>
      (init?.method ?? 'GET').toUpperCase() === 'PATCH'
        ? violations({ field: 'owner_user_id', detail: 'unknown user' })
        : original(input, init),
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Erika Muster entfernen' }));
    await pickUser('max', /Max Beispiel/);
    fireEvent.click(submitButton());

    const group = screen.getByRole('group', { name: 'Verantwortlich' });
    expect(await within(group).findByText('unknown user')).toBeInTheDocument();
    expect(screen.getByText('Bitte korrigieren Sie die markierten Felder.')).toBeInTheDocument();
  });
});
