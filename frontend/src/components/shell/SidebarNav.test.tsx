import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { useShellStore } from '../../stores/shell';
import type { AppPage } from '../CommandPalette';
import { SidebarNav } from './SidebarNav';
import { NavLockContext } from './navigation';

const PAGES: AppPage[] = ['dashboard', 'cmdb', 'topology', 'reviews', 'tickets', 'users'];

function renderNav(props: Partial<React.ComponentProps<typeof SidebarNav>> = {}, route = '/cmdb') {
  return renderWithProviders(<SidebarNav visiblePages={new Set(PAGES)} {...props} />, { route });
}

describe('SidebarNav', () => {
  beforeEach(() => {
    window.localStorage.clear();
    useShellStore.setState({ collapsed: false, groups: {} });
  });

  it('shows only the pages it is given and drops empty groups', () => {
    renderNav();
    const nav = screen.getByRole('navigation', { name: 'Hauptnavigation' });
    expect(within(nav).getByRole('link', { name: 'Topologie' })).toBeInTheDocument();
    expect(within(nav).queryByRole('link', { name: 'Racks' })).toBeNull();
    // Governance has no visible page here: no heading either.
    expect(within(nav).queryByRole('button', { name: 'Governance' })).toBeNull();
    expect(within(nav).getByRole('button', { name: 'Infrastruktur' })).toBeInTheDocument();
  });

  it('marks the current page, also on a detail route', () => {
    renderNav({}, '/cmdb/123');
    expect(screen.getByRole('link', { name: 'CMDB' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'Topologie' })).not.toHaveAttribute('aria-current');
  });

  it('folds a group by its heading and remembers the choice', () => {
    renderNav();
    const heading = screen.getByRole('button', { name: 'Infrastruktur' });
    expect(heading).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(heading);
    expect(heading).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('link', { name: 'Topologie' })).toBeNull();
    expect(useShellStore.getState().groups.infrastructure).toBe(false);
  });

  it('opens a closed group again when its page becomes current', () => {
    useShellStore.setState({ groups: { infrastructure: false } });
    renderNav({}, '/topology');
    expect(screen.getByRole('button', { name: 'Infrastruktur' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    expect(screen.getByRole('link', { name: 'Topologie' })).toBeInTheDocument();
  });

  it('shows the open count beside a page and names it for screen readers', () => {
    renderNav({ counts: { reviews: 7 } });
    expect(screen.getByRole('link', { name: /Reviews/ })).toHaveTextContent('7');
    expect(screen.getByText('7 offen')).toHaveClass('sr-only');
  });

  it('keeps every link named in the icon rail', () => {
    renderNav({ collapsed: true });
    expect(screen.getByRole('link', { name: 'Topologie' })).toBeInTheDocument();
    // The rail has no group headings.
    expect(screen.queryByRole('button', { name: 'Infrastruktur' })).toBeNull();
  });

  describe('locked pages of the plan (WP-857)', () => {
    function renderLocked(plan: string | undefined, route = '/cmdb', collapsed = false) {
      useShellStore.setState({ collapsed: false, groups: { assets: true } });
      return renderWithProviders(
        <NavLockContext.Provider value={{ pages: new Set<AppPage>(['assets']), plan }}>
          <SidebarNav visiblePages={new Set(PAGES)} collapsed={collapsed} />
        </NavLockContext.Provider>,
        { route },
      );
    }

    it('shows a locked page with lock, aria-disabled and the plan', () => {
      renderLocked('essential');
      const link = screen.getByRole('link', { name: 'Inventar' });
      expect(link).toHaveAttribute('aria-disabled', 'true');
      expect(link).toHaveAttribute('data-locked', 'true');
      expect(link).toHaveAccessibleDescription('Nicht im Paket Essential enthalten');
      expect(link.querySelector('svg.lucide-lock')).not.toBeNull();
      // The group of a locked page is shown, the link leads to the hint page.
      expect(screen.getByRole('button', { name: 'Assets' })).toBeInTheDocument();
      expect(link).toHaveAttribute('href', '/assets');
    });

    it('names no plan it does not know', () => {
      renderLocked(undefined);
      expect(screen.getByRole('link', { name: 'Inventar' })).toHaveAccessibleDescription(
        'Nicht in Ihrem Paket enthalten',
      );
    });

    it('leaves the other pages unlocked', () => {
      renderLocked('essential');
      const link = screen.getByRole('link', { name: 'Topologie' });
      expect(link).not.toHaveAttribute('aria-disabled');
      expect(link).not.toHaveAccessibleDescription();
    });

    it('keeps the hint in the icon rail', () => {
      renderLocked('standard', '/cmdb', true);
      expect(screen.getByRole('link', { name: 'Inventar' })).toHaveAccessibleDescription(
        'Nicht im Paket Standard enthalten',
      );
    });
  });
});
