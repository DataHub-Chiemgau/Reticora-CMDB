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
 * - Server-side validation: the backend validates CI attributes against the
 *   CI type's field metadata and answers with per-field `violations`; before
 *   the regression test the client discarded that array, so the user saw one
 *   opaque sentence and had no idea which field to fix.
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
  /** Further types GET /ci-types/{id} knows (deactivated ones). */
  hidden?: TypeFixture[];
  /** Answer of POST /cis; the default stores the body and answers 201. */
  post?: (body: Record<string, unknown>) => Response;
}

/** Answers by exact API path; records the requests and the POST/PATCH bodies. */
function stubBackend({ types = [serverType], hidden = [], post }: BackendOptions = {}) {
  const posted: Array<Record<string, unknown>> = [];
  const patched: Array<Record<string, unknown>> = [];
  const requests: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input.toString();
    const url = new URL(raw, 'http://localhost');
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const method = (init?.method ?? 'GET').toUpperCase();
    requests.push(`${method} ${path}${url.search}`);
    if (method === 'GET' && path === '/ci-types') {
      return json({ data: types, total: types.length, limit: 200, offset: 0, has_more: false });
    }
    const known = [...types, ...hidden].find((ty) => path === `/ci-types/${ty.id}`);
    if (method === 'GET' && known) {
      return json(known);
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

async function chooseType(id: string) {
  await waitFor(() => {
    expect(Array.from(typeSelect().options).map((o) => o.value)).toContain(id);
  });
  fireEvent.change(typeSelect(), { target: { value: id } });
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

describe('CIFormModal CI types come from the metamodel API', () => {
  it('offers the org type created at runtime (MET-04)', async () => {
    stubBackend({
      types: [
        ciType(SYSTEM_TYPE_ID, 'server', 'Server', true),
        ciType(ORG_TYPE_ID, 'audit-custom-pdu', 'Audit Custom PDU', false),
      ],
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await waitFor(
      () => {
        const labels = Array.from(typeSelect().options).map((o) => o.textContent ?? '');
        expect(labels.some((l) => l.includes('Audit Custom PDU'))).toBe(true);
      },
      { timeout: 2000 },
    );
  });

  it('uses the API type ids as option values and posts the selected id (CI-03)', async () => {
    const { posted } = stubBackend({
      types: [
        ciType(SYSTEM_TYPE_ID, 'server', 'Server', true),
        ciType(ORG_TYPE_ID, 'audit-custom-pdu', 'Audit Custom PDU', false),
      ],
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await waitFor(
      () => {
        const values = Array.from(typeSelect().options)
          .map((o) => o.value)
          .filter((v) => v !== '');
        expect(values.sort()).toEqual([SYSTEM_TYPE_ID, ORG_TYPE_ID].sort());
      },
      { timeout: 2000 },
    );

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'srv-audit-01' } });
    fireEvent.change(typeSelect(), { target: { value: ORG_TYPE_ID } });
    fireEvent.click(screen.getByRole('button', { name: /anlegen|create/i }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]?.ci_type_id).toBe(ORG_TYPE_ID);
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
    await chooseType(SYSTEM_TYPE_ID);
    expect(requests.filter((r) => r.startsWith('GET /ci-types?'))).toEqual([
      'GET /ci-types?limit=200',
    ]);

    // An organization type is created elsewhere while the SPA stays loaded.
    types.push(ciType(ORG_TYPE_ID, 'audit-runtime-type', 'Laufzeittyp', false));
    view.rerender(<CIFormModal open={false} onOpenChange={() => {}} />);
    view.rerender(<CIFormModal open onOpenChange={() => {}} />);
    await waitFor(() => {
      const labels = Array.from(typeSelect().options).map((o) => o.textContent ?? '');
      expect(labels).toContain('Laufzeittyp');
    });
  });

  it('labels the types in the UI language and falls back to the key', async () => {
    stubBackend({
      types: [serverType, { ...ciType(ORG_TYPE_ID, 'org_only_key', 'x', false), display_name: {} }],
    });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await waitFor(() => {
      const options = Array.from(typeSelect().options).map((o) => [o.value, o.textContent]);
      expect(options).toEqual([
        ['', 'Bitte auswählen'],
        [ORG_TYPE_ID, 'org_only_key'],
        [SERVER_TYPE_ID, 'Server (physisch)'],
      ]);
    });
  });

  it('does not post without a type choice and reports the required type at the field', async () => {
    const { posted } = stubBackend();
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);
    await waitFor(() => expect(typeSelect().options.length).toBe(2));

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'srv-no-type' } });
    // The browser refuses the submit of the required select ...
    fireEvent.click(submitButton());
    expect(typeSelect().validity.valueMissing).toBe(true);
    // ... and the dialog's own check names the field when the submit happens.
    fireEvent.submit(typeSelect().form!);
    expect(await screen.findByText('CI-Typ ist erforderlich.')).toBeInTheDocument();
    expect(typeSelect()).toHaveAttribute('aria-invalid', 'true');
    expect(typeSelect()).toHaveAccessibleDescription('CI-Typ ist erforderlich.');
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
  });
});

