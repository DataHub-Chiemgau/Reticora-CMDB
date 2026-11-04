/**
 * WP-800 (UI-11 [P2], ABN-01 (c)): the templates at the CI.
 *
 *   Gate      without citype:manage the section is missing and GET
 *             /cis/{id}/templates is not called (UI-12)
 *   Assign    two templates assigned and reordered by keyboard and buttons;
 *             PUT /cis/{id}/templates sends the ordered list
 *             [{template_id, version}] with If-Match of the CI version
 *   Choice    only published versions of active templates of the type chain
 *   Refusal   412 reloads the list; 422 shows the violations per field
 *   Migrate   "migrate to version n" opens the MigrateDialog for this CI
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/metamodel/CITemplatesSection.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { useAuthStore } from '../../auth/authStore';
import { CITemplatesSection } from './CITemplatesSection';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  path: string;
  body: Json | undefined;
  headers: Headers;
}

const AT = '2026-01-01T00:00:00Z';
const CI_ID = 'c0000000-0000-4000-8000-000000000001';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, body: Json) {
  return new Response(JSON.stringify({ status, title: 'Problem', ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function ciType(id: string, name: string, extra: Json = {}): Json {
  return {
    id,
    key: name.toLowerCase(),
    name,
    display_name: { de: name, en: name },
    is_builtin: false,
    is_system: false,
    is_active: true,
    is_logical: false,
    is_observable: false,
    schema_version: 1,
    version: 1,
    capabilities: {},
    allowed_relationship_types: [],
    ui_schema: {},
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const TYPES = [
  ciType('t-net', 'Netzwerk'),
  ciType('t-router', 'Router', { parent_type_id: 't-net' }),
  ciType('t-printer', 'Drucker'),
];

function template(id: string, name: string, ciTypeId: string, extra: Json = {}): Json {
  return {
    id,
    key: id,
    ci_type_id: ciTypeId,
    display_name: { de: name, en: name },
    is_system: false,
    is_active: true,
    current_version: 1,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const ROUTER = template('tp-router', 'Routerbasis', 't-router', { current_version: 2 });
const NET = template('tp-net', 'Netzbasis', 't-net');
const PRINTER = template('tp-print', 'Druckerbasis', 't-printer');
const OFF = template('tp-off', 'Altvorlage', 't-router', { is_active: false });

function version(templateId: string, n: number, status: string): Json {
  return {
    id: `${templateId}-v${n}`,
    template_id: templateId,
    version: n,
    status,
    created_at: AT,
    updated_at: AT,
    items: [],
  };
}

const VERSIONS: Record<string, Json[]> = {
  'tp-router': [version('tp-router', 2, 'published'), version('tp-router', 1, 'retired')],
  'tp-net': [version('tp-net', 2, 'draft'), version('tp-net', 1, 'published')],
};

interface Backend {
  permissions?: string[];
  put?: (body: Json) => Response;
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  let assigned: Json[] = [{ template_id: 'tp-router', key: 'tp-router', version: 1, position: 0 }];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ method, path, body, headers: new Headers(init?.headers) });
      if (path === '/me/permissions') {
        return json({
          user_id: 'u1',
          permissions: b.permissions ?? ['ci:read', 'ci:write', 'citype:manage'],
        });
      }
      if (path === '/modules') return json({ data: [] });
      if (path === '/ci-types') {
        return json({ data: TYPES, total: TYPES.length, limit: 200, offset: 0, has_more: false });
      }
      if (path === '/templates') {
        return json({ data: [ROUTER, NET, PRINTER, OFF], has_more: false });
      }
      const versions = /^\/templates\/([^/]+)\/versions$/.exec(path);
      if (versions) return json({ data: VERSIONS[versions[1]!] ?? [], has_more: false });
      if (path === `/cis/${CI_ID}/templates` && method === 'GET') {
        return json({ data: assigned });
      }
      if (path === `/cis/${CI_ID}/templates` && method === 'PUT') {
        if (b.put) return b.put(body!);
        const list = body!.templates as Json[];
        assigned = list.map((entry, position) => ({
          ...entry,
          key: entry.template_id,
          position,
        }));
        return json({ id: CI_ID, name: 'r1', ci_type_id: 't-router', version: 8 });
      }
      if (path === '/saved-views') {
        return json({ data: [], total: 0, limit: 100, offset: 0, has_more: false });
      }
      return json({ data: [] });
    }),
  );
  return calls;
}

function renderSection() {
  return renderWithProviders(<CITemplatesSection ciId={CI_ID} ciTypeId="t-router" version={7} />);
}

/** Lays the entries of a sortable list out one below the other (jsdom has no layout). */
function layoutRows() {
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    const index = this.getAttribute('data-sortable-index');
    const top = index === null ? 0 : Number(index) * 40;
    const height = index === null ? 0 : 40;
    const width = index === null ? 0 : 400;
    return {
      x: 0,
      y: top,
      top,
      left: 0,
      width,
      height,
      right: width,
      bottom: top + height,
      toJSON: () => ({}),
    } as DOMRect;
  });
}

/** The KeyboardSensor listens for the next key after a tick. */
async function key(target: Element, code: string) {
  await act(async () => {
    fireEvent.keyDown(target, { key: code === 'Space' ? ' ' : code, code });
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

const list = () => screen.getByRole('list', { name: 'Templates des CI' });
const order = () =>
  Array.from(list().querySelectorAll('[data-template-key]')).map((el) =>
    el.getAttribute('data-template-key'),
  );
const puts = (calls: Call[]) =>
  calls.filter((c) => c.method === 'PUT' && c.path === `/cis/${CI_ID}/templates`);
const reads = (calls: Call[]) =>
  calls.filter((c) => c.method === 'GET' && c.path === `/cis/${CI_ID}/templates`);

async function addNet() {
  const select = await screen.findByLabelText('Template hinzufügen');
  await waitFor(() =>
    expect(within(select).getByRole('option', { name: 'Netzbasis' })).toBeInTheDocument(),
  );
  fireEvent.change(select, { target: { value: 'tp-net' } });
  await waitFor(() => expect(screen.getByLabelText('Version')).toHaveValue('1'));
  fireEvent.click(screen.getByRole('button', { name: 'Hinzufügen' }));
}

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    user: { sub: 'u1' },
  });
});

