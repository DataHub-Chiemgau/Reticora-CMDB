import { Suspense, lazy, useEffect, useMemo, useState } from 'react';
import type { ComponentProps } from 'react';
import { Routes, Route, useNavigate, useLocation, Navigate, Outlet } from 'react-router';
import { useTranslation } from 'react-i18next';
import { CommandPalette } from './components/CommandPalette';
import { EntitlementExpiryBanner } from './components/EntitlementExpiryBanner';
import { DraftBanner } from './components/metamodel/DraftBanner';
import { pageToPath } from './components/CommandPalette';
import type { AppPage } from './components/CommandPalette';
import { AppShell, PublicShell } from './components/shell/AppShell';
import { NavLockContext } from './components/shell/navigation';
import type { NavLock } from './components/shell/navigation';
import { pageOfPath } from './components/shell/SidebarNav';
import { Skeleton } from './components/ui/Skeleton';
import { ToastViewport } from './components/ui/Toast';
import { useAuthStore } from './auth/authStore';
import { refreshSession } from './auth/session';
import { signOut } from './auth/signOut';
import { LoginPage } from './pages/auth/LoginPage';
import { CallbackPage } from './pages/auth/CallbackPage';
import { navFeatureFor, useEntitlements, useModules, useNavAccess } from './hooks/useEntitlements';
import { useHasPermission } from './api/hooks';
import { useOpenReviewCount } from './hooks/useReviews';
import { nextThemeMode, useThemeStore } from './stores/theme';
import type { ThemeMode } from './stores/theme';

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
const AssetDetailPage = lazyPage(() => import('./pages/AssetDetailPage'), 'AssetDetailPage');
const AssignmentListPage = lazyPage(
  () => import('./pages/AssignmentListPage'),
  'AssignmentListPage',
);
const AssistantPage = lazyPage(() => import('./pages/AssistantPage'), 'AssistantPage');
const CIFormModal = lazyPage(() => import('./pages/CIFormModal'), 'CIFormModal');
const ScanDialog = lazyPage(() => import('./components/flows/ScanDialog'), 'ScanDialog');
const CIDetailPage = lazyPage(() => import('./pages/CIDetailPage'), 'CIDetailPage');
const CIListPage = lazyPage(() => import('./pages/CIListPage'), 'CIListPage');
const DashboardPage = lazyPage(() => import('./pages/DashboardPage'), 'DashboardPage');
const DiscoveryPage = lazyPage(() => import('./pages/DiscoveryPage'), 'DiscoveryPage');
const DocumentListPage = lazyPage(() => import('./pages/DocumentListPage'), 'DocumentListPage');
const DocumentDetailPage = lazyPage(
  () => import('./pages/DocumentDetailPage'),
  'DocumentDetailPage',
);
const ExportPage = lazyPage(() => import('./pages/ExportPage'), 'ExportPage');
const ImportPage = lazyPage(() => import('./pages/ImportPage'), 'ImportPage');
const MonitoringPage = lazyPage(() => import('./pages/MonitoringPage'), 'MonitoringPage');
const AlarmsPage = lazyPage(() => import('./pages/AlarmsPage'), 'AlarmsPage');
const IncidentsPage = lazyPage(() => import('./pages/IncidentsPage'), 'IncidentsPage');
const PermissionsPage = lazyPage(() => import('./pages/PermissionsPage'), 'PermissionsPage');
const RackPage = lazyPage(() => import('./pages/RackPage'), 'RackPage');
const TopologyPage = lazyPage(() => import('./pages/TopologyPage'), 'TopologyPage');
const IpamPage = lazyPage(() => import('./pages/IpamPage'), 'IpamPage');
const SubnetDetailPage = lazyPage(() => import('./pages/SubnetDetailPage'), 'SubnetDetailPage');
const SLAPage = lazyPage(() => import('./pages/SLAPage'), 'SLAPage');
const FormsPage = lazyPage(() => import('./pages/FormsPage'), 'FormsPage');
const WorkflowPage = lazyPage(() => import('./pages/WorkflowPage'), 'WorkflowPage');
const CompliancePage = lazyPage(() => import('./pages/CompliancePage'), 'CompliancePage');
const IGAPage = lazyPage(() => import('./pages/IGAPage'), 'IGAPage');
const StocktakeListPage = lazyPage(() => import('./pages/StocktakeListPage'), 'StocktakeListPage');
const StocktakeDetailPage = lazyPage(
  () => import('./pages/StocktakeDetailPage'),
  'StocktakeDetailPage',
);
const TicketListPage = lazyPage(() => import('./pages/TicketListPage'), 'TicketListPage');
const TicketDetailPage = lazyPage(() => import('./pages/TicketDetailPage'), 'TicketDetailPage');
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
const ConsumableDetailPage = lazyPage(
  () => import('./pages/ConsumableDetailPage'),
  'ConsumableDetailPage',
);
const OrderListPage = lazyPage(() => import('./pages/OrderListPage'), 'OrderListPage');
const MaintenancePage = lazyPage(() => import('./pages/MaintenancePage'), 'MaintenancePage');
const DisposalListPage = lazyPage(() => import('./pages/DisposalListPage'), 'DisposalListPage');
const CITypeAdminPage = lazyPage(() => import('./pages/CITypeAdminPage'), 'CITypeAdminPage');
const RelationshipTypesPage = lazyPage(
  () => import('./pages/RelationshipTypesPage'),
  'RelationshipTypesPage',
);
const TemplatesPage = lazyPage(() => import('./pages/TemplatesPage'), 'TemplatesPage');
const RulesPage = lazyPage(() => import('./pages/RulesPage'), 'RulesPage');
const SourceRanksPage = lazyPage(() => import('./pages/SourceRanksPage'), 'SourceRanksPage');
const FormBuilderPage = lazyPage(() => import('./pages/FormBuilderPage'), 'FormBuilderPage');
const MetamodelDraftsPage = lazyPage(
  () => import('./pages/MetamodelDraftsPage'),
  'MetamodelDraftsPage',
);
const LocationTreePage = lazyPage(() => import('./pages/LocationTreePage'), 'LocationTreePage');
const ClientsPage = lazyPage(() => import('./pages/ClientsPage'), 'ClientsPage');
const OperatorApp = lazyPage(() => import('./operator/OperatorApp'), 'OperatorApp');
const PlanPage = lazyPage(() => import('./pages/PlanPage'), 'PlanPage');
const InventoryPage = lazyPage(() => import('./pages/InventoryPage'), 'InventoryPage');
const SavedViewsPage = lazyPage(() => import('./pages/SavedViewsPage'), 'SavedViewsPage');
const TagsPage = lazyPage(() => import('./pages/TagsPage'), 'TagsPage');
const ContractsPage = lazyPage(() => import('./pages/ContractsPage'), 'ContractsPage');
const SuppliersPage = lazyPage(() => import('./pages/SuppliersPage'), 'SuppliersPage');
const KeyListPage = lazyPage(() => import('./pages/KeyListPage'), 'KeyListPage');
const TrainingListPage = lazyPage(() => import('./pages/TrainingListPage'), 'TrainingListPage');
const DeskListPage = lazyPage(() => import('./pages/DeskListPage'), 'DeskListPage');
const AgentListPage = lazyPage(() => import('./pages/AgentListPage'), 'AgentListPage');
const FindingsPage = lazyPage(() => import('./pages/FindingsPage'), 'FindingsPage');
const ReviewInboxPage = lazyPage(() => import('./pages/ReviewInboxPage'), 'ReviewInboxPage');
const JobCenterPage = lazyPage(() => import('./pages/JobCenterPage'), 'JobCenterPage');
const OnboardingPage = lazyPage(() => import('./pages/OnboardingPage'), 'OnboardingPage');
const InvitationAcceptPage = lazyPage(
  () => import('./pages/InvitationAcceptPage'),
  'InvitationAcceptPage',
);
const MapPage = lazyPage(() => import('./pages/MapPage'), 'MapPage');
const RoomPlanPage = lazyPage(() => import('./pages/RoomPlanPage'), 'RoomPlanPage');
const FeatureLockedPage = lazyPage(() => import('./pages/FeatureLockedPage'), 'FeatureLockedPage');

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

