/**
 * Phase-1 acceptance audit, group T7-ui – DEFECT test (fails until fixed).
 *
 * WP-194 (GLO-10, UI-18) P2 "CI form and CI detail ignore metamodel label
 * maps (hard-coded types, UUID shown)": CIDetailPage.tsx:114/136 shows the
 * raw ci_type_id (a UUID) instead of the type label (F-175, WP-523).
 *
 * Requirement encoded: the detail page shows the localized type label
 * (display_name.de / .en) and never the UUID.
 *
 * The CI form cases of this file (WP-201 P1, the type options come from
 * GET /ci-types and the POST body carries the chosen id; WP-194 P2, the
 * options are labelled with display_name in the UI language) are green since
 * WP-397 and moved to CIFormModal.test.tsx.
 *
 * Retest:
 *   cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/CITypeLabels.auditdefect.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, screen, waitFor } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
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

/** Answers by exact API path. */
function stubBackend() {
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
}

afterEach(async () => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('CI type labels and ids from the metamodel (audit defect WP-201 / WP-194)', () => {
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