afterEach(() => {
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CITemplatesSection', () => {
  it('is missing without citype:manage and then does not read the templates of the CI', async () => {
    const calls = stubBackend({ permissions: ['ci:read', 'ci:write'] });
    const { container } = renderSection();
    await waitFor(() => expect(calls.some((c) => c.path === '/me/permissions')).toBe(true));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByText(/Template/)).not.toBeInTheDocument();
    expect(reads(calls)).toHaveLength(0);
  });

  it('offers only published versions of active templates of the type chain', async () => {
    stubBackend();
    renderSection();
    const select = await screen.findByLabelText('Template hinzufügen');
    await waitFor(() =>
      expect(within(select).getByRole('option', { name: 'Netzbasis' })).toBeInTheDocument(),
    );
    // Router is assigned already, Drucker is another type, Altvorlage inactive.
    expect(
      within(select)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Template wählen', 'Netzbasis']);
    fireEvent.change(select, { target: { value: 'tp-net' } });
    await waitFor(() => expect(screen.getByLabelText('Version')).toHaveValue('1'));
    expect(
      within(screen.getByLabelText('Version'))
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['v1']);
  });

  it('assigns two templates, reorders them by keyboard and buttons and saves the list with If-Match', async () => {
    layoutRows();
    const calls = stubBackend();
    renderSection();
    await screen.findByText('Routerbasis');
    expect(screen.getByText('Version 1 · Position 1')).toBeInTheDocument();
    await addNet();
    expect(order()).toEqual(['tp-router', 'tp-net']);

    // Keyboard: Space picks Netzbasis up, ArrowUp moves it, Space drops it.
    const handle = screen.getByRole('button', { name: 'Verschieben: Netzbasis' });
    handle.focus();
    await key(handle, 'Space');
    await key(handle, 'ArrowUp');
    await key(handle, 'Space');
    await waitFor(() => expect(order()).toEqual(['tp-net', 'tp-router']));

    // Buttons: down and up again.
    fireEvent.click(screen.getByRole('button', { name: 'Nach unten: Netzbasis' }));
    expect(order()).toEqual(['tp-router', 'tp-net']);
    fireEvent.click(screen.getByRole('button', { name: 'Nach oben: Netzbasis' }));
    expect(order()).toEqual(['tp-net', 'tp-router']);

    fireEvent.click(screen.getByRole('button', { name: 'Templates speichern' }));
    await waitFor(() => expect(puts(calls)).toHaveLength(1));
    const put = puts(calls)[0]!;
    expect(put.body).toEqual({
      templates: [
        { template_id: 'tp-net', version: 1 },
        { template_id: 'tp-router', version: 1 },
      ],
    });
    expect(put.headers.get('If-Match')).toBe('"7"');
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Templates speichern' })).not.toBeInTheDocument(),
    );
  });

  it('removes a template and saves the shorter list', async () => {
    const calls = stubBackend();
    renderSection();
    fireEvent.click(await screen.findByRole('button', { name: 'Template entfernen: Routerbasis' }));
    fireEvent.click(screen.getByRole('button', { name: 'Templates speichern' }));
    await waitFor(() => expect(puts(calls)).toHaveLength(1));
    expect(puts(calls)[0]!.body).toEqual({ templates: [] });
  });

  it('reloads the list after a 412', async () => {
    const calls = stubBackend({
      put: () => problem(412, { detail: 'not the current version' }),
    });
    renderSection();
    await addNet();
    const before = reads(calls).length;
    fireEvent.click(screen.getByRole('button', { name: 'Templates speichern' }));
    expect(
      await screen.findByText(
        'Das CI wurde inzwischen geändert; die Templates wurden neu geladen.',
      ),
    ).toBeInTheDocument();
    await waitFor(() => expect(reads(calls).length).toBeGreaterThan(before));
    expect(order()).toEqual(['tp-router']);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('shows the violations of a 422 per field', async () => {
    stubBackend({
      put: () =>
        problem(422, {
          detail: 'template list refused',
          violations: [{ field: 'templates[1]', detail: 'template has no published version' }],
          trace_id: 'trace-1',
        }),
    });
    renderSection();
    await addNet();
    fireEvent.click(screen.getByRole('button', { name: 'Templates speichern' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('templates[1]: template has no published version');
    expect(alert).toHaveTextContent('Trace-ID: trace-1');
    const net = list().querySelector('[data-template-key="tp-net"]') as HTMLElement;
    expect(within(net).getByText('template has no published version')).toBeInTheDocument();
    const router = list().querySelector('[data-template-key="tp-router"]') as HTMLElement;
    expect(within(router).queryByText('template has no published version')).not.toBeInTheDocument();
  });

  it('opens the migration of this CI to the current version', async () => {
    stubBackend();
    renderSection();
    fireEvent.click(await screen.findByRole('button', { name: 'Auf Version 2 migrieren' }));
    const dialog = await screen.findByRole('dialog', { name: 'Template migrieren: Routerbasis' });
    expect(
      within(dialog).getByText('Migriert werden nur die gewählten CIs (1).'),
    ).toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByLabelText('Zielversion')).toHaveValue('2'));
    expect(within(dialog).getByLabelText('Ausgangsversion')).toHaveValue('1');
  });
});