describe('CI type labels and ids from the metamodel (WP-201 / WP-194)', () => {
  it('posts the CI type UUID from GET /ci-types when the user picks "Server" (WP-201 P1)', async () => {
    const { posted } = stubBackend();
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    // The user picks the server type, whatever the option is labelled today.
    let serverOption: HTMLOptionElement | undefined;
    await waitFor(
      () => {
        serverOption = Array.from(typeSelect().options).find((o) =>
          /server/i.test(o.textContent ?? ''),
        );
        expect(serverOption, 'a server option in the CI type select').toBeDefined();
      },
      { timeout: 2000 },
    );

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'srv-label-01' } });
    fireEvent.change(typeSelect(), { target: { value: serverOption?.value } });
    fireEvent.click(screen.getByRole('button', { name: /anlegen|create|speichern|save/i }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(
      posted[0]?.ci_type_id,
      'POST /cis ci_type_id must be the type id from GET /ci-types (the API rejects a type key with 400)',
    ).toBe(SERVER_TYPE_ID);
  });

  it('labels the type options with display_name in the UI language (WP-194 P2)', async () => {
    stubBackend();
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    const labels = () => Array.from(typeSelect().options).map((o) => (o.textContent ?? '').trim());
    await waitFor(() => expect(labels()).toContain('Server (physisch)'), { timeout: 2000 });

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    await waitFor(() => expect(labels()).toContain('Server (physical)'));
  });
});

describe('CIFormModal type attributes', () => {
  it('renders the attributes of the chosen type and posts them in attributes', async () => {
    const { posted, requests } = stubBackend({ types: [serverType, rackType] });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'rack-unit-02' } });
    expect(screen.queryByLabelText(/Höheneinheiten/)).not.toBeInTheDocument();
    await chooseType(RACK_TYPE_ID);

    const units = await screen.findByLabelText('Höheneinheiten *');
    expect(requests).toContain(`GET /ci-types/${RACK_TYPE_ID}`);
    expect(screen.getByRole('heading', { name: 'Typattribute' })).toBeInTheDocument();
    fireEvent.change(units, { target: { value: '4' } });
    fireEvent.change(screen.getByLabelText('Luftstrom'), { target: { value: 'back_to_front' } });
    fireEvent.click(screen.getByLabelText('Redundantes Netzteil'));
    fireEvent.click(submitButton());

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

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'srv-switched' } });
    await chooseType(RACK_TYPE_ID);
    fireEvent.change(await screen.findByLabelText('Höheneinheiten *'), { target: { value: '4' } });
    await chooseType(SERVER_TYPE_ID);
    await waitFor(() =>
      expect(screen.queryByLabelText('Höheneinheiten *')).not.toBeInTheDocument(),
    );
    fireEvent.click(submitButton());

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]?.ci_type_id).toBe(SERVER_TYPE_ID);
    expect(posted[0]).not.toHaveProperty('attributes');
  });

  it('checks the type attributes before posting and names the invalid field', async () => {
    const { posted } = stubBackend({ types: [rackType] });
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'rack-unit-03' } });
    await chooseType(RACK_TYPE_ID);
    const units = await screen.findByLabelText('Höheneinheiten *');
    // An empty required attribute: the browser refuses the submit ...
    fireEvent.click(submitButton());
    expect((units as HTMLInputElement).validity.valueMissing).toBe(true);
    // ... and the dialog's check marks the field.
    fireEvent.submit(typeSelect().form!);
    expect(await screen.findByText('Höheneinheiten is required')).toBeInTheDocument();
    expect(units).toHaveAttribute('aria-invalid', 'true');
    // A fraction is no integer.
    fireEvent.change(units, { target: { value: '1.5' } });
    fireEvent.submit(typeSelect().form!);
    expect(await screen.findByText('Höheneinheiten must be an integer')).toBeInTheDocument();
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
  /** Fills the two client-required fields and submits the modal. */
  async function fillAndSubmit() {
    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'srv-01' } });
    await chooseType(SERVER_TYPE_ID);
    fireEvent.click(screen.getByRole('button', { name: /anlegen|create/i }));
  }

  it('renders a 422 violation on the matching input and unmapped ones in the banner', async () => {
    stubBackend({
      post: () =>
        violations(
          { field: 'serial_number', detail: 'must match the required format' },
          { field: 'ram_gb', detail: 'field is required' },
        ),
    });

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await fillAndSubmit();

    // The violation whose field matches a form input is shown inline.
    await waitFor(() => {
      expect(screen.getByText('must match the required format')).toBeInTheDocument();
    });

    // `ram_gb` is an attribute the chosen type does not describe, so it has no
    // input in this modal and must still be reported rather than silently dropped.
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

  it('shows a 422 violation of ci_type_id at the type field', async () => {
    stubBackend({ post: () => violations({ field: 'ci_type_id', detail: 'CI type not found' }) });

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await fillAndSubmit();

    await waitFor(() => expect(typeSelect()).toHaveAttribute('aria-invalid', 'true'));
    expect(typeSelect()).toHaveAccessibleDescription('CI type not found');
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Bitte korrigieren Sie die markierten Felder.',
    );
  });

  it('shows a 422 violation of a type attribute at its field', async () => {
    stubBackend({
      types: [rackType],
      post: () => violations({ field: 'rack_units', detail: 'must be at most 60' }),
    });

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'rack-unit-04' } });
    await chooseType(RACK_TYPE_ID);
    const units = await screen.findByLabelText('Höheneinheiten *');
    fireEvent.change(units, { target: { value: '61' } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(units).toHaveAttribute('aria-invalid', 'true'));
    expect(units).toHaveAccessibleDescription('must be at most 60');
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Bitte korrigieren Sie die markierten Felder.');
    expect(within(alert).queryByText(/rack_units/)).not.toBeInTheDocument();
  });

  it('reports types that cannot be loaded at the type field', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json({ detail: 'unavailable' }, 503)),
    );
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    expect(
      await screen.findByText('Die CI-Typen konnten nicht geladen werden.'),
    ).toBeInTheDocument();
    expect(typeSelect()).toHaveAttribute('aria-invalid', 'true');
  });
});
