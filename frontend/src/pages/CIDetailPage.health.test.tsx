/**
 * CIDetailPage health badge: ci.health is computed from monitoring by the
 * worker health (DB-05, MON-02), also in the G1 default configuration
 * without the module alerting (F-087, WP-480). The detail page shows it as a
 * badge with its translation and a variant per state: ok (success), the
 * degraded states warning and critical (warning, danger) and unknown
 * (neutral), in de-DE and en-US.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/CIDetailPage.health.test.tsx
 */
import { act, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Route, Routes } from 'react-router-dom';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
import { CIDetailPage } from './CIDetailPage';

const CI_ID = 'ci-health-1';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** stubBackend answers the detail page's requests with a CI of the given health. */
function stubBackend(health: string) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://localhost');
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const method = (init?.method ?? 'GET').toUpperCase();
      if (method === 'GET' && path === `/cis/${CI_ID}`) {
        return json({
          id: CI_ID,
          organization_id: 'org-1',
          ci_type_id: 'server',
          name: 'srv-health-01',
          status: 'active',
          health,
          health_updated_at: '2026-01-02T00:00:00Z',
          version: 1,
          attributes: {},
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-02T00:00:00Z',
        });
      }
      if (method === 'GET' && path === `/cis/${CI_ID}/field-definitions`) return json({ data: [] });
      if (method === 'GET' && path.endsWith('/relationships')) {
        return json({ data: [], total: 0, limit: 25, offset: 0, has_more: false });
      }
      if (method === 'GET' && path.startsWith('/topology/')) return json({ nodes: [], edges: [] });
      return json({ detail: `unexpected request: ${method} ${path}` }, 404);
    }),
  );
}

function renderDetail() {
  return renderWithProviders(
    <Routes>
      <Route path="/cmdb/:id" element={<CIDetailPage />} />
    </Routes>,
    { route: `/cmdb/${CI_ID}` },
  );
}

afterEach(async () => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('CIDetailPage health badge (DB-05, F-087)', () => {
  // color is the class of the Badge variant (success, warning, danger,
  // neutral).
  const cases = [
    { health: 'ok', de: 'Zustand: OK', en: 'Health: OK', color: 'bg-green-100' },
    { health: 'warning', de: 'Zustand: Warnung', en: 'Health: warning', color: 'bg-yellow-100' },
    { health: 'critical', de: 'Zustand: kritisch', en: 'Health: critical', color: 'bg-red-100' },
    { health: 'unknown', de: 'Zustand: unbekannt', en: 'Health: unknown', color: 'bg-gray-100' },
  ];

  it.each(cases)('shows $health translated in de-DE', async ({ health, de, color }) => {
    stubBackend(health);
    renderDetail();
    await screen.findByRole('heading', { name: 'srv-health-01' });
    const badge = await screen.findByText(de);
    expect(badge).toHaveClass(color);
    expect(screen.queryByText(`ci.healthState.${health}`)).not.toBeInTheDocument();
  });

  it.each(cases)('shows $health translated in en-US', async ({ health, en }) => {
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    stubBackend(health);
    renderDetail();
    await screen.findByRole('heading', { name: 'srv-health-01' });
    expect(await screen.findByText(en)).toBeInTheDocument();
  });
});
