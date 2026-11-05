import * as Dialog from '@radix-ui/react-dialog';
import { Menu, PanelLeftClose, PanelLeftOpen, Search, X } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import type { CSSProperties, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation } from 'react-router-dom';
import { useMediaQuery } from '../../hooks/useMediaQuery';
import { useShellStore } from '../../stores/shell';
import type { ThemeMode } from '../../stores/theme';
import { LanguageSwitch } from '../LanguageSwitch';
import { NotificationCenter } from '../NotificationCenter';
import { ScopeSwitcher } from '../ScopeSwitcher';
import { IconButton } from '../ui/Button';
import { Kbd } from '../ui/Kbd';
import { Tooltip } from '../ui/Tooltip';
import { Brand, BrandMark } from './Brand';
import { openCommandPalette } from './paletteEvent';
import { SidebarNav } from './SidebarNav';
import { ThemeToggle } from './ThemeToggle';
import { UserMenu } from './UserMenu';
import type { AppPage } from '../CommandPalette';

interface ShellProps {
  children: ReactNode;
  /** Notices that stand above the page (expiring licences, draft state). */
  banners?: ReactNode;
  theme: { mode: ThemeMode; label: string; onToggle: () => void };
}

interface AppShellProps extends ShellProps {
  visiblePages: ReadonlySet<AppPage>;
  counts?: Partial<Record<AppPage, number>>;
  user: { name?: string; email?: string };
  onSignOut: () => void;
}

const LARGE = '(min-width: 1024px)';

function SkipLink() {
  const { t } = useTranslation();
  return (
    <a
      href="#main-content"
      className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-[80] focus:rounded-lg focus:bg-accent focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-accent-fg"
    >
      {t('accessibility.skipToContent')}
    </a>
  );
}

function Main({ children, banners }: { children: ReactNode; banners?: ReactNode }) {
  return (
    <>
      {banners ? <div className="space-y-3 px-4 pt-4 md:px-6 lg:px-8">{banners}</div> : null}
      {/* Focusable by script only: target of the skip link and of the command
          palette after a navigation (UI-10, WP-519). */}
      <main
        id="main-content"
        tabIndex={-1}
        className="min-w-0 flex-1 px-4 py-6 outline-none md:px-6 lg:px-8"
      >
        {children}
      </main>
    </>
  );
}

function SearchTrigger() {
  const { t } = useTranslation();
  const shortcut = useMemo(
    () =>
      typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
        ? '⌘K'
        : 'Ctrl K',
    [],
  );
  return (
    <>
      <button
        type="button"
        onClick={openCommandPalette}
        className="hidden h-9 w-72 items-center gap-2 rounded-lg border border-line-strong bg-panel px-3 text-sm text-fg-subtle shadow-sm transition-colors hover:border-fg-subtle/60 hover:text-fg-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent xl:flex"
      >
        <Search aria-hidden="true" className="h-4 w-4 shrink-0" />
        <span className="flex-1 truncate text-left">{t('shell.search')}</span>
        <Kbd>{shortcut}</Kbd>
      </button>
      <IconButton
        label={t('shell.searchShort')}
        icon={<Search />}
        onClick={openCommandPalette}
        className="xl:hidden"
      />
    </>
  );
}

/**
 * AppShell is the frame of every signed-in page: the sidebar (icon rail or
 * full, a drawer below 1024 px), a 56 px header that carries the scope, the
 * search and the account, and the main area that the page fills.
 */
