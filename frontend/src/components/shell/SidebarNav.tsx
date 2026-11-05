import { ChevronDown } from 'lucide-react';
import { useEffect, useId, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { NavLink, useLocation } from 'react-router-dom';
import { pageToPath } from '../CommandPalette';
import type { AppPage } from '../CommandPalette';
import { cn } from '../ui/utils';
import { Tooltip } from '../ui/Tooltip';
import { useShellStore } from '../../stores/shell';
import { NAV_GROUPS } from './navigation';
import type { NavGroupDef } from './navigation';

interface SidebarNavProps {
  /** Pages the tenant, the release and the user's rights admit. */
  visiblePages: ReadonlySet<AppPage>;
  /** Open-item counts to show beside a page (e.g. open reviews). */
  counts?: Partial<Record<AppPage, number>>;
  /** True: the icon rail (desktop only, never in the drawer). */
  collapsed?: boolean;
  /** Called after a link was followed (the drawer closes itself). */
  onNavigate?: () => void;
}

/** pageOfPath is the page whose route the location is on, if any. */
function pageOfPath(pathname: string): AppPage | undefined {
  let best: { page: AppPage; length: number } | undefined;
  for (const [page, path] of Object.entries(pageToPath) as [AppPage, string][]) {
    const matches = pathname === path || (path !== '/' && pathname.startsWith(`${path}/`));
    if (matches && (!best || path.length > best.length)) {
      best = { page, length: path.length };
    }
  }
  return best?.page;
}

function Group({
  group,
  pages,
  counts,
  collapsed,
  active,
  isFirst,
  onNavigate,
}: {
  group: NavGroupDef;
  pages: NavGroupDef['items'];
  counts: SidebarNavProps['counts'];
  collapsed: boolean;
  active: AppPage | undefined;
  isFirst: boolean;
  onNavigate?: () => void;
}) {
  const { t } = useTranslation();
  const listId = useId();
  const stored = useShellStore((state) => state.groups[group.id]);
  const setGroup = useShellStore((state) => state.setGroup);
  const hasHeading = Boolean(group.labelKey);
  const open = !hasHeading || collapsed || (stored ?? group.defaultOpen);
  const containsActive = pages.some((item) => item.page === active);

  // Arriving on a page (link, palette, back button) opens its group.
  useEffect(() => {
    if (containsActive && stored === false) {
      setGroup(group.id, true);
    }
    // Only a change of the active page may reopen a group the user closed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);

  return (
    <li className={cn(collapsed && !isFirst && 'mt-2 border-t border-line pt-2')}>
      {hasHeading && !collapsed ? (
        <button
          type="button"
          aria-expanded={open}
          aria-controls={listId}
          onClick={() => setGroup(group.id, !open)}
          className="mt-3 flex h-7 w-full items-center justify-between rounded-md px-2.5 text-xs font-medium text-fg-subtle transition-colors hover:text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
        >
          <span>{t(group.labelKey!)}</span>
          <ChevronDown
            aria-hidden="true"
            className={cn('h-3.5 w-3.5 transition-transform', !open && '-rotate-90')}
          />
        </button>
      ) : null}
      {/* Hidden groups stay out of the Tab order and the accessibility tree. */}
      <ul id={listId} hidden={!open} className="mt-0.5 space-y-0.5">
        {pages.map(({ page, icon: Icon }) => {
          const label = t(`nav.${page}`);
          const count = counts?.[page];
          const link = (
            <NavLink
              to={pageToPath[page]}
              onClick={onNavigate}
              aria-label={collapsed ? label : undefined}
              className={({ isActive }) =>
                cn(
                  'group relative flex h-8 items-center rounded-lg text-sm font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-accent',
                  collapsed ? 'mx-auto w-10 justify-center' : 'gap-2.5 px-2.5',
                  isActive || page === active
                    ? 'bg-panel text-fg shadow-sm ring-1 ring-line'
                    : 'text-fg-muted hover:bg-fg/[0.05] hover:text-fg',
                )
              }
            >
              <Icon
                aria-hidden="true"
                className={cn(
                  'h-4 w-4 shrink-0 transition-colors',
                  page === active ? 'text-accent' : 'text-fg-subtle group-hover:text-fg-muted',
                )}
              />
              {collapsed ? null : <span className="min-w-0 flex-1 truncate">{label}</span>}
              {count && count > 0 ? (
                collapsed ? (
                  <span
                    aria-hidden="true"
                    className="absolute right-1.5 top-1.5 h-2 w-2 rounded-full bg-accent ring-2 ring-canvas"
                  />
                ) : (
                  <span className="ml-auto rounded-md bg-accent-soft px-1.5 text-xs font-medium tabular-nums text-accent-strong">
                    <span className="sr-only">{t('shell.openCount', { count })}</span>
                    <span aria-hidden="true">{count > 99 ? '99+' : count}</span>
                  </span>
                )
              ) : null}
            </NavLink>
          );
          return (
            <li key={page}>
              <Tooltip label={label} side="right" disabled={!collapsed}>
                {link}
              </Tooltip>
            </li>
          );
        })}
      </ul>
    </li>
  );
}

/**
 * SidebarNav is the primary navigation: grouped pages with icons, the
 * current page marked (aria-current), open counts, and an icon rail mode.
 */
export function SidebarNav({
  visiblePages,
  counts,
  collapsed = false,
  onNavigate,
}: SidebarNavProps) {
  const { t } = useTranslation();
  const { pathname } = useLocation();
  const active = pageOfPath(pathname);

  const groups = useMemo(
    () =>
      NAV_GROUPS.map((group) => ({
        group,
        pages: group.items.filter((item) => visiblePages.has(item.page)),
      })).filter((entry) => entry.pages.length > 0),
    [visiblePages],
  );

  return (
    <nav aria-label={t('accessibility.primaryNavigation')}>
      <ul>
        {groups.map(({ group, pages }, index) => (
          <Group
            key={group.id}
            group={group}
            pages={pages}
            counts={counts}
            collapsed={collapsed}
            active={active}
            isFirst={index === 0}
            onNavigate={onNavigate}
          />
        ))}
      </ul>
    </nav>
  );
}
