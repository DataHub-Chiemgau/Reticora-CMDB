/**
 * Phase-1 acceptance audit, group T7-ui – DEFECT tests (fail until fixed).
 *
 * WP-201 (TST-03) P1 "Mocked ci.spec.ts passes although the real CMDB create
 * form fails (ci_type_id sent as type key → 400)": CIFormModal.tsx:30 and
 * 66-74 use the hard-coded list ciTypeValues ('server', 'switch', …) as option
 * values; POST /api/v1/cis expects ci_type_id as the CI type UUID from
 * GET /ci-types, so the real backend answers 400 and nothing is stored.
 * WP-194 (GLO-10, UI-18) P2 "CI form and CI detail ignore metamodel label
 * maps (hard-coded types, UUID shown)": types are not rendered through
 * localizedText(display_name); CIDetailPage.tsx:114/136 shows the raw
 * ci_type_id (a UUID).
 *
 * Requirement encoded: the type options come from GET /ci-types with value =
 * type id and the label in the UI language (display_name.de / .en); the POST
 * body carries the chosen id; the detail page shows the localized type label
 * and never the UUID.
 *
 * Retest:
 *   cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/CITypeLabels.auditdefect.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
import { CIFormModal } from './CIFormModal';
import { CIDetailPage } from './CIDetailPage';

const SERVER_TYPE_ID = '7a0c1e2d-0000-4000-8000-0000000000a1';
const CI_ID = '7a0c1e2d-0000-4000-8000-0000000000c1';

const serverType = {
  id: SERVER_TYPE_ID,
  key: 'server',
  name: 'server',
  display_name: { de: 'Server (physisch)', en: 'Server (physical)' },
  is_builtin: true,
  is_system: true,
  is_active: true,
  is_logical: false,
  is_observable: true,
  schema_version: 1,
  version: 1,
};

const ci = {
  id: CI_ID,
  organization_id: 'org-1',
  ci_type_id: SERVER_TYPE_ID,
  name: 'srv-label-01',
  status: 'active',
  attributes: {},
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-02T00:00:00Z',
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const emptyList = { data: [], total: 0, limit: 50, offset: 0, has_more: false };

/** Answers by exact API path; records POST /cis bodies. */
function stubBackend() {
  const posted: Array<Record<string, unknown>> = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input.toString();
    const path = new URL(raw, 'http://localhost').pathname.replace(/^\/api\/v1/, '');
    const method = (init?.method ?? 'GET').toUpperCase();
    if (path === '/ci-types') {
      return json({ data: [serverType], total: 1, limit: 200, offset: 0, has_more: false });
    }
    if (path === `/ci-types/${SERVER_TYPE_ID}`) {
      return json(serverType);
    }
    if (path === '/cis' && method === 'POST') {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
      posted.push(body);
      return json({ ...ci, ...body }, 201);
    }
    if (path === `/cis/${CI_ID}`) {
      return json(ci);
    }
    if (path === `/topology/cis/${CI_ID}/neighbors`) {
      return json({ nodes: [], edges: [] });
    }
    if (method === 'GET' && path.startsWith(`/cis/${CI_ID}/`)) {
      return json(emptyList);
    }
    return json({ detail: `unexpected request: ${method} ${path}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return posted;
}

function typeSelect(): HTMLSelectElement {
  return screen.getByLabelText(/typ|type/i) as HTMLSelectElement;
}

afterEach(async () => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('CI type labels and ids from the metamodel (audit defect WP-201 / WP-194)', () => {
  it('posts the CI type UUID from GET /ci-types when the user picks "Server" (WP-201 P1)', async () => {
    const posted = stubBackend();
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

  it('shows the localized type label on the CI detail page, not the type UUID', async () => {
    stubBackend();
    renderWithProviders(
      <Routes>
        <Route path="/cmdb/:id" element={<CIDetailPage />} />
      </Routes>,
      { route: `/cmdb/${CI_ID}` },
    );

    expect(await screen.findByRole('heading', { name: 'srv-label-01' })).toBeInTheDocument();
    await waitFor(
      () => {
        expect(
          screen.queryAllByText('Server (physisch)').length,
          'detail shows the type label display_name.de',
        ).toBeGreaterThan(0);
      },
      { timeout: 2000 },
    );
    expect(document.body.textContent ?? '').not.toContain(SERVER_TYPE_ID);
  });
});