function App() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [isCreateOpen, setIsCreateOpen] = useState(false);
  const [isScanOpen, setIsScanOpen] = useState(false);
  // The scan dialog loads with its first use and stays mounted afterwards:
  // a handover it started waits out its undo window there.
  const [scanLoaded, setScanLoaded] = useState(false);
  const mode = useThemeStore((state) => state.mode);
  const resolvedTheme = useThemeStore((state) => state.resolved);
  const osTheme = useThemeStore((state) => state.os);
  const setMode = useThemeStore((state) => state.setMode);
  const user = useAuthStore((state) => state.user);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated());
  // Both read only with a session (their queries are enabled by it).
  const { isEnabled } = useEntitlements();
  const { isPageEnabled, isModuleEnabled } = useModules();
  // Scanning needs the feature barcode and the module inventory that serves
  // /api/v1/labels (E-44, E-105, WP-807); without them the palette has no
  // "Scan" command.
  const canScan = isEnabled('barcode') && isModuleEnabled('inventory');

  // The theme toggle (header and command palette) cycles relative to the OS
  // theme: system -> opposite of the OS -> OS theme -> system, so every click
  // away from 'system' changes the page (nextThemeMode). Its label names the
  // current mode, in 'system' mode with the theme in effect, and the mode the
  // next click selects.
  const themeLabels: Record<ThemeMode, string> = {
    light: t('theme.light'),
    dark: t('theme.dark'),
    system: t('theme.system'),
  };
  const themeToggleLabel = t('theme.toggle', {
    current:
      mode === 'system'
        ? t('theme.systemResolved', { theme: themeLabels[resolvedTheme] })
        : themeLabels[mode],
    next: themeLabels[nextThemeMode(mode, osTheme)],
  });
  const theme = { mode, label: themeToggleLabel, onToggle: cycleTheme };

  function cycleTheme() {
    setMode(nextThemeMode(mode, osTheme));
  }

  function handleNavigate(page: AppPage) {
    navigate(pageToPath[page] || '/cmdb');
  }

  function openCreateCI() {
    navigate('/cmdb');
    setIsCreateOpen(true);
  }

  function openScan() {
    setScanLoaded(true);
    setIsScanOpen(true);
  }

  // handleLogout ends the Reticora and the IdP session; the IdP returns to
  // the sign-in page. Without an end-session endpoint the sign-in is shown
  // right away.
  async function handleLogout() {
    if ((await signOut()) === 'local') {
      navigate('/login', { replace: true });
    }
  }

  const routes = (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/auth/callback" element={<CallbackPage />} />
      {/* The operator area has its own session, outside the tenant login (WP-887). */}
      <Route
        path="/operator/*"
        element={
          <Suspense fallback={<PageFallback />}>
            <OperatorApp />
          </Suspense>
        }
      />
      {/* Signup and verification run without a session (UI-17). */}
      <Route
        path="/onboarding"
        element={
          <Suspense fallback={<PageFallback />}>
            <OnboardingPage />
          </Suspense>
        }
      />
      {/* The invitation link opens the acceptance without a session (TLC-03). */}
      <Route
        path="/invitations/accept"
        element={
          <Suspense fallback={<PageFallback />}>
            <InvitationAcceptPage />
          </Suspense>
        }
      />
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
          <Route path="/ipam" element={<IpamPage />} />
          <Route path="/ipam/subnets/:id" element={<SubnetDetailPage />} />
          <Route path="/map" element={<MapPage />} />
          <Route path="/roomplan" element={<RoomPlanPage />} />
          <Route path="/racks" element={<RackPage />} />
          <Route path="/discovery" element={<DiscoveryPage />} />
          <Route path="/assets" element={<AssetListPage />} />
          <Route path="/assets/:id" element={<AssetDetailPage />} />
          <Route path="/inventory" element={<InventoryPage />} />
          <Route path="/locations" element={<LocationTreePage />} />
          <Route path="/clients" element={<ClientsPage />} />
          <Route path="/plan" element={<PlanPage />} />
          <Route path="/saved-views" element={<SavedViewsPage />} />
          <Route path="/tags" element={<TagsPage />} />
          <Route path="/ci-types" element={<CITypeAdminPage />} />
          <Route path="/ci-types/templates" element={<TemplatesPage />} />
          <Route path="/ci-types/templates/:id" element={<TemplatesPage />} />
          <Route path="/ci-types/drafts" element={<MetamodelDraftsPage />} />
          <Route path="/ci-types/drafts/:id" element={<MetamodelDraftsPage />} />
          <Route path="/ci-types/:id/form-builder" element={<FormBuilderPage />} />
          <Route path="/rules" element={<RulesPage />} />
          <Route path="/source-ranks" element={<SourceRanksPage />} />
          <Route path="/relationship-types" element={<RelationshipTypesPage />} />
          <Route path="/assignments" element={<AssignmentListPage />} />
          <Route path="/contracts" element={<ContractsPage />} />
          <Route path="/contracts/:id" element={<ContractsPage />} />
          <Route path="/suppliers" element={<SuppliersPage />} />
          <Route path="/documents" element={<DocumentListPage />} />
          <Route path="/documents/:id" element={<DocumentDetailPage />} />
          <Route path="/stocktake" element={<StocktakeListPage />} />
          <Route path="/stocktake/:id" element={<StocktakeDetailPage />} />
          <Route path="/consumables" element={<ConsumableListPage />} />
          <Route path="/consumables/:id" element={<ConsumableDetailPage />} />
          <Route path="/orders" element={<OrderListPage />} />
          <Route path="/maintenance" element={<MaintenancePage />} />
          <Route path="/disposal" element={<DisposalListPage />} />
          <Route path="/keys" element={<KeyListPage />} />
          <Route path="/trainings" element={<TrainingListPage />} />
          <Route path="/desks" element={<DeskListPage />} />
          <Route path="/agents" element={<AgentListPage />} />
          <Route path="/findings" element={<FindingsPage />} />
          <Route path="/reviews" element={<ReviewInboxPage />} />
          <Route path="/jobs" element={<JobCenterPage />} />
          <Route path="/tickets" element={<TicketListPage />} />
          <Route path="/tickets/:id" element={<TicketDetailPage />} />
          <Route path="/users" element={<UserManagementPage />} />
          <Route path="/permissions" element={<PermissionsPage />} />
          <Route path="/slas" element={<SLAPage />} />
          <Route path="/forms" element={<FormsPage />} />
          <Route path="/workflows" element={<WorkflowPage />} />
          <Route path="/compliance" element={<CompliancePage />} />
          <Route path="/iga" element={<IGAPage />} />
          <Route path="/assistant" element={<AssistantPage />} />
          <Route path="/webhooks" element={<WebhooksPage />} />
          <Route path="/imports" element={<ImportPage />} />
          <Route path="/export" element={<ExportPage />} />
          <Route path="/monitoring" element={<MonitoringPage />} />
          <Route path="/alarms" element={<AlarmsPage />} />
          <Route path="/incidents" element={<IncidentsPage />} />
          <Route path="/audit" element={<AuditPage />} />
          <Route path="/security" element={<SecurityPage />} />
          <Route path="*" element={<CIListPage onCreateCI={openCreateCI} />} />
        </Route>
      </Route>
    </Routes>
  );

  if (!isAuthenticated) {
    return (
      <>
        <PublicShell theme={theme}>{routes}</PublicShell>
        <ToastViewport />
      </>
    );
  }

  return (
    <>
      <SignedInShell
        theme={theme}
        user={{ name: user?.name, email: user?.email }}
        onSignOut={() => void handleLogout()}
      >
        {routes}
      </SignedInShell>
      <PermittedCommandPalette
        onNavigate={handleNavigate}
        onCreateCI={openCreateCI}
        onToggleDarkMode={cycleTheme}
        themeToggleLabel={themeToggleLabel}
        isFeatureEnabled={isEnabled}
        isPageEnabled={isPageEnabled}
        onScan={canScan ? openScan : undefined}
      />
      <Suspense fallback={null}>
        <CIFormModal open={isCreateOpen} onOpenChange={setIsCreateOpen} />
      </Suspense>
      {scanLoaded ? (
        <Suspense fallback={null}>
          <ScanDialog open={isScanOpen} onOpenChange={setIsScanOpen} />
        </Suspense>
      ) : null}
      <ToastViewport />
    </>
  );
}

