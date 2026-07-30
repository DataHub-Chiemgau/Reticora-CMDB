import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { CommandPalette } from './CommandPalette';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CommandPalette', () => {
  it('shows real search results', async () => {
    stubFetchRoutes({
      '/search': {
        data: [
          {
            id: 's1',
            organization_id: 'org',
            entity_type: 'ci',
            entity_id: 'ci-1',
            title: 'Core Router',
            summary: 'edge',
            url: '/cmdb/ci-1',
            score: 1,
            updated_at: '2026-01-01T00:00:00Z',
          },
        ],
        total: 1,
        limit: 5,
        offset: 0,
        has_more: false,
      },
    });
    renderWithProviders(
      <CommandPalette onNavigate={vi.fn()} onCreateCI={vi.fn()} onToggleDarkMode={vi.fn()} />,
    );
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'router' } });
    await waitFor(() => expect(screen.getByText('Core Router · ci')).toBeInTheDocument());
  });
});
