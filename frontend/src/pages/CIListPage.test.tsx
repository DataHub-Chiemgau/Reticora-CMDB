import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { CIListPage } from './CIListPage';
import { ToastViewport } from '../components/ui/Toast';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

const ci = {
  id: 'ci-1',
  organization_id: 'org-1',
  ci_type_id: 'switch',
  name: 'core-sw-01',
  status: 'active',
  manufacturer: 'Acme',
  attributes: {},
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CIListPage', () => {
  it('links every row to the deep-linkable detail page', async () => {
    stubFetchRoutes({
      '/cis': { data: [ci], total: 1, limit: 25, offset: 0, has_more: false },
    });

    renderWithProviders(<CIListPage onCreateCI={() => {}} />, { route: '/cmdb' });

    expect(await screen.findByRole('link', { name: 'core-sw-01' })).toHaveAttribute(
      'href',
      '/cmdb/ci-1',
    );
  });

  it('offers the create action from the empty state', async () => {
    stubFetchRoutes({
      '/cis': { data: [], total: 0, limit: 25, offset: 0, has_more: false },
    });
    const onCreateCI = vi.fn();

    renderWithProviders(<CIListPage onCreateCI={onCreateCI} />, { route: '/cmdb' });

    expect(await screen.findByText('Noch keine Configuration Items')).toBeInTheDocument();
    const createButtons = screen.getAllByRole('button', { name: 'CI erstellen' });
    fireEvent.click(createButtons[createButtons.length - 1]!);
    expect(onCreateCI).toHaveBeenCalledTimes(1);
  });

  it('bulk-updates the status of selected CIs', async () => {
    const fetchMock = stubFetchRoutes({
      '/cis': { data: [ci], total: 1, limit: 25, offset: 0, has_more: false },
    });

    renderWithProviders(
      <>
        <CIListPage onCreateCI={() => {}} />
        <ToastViewport />
      </>,
      { route: '/cmdb' },
    );

    // Select the single visible row; the bulk toolbar appears.
    fireEvent.click(await screen.findByRole('checkbox', { name: 'core-sw-01 auswählen' }));
    expect(await screen.findByText('1 ausgewählt')).toBeInTheDocument();

    // Choose a new status and apply it.
    fireEvent.change(screen.getByRole('combobox', { name: 'Status setzen…' }), {
      target: { value: 'maintenance' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Anwenden' }));

    const calls = fetchMock.mock.calls as unknown as [string, RequestInit | undefined][];
    const isPatch = (call: [string, RequestInit | undefined]) =>
      call[0].includes('/cis/ci-1') && call[1]?.method === 'PATCH';

    await waitFor(() => {
      expect(calls.find(isPatch)).toBeTruthy();
    });
    const patchCall = calls.find(isPatch);
    expect(JSON.parse(patchCall?.[1]?.body as string)).toEqual({ status: 'maintenance' });

    // Success toast is announced in the live region.
    expect(await screen.findByText('Status von 1 CIs aktualisiert')).toBeInTheDocument();
  });
});
