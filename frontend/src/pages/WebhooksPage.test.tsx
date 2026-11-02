import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { WebhooksPage } from './WebhooksPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

const managePermissions = { permissions: ['webhook:read', 'webhook:manage'] };

const subscription = {
  id: 'wh-1',
  organization_id: 'org-1',
  name: 'Ticket hook',
  url: 'https://example.com/hook',
  events: ['ci.created'],
  is_active: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

function problem(status: number, body: Record<string, unknown>) {
  return new Response(JSON.stringify({ status, ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

/**
 * stubWebhookApi answers the reads of the page and hands every write
 * (POST/DELETE) to `write`, so a test decides what the server answers.
 */
function stubWebhookApi(permissions: string[], write: (url: string, method: string) => Response) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    const method = (init?.method ?? 'GET').toUpperCase();
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    if (method !== 'GET') return write(url, method);
    if (url.includes('/me/permissions')) return json({ permissions });
    if (url.includes('/webhooks/dead-letters')) return json(paginated([]));
    if (url.includes('/webhooks')) return json(paginated([subscription]));
    return problem(404, { detail: `unexpected request: ${url}` });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

async function fillForm() {
  fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Catcher' } });
  fireEvent.change(screen.getByLabelText('URL'), {
    target: { value: 'http://127.0.0.1:9000/hook' },
  });
  fireEvent.change(screen.getByLabelText('Geheimnis (HMAC-Signatur)'), {
    target: { value: 'secret-0123456789' },
  });
  fireEvent.click(screen.getByLabelText('ci.created'));
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('WebhooksPage', () => {
  it('renders subscriptions and an empty dead-letter queue', async () => {
    stubFetchRoutes({
      '/me/permissions': managePermissions,
      '/webhooks/dead-letters': paginated([]),
      '/webhooks': paginated([
        {
          id: 'wh-1',
          organization_id: 'org-1',
          name: 'Ticket hook',
          url: 'https://example.com/hook',
          events: ['ci.created'],
          is_active: true,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
    });

    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });

    expect(await screen.findByText('Ticket hook')).toBeInTheDocument();
    expect(screen.getByText('Keine Dead Letters')).toBeInTheDocument();
  });

  it('shows the empty state when no subscriptions exist', async () => {
    stubFetchRoutes({
      '/me/permissions': managePermissions,
      '/webhooks/dead-letters': paginated([]),
      '/webhooks': paginated([]),
    });

    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });

    expect(await screen.findByText('Keine Webhooks vorhanden')).toBeInTheDocument();
  });

  it('retries a dead delivery (WHK-01)', async () => {
    const fetchMock = stubFetchRoutes({
      '/me/permissions': managePermissions,
      '/webhooks/dead-letters': paginated([]),
      '/webhooks/wh-1/deliveries/dl-1/retry': { id: 'dl-1', status: 'pending' },
      '/webhooks/wh-1/deliveries': paginated([
        {
          id: 'dl-1',
          organization_id: 'org-1',
          subscription_id: 'wh-1',
          event: 'ci.created',
          status: 'dead',
          attempt: 6,
          max_attempts: 6,
          error: 'unexpected status 502',
          created_at: '2026-01-01T00:00:00Z',
        },
      ]),
      '/webhooks': paginated([
        {
          id: 'wh-1',
          organization_id: 'org-1',
          name: 'Ticket hook',
          url: 'https://example.com/hook',
          events: ['ci.created'],
          is_active: false,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
    });

    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });
    fireEvent.click(await screen.findByRole('button', { name: 'Zustellverlauf' }));
    expect(await screen.findByText('aufgegeben')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Erneut senden' }));
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([url]) =>
          String(url).includes('/webhooks/wh-1/deliveries/dl-1/retry'),
        ),
      ).toBe(true),
    );
  });

  // F-062: a refusal of the server is shown at the form, the inputs stay.
  it('shows an egress refusal with its reason at the URL field and keeps the inputs', async () => {
    stubWebhookApi(managePermissions.permissions, () =>
      problem(400, {
        title: 'Bad Request',
        detail: 'egress: destination is not allowed: 127.0.0.1',
      }),
    );
    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    const url = screen.getByLabelText('URL');
    await waitFor(() => expect(url).toHaveAttribute('aria-invalid', 'true'));
    expect(
      screen.getByText('Ziel vom Egress-Schutz blockiert, nicht erlaubt: 127.0.0.1'),
    ).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('Der Webhook wurde nicht gespeichert.');
    expect(url).toHaveValue('http://127.0.0.1:9000/hook');
    expect(screen.getByLabelText('Name')).toHaveValue('Catcher');
    expect(screen.getByLabelText('Geheimnis (HMAC-Signatur)')).toHaveValue('secret-0123456789');
    expect(screen.getByLabelText('ci.created')).toBeChecked();
  });

  it('shows a missing right (403) as a permission hint', async () => {
    stubWebhookApi(managePermissions.permissions, () =>
      problem(403, { title: 'Forbidden', detail: 'missing permission webhook:manage' }),
    );
    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Keine Berechtigung: Für diese Aktion fehlt Ihnen das Recht webhook:manage.',
    );
    expect(screen.getByLabelText('Name')).toHaveValue('Catcher');
  });

  it('attaches validation errors to their fields', async () => {
    let answer = problem(400, { title: 'Bad Request', detail: 'invalid event: ci.created' });
    stubWebhookApi(managePermissions.permissions, () => answer);
    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    expect(await screen.findByText('Unbekanntes Event: ci.created')).toBeInTheDocument();

    answer = problem(422, {
      title: 'Unprocessable Entity',
      detail: 'validation failed',
      violations: [{ field: 'name', detail: 'name already exists' }],
    });
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    expect(await screen.findByText('Ungültiger Wert: name already exists')).toBeInTheDocument();
    expect(screen.getByLabelText('Name')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.queryByText('Unbekanntes Event: ci.created')).not.toBeInTheDocument();
  });

  it('clears the form after a successful save', async () => {
    const fetchMock = stubWebhookApi(
      managePermissions.permissions,
      () =>
        new Response(JSON.stringify({ ...subscription, id: 'wh-2', name: 'Catcher' }), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue(''));
    expect(screen.getByLabelText('URL')).toHaveValue('');
    expect(screen.getByLabelText('Geheimnis (HMAC-Signatur)')).toHaveValue('');
    expect(screen.getByLabelText('ci.created')).not.toBeChecked();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(
        ([url, init]) => String(url).endsWith('/webhooks') && init?.method === 'POST',
      ),
    ).toBe(true);
  });

  it('reports a refused delete and test at the list', async () => {
    let answer = problem(403, { title: 'Forbidden', detail: 'forbidden' });
    stubWebhookApi(managePermissions.permissions, () => answer);
    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });
    await screen.findByText('Ticket hook');

    fireEvent.click(screen.getByRole('button', { name: 'Löschen' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Keine Berechtigung');
    expect(screen.getByText('Ticket hook')).toBeInTheDocument();

    answer = problem(503, {
      title: 'Service Unavailable',
      detail: 'webhook dispatcher is not configured',
    });
    fireEvent.click(screen.getByRole('button', { name: 'Test senden' }));
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(
        'Die Aktion ist fehlgeschlagen: webhook dispatcher is not configured',
      ),
    );
  });

  it('offers no save, delete, test or retry without webhook:manage and keeps the list readable', async () => {
    const fetchMock = stubFetchRoutes({
      '/me/permissions': { permissions: ['webhook:read', 'ci:read'] },
      '/webhooks/dead-letters': paginated([]),
      '/webhooks/wh-1/deliveries': paginated([
        {
          id: 'dl-1',
          organization_id: 'org-1',
          subscription_id: 'wh-1',
          event: 'ci.created',
          status: 'dead',
          attempt: 6,
          max_attempts: 6,
          created_at: '2026-01-01T00:00:00Z',
        },
      ]),
      '/webhooks': paginated([subscription]),
    });
    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });

    expect(await screen.findByText('Ticket hook')).toBeInTheDocument();
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/me/permissions'))).toBe(
        true,
      ),
    );
    expect(
      await screen.findByText(
        'Sie können Webhooks einsehen; Anlegen, Löschen und Testen erfordern das Recht webhook:manage.',
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Speichern' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Löschen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Test senden' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Zustellverlauf' }));
    expect(await screen.findByText('aufgegeben')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Erneut senden' })).not.toBeInTheDocument();
  });

  it('offers no actions while the effective rights cannot be read', async () => {
    stubFetchRoutes({
      '/me/permissions': problem(500, { title: 'Internal Error', detail: 'boom' }),
      '/webhooks/dead-letters': paginated([]),
      '/webhooks': paginated([subscription]),
    });
    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });

    expect(await screen.findByText('Ticket hook')).toBeInTheDocument();
    expect(
      await screen.findByText(
        'Sie können Webhooks einsehen; Anlegen, Löschen und Testen erfordern das Recht webhook:manage.',
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Speichern' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Löschen' })).not.toBeInTheDocument();
  });
});
