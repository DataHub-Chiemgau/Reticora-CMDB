import { Suspense, lazy, useMemo, useState } from 'react';
import { Routes, Route, useNavigate, useLocation, Navigate, Outlet } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { CommandPalette } from './components/CommandPalette';
import { pageToPath } from './components/CommandPalette';
import type { AppPage } from './components/CommandPalette';
import { Button } from './components/ui/Button';
import { Skeleton } from './components/ui/Skeleton';
import { ToastViewport } from './components/ui/Toast';
import { useAuthStore } from './auth/authStore';
import { fetchAuthConfig, getStoredAuthConfig } from './auth/oidc';
import { logout } from './auth/session';
import { LoginPage } from './pages/auth/LoginPage';
import { CallbackPage } from './pages/auth/CallbackPage';
import { useEntitlements, navFeatureFor } from './hooks/useEntitlements';
import { useThemeStore } from './stores/theme';

// Route-level code splitting: every feature page is a separate chunk so the
// initial bundle stays small (perceived speed, spec §8.1). Pages use named
// exports; they are remapped to a default export for React.lazy. The loader
// is intentionally typed loosely — the module shape is fixed by convention.
function lazyPage(loader: () => Promise<Record<string, unknown>>, name: string) {
  return lazy(async () => {
    const mod = (await loader()) as Record<string, React.ComponentType<Record<string, unknown>>>;
    const component = mod[name];
    if (!component) {
      throw new Error(`page chunk does not export ${name}`);
    }
    return { default: component };
  });
}

const AssetListPage = lazyPage(() => import('./pages/AssetListPage'), 'AssetListPage');
const AssignmentListPage = lazyPage(
  () => import('./pages/AssignmentListPage'),
  'AssignmentListPage',
);
const AssistantPage = lazyPage(() => import('./pages/AssistantPage'), 'AssistantPage');
const CIFormModal = lazyPage(() => import('./pages/CIFormModal'), 'CIFormModal');
const CIDetailPage = lazyPage(() => import('./pages/CIDetailPage'), 'CIDetailPage');
const CIListPage = lazyPage(() => import('./pages/CIListPage'), 'CIListPage');
const DashboardPage = lazyPage(() => import('./pages/DashboardPage'), 'DashboardPage');
const DiscoveryPage = lazyPage(() => import('./pages/DiscoveryPage'), 'DiscoveryPage');
const DocumentListPage = lazyPage(() => import('./pages/DocumentListPage'), 'DocumentListPage');
const ExportPage = lazyPage(() => import('./pages/ExportPage'), 'ExportPage');
const MonitoringPage = lazyPage(() => import('./pages/MonitoringPage'), 'MonitoringPage');
const PermissionsPage = lazyPage(() => import('./pages/PermissionsPage'), 'PermissionsPage');
const RackPage = lazyPage(() => import('./pages/RackPage'), 'RackPage');
const TopologyPage = lazyPage(() => import('./pages/TopologyPage'), 'TopologyPage');
const SLAPage = lazyPage(() => import('./pages/SLAPage'), 'SLAPage');
const FormsPage = lazyPage(() => import('./pages/FormsPage'), 'FormsPage');
const WorkflowPage = lazyPage(() => import('./pages/WorkflowPage'), 'WorkflowPage');
const CompliancePage = lazyPage(() => import('./pages/CompliancePage'), 'CompliancePage');
const IGAPage = lazyPage(() => import('./pages/IGAPage'), 'IGAPage');
const StocktakeListPage = lazyPage(() => import('./pages/StocktakeListPage'), 'StocktakeListPage');
const TicketListPage = lazyPage(() => import('./pages/TicketListPage'), 'TicketListPage');
const UserManagementPage = lazyPage(
  () => import('./pages/UserManagementPage'),
  'UserManagementPage',
);
const WebhooksPage = lazyPage(() => import('./pages/WebhooksPage'), 'WebhooksPage');
const AuditPage = lazyPage(() => import('./pages/AuditPage'), 'AuditPage');
const SecurityPage = lazyPage(() => import('./pages/SecurityPage'), 'SecurityPage');
const ConsumableListPage = lazyPage(
  () => import('./pages/ConsumableListPage'),
  'ConsumableListPage',
);
const OrderListPage = lazyPage(() => import('./pages/OrderListPage'), 'OrderListPage');
const MaintenancePage = lazyPage(() => import('./pages/MaintenancePage'), 'MaintenancePage');
const DisposalListPage = lazyPage(() => import('./pages/DisposalListPage'), 'DisposalListPage');
const CITypeAdminPage = lazyPage(() => import('./pages/CITypeAdminPage'), 'CITypeAdminPage');
const LocationTreePage = lazyPage(() => import('./pages/LocationTreePage'), 'LocationTreePage');
const InventoryPage = lazyPage(() => import('./pages/InventoryPage'), 'InventoryPage');
const SavedViewsPage = lazyPage(() => import('./pages/SavedViewsPage'), 'SavedViewsPage');
const KeyListPage = lazyPage(() => import('./pages/KeyListPage'), 'KeyListPage');
const TrainingListPage = lazyPage(() => import('./pages/TrainingListPage'), 'TrainingListPage');
const DeskListPage = lazyPage(() => import('./pages/DeskListPage'), 'DeskListPage');
const AgentListPage = lazyPage(() => import('./pages/AgentListPage'), 'AgentListPage');
const FindingsPage = lazyPage(() => import('./pages/FindingsPage'), 'FindingsPage');
const MapPage = lazyPage(() => import('./pages/MapPage'), 'MapPage');
const RoomPlanPage = lazyPage(() => import('./pages/RoomPlanPage'), 'RoomPlanPage');

