import { useQuery } from '@tanstack/react-query';
import { entitlementApi } from '../api/client';
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
 * navFeatureFor maps a navigation page to the entitlement feature that gates
 * it. Pages without a mapping are always available (core CMDB surface).
 */
export const navFeatureFor: Record<string, string | undefined> = {
  dashboard: undefined,
  cmdb: undefined,
  topology: undefined,
  map: undefined,
  roomplan: undefined,
  racks: undefined,
  discovery: 'discovery',
  assets: 'inventory',
  assignments: 'inventory',
  documents: 'documents',
  stocktake: 'stocktake',
  tickets: 'ticketing',
  users: undefined,
  permissions: undefined,
  slas: 'ticketing',
  forms: 'workflow_forms',
  workflows: 'workflow_forms',
  compliance: 'compliance',
  iga: 'iga',
  assistant: 'ai_assistant',
  webhooks: 'webhooks',
  export: 'export',
  monitoring: 'monitoring',
  audit: undefined,
  security: undefined,
};
