import { useQuery } from '@tanstack/react-query';
import { entitlementApi, moduleApi } from '../api/client';
import { useAuthStore } from '../auth/authStore';

/**
 * Map of feature_key -> enabled for the current tenant. When the tenant has no
 * explicit rows the server applies the default plan (essential), so the map is
 * a reliable statement about availability, not just overrides.
 */
export type EntitlementMap = Record<string, boolean>;

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
      for (const e of res.data ?? []) {
        map[e.feature_key] = e.enabled;
      }
      return map;
    },
    enabled: isAuthenticated,
    staleTime: 60_000,
  });

  const map = query.data;
  const isEnabled = (feature: string | undefined): boolean => {
    // While loading or when the feature has no gate, default to visible so the
    // core navigation (dashboard, cmdb) never flickers away.
    if (!feature) return true;
    if (!map) return true;
    return map[feature] ?? false;
  };

  return { entitlements: map, isEnabled, isLoading: query.isLoading };
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
  });

  const modules = query.data;
  const isModuleEnabled = (key: string): boolean =>
    // Unknown until loaded: hide nothing so the navigation does not flicker.
    !modules || (modules.find((m) => m.key === key)?.enabled ?? true);
  const isPageEnabled = (page: string): boolean =>
    !modules || !modules.some((m) => !m.enabled && m.nav.includes(page));

  return { modules, isModuleEnabled, isPageEnabled, isLoading: query.isLoading };
}

/**
 * navFeatureFor maps a navigation page to the entitlement feature that gates
 * it. Pages without a mapping are always available (core CMDB surface).
 * Every value is a feature key the server knows (entitlement.FeatureKeys):
 * isEnabled reports an unknown key as not entitled, which would hide the
 * page for every organization. The phase-2 pages use the keys of ENT-06
 * (E-44, E-39).
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
  documents: 'documents',
  stocktake: 'stocktake_standard',
  consumables: 'stock',
  orders: 'internal_orders',
  maintenance: 'monitoring',
  disposal: 'assets_basic',
  keys: 'compliance',
  trainings: 'workflow_forms',
  desks: 'workplace',
  agents: 'endpoint_agent',
  findings: 'compliance',
  reviews: undefined,
  jobs: undefined,
  tickets: 'ticketing_essential',
  users: undefined,
  permissions: undefined,
  slas: 'ticketing_standard',
  forms: 'workflow_forms',
  workflows: 'workflow_forms',
  compliance: 'compliance',
  iga: 'iga',
  assistant: 'ai',
  webhooks: 'webhooks',
  export: 'export_csv',
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