/**
 * SignedInShell is the frame of the signed-in pages. It lives apart from App
 * so that the requests it makes (entitlements, release modules, rights, open
 * reviews) happen only with a session: the sign-in page sends none.
 */
function SignedInShell({
  children,
  theme,
  user,
  onSignOut,
}: {
  children: React.ReactNode;
  theme: ComponentProps<typeof AppShell>['theme'];
  user: { name?: string; email?: string };
  onSignOut: () => void;
}) {
  // One visibility rule for the sidebar, the palette and the dashboard
  // (navPageState, WP-857): modules of later phases switched off in the
  // release (WP-214) and pages without the user's permission
  // (navPermissionFor, UI-12, WP-804) are hidden; pages the tenant's plan
  // does not include are shown locked and lead to a hint page instead of a
  // page full of 403 errors.
  const { states, plan, isLoading: entitlementsLoading } = useNavAccess();
  const { visiblePages, lock } = useMemo(() => {
    const visible = new Set<AppPage>();
    const locked = new Set<AppPage>();
    for (const [page, state] of states) {
      if (state === 'visible') visible.add(page);
      if (state === 'locked') locked.add(page);
    }
    return { visiblePages: visible, lock: { pages: locked, plan } satisfies NavLock };
  }, [states, plan]);

  // The route of a locked page shows the hint page; while the plan loads a
  // gated page waits instead of sending requests the server would refuse.
  const { pathname } = useLocation();
  const activePage = pageOfPath(pathname);
  let content = children;
  if (activePage && lock.pages.has(activePage)) {
    content = (
      <Suspense fallback={<PageFallback />}>
        <FeatureLockedPage page={activePage} plan={plan} />
      </Suspense>
    );
  } else if (activePage && navFeatureFor[activePage] && entitlementsLoading) {
    content = <PageFallback />;
  }

  // The inbox shows its open items beside the navigation entry. Without
  // discovery:read the request is not made (like the dashboard widget).
  const canReadReviews = useHasPermission('discovery:read');
  const openReviews = useOpenReviewCount(canReadReviews === true && visiblePages.has('reviews'));
  const counts = { reviews: openReviews.data };

  return (
    <NavLockContext.Provider value={lock}>
      <AppShell
        theme={theme}
        user={user}
        onSignOut={onSignOut}
        visiblePages={visiblePages}
        counts={counts}
        banners={
          <>
            {/* Expiring entitlements (ENT-07, WP-464). */}
            <EntitlementExpiryBanner />
            {/* The active metamodel draft above every page (MET-53, WP-798). */}
            <DraftBanner />
          </>
        }
      >
        {content}
      </AppShell>
    </NavLockContext.Provider>
  );
}