export function AppShell({
  children,
  banners,
  theme,
  visiblePages,
  counts,
  user,
  onSignOut,
}: AppShellProps) {
  const { t } = useTranslation();
  const isLarge = useMediaQuery(LARGE);
  const collapsed = useShellStore((state) => state.collapsed);
  const toggleCollapsed = useShellStore((state) => state.toggleCollapsed);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const { pathname } = useLocation();

  // A route change (palette, back button, link) always closes the drawer.
  useEffect(() => {
    setDrawerOpen(false);
  }, [pathname]);
  useEffect(() => {
    if (isLarge) setDrawerOpen(false);
  }, [isLarge]);

  const railed = isLarge && collapsed;
  const shellStyle = {
    '--sidebar-w': railed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)',
  } as CSSProperties;

  const toggleLabel = collapsed ? t('shell.expandSidebar') : t('shell.collapseSidebar');

  return (
    <div className="min-h-screen bg-canvas text-fg" style={shellStyle}>
      <SkipLink />

      {isLarge ? (
        <aside className="fixed inset-y-0 left-0 z-30 flex w-[var(--sidebar-w)] flex-col border-r border-line bg-canvas transition-[width] duration-200">
          <div
            className={
              railed
                ? 'flex h-14 shrink-0 items-center justify-center'
                : 'flex h-14 shrink-0 items-center px-3.5'
            }
          >
            <Brand compact={railed} />
          </div>
          <div className="scroll-thin flex-1 overflow-y-auto overflow-x-hidden px-2 pb-3">
            <SidebarNav visiblePages={visiblePages} counts={counts} collapsed={railed} />
          </div>
          <div
            className={
              railed
                ? 'flex shrink-0 justify-center border-t border-line p-2'
                : 'flex shrink-0 border-t border-line p-2'
            }
          >
            <Tooltip label={toggleLabel} side="right" disabled={!railed}>
              <button
                type="button"
                onClick={toggleCollapsed}
                aria-label={railed ? toggleLabel : undefined}
                className={
                  railed
                    ? 'inline-flex h-9 w-10 items-center justify-center rounded-lg text-fg-muted transition-colors hover:bg-fg/[0.06] hover:text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent'
                    : 'inline-flex h-9 w-full items-center gap-2.5 rounded-lg px-2.5 text-sm font-medium text-fg-muted transition-colors hover:bg-fg/[0.06] hover:text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent'
                }
              >
                {railed ? (
                  <PanelLeftOpen aria-hidden="true" className="h-4 w-4" />
                ) : (
                  <>
                    <PanelLeftClose aria-hidden="true" className="h-4 w-4 text-fg-subtle" />
                    <span>{toggleLabel}</span>
                  </>
                )}
              </button>
            </Tooltip>
          </div>
        </aside>
      ) : null}

      <div className="flex min-h-screen min-w-0 flex-col lg:pl-[var(--sidebar-w)] lg:transition-[padding] lg:duration-200">
        <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-2 border-b border-line bg-canvas/85 px-3 backdrop-blur md:px-4 lg:px-6">
          {isLarge ? null : (
            <>
              <IconButton
                label={t('shell.openNavigation')}
                icon={<Menu />}
                onClick={() => setDrawerOpen(true)}
              />
              <Brand compact className="-ml-1" />
            </>
          )}
          {isLarge ? <ScopeSwitcher /> : null}
          <div className="ml-auto flex shrink-0 items-center gap-1.5">
            <SearchTrigger />
            <NotificationCenter />
            <ThemeToggle mode={theme.mode} label={theme.label} onToggle={theme.onToggle} />
            <UserMenu name={user.name} email={user.email} onSignOut={onSignOut} />
          </div>
        </header>
        <Main banners={banners}>{children}</Main>
      </div>

      {isLarge ? null : (
        <Dialog.Root open={drawerOpen} onOpenChange={setDrawerOpen}>
          <Dialog.Portal>
            <Dialog.Overlay className="fixed inset-0 z-40 animate-fade-in bg-gray-950/55 backdrop-blur-[2px]" />
            <Dialog.Content
              aria-describedby={undefined}
              className="fixed inset-y-0 left-0 z-50 flex w-[min(86vw,19rem)] animate-drawer-in flex-col border-r border-line bg-canvas text-gray-900 shadow-2xl outline-none dark:text-gray-100"
            >
              <Dialog.Title className="sr-only">
                {t('accessibility.primaryNavigation')}
              </Dialog.Title>
              <div className="flex h-14 shrink-0 items-center justify-between px-3.5">
                <span className="inline-flex items-center gap-2 text-[15px] font-semibold tracking-tight">
                  <BrandMark className="text-accent" />
                  <span>
                    Reticora<span className="ml-1.5 font-medium text-fg-subtle">CMDB</span>
                  </span>
                </span>
                <Dialog.Close asChild>
                  <IconButton label={t('shell.closeNavigation')} icon={<X />} />
                </Dialog.Close>
              </div>
              <div className="border-y border-line px-3.5 py-3">
                <ScopeSwitcher layout="stacked" />
              </div>
              <div className="scroll-thin flex-1 overflow-y-auto px-2 pb-6">
                <SidebarNav
                  visiblePages={visiblePages}
                  counts={counts}
                  onNavigate={() => setDrawerOpen(false)}
                />
              </div>
            </Dialog.Content>
          </Dialog.Portal>
        </Dialog.Root>
      )}
    </div>
  );
}

/**
 * PublicShell frames the pages that run without a session (sign-in,
 * callback, onboarding, invitation): the brand, the two preferences that
 * matter before sign-in (language, appearance) and the page.
 */
export function PublicShell({ children, theme }: ShellProps) {
  return (
    <div className="relative flex min-h-screen flex-col overflow-hidden bg-canvas text-fg">
      {/* A faint survey grid behind the card: the one decorative moment of the
          product, only where there is nothing to read yet. */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 hidden md:block [background-image:radial-gradient(hsl(var(--line-strong))_1px,transparent_1.2px)] [background-size:28px_28px] [mask-image:radial-gradient(ellipse_70%_55%_at_50%_28%,black,transparent_78%)]"
      />
      <SkipLink />
      <header className="relative flex h-14 shrink-0 items-center justify-between px-4 md:px-6">
        <Brand />
        <div className="flex items-center gap-2">
          <LanguageSwitch />
          <ThemeToggle mode={theme.mode} label={theme.label} onToggle={theme.onToggle} />
        </div>
      </header>
      <main
        id="main-content"
        tabIndex={-1}
        className="relative flex-1 px-4 py-8 outline-none md:px-6"
      >
        {children}
      </main>
    </div>
  );
}
