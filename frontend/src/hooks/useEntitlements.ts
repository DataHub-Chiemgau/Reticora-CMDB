import { useQuery } from '@tanstack/react-query';
import { useMemo } from 'react';
import { ApiError, entitlementApi, moduleApi } from '../api/client';
import { useEffectivePermissions } from '../api/hooks';
import { useAuthStore } from '../auth/authStore';
import { navPageStates } from '../components/shell/navigation';
import type { NavPageState } from '../components/shell/navigation';
import type { AppPage } from '../components/CommandPalette';

/**
 * Map of feature_key -> enabled for the current tenant. When the tenant has no
 * explicit rows the server applies the default plan (essential), so the map is
 * a reliable statement about availability, not just overrides.
 */
export type EntitlementMap = Record<string, boolean>;

/**
 * entitlementReadOptions are the query options of the shell's plan, module
 * and expiry reads. A
 * user without entitlement:read (e.g. the client-scoped role
 * client_technician) gets 403 on both: retrying does not change that, and
 * every part that mounts later (a page reading useEntitlements) must not
 * send the refused read again. Such a refetch put the query back to
 * "pending" and the gate of SignedInShell unmounted the page it had just
 * shown, endlessly (e2e-real CS01/TP05, WP-857).
 */
export const entitlementReadOptions = {
  retry: (failureCount: number, error: unknown) =>
    !(error instanceof ApiError && (error.status === 401 || error.status === 403)) &&
    failureCount < 1,
  retryOnMount: false,
} as const;

/**
 * useEntitlements resolves which product modules are enabled for the current
 * tenant. The navigation and command palette use it to hide gated modules
 * instead of letting the user click into a 403 (progressive disclosure,
 * spec §8.1).
 */
export function useEntitlements() {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated());
  const query = useQuery({
    queryKey: ['entitlements'],
    queryFn: async () => {
      const res = await entitlementApi.list({ limit: 200 });
      const map: EntitlementMap = {};
      let plan: string | undefined;
      for (const e of res.data ?? []) {
        map[e.feature_key] = e.enabled;
        // Every row carries the plan of the organization; cmdb_core is
        // always there and wins over the others (WP-857).
        if (e.plan && (!plan || e.feature_key === 'cmdb_core')) {
          plan = e.plan;
        }
      }
      return { map, plan };
    },
    enabled: isAuthenticated,
    staleTime: 60_000,
    ...entitlementReadOptions,
  });

  const map = query.data?.map;
  const isEnabled = (feature: string | undefined): boolean => {
    // While loading or when the feature has no gate, default to visible so the
    // core navigation (dashboard, cmdb) never flickers away.
    if (!feature) return true;
    if (!map) return true;
    return map[feature] ?? false;
  };

  return {
    entitlements: map,
    plan: query.data?.plan,
    isEnabled,
    // Only the first read: once it has settled (data or error), a later
    // read never counts as loading again, so a gated page that is shown
    // is not swapped for the loading state. Without the plan (an error,
    // e.g. 403 without entitlement:read) every page counts as entitled:
    // the server stays the one that refuses.
    isLoading: query.isLoading && query.errorUpdateCount === 0,
  };
}

type ModuleList = Awaited<ReturnType<typeof moduleApi.list>>['data'];

/** isPageServed: no switched-off module lists the page (unknown until loaded: served). */
function isPageServed(modules: ModuleList | undefined, page: string): boolean {
  return !modules || !modules.some((m) => !m.enabled && m.nav.includes(page));
}

/**
 * useModules resolves which late modules (phase P2 and later, add-ons) this
 * release serves (WP-214, E-03). The pages of a switched-off module are
 * hidden whatever the plan says: their routes do not exist on the server.
 */
export function useModules() {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated());
  const query = useQuery({
    queryKey: ['modules'],
    queryFn: async () => (await moduleApi.list()).data ?? [],
    enabled: isAuthenticated,
    staleTime: 5 * 60_000,
    ...entitlementReadOptions,
  });

  const modules = query.data;
  const isModuleEnabled = (key: string): boolean =>
    // Unknown until loaded: hide nothing so the navigation does not flicker.
    !modules || (modules.find((m) => m.key === key)?.enabled ?? true);
  const isPageEnabled = (page: string): boolean => isPageServed(modules, page);

  return { modules, isModuleEnabled, isPageEnabled, isLoading: query.isLoading };
}

