import { describe, expect, it } from 'vitest';
import { navFeatureFor } from './useEntitlements';

describe('navFeatureFor', () => {
  it('leaves core CMDB surface ungated', () => {
    for (const page of ['dashboard', 'cmdb', 'topology', 'racks', 'users', 'permissions', 'audit', 'security']) {
      expect(navFeatureFor[page], page).toBeUndefined();
    }
  });

  it('gates add-on modules behind their entitlement feature', () => {
    expect(navFeatureFor.tickets).toBe('ticketing');
    expect(navFeatureFor.iga).toBe('iga');
    expect(navFeatureFor.monitoring).toBe('monitoring');
    expect(navFeatureFor.workflows).toBe('workflow_forms');
    expect(navFeatureFor.forms).toBe('workflow_forms');
    expect(navFeatureFor.assistant).toBe('ai_assistant');
    expect(navFeatureFor.export).toBe('export');
    expect(navFeatureFor.webhooks).toBe('webhooks');
    expect(navFeatureFor.compliance).toBe('compliance');
  });
});
