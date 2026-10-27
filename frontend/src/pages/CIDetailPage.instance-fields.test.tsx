/**
 * CIDetailPage instance attributes: the inline edit of the instance fields is
 * a CI change like the edit dialog and carries If-Match (API-07, API-03).
 *
 * - F-082 (WP-477): InstanceFieldsSection saved through updateCI.mutate
 *   without a version, so the client sent no If-Match; with the organization
 *   setting require_if_match every save failed with 412, without it the last
 *   write won. Now every save carries If-Match with the shown CI version.
 * - The fields save on every valid change; a save that waits behind another
 *   uses the version that save returned, so the user's own previous save is
 *   no conflict.
 * - 409 shows the conflict hint with the fields the API names, 412 the
 *   request to reload (de-DE and en-US).
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/CIDetailPage.instance-fields.test.tsx
 */
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Route, Routes } from 'react-router-dom';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
import { CIDetailPage } from './CIDetailPage';

const CI_ID = 'ci-1';

function json(body: unknown, status = 200, contentType = 'application/json') {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': contentType } });
}

interface Patch {
  ifMatch: string | null;
  body: Record<string, unknown>;
}

/**
 * stubBackend answers the detail page's requests; GET /cis/ci-1 returns the
 * current version, PATCH /cis/ci-1 is answered by `patch` (default: stored,
 * version + 1).
 */
function stubBackend(patch?: (p: Patch, current: number) => Response | Promise<Response>) {
  let version = 7;
  let attributes: Record<string, unknown> = { ilo_ip: '10.0.0.1' };
  const patches: Patch[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://localhost');
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const method = (init?.method ?? 'GET').toUpperCase();
      const ci = () => ({
        id: CI_ID,
        organization_id: 'org-1',
        ci_type_id: 'server',
        name: 'srv-instance-01',
        status: 'active',
        version,
        attributes,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-02T00:00:00Z',
      });
      if (method === 'GET' && path === `/cis/${CI_ID}`) return json(ci());
      if (method === 'GET' && path === `/cis/${CI_ID}/field-definitions`) {
        return json({ data: [{ name: 'ilo_ip', label: 'iLO-IP', data_type: 'text' }] });
      }
      if (method === 'PATCH' && path === `/cis/${CI_ID}`) {
        const p: Patch = {
          ifMatch: new Headers(init?.headers).get('If-Match'),
          body: JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>,
        };
        patches.push(p);
        if (patch) return patch(p, version);
        version += 1;
        attributes = { ...attributes, ...(p.body.attributes as Record<string, unknown>) };
        return json(ci());
      }
      if (method === 'GET' && path.endsWith('/relationships')) {
        return json({ data: [], total: 0, limit: 25, offset: 0, has_more: false });
      }
      if (method === 'GET' && path.startsWith('/topology/')) return json({ nodes: [], edges: [] });
      return json({ detail: `unexpected request: ${method} ${path}` }, 404);
    }),
  );
  return { patches };
}

function renderDetail() {
  return renderWithProviders(
    <Routes>
      <Route path="/cmdb/:id" element={<CIDetailPage />} />
    </Routes>,
    { route: `/cmdb/${CI_ID}` },
  );
}

async function iloInput() {
  await screen.findByRole('heading', { name: 'srv-instance-01' });
  const input = await screen.findByLabelText('iLO-IP');
  await waitFor(() => expect(input).toHaveValue('10.0.0.1'));
  return input;
}

afterEach(async () => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('CIDetailPage instance attributes send If-Match (F-082)', () => {
  it('saves an instance attribute with If-Match on the shown version 7', async () => {
    const { patches } = stubBackend();
    renderDetail();

    fireEvent.change(await iloInput(), { target: { value: '10.0.0.5' } });

    await waitFor(() => expect(patches).toHaveLength(1));
    expect(patches[0]?.ifMatch).toBe('"7"');
    expect(patches[0]?.body).toEqual({ attributes: { ilo_ip: '10.0.0.5' } });
  });

  it('runs saves one after another; the waiting save uses the version the first returned', async () => {
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let current = 7;
    const { patches } = stubBackend(async (p) => {
      if (patches.length === 1) await gate;
      current += 1;
      return json({
        id: CI_ID,
        organization_id: 'org-1',
        ci_type_id: 'server',
        name: 'srv-instance-01',
        status: 'active',
        version: current,
        attributes: p.body.attributes,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-02T00:00:00Z',
      });
    });
    renderDetail();

    const input = await iloInput();
    fireEvent.change(input, { target: { value: '10.0.0.5' } });
    await waitFor(() => expect(patches).toHaveLength(1));
    // Typed while the first save is in flight: waits, only the latest is sent.
    fireEvent.change(input, { target: { value: '10.0.0.50' } });
    fireEvent.change(input, { target: { value: '10.0.0.51' } });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    expect(patches, 'no second save while the first is in flight').toHaveLength(1);

    release?.();
    await waitFor(() => expect(patches).toHaveLength(2));
    expect(patches.map((p) => p.ifMatch)).toEqual(['"7"', '"8"']);
    expect(patches[1]?.body).toEqual({ attributes: { ilo_ip: '10.0.0.51' } });
  });

  it('shows the conflict hint with the fields on 409', async () => {
    stubBackend((_p, version) =>
      json(
        {
          type: 'urn:reticora:problem:conflict',
          title: 'Conflict',
          status: 409,
          detail: 'conflict: fields changed since the given version: attributes',
          fields: ['attributes'],
          current_version: version + 1,
        },
        409,
        'application/problem+json',
      ),
    );
    renderDetail();

    fireEvent.change(await iloInput(), { target: { value: '10.0.0.5' } });

    expect(
      await screen.findByText(
        'Speichern abgelehnt: attributes wurde inzwischen von jemand anderem geändert. Bitte die Seite neu laden und die Änderung wiederholen.',
      ),
    ).toBeInTheDocument();

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(
      await screen.findByText(
        'Save refused: attributes was changed by someone else in the meantime. Please reload the page and repeat the change.',
      ),
    ).toBeInTheDocument();
  });

  it('asks to reload on 412', async () => {
    stubBackend(() =>
      json(
        {
          type: 'urn:reticora:problem:precondition-failed',
          title: 'Precondition Failed',
          status: 412,
          detail: 'If-Match does not name a version of this CI',
        },
        412,
        'application/problem+json',
      ),
    );
    renderDetail();

    fireEvent.change(await iloInput(), { target: { value: '10.0.0.5' } });

    expect(
      await screen.findByText(
        'Speichern abgelehnt: Die Version dieses CIs ist veraltet oder unbekannt. Bitte die Seite neu laden.',
      ),
    ).toBeInTheDocument();

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(
      await screen.findByText(
        'Save refused: the version of this CI is outdated or unknown. Please reload the page.',
      ),
    ).toBeInTheDocument();
  });
});
