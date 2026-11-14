/**
 * WP-1157 (UI-14, TLC-06): the page Organisationsdaten, the banner of the
 * grace period and the 423 message. Starting an export lists it and its
 * objects with expiry; a 403 mfa-required shows the MFA hint; the delete
 * button is enabled only for the matching slug; the deletion is requested
 * and withdrawn; the banner shows the due date and, for org_admin, the
 * withdrawal; a 423 org-pending-deletion says "nur Lesen bis <Datum>".
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/OrgDataPage.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { useMutation } from '@tanstack/react-query';
import { renderWithProviders } from '../test/utils';
import i18n from '../i18n';
import { useAuthStore } from '../auth/authStore';
import { ApiError, fetchAPI } from '../api/client';
import { OrgDeletionBanner } from '../components/OrgDeletionBanner';
import { OrgDataPage } from './OrgDataPage';

type Json = Record<string, unknown>;

const AT = '2026-11-14T08:00:00Z';
const DUE = '2026-12-14T08:00:00Z';
const ORG = 'a0000000-0000-4000-8000-0000000000aa';
const EXPORT = 'e0000000-0000-4000-8000-000000000001';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 50,
  offset: 0,
  has_more: false,
});

const deletion = (state: string) =>
  state === 'none'
    ? { state, requested_at: null, due_at: null, requested_by: null, remaining_seconds: null }
    : {
        state,
        requested_at: AT,
        due_at: DUE,
        requested_by: 'u',
        remaining_seconds: 30 * 86_400 - 3_600,
      };

interface Options {
  permissions?: string[];
  state?: string;
  mfa?: boolean;
  modules?: Json[];
}

function stubApi({
  permissions = ['org:export', 'org:delete', 'location:read'],
  state = 'none',
  mfa = true,
  modules = [{ key: 'org_lifecycle', enabled: true, phase: 'P3', nav: ['org-data'] }],
}: Options = {}) {
  let current = state;
  const exports: Json[] = [];
  const calls: { method: string; path: string; body?: Json }[] = [];
  const mfaProblem = () =>
    json(
      {
        type: 'urn:reticora:problem:mfa-required',
        title: 'Forbidden',
        status: 403,
        detail: 'the deletion of the organization needs a session opened with a second factor',
      },
      403,
    );
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = init?.method ?? 'GET';
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      calls.push({ method, path, body });
      if (path === '/me/permissions') return json({ user_id: 'u', permissions });
      if (path === '/modules') return json({ data: modules });
      if (path === '/org') {
        return json({
          id: ORG,
          name: 'Acme',
          slug: 'acme-gmbh',
          plan: 'pro',
          settings: {},
          created_at: AT,
          updated_at: AT,
        });
      }
      if (path === '/org/exports' && method === 'POST') {
        if (!mfa) return mfaProblem();
        exports.unshift({
          id: EXPORT,
          job_id: EXPORT,
          status: 'completed',
          progress: 100,
          created_at: AT,
          completed_at: AT,
          expires_at: '2026-11-15T08:00:00Z',
        });
        return json(exports[0], 202);
      }
      if (path === '/org/exports') return json(page(exports));
      if (path === `/org/exports/${EXPORT}/download`) {
        return json({
          id: EXPORT,
          expires_at: '2026-11-15T08:00:00Z',
          objects: [
            {
              name: 'manifest.json',
              bytes: 512,
              sha256: 'a'.repeat(64),
              url: 'https://s3.example/m',
              url_expires_at: '2026-11-14T08:15:00Z',
            },
            {
              name: 'tables/ci/part-0001.jsonl',
              bytes: 2048,
              sha256: 'b'.repeat(64),
              url: 'https://s3.example/ci',
              url_expires_at: '2026-11-14T08:15:00Z',
            },
          ],
        });
      }
      if (path === '/org/deletion') {
        if (method === 'POST') {
          if (!mfa) return mfaProblem();
          current = 'requested';
          return json(deletion(current), 202);
        }
        if (method === 'DELETE') {
          if (!mfa) return mfaProblem();
          current = 'none';
          return json(deletion(current));
        }
        return json(deletion(current));
      }
      return json(page([]));
    }),
  );
  return calls;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useAuthStore.setState({
    token: 't',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'u' },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('OrgDataPage', () => {
  it('starts an export and lists its objects with expiry', async () => {
    const calls = stubApi();
    renderWithProviders(<OrgDataPage />);

    expect(await screen.findByText('Noch kein Vollexport.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Export starten' }));
    const row = await screen.findByTestId(`org-export-${EXPORT}`);
    expect(within(row).getByText('abgeschlossen')).toBeInTheDocument();
    expect(within(row).getByText('100 %')).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'POST' && c.path === '/org/exports')).toBe(true);

    fireEvent.click(within(row).getByRole('button', { name: 'Objekte anzeigen' }));
    const objects = await screen.findByTestId('org-export-objects');
    expect(within(objects).getByRole('link', { name: 'manifest.json' })).toHaveAttribute(
      'href',
      'https://s3.example/m',
    );
    expect(
      within(objects).getByRole('link', { name: 'tables/ci/part-0001.jsonl' }),
    ).toBeInTheDocument();
    expect(objects).toHaveTextContent('abrufbar bis');
  });

  it('shows the MFA hint on 403 mfa-required', async () => {
    stubApi({ mfa: false });
    renderWithProviders(<OrgDataPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Export starten' }));
    const hints = await screen.findAllByTestId('mfa-hint');
    expect(hints[0]).toHaveTextContent('Mit zweitem Faktor anmelden');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('enables the delete button only for the matching slug', async () => {
    stubApi();
    renderWithProviders(<OrgDataPage />);

    const input = await screen.findByLabelText(/Kurznamen der Organisation eingeben: acme-gmbh/);
    const button = screen.getByRole('button', { name: 'Löschung beantragen' });
    expect(button).toBeDisabled();
    fireEvent.change(input, { target: { value: 'acme' } });
    expect(button).toBeDisabled();
    fireEvent.change(input, { target: { value: 'acme-gmbh' } });
    expect(button).toBeEnabled();
    expect(screen.getByText(/Exportieren Sie die Daten vor dem Antrag/)).toBeInTheDocument();
    expect(screen.getByText(/zweitem Faktor \(MFA\)/)).toBeInTheDocument();
  });

  it('requests and cancels the deletion', async () => {
    const calls = stubApi();
    renderWithProviders(<OrgDataPage />);

    const input = await screen.findByLabelText(/Kurznamen der Organisation eingeben: acme-gmbh/);
    fireEvent.change(input, { target: { value: 'acme-gmbh' } });
    fireEvent.click(screen.getByRole('button', { name: 'Löschung beantragen' }));
    expect(await screen.findByTestId('deletion-due')).toHaveTextContent('Endgültige Löschung am');
    expect(screen.getByText(/Verbleibend: 29 Tage, 23 Stunden/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === 'POST' && c.path === '/org/deletion')?.body).toEqual({
      confirm_slug: 'acme-gmbh',
    });

    fireEvent.click(screen.getByRole('button', { name: 'Löschung widerrufen' }));
    await waitFor(() =>
      expect(screen.getByTestId('deletion-state')).toHaveTextContent('keine Löschung beantragt'),
    );
    expect(calls.some((c) => c.method === 'DELETE' && c.path === '/org/deletion')).toBe(true);
  });

  it('shows the deletion banner with due date and cancel for org_admin', async () => {
    const calls = stubApi({ state: 'requested' });
    const { unmount } = renderWithProviders(<OrgDeletionBanner />);
    const banner = await screen.findByTestId('org-deletion-banner');
    expect(banner).toHaveTextContent('nur lesbar');
    expect(banner).toHaveTextContent('14. Dezember 2026');
    fireEvent.click(within(banner).getByRole('button', { name: 'Löschung widerrufen' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true));
    await waitFor(() => expect(screen.queryByTestId('org-deletion-banner')).toBeNull());
    unmount();
    vi.unstubAllGlobals();

    // Every other user sees the banner without the withdrawal.
    stubApi({ state: 'requested', permissions: ['location:read'] });
    renderWithProviders(<OrgDeletionBanner />);
    const plain = await screen.findByTestId('org-deletion-banner');
    expect(within(plain).queryByRole('button')).toBeNull();
  });

  it('shows the banner once a write is refused with 423 during the session', async () => {
    let current = 'none';
    const calls = stubApi();
    const base = vi.mocked(fetch);
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const path = new URL(String(input), 'http://localhost').pathname.replace(/^\/api\/v1/, '');
        if (path === '/org/deletion') return json(deletion(current));
        if (path === '/cis' && init?.method === 'POST') {
          // Another admin requested the deletion meanwhile.
          current = 'requested';
          return json(
            {
              type: 'urn:reticora:problem:org-pending-deletion',
              title: 'Organization pending deletion',
              status: 423,
              due_at: DUE,
            },
            423,
          );
        }
        return base(input, init);
      }),
    );
    function Writer() {
      const write = useMutation({
        mutationFn: () => fetchAPI('/cis', { method: 'POST', body: '{}' }),
      });
      return (
        <button type="button" onClick={() => write.mutate()}>
          write
        </button>
      );
    }
    renderWithProviders(
      <>
        <OrgDeletionBanner />
        <Writer />
      </>,
    );
    await waitFor(() => expect(calls.some((c) => c.path === '/modules')).toBe(true));
    expect(screen.queryByTestId('org-deletion-banner')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'write' }));
    expect(await screen.findByTestId('org-deletion-banner')).toHaveTextContent('14. Dezember 2026');
  });

  it('sends no request without the module org_lifecycle', async () => {
    const calls = stubApi({ state: 'requested', modules: [] });
    const { container } = renderWithProviders(<OrgDeletionBanner />);
    await waitFor(() => expect(calls.some((c) => c.path === '/modules')).toBe(true));
    expect(container).toBeEmptyDOMElement();
    expect(calls.some((c) => c.path === '/org/deletion')).toBe(false);
  });

  it('shows the read-only message on 423 org-pending-deletion', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        json(
          {
            type: 'urn:reticora:problem:org-pending-deletion',
            title: 'Organization pending deletion',
            status: 423,
            detail: 'the organization is read-only until its deletion',
            due_at: DUE,
          },
          423,
        ),
      ),
    );
    const error = await fetchAPI('/cis', { method: 'POST', body: '{}' }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    const apiError = error as ApiError;
    expect(apiError.status).toBe(423);
    expect(apiError.message).toBe('Organisation wird gelöscht – nur Lesen bis 14. Dezember 2026');
    // Forms that show detail or message both read the message.
    expect(apiError.detail).toBe(apiError.message);
  });
});
