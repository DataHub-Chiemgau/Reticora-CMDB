import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { FindingsPage } from './FindingsPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const empty = { data: [], total: 0, limit: 100, offset: 0, has_more: false };

function listCalls(fetchMock: ReturnType<typeof stubFetchRoutes>) {
  return fetchMock.mock.calls
    .map(([input]) => (typeof input === 'string' ? input : input.toString()))
    .filter((url) => url.includes('/security/findings?'));
}

describe('FindingsPage', () => {
  it('honors ?ci_id= of the CI detail link and removes the filter (WP-1137)', async () => {
    const fetchMock = stubFetchRoutes({
      '/security/findings/summary': { by_severity: {}, open_total: 0 },
      '/security/findings': empty,
    });
    renderWithProviders(<FindingsPage />, { route: '/findings?ci_id=ci-42' });
    await waitFor(() =>
      expect(listCalls(fetchMock).some((url) => url.includes('ci_id=ci-42'))).toBe(true),
    );
    const chip = await screen.findByRole('button', { name: /ci-42/ });
    fireEvent.click(chip);
    await waitFor(() => expect(listCalls(fetchMock).slice(-1)[0]).not.toContain('ci_id='));
    expect(screen.queryByRole('button', { name: /ci-42/ })).toBeNull();
  });
});