// RequireAuth guards the signed-in pages. An expired access token of a stored
// user (reload, navigation, new start after 15 minutes) is renewed with the
// refresh cookie before the user is sent to /login (AUT-02, F-066).
function RequireAuth() {
  const location = useLocation();
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated());
  const hasSession = useAuthStore((state) => Boolean(state.token));
  const hasUser = useAuthStore((state) => Boolean(state.user));
  const [renewalFailed, setRenewalFailed] = useState(false);
  const renewing = !isAuthenticated && hasUser && !renewalFailed;

  useEffect(() => {
    if (!renewing) {
      return;
    }
    let active = true;
    void refreshSession()
      .catch(() => false)
      .then((renewed) => {
        // A renewed token the clock still sees as expired (clock skew) must
        // not keep the user waiting.
        if (active && !(renewed && useAuthStore.getState().isAuthenticated())) {
          setRenewalFailed(true);
        }
      });
    return () => {
      active = false;
    };
  }, [renewing]);

  if (renewing) {
    return <PageFallback />;
  }

  if (!isAuthenticated) {
    const from = `${location.pathname}${location.search}${location.hash}`;
    return <Navigate to="/login" replace state={{ from, expired: hasSession }} />;
  }

  return <Outlet />;
}

/**
 * PermittedCommandPalette offers the pages the navigation shows (the rule
 * of navPageState, WP-857): neither hidden nor locked pages. Only signed-in
 * parts of the shell render it, so the sign-in page sends no request.
 */
function PermittedCommandPalette({
  isPageEnabled = () => true,
  ...props
}: ComponentProps<typeof CommandPalette>) {
  const { stateOf } = useNavAccess();
  return (
    <CommandPalette
      {...props}
      isPageEnabled={(page) => isPageEnabled(page) && stateOf(page) === 'visible'}
    />
  );
}

export default App;