/**
 * navFeatureFor maps a navigation page to the entitlement feature that gates
 * it. Pages without a mapping are always available (core CMDB surface).
 * Every value is a feature key the server knows (entitlement.FeatureKeys):
 * isEnabled reports an unknown key as not entitled, which would hide the
 * page for every organization. The phase-2 pages use the keys of ENT-06
 * (E-44, E-39), the phase-3 pages those of E-151 (WP-862): workflows and
 * compliance_findings replace workflow_forms and compliance.
 */
export const navFeatureFor: Record<string, string | undefined> = {
  dashboard: undefined,
  cmdb: undefined,
  topology: 'topology',
  map: undefined,
  roomplan: 'rack_view',
  racks: 'rack_view',
  discovery: 'discovery',
  assets: 'assets_basic',
  assignments: 'assets_basic',
  // Contracts and suppliers (MGT-12, WP-808) follow assets_basic (E-44, WP-244).
  contracts: 'assets_basic',
  suppliers: 'assets_basic',
  documents: 'documents',
  stocktake: 'stocktake_standard',
  consumables: 'stock',
  orders: 'internal_orders',
  maintenance: 'monitoring',
  disposal: 'assets_basic',
  keys: 'compliance_findings',
  trainings: 'workflows',
  desks: 'workplace',
  agents: 'endpoint_agent',
  findings: 'compliance_findings',
  reviews: undefined,
  jobs: undefined,
  tickets: 'ticketing_essential',
  users: undefined,
  permissions: undefined,
  slas: 'ticketing_standard',
  forms: 'workflows',
  workflows: 'workflows',
  compliance: 'compliance_findings',
  iga: 'iga',
  assistant: 'ai',
  webhooks: 'webhooks',
  export: 'export_csv',
  // The import assistant (WP-322, E-44).
  import: 'import',
  monitoring: 'monitoring',
  audit: undefined,
  security: undefined,
  // Enterprise CMDB + asset/inventory extension. ci-types, saved-views and
  // the location tree are core (G1; /api/v1/locations is ungated on the
  // server); the inventory page follows stock.
  'ci-types': undefined,
  locations: undefined,
  inventory: 'stock',
  'saved-views': undefined,
  // The client administration (WP-855) is core like the location tree.
  clients: undefined,
};

/**
 * navPermissionFor maps a navigation page to the permission its use requires
 * (UI-12, WP-804), next to the feature of navFeatureFor. Pages without a
 * mapping need no permission beyond the session. The metamodel
 * administration needs citype:manage (WP-363, migration 000147, replaces
 * ci_type:manage); later metamodel pages add their permission here.
 */
export const navPermissionFor: Record<string, string | undefined> = {
  'ci-types': 'citype:manage',
  // The client list reads with location:read; writing needs user:manage (WP-855).
  clients: 'location:read',
};

/**
 * hasPermission tells whether the effective permissions (GET
 * /me/permissions) name the key, as an exact match like
 * identity.HasPermission on the server. Rights that are still loading or
 * could not be read count as missing: fail closed like useHasPermission.
 */
export function hasPermission(permissions: readonly string[] | undefined, key: string): boolean {
  return permissions?.includes(key) ?? false;
}

/**
 * useNavAccess applies the one visibility rule of the navigation (WP-857,
 * navPageState) to every page: the sidebar, the command palette, the
 * dashboard's quick access and the hint page of a locked route all read
 * it. A page of a switched-off release module or without the user's right
 * is hidden; a page whose only obstacle is the plan is locked. The rights
 * fail closed while they load (like useHasPermission). Call it only from
 * signed-in parts: it reads /entitlements, /modules and /me/permissions.
 */
export function useNavAccess() {
  const { entitlements, plan, isLoading } = useEntitlements();
  const { modules } = useModules();
  const permissions = useEffectivePermissions().data?.permissions;
  const states = useMemo(
    () =>
      navPageStates({
        isEntitled: (page) => {
          const feature = navFeatureFor[page];
          return !feature || !entitlements || (entitlements[feature] ?? false);
        },
        isPageEnabled: (page) => isPageServed(modules, page),
        isPermitted: (page) => {
          const required = navPermissionFor[page];
          return !required || hasPermission(permissions, required);
        },
      }),
    [entitlements, modules, permissions],
  );
  const stateOf = (page: AppPage): NavPageState => states.get(page) ?? 'hidden';
  return { states, stateOf, plan, isLoading };
}
