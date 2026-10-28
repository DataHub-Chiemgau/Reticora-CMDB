import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
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

  // F-334 (WP-594): contacts have no page; their hit has an empty url and
  // opens a card with the details the hit carries instead of navigating.
  describe('search hits of contacts and locations (F-334)', () => {
    const hit = (over: Record<string, unknown>) => ({
      id: `s-${String(over.entity_id)}`,
      organization_id: 'org',
      score: 1,
      updated_at: '2026-01-01T00:00:00Z',
      ...over,
    });
    const contact = hit({
      entity_type: 'contact',
      entity_id: 'c-1',
      title: 'Gerda Kabelzug',
      summary: 'Netzbetrieb',
      url: '',
      metadata: { email: 'gerda@example.org', phone: '+49 861 4711', role: 'Netzbetrieb' },
    });
    const sparse = hit({
      entity_type: 'contact',
      entity_id: 'c-2',
      title: 'Kabelzug Zwei',
      url: '',
    });
    const location = hit({
      entity_type: 'location',
      entity_id: 'loc-1',
      title: 'Kabelzug Halle',
      summary: 'building',
      url: '/locations?node=loc-1',
    });

    function setup() {
      stubFetchRoutes({
        '/search': {
          data: [contact, sparse, location],
          total: 3,
          limit: 5,
          offset: 0,
          has_more: false,
        },
      });
      const assign = vi.fn();
      vi.stubGlobal('location', { ...window.location, assign });
      renderWithProviders(
        <CommandPalette onNavigate={vi.fn()} onCreateCI={vi.fn()} onToggleDarkMode={vi.fn()} />,
      );
      return assign;
    }

    it('shows a contact hit in a card instead of navigating', async () => {
      const assign = setup();
      const user = userEvent.setup();
      await user.keyboard('{Control>}k{/Control}');
      const input = await screen.findByRole('combobox');
      await user.type(input, 'Kabelzug');
      await screen.findByRole('option', { name: /Gerda Kabelzug · contact/ });
      await user.keyboard('{Enter}');

      const card = await screen.findByRole('region', { name: 'Gerda Kabelzug' });
      await waitFor(() => expect(document.activeElement).toBe(card));
      expect(screen.getByRole('dialog')).toBeInTheDocument();
      expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
      expect(within(card).getByRole('link', { name: 'gerda@example.org' })).toHaveAttribute(
        'href',
        'mailto:gerda@example.org',
      );
      expect(within(card).getByRole('link', { name: '+49 861 4711' })).toHaveAttribute(
        'href',
        'tel:+498614711',
      );
      const detail = (label: RegExp) =>
        within(card).getByText(label).nextElementSibling as HTMLElement;
      expect(detail(/^(E-Mail|E-mail)$/)).toHaveTextContent('gerda@example.org');
      expect(detail(/^(Telefon|Phone)$/)).toHaveTextContent('+49 861 4711');
      expect(detail(/^(Rolle|Role)$/)).toHaveTextContent('Netzbetrieb');
      expect(assign).not.toHaveBeenCalled();

      // Back returns to the list with the same results and focus in the field.
      await user.click(
        within(card).getByRole('button', { name: /Zurück zur Suche|Back to search/ }),
      );
      const again = await screen.findByRole('combobox');
      await waitFor(() => expect(document.activeElement).toBe(again));
      expect(again).toHaveValue('Kabelzug');

      // A contact without details shows dashes; Escape closes the palette.
      await user.click(screen.getByRole('option', { name: /Kabelzug Zwei · contact/ }));
      const second = await screen.findByRole('region', { name: 'Kabelzug Zwei' });
      expect(
        within(second)
          .getAllByRole('definition')
          .map((d) => d.textContent),
      ).toEqual(['—', '—', '—']);
      await user.keyboard('{Escape}');
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
      expect(assign).not.toHaveBeenCalled();
    });

    it('opens a location hit at its node in the tree', async () => {
      const assign = setup();
      const user = userEvent.setup();
      await user.keyboard('{Control>}k{/Control}');
      await user.type(await screen.findByRole('combobox'), 'Kabelzug');
      await user.click(await screen.findByRole('option', { name: /Kabelzug Halle · location/ }));

      expect(assign).toHaveBeenCalledWith('/locations?node=loc-1');
      await waitFor(() => expect(screen.queryByRole('combobox')).not.toBeInTheDocument());
    });
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