// Full-screen fallback while a page chunk loads.
function PageFallback() {
  return (
    <div className="space-y-3 p-6" aria-busy="true">
      <Skeleton className="h-8 w-1/3" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-2/3" />
    </div>
  );
}

const pathToPage: Record<string, AppPage> = Object.fromEntries(
  Object.entries(pageToPath).map(([k, v]) => [v, k as AppPage]),
);

function App() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const [isCreateOpen, setIsCreateOpen] = useState(false);
  const mode = useThemeStore((state) => state.mode);
  const setMode = useThemeStore((state) => state.setMode);
  const user = useAuthStore((state) => state.user);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated());

  const currentPage: AppPage =
    pathToPage[location.pathname] ||
    (Object.entries(pageToPath).find(
      ([, path]) => path !== '/' && location.pathname.startsWith(`${path}/`),
    )?.[0] as AppPage) ||
    'cmdb';

  const shortcutHint = useMemo(() => {
    if (typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)) {
      return '⌘K';
    }
    return 'Ctrl+K';
  }, []);

  const isDark =
    mode === 'dark' ||
    (mode === 'system' &&
      typeof document !== 'undefined' &&
      document.documentElement.classList.contains('dark'));

  function toggleDarkMode() {
    setMode(isDark ? 'light' : 'dark');
  }

  function handleNavigate(page: AppPage) {
    navigate(pageToPath[page] || '/cmdb');
  }

  // Entitlement-gated navigation: modules the tenant's plan does not include
  // are hidden instead of producing a 403 on click (progressive disclosure).
  const { isEnabled } = useEntitlements();
  const navItems: { page: AppPage; label: string }[] = (
    [
      'dashboard',
      'cmdb',
      'topology',
      'map',
      'roomplan',
      'racks',
      'assets',
      'inventory',
      'locations',
      'saved-views',
      'tickets',
      'assignments',
      'documents',
      'stocktake',
      'consumables',
      'orders',
      'maintenance',
      'disposal',
      'keys',
      'trainings',
      'desks',
      'agents',
      'findings',
      'discovery',
      'users',
      'permissions',
      'ci-types',
      'slas',
      'forms',
      'workflows',
      'compliance',
      'iga',
      'assistant',
      'webhooks',
      'export',
      'monitoring',
      'audit',
      'security',
    ] as AppPage[]
  )
    .filter((page) => isEnabled(navFeatureFor[page]))
    .map((page) => ({ page, label: t(`nav.${page}`, page) }));

  function openCreateCI() {
    navigate('/cmdb');
    setIsCreateOpen(true);
  }

  async function handleLogout() {
    await logout();

    try {
      const config = getStoredAuthConfig() ?? (await fetchAuthConfig());
      if (config.end_session_endpoint) {
        window.location.assign(config.end_session_endpoint);
        return;
      }
    } catch {
      // Fall back to local logout below when OIDC configuration is unreachable.
    }

    navigate('/login', { replace: true });
  }

  return (
    <div className="min-h-screen bg-surface text-gray-900 transition-colors dark:text-gray-100">
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-lg focus:bg-primary focus:px-4 focus:py-2 focus:text-white"
      >
        {t('accessibility.skipToContent')}
      </a>
      <header className="border-b border-gray-200 bg-white/90 px-6 py-4 backdrop-blur dark:border-gray-800 dark:bg-gray-950/90">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h1 className="text-xl font-semibold">{t('app.title')}</h1>
          </div>
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
            {isAuthenticated ? (
              <nav
                className="flex flex-wrap gap-2"
                aria-label={t('accessibility.primaryNavigation')}
              >
                {navItems.map(({ page, label }) => (
                  <NavButton
                    key={page}
                    active={currentPage === page}
                    onClick={() => handleNavigate(page)}
                  >
                    {label}
                  </NavButton>
                ))}
              </nav>
            ) : null}
            <div className="flex items-center gap-2 text-sm text-gray-500 dark:text-gray-400">
              {isAuthenticated ? (
                <span className="max-w-48 truncate">{user?.name || user?.email}</span>
              ) : null}
              <span>{shortcutHint}</span>
              <Button
                variant="secondary"
                size="sm"
                onClick={toggleDarkMode}
                aria-label={t('accessibility.toggleDarkMode')}
              >
                {isDark ? (
                  <svg
                    viewBox="0 0 24 24"
                    className="h-4 w-4"
                    aria-hidden="true"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                  >
                    <circle cx="12" cy="12" r="4" />
                    <path d="M12 2v2" />
                    <path d="M12 20v2" />
                    <path d="m4.93 4.93 1.41 1.41" />
                    <path d="m17.66 17.66 1.41 1.41" />
                    <path d="M2 12h2" />
                    <path d="M20 12h2" />
                    <path d="m6.34 17.66-1.41 1.41" />
                    <path d="m19.07 4.93-1.41 1.41" />
                  </svg>
                ) : (
                  <svg
                    viewBox="0 0 24 24"
                    className="h-4 w-4"
                    aria-hidden="true"
                    fill="currentColor"
                  >
                    <path d="M21 12.79A9 9 0 0 1 11.21 3c0-.34.02-.67.05-1A10 10 0 1 0 22 12c0-.05 0-.11-.01-.16-.3.63-.64.95-.99.95Z" />
                  </svg>
                )}
              </Button>
              {isAuthenticated ? (
                <Button variant="ghost" size="sm" onClick={handleLogout}>
                  {t('auth.logout')}
                </Button>
              ) : null}
            </div>
          </div>
        </div>
      </header>
      <main id="main-content" className="p-6">
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/auth/callback" element={<CallbackPage />} />
          <Route element={<RequireAuth />}>
            <Route
              element={
                <Suspense fallback={<PageFallback />}>
                  <Outlet />
                </Suspense>
              }
            >
              <Route path="/" element={<Navigate to="/dashboard" replace />} />
              <Route path="/dashboard" element={<DashboardPage />} />
              <Route path="/cmdb" element={<CIListPage onCreateCI={openCreateCI} />} />
              <Route path="/cmdb/:id" element={<CIDetailPage />} />
              <Route path="/topology" element={<TopologyPage />} />
              <Route path="/map" element={<MapPage />} />
              <Route path="/roomplan" element={<RoomPlanPage />} />
              <Route path="/racks" element={<RackPage />} />
              <Route path="/discovery" element={<DiscoveryPage />} />
              <Route path="/assets" element={<AssetListPage />} />
              <Route path="/inventory" element={<InventoryPage />} />
              <Route path="/locations" element={<LocationTreePage />} />
              <Route path="/saved-views" element={<SavedViewsPage />} />
              <Route path="/ci-types" element={<CITypeAdminPage />} />
              <Route path="/assignments" element={<AssignmentListPage />} />
              <Route path="/documents" element={<DocumentListPage />} />
              <Route path="/stocktake" element={<StocktakeListPage />} />
              <Route path="/consumables" element={<ConsumableListPage />} />
              <Route path="/orders" element={<OrderListPage />} />
              <Route path="/maintenance" element={<MaintenancePage />} />
              <Route path="/disposal" element={<DisposalListPage />} />
              <Route path="/keys" element={<KeyListPage />} />
              <Route path="/trainings" element={<TrainingListPage />} />
              <Route path="/desks" element={<DeskListPage />} />
              <Route path="/agents" element={<AgentListPage />} />
              <Route path="/findings" element={<FindingsPage />} />
              <Route path="/tickets" element={<TicketListPage />} />
              <Route path="/users" element={<UserManagementPage />} />
              <Route path="/permissions" element={<PermissionsPage />} />
              <Route path="/slas" element={<SLAPage />} />
              <Route path="/forms" element={<FormsPage />} />
              <Route path="/workflows" element={<WorkflowPage />} />
              <Route path="/compliance" element={<CompliancePage />} />
              <Route path="/iga" element={<IGAPage />} />
              <Route path="/assistant" element={<AssistantPage />} />
              <Route path="/webhooks" element={<WebhooksPage />} />
              <Route path="/export" element={<ExportPage />} />
              <Route path="/monitoring" element={<MonitoringPage />} />
              <Route path="/audit" element={<AuditPage />} />
              <Route path="/security" element={<SecurityPage />} />
              <Route path="*" element={<CIListPage onCreateCI={openCreateCI} />} />
            </Route>
          </Route>
        </Routes>
      </main>

      {isAuthenticated ? (
        <CommandPalette
          onNavigate={handleNavigate}
          onCreateCI={openCreateCI}
          onToggleDarkMode={toggleDarkMode}
          isFeatureEnabled={isEnabled}
        />
      ) : null}
      {isAuthenticated ? (
        <Suspense fallback={null}>
          <CIFormModal open={isCreateOpen} onOpenChange={setIsCreateOpen} />
        </Suspense>
      ) : null}
      <ToastViewport />
    </div>
  );
}

function RequireAuth() {
  const location = useLocation();
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated());
  const hasSession = useAuthStore((state) => Boolean(state.token));

  if (!isAuthenticated) {
    const from = `${location.pathname}${location.search}${location.hash}`;
    return <Navigate to="/login" replace state={{ from, expired: hasSession }} />;
  }

  return <Outlet />;
}

function NavButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <Button variant={active ? 'primary' : 'ghost'} size="sm" onClick={onClick}>
      {children}
    </Button>
  );
}

export default App;
