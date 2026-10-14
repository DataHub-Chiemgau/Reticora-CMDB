import { createElement } from 'react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { navFeatureFor, useModules } from './useEntitlements';
import { useAuthStore } from '../auth/authStore';
import { createTestQueryClient, stubFetchRoutes } from '../test/utils';

describe('navFeatureFor', () => {
  it('leaves core CMDB surface ungated', () => {
    for (const page of ['dashboard', 'cmdb', 'users', 'permissions', 'audit', 'security']) {
      expect(navFeatureFor[page], page).toBeUndefined();
    }
  });

  it('gates add-on modules behind their entitlement feature', () => {
    expect(navFeatureFor.tickets).toBe('ticketing');
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
