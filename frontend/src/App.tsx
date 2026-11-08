import { Suspense, lazy, useEffect, useMemo, useState } from 'react';
import type { ComponentProps } from 'react';
import { Routes, Route, useNavigate, useLocation, Navigate, Outlet } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { CommandPalette } from './components/CommandPalette';
import { EntitlementExpiryBanner } from './components/EntitlementExpiryBanner';
import { DraftBanner } from './components/metamodel/DraftBanner';
import { pageToPath } from './components/CommandPalette';
import type { AppPage } from './components/CommandPalette';
import { AppShell, PublicShell } from './components/shell/AppShell';
import { NAV_GROUPS } from './components/shell/navigation';
import { Skeleton } from './components/ui/Skeleton';
import { ToastViewport } from './components/ui/Toast';
import { useAuthStore } from './auth/authStore';
import { refreshSession } from './auth/session';
import { signOut } from './auth/signOut';
import { LoginPage } from './pages/auth/LoginPage';
import { CallbackPage } from './pages/auth/CallbackPage';
import {
  hasPermission,
  navFeatureFor,
  navPermissionFor,
  useEntitlements,
  useModules,
} from './hooks/useEntitlements';
import { useEffectivePermissions, useHasPermission } from './api/hooks';
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
const DocumentDetailPage = lazyPage(
  () => import('./pages/DocumentDetailPage'),
  'DocumentDetailPage',
);
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
const RelationshipTypesPage = lazyPage(
  () => import('./pages/RelationshipTypesPage'),
  'RelationshipTypesPage',
);
const TemplatesPage = lazyPage(() => import('./pages/TemplatesPage'), 'TemplatesPage');
const MetamodelDraftsPage = lazyPage(
  () => import('./pages/MetamodelDraftsPage'),
  'MetamodelDraftsPage',
);
const LocationTreePage = lazyPage(() => import('./pages/LocationTreePage'), 'LocationTreePage');
const InventoryPage = lazyPage(() => import('./pages/InventoryPage'), 'InventoryPage');
const SavedViewsPage = lazyPage(() => import('./pages/SavedViewsPage'), 'SavedViewsPage');
const TagsPage = lazyPage(() => import('./pages/TagsPage'), 'TagsPage');
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
  const mode = useThemeStore((state) => state.mode);
  const resolvedTheme = useThemeStore((state) => state.resolved);
  const osTheme = useThemeStore((state) => state.os);
  const setMode = useThemeStore((state) => state.setMode);
  const user = useAuthStore((state) => state.user);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated());
  // Both read only with a session (their queries are enabled by it).
  const { isEnabled } = useEntitlements();
  const { isPageEnabled } = useModules();

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
          <Route path="/map" element={<MapPage />} />
          <Route path="/roomplan" element={<RoomPlanPage />} />
          <Route path="/racks" element={<RackPage />} />
          <Route path="/discovery" element={<DiscoveryPage />} />
          <Route path="/assets" element={<AssetListPage />} />
          <Route path="/inventory" element={<InventoryPage />} />
          <Route path="/locations" element={<LocationTreePage />} />
          <Route path="/saved-views" element={<SavedViewsPage />} />
          <Route path="/tags" element={<TagsPage />} />
          <Route path="/ci-types" element={<CITypeAdminPage />} />
          <Route path="/ci-types/templates" element={<TemplatesPage />} />
          <Route path="/ci-types/templates/:id" element={<TemplatesPage />} />
          <Route path="/ci-types/drafts" element={<MetamodelDraftsPage />} />
          <Route path="/ci-types/drafts/:id" element={<MetamodelDraftsPage />} />
          <Route path="/relationship-types" element={<RelationshipTypesPage />} />
          <Route path="/assignments" element={<AssignmentListPage />} />
          <Route path="/documents" element={<DocumentListPage />} />
          <Route path="/documents/:id" element={<DocumentDetailPage />} />
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
          <Route path="/reviews" element={<ReviewInboxPage />} />
          <Route path="/jobs" element={<JobCenterPage />} />
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
      />
      <Suspense fallback={null}>
        <CIFormModal open={isCreateOpen} onOpenChange={setIsCreateOpen} />
      </Suspense>
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
  // Entitlement-gated navigation: modules the tenant's plan does not
  // include are hidden instead of producing a 403 on click (progressive
  // disclosure). Modules of later phases are switched off in the G1 release
  // (WP-214): their pages are hidden whatever the plan includes. Pages that
  // need a permission (navPermissionFor) are filtered as well (UI-12,
  // WP-804), so the palette and the sidebar offer the same pages.
  const { isEnabled } = useEntitlements();
  const { isPageEnabled } = useModules();
  const isPermitted = usePermittedPage();
  const visiblePages = useMemo(() => {
    const pages = new Set<AppPage>();
    for (const group of NAV_GROUPS) {
      for (const { page } of group.items) {
        if (isEnabled(navFeatureFor[page]) && isPageEnabled(page) && isPermitted(page)) {
          pages.add(page);
        }
      }
    }
    return pages;
  }, [isEnabled, isPageEnabled, isPermitted]);

  // The inbox shows its open items beside the navigation entry. Without
  // discovery:read the request is not made (like the dashboard widget).
  const canReadReviews = useHasPermission('discovery:read');
  const openReviews = useOpenReviewCount(canReadReviews === true && visiblePages.has('reviews'));
  const counts = { reviews: openReviews.data };

  return (
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
      {children}
    </AppShell>
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
 * usePermittedPage tells whether the signed-in user holds the permission a
 * page requires (navPermissionFor, UI-12, WP-804), read from the effective
 * rights of GET /me/permissions (with the rights of the session's IdP
 * groups since WP-428). While the rights load or cannot be read the page
 * counts as not permitted (fail closed like useHasPermission). Only signed-in
 * parts of the shell call it, so the sign-in page sends no request.
 */
function usePermittedPage(): (page: AppPage) => boolean {
  const permissions = useEffectivePermissions().data?.permissions;
  return (page) => {
    const required = navPermissionFor[page];
    return !required || hasPermission(permissions, required);
  };
}

/**
 * PermittedCommandPalette passes the command palette a page predicate that
 * combines the release modules with the permission of navPermissionFor, so
 * the palette offers the same pages as the navigation.
 */
function PermittedCommandPalette({
  isPageEnabled = () => true,
  ...props
}: ComponentProps<typeof CommandPalette>) {
  const isPermitted = usePermittedPage();
  return (
    <CommandPalette {...props} isPageEnabled={(page) => isPageEnabled(page) && isPermitted(page)} />
  );
}

export default App;
