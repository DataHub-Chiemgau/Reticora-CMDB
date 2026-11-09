import { createElement } from 'react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import {
  hasPermission,
  navFeatureFor,
  navPermissionFor,
  useEntitlements,
  useModules,
} from './useEntitlements';
import { useAuthStore } from '../auth/authStore';
import { createTestQueryClient, stubFetchRoutes } from '../test/utils';

/**
 * The feature keys the server accepts (entitlement.FeatureKeys in
 * backend/internal/entitlement/entitlement.go): the phase-1 keys of ENT-02,
 * the phase-2 keys of ENT-06 (E-44), the module keys of phases 3 and 4 and
 * the add-ons.
 */
const FEATURE_KEYS = [
  'cmdb_core',
  'discovery',
  'topology',
  'rack_view',
  'export_csv',
  'webhooks',
  'api_access',
  'notifications_email',
  'assets_basic',
  'documents',
  'import',
  'ticketing_essential',
  'ticketing_standard',
  'signature',
  'stock',
  'stocktake_standard',
  'barcode',
  'gps_standard',
  'workplace',
  'export_datev',
  'internal_orders',
  'monitoring',
  'endpoint_agent',
  'workflow_forms',
  'compliance',
  'iga',
  'ai',
];

describe('navFeatureFor', () => {
  it('uses only feature keys the server knows (WP-244)', () => {
    for (const [page, feature] of Object.entries(navFeatureFor)) {
      if (feature !== undefined) {
        expect(FEATURE_KEYS, page).toContain(feature);
      }
    }
    for (const former of ['inventory', 'stocktake', 'ticketing']) {
      expect(Object.values(navFeatureFor)).not.toContain(former);
    }
  });

  it('gates the phase-2 pages with the keys of ENT-06 (E-44, E-39)', () => {
    expect(navFeatureFor.assets).toBe('assets_basic');
    expect(navFeatureFor.assignments).toBe('assets_basic');
    expect(navFeatureFor.disposal).toBe('assets_basic');
    expect(navFeatureFor.consumables).toBe('stock');
    expect(navFeatureFor.inventory).toBe('stock');
    expect(navFeatureFor.stocktake).toBe('stocktake_standard');
    expect(navFeatureFor.orders).toBe('internal_orders');
    expect(navFeatureFor.tickets).toBe('ticketing_essential');
    expect(navFeatureFor.slas).toBe('ticketing_standard');
    expect(navFeatureFor.desks).toBe('workplace');
    expect(navFeatureFor.documents).toBe('documents');
  });

  it('leaves the location tree ungated (G1 core)', () => {
    expect('locations' in navFeatureFor).toBe(true);
    expect(navFeatureFor.locations).toBeUndefined();
  });

  it('leaves core CMDB surface ungated', () => {
    for (const page of ['dashboard', 'cmdb', 'users', 'permissions', 'audit', 'security']) {
      expect(navFeatureFor[page], page).toBeUndefined();
    }
  });

  it('gates add-on modules behind their entitlement feature', () => {
    expect(navFeatureFor.tickets).toBe('ticketing_essential');
    expect(navFeatureFor.iga).toBe('iga');
    expect(navFeatureFor.monitoring).toBe('monitoring');
    expect(navFeatureFor.workflows).toBe('workflow_forms');
    expect(navFeatureFor.forms).toBe('workflow_forms');
    expect(navFeatureFor.assistant).toBe('ai');
    expect(navFeatureFor.export).toBe('export_csv');
  });

  it('uses the phase-1 feature keys of ENT-02', () => {
    expect(navFeatureFor.topology).toBe('topology');
    expect(navFeatureFor.racks).toBe('rack_view');
    expect(navFeatureFor.roomplan).toBe('rack_view');
    expect(navFeatureFor.webhooks).toBe('webhooks');
    expect(navFeatureFor.compliance).toBe('compliance');
  });
});

