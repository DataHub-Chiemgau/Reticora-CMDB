import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
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
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'router' } });
    await waitFor(() => expect(screen.getByText('Core Router · ci')).toBeInTheDocument());
  });

  it('is operable with the keyboard only (UI-10)', async () => {
    stubFetchRoutes({
      '/search': { data: [], total: 0, limit: 5, offset: 0, has_more: false },
    });
    const onNavigate = vi.fn();
    renderWithProviders(
      <CommandPalette onNavigate={onNavigate} onCreateCI={vi.fn()} onToggleDarkMode={vi.fn()} />,
    );

    // Ctrl+K opens the palette with focus in the search field.
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const input = await screen.findByRole('combobox');
    await waitFor(() => expect(document.activeElement).toBe(input));
    const listbox = screen.getByRole('listbox');
    expect(input).toHaveAttribute('aria-controls', listbox.id);

    const options = screen.getAllByRole('option');
    expect(options.length).toBeGreaterThan(2);
    // Options are not in the Tab order; the active one is announced through
    // aria-activedescendant.
    options.forEach((option) => expect(option.tabIndex).toBe(-1));
    expect(input).toHaveAttribute('aria-activedescendant', options[0]?.id);
    expect(options[0]).toHaveAttribute('aria-selected', 'true');

    fireEvent.keyDown(input, { key: 'ArrowDown' });
    expect(input).toHaveAttribute('aria-activedescendant', options[1]?.id);
    fireEvent.keyDown(input, { key: 'ArrowUp' });
    fireEvent.keyDown(input, { key: 'ArrowUp' });
    expect(input).toHaveAttribute('aria-activedescendant', options[options.length - 1]?.id);
    fireEvent.keyDown(input, { key: 'Home' });
    expect(input).toHaveAttribute('aria-activedescendant', options[0]?.id);
    fireEvent.keyDown(input, { key: 'End' });
    expect(input).toHaveAttribute('aria-activedescendant', options[options.length - 1]?.id);

    // Typing filters, Enter runs the active command and closes the palette.
    fireEvent.change(input, { target: { value: 'dashboard' } });
    await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(1));
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onNavigate).toHaveBeenCalledWith('dashboard');
    await waitFor(() => expect(screen.queryByRole('combobox')).not.toBeInTheDocument());
  });

  it('closes with Escape and reopens with Ctrl+K', async () => {
    stubFetchRoutes({
      '/search': { data: [], total: 0, limit: 5, offset: 0, has_more: false },
    });
    renderWithProviders(
      <CommandPalette onNavigate={vi.fn()} onCreateCI={vi.fn()} onToggleDarkMode={vi.fn()} />,
    );
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const input = await screen.findByRole('combobox');
    fireEvent.keyDown(input, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('combobox')).not.toBeInTheDocument());
    fireEvent.keyDown(window, { key: 'k', metaKey: true });
    expect(await screen.findByRole('combobox')).toBeInTheDocument();
  });

  // F-171 (WP-519): the palette has no Dialog.Trigger; it must hand focus
  // back itself instead of leaving it on <body>.
  it('returns focus to the trigger after Escape', async () => {
    stubFetchRoutes({
      '/search': { data: [], total: 0, limit: 5, offset: 0, has_more: false },
    });
    const user = userEvent.setup();
    renderWithProviders(
      <>
        <button type="button">previous control</button>
        <CommandPalette onNavigate={vi.fn()} onCreateCI={vi.fn()} onToggleDarkMode={vi.fn()} />
      </>,
    );
    const previous = screen.getByRole('button', { name: 'previous control' });
    await user.tab();
    expect(document.activeElement).toBe(previous);

    await user.keyboard('{Control>}k{/Control}');
    const input = await screen.findByRole('combobox');
    await waitFor(() => expect(document.activeElement).toBe(input));
    await user.keyboard('{Escape}');

    await waitFor(() => expect(screen.queryByRole('combobox')).not.toBeInTheDocument());
    await waitFor(() => expect(document.activeElement).toBe(previous));
    expect(document.activeElement).not.toBe(document.body);

    // Ctrl+K on the open palette closes it the same way.
    await user.keyboard('{Control>}k{/Control}');
    await screen.findByRole('combobox');
    await user.keyboard('{Control>}k{/Control}');
    await waitFor(() => expect(screen.queryByRole('combobox')).not.toBeInTheDocument());
    await waitFor(() => expect(document.activeElement).toBe(previous));
  });

  it('returns focus after a command that stays on the page', async () => {
    stubFetchRoutes({
      '/search': { data: [], total: 0, limit: 5, offset: 0, has_more: false },
    });
    const user = userEvent.setup();
    const onToggleDarkMode = vi.fn();
    renderWithProviders(
      <>
        <button type="button">previous control</button>
        <CommandPalette
          onNavigate={vi.fn()}
          onCreateCI={vi.fn()}
          onToggleDarkMode={onToggleDarkMode}
        />
      </>,
    );
    const previous = screen.getByRole('button', { name: 'previous control' });
    await user.tab();
    await user.keyboard('{Control>}k{/Control}');
    const input = await screen.findByRole('combobox');
    await user.type(input, 'appearance');
    await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(1));
    await user.keyboard('{Enter}');

    expect(onToggleDarkMode).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole('combobox')).not.toBeInTheDocument());
    await waitFor(() => expect(document.activeElement).toBe(previous));
  });

  it('puts focus on the main content of the target page after a navigation', async () => {
    stubFetchRoutes({
      '/search': { data: [], total: 0, limit: 5, offset: 0, has_more: false },
    });
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    renderWithProviders(
      <>
        <button type="button">previous control</button>
        <main id="main-content" tabIndex={-1}>
          page
        </main>
        <CommandPalette onNavigate={onNavigate} onCreateCI={vi.fn()} onToggleDarkMode={vi.fn()} />
      </>,
    );
    await user.tab();
    await user.keyboard('{Control>}k{/Control}');
    const input = await screen.findByRole('combobox');
    await user.type(input, 'dashboard');
    await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(1));
    await user.keyboard('{Enter}');

    expect(onNavigate).toHaveBeenCalledWith('dashboard');
    await waitFor(() => expect(screen.queryByRole('combobox')).not.toBeInTheDocument());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  });
});
