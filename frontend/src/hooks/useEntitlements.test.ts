import { describe, expect, it } from 'vitest';
import { navFeatureFor } from './useEntitlements';

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