describe('navPermissionFor (UI-12, WP-804)', () => {
  it('requires citype:manage for the CI type administration (WP-363, 000147)', () => {
    expect(navPermissionFor['ci-types']).toBe('citype:manage');
    expect(Object.values(navPermissionFor)).not.toContain('ci_type:manage');
  });

  it('requires no permission for the core pages', () => {
    for (const page of ['dashboard', 'cmdb', 'locations', 'saved-views', 'reviews']) {
      expect(navPermissionFor[page], page).toBeUndefined();
    }
  });
});

describe('hasPermission (UI-12, WP-804)', () => {
  it('matches the key exactly like identity.HasPermission', () => {
    const permissions = ['ci:read', 'citype:manage'];
    expect(hasPermission(permissions, 'citype:manage')).toBe(true);
    expect(hasPermission(permissions, 'ci:write')).toBe(false);
    expect(hasPermission(permissions, 'citype')).toBe(false);
    expect(hasPermission(permissions, 'CITYPE:MANAGE')).toBe(false);
    expect(hasPermission(['ci_type:manage'], 'citype:manage')).toBe(false);
  });

  it('fails closed while the rights load or cannot be read', () => {
    expect(hasPermission(undefined, 'citype:manage')).toBe(false);
    expect(hasPermission([], 'citype:manage')).toBe(false);
  });
});

describe('useModules', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    useAuthStore.setState({ token: null, expiresAt: null, user: null });
  });

  it('hides the pages of switched-off late modules (WP-214)', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    stubFetchRoutes({
      '/modules': {
        data: [
          { key: 'ticketing', phase: 'P2', nav: ['tickets', 'slas'], enabled: false },
          { key: 'iga', phase: 'A', nav: ['iga'], enabled: true },
        ],
      },
    });
    const client = createTestQueryClient();
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);

    const { result } = renderHook(() => useModules(), { wrapper });
    // Until the release is known nothing is hidden.
    expect(result.current.isPageEnabled('tickets')).toBe(true);

    await waitFor(() => expect(result.current.modules).toBeDefined());
    expect(result.current.isPageEnabled('tickets')).toBe(false);
    expect(result.current.isPageEnabled('slas')).toBe(false);
    expect(result.current.isPageEnabled('iga')).toBe(true);
    expect(result.current.isPageEnabled('cmdb')).toBe(true);
    expect(result.current.isModuleEnabled('ticketing')).toBe(false);
    expect(result.current.isModuleEnabled('iga')).toBe(true);
  });
});

describe('useEntitlements without entitlement:read', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    useAuthStore.setState({ token: null, expiresAt: null, user: null });
  });

  // A client-scoped technician (client_technician) lacks entitlement:read:
  // the plan cannot be read (403). Every page counts as entitled (the
  // server refuses where needed), and a part that mounts later neither
  // reads again nor reports loading: the gate of SignedInShell unmounted
  // the topology page on that endlessly (e2e-real CS01/TP05, WP-857).
  it('settles once on 403 and never reports loading again', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    const fetchMock = stubFetchRoutes({
      '/entitlements': new Response(
        JSON.stringify({ status: 403, title: 'Forbidden', detail: 'missing permission' }),
        { status: 403, headers: { 'Content-Type': 'application/problem+json' } },
      ),
    });
    const client = createTestQueryClient();
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);

    const shell = renderHook(() => useEntitlements(), { wrapper });
    await waitFor(() => expect(shell.result.current.isLoading).toBe(false));
    expect(shell.result.current.entitlements).toBeUndefined();
    expect(shell.result.current.isEnabled('topology')).toBe(true);

    // The page mounts its own reader of the same query.
    const page = renderHook(() => useEntitlements(), { wrapper });
    expect(page.result.current.isLoading).toBe(false);
    expect(shell.result.current.isLoading).toBe(false);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(page.result.current.isLoading).toBe(false);
    expect(shell.result.current.isLoading).toBe(false);
    const reads = fetchMock.mock.calls.filter(([input]) => String(input).includes('/entitlements'));
    expect(reads).toHaveLength(1);
  });
});
