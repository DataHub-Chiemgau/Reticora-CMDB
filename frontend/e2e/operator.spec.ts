import AxeBuilder from '@axe-core/playwright';
import { AT, expect, mockBackend, test } from './support/backend';
import type { Page } from '@playwright/test';

// PKG-07, UI-14 (WP-887, WP-888): the operator area in a real browser
// against the strict mocked backend (requests and answers checked against
// api/openapi.yaml). The operator signs in through the operator callback,
// opens the list of organizations and the detail of one, previews and
// confirms a downgrade with a reason and books an add-on; axe finds nothing
// on the list and the detail.

test.use({ locale: 'de-DE' });

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];
const ORG = '11111111-1111-4111-8111-111111111111';

function row(feature_key: string, origin: string, limits: Record<string, number> = {}) {
  return {
    organization_id: ORG,
    feature_key,
    plan: 'pro',
    enabled: true,
    limits,
    source: 'manual',
    origin,
    updated_at: AT,
  };
}

const ROWS = [
  row('cmdb_core', 'plan', { max_cis: 10000, max_users: 100 }),
  row('documents', 'plan', { max_storage_mb: 50000 }),
  row('monitoring', 'plan'),
  row('workflows', 'plan'),
];

const MATRIX = {
  data: [
    {
      plan: 'essential',
      features: ['cmdb_core', 'documents'],
      quota_defaults: { max_cis: 500, max_users: 5, max_storage_mb: 1000 },
    },
    {
      plan: 'standard',
      features: ['cmdb_core', 'documents', 'monitoring'],
      quota_defaults: { max_cis: 2500, max_users: 25, max_storage_mb: 10000 },
    },
    {
      plan: 'pro',
      features: ['cmdb_core', 'documents', 'monitoring', 'workflows'],
      quota_defaults: { max_cis: 10000, max_users: 100, max_storage_mb: 50000 },
    },
    {
      plan: 'enterprise',
      features: ['cmdb_core', 'documents', 'monitoring', 'workflows'],
      quota_defaults: { max_cis: 50000 },
    },
  ],
  addons: [
    { key: 'iga', phase: 'A', description_key: 'addons.iga.description' },
    { key: 'ai', phase: 'A', description_key: 'addons.ai.description' },
    { key: 'admp', phase: 'A', description_key: 'addons.admp.description' },
  ],
};

async function mockOperator(page: Page) {
  return mockBackend(page, [
    {
      method: 'POST',
      path: '/admin/auth/callback',
      reply: () => ({
        body: {
          token: 'operator-token',
          expires_at: new Date(Date.now() + 15 * 60_000).toISOString(),
          operator: { id: 'op-1', name: 'Olga Operator', kind: 'oidc' },
        },
      }),
    },
    {
      path: '/admin/orgs',
      reply: () => ({
        body: {
          data: [{ id: ORG, name: 'Acme GmbH', slug: 'acme', plan: 'pro', created_at: AT }],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        },
      }),
    },
    {
      path: `/admin/orgs/${ORG}/usage`,
      reply: () => ({
        body: {
          plan: 'pro',
          data: [
            { quota: 'max_cis', used: 3, limit: 10000, origin: 'plan' },
            { quota: 'max_users', used: 2, limit: 100, origin: 'plan' },
          ],
        },
      }),
    },
    {
      path: `/admin/orgs/${ORG}/entitlements`,
      reply: () => ({
        body: {
          data: ROWS,
          total: ROWS.length,
          limit: ROWS.length,
          offset: 0,
          has_more: false,
          plan: 'pro',
          plan_changed_at: null,
        },
      }),
    },
    { path: '/admin/plans', reply: () => ({ body: MATRIX }) },
    {
      path: '/admin/audit',
      reply: () => ({
        body: {
          data: [
            {
              id: 7,
              timestamp: AT,
              operator_id: 'op-1',
              operator_kind: 'oidc',
              action: 'org.create',
              target_org: ORG,
              status: 200,
              details: {},
              previous_hash: '0'.repeat(64),
              entry_hash: '1'.repeat(64),
            },
          ],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        },
      }),
    },
    {
      method: 'PUT',
      path: `/admin/orgs/${ORG}/plan`,
      reply: () => ({
        body: {
          plan: 'essential',
          plan_before: 'pro',
          plan_changed_at: AT,
          unchanged: false,
          changed: ['monitoring', 'workflows', 'max_users'],
          entitlements: [],
        },
      }),
    },
    {
      method: 'POST',
      path: `/admin/orgs/${ORG}/addons/iga`,
      reply: () => ({ status: 201, body: row('iga', 'addon') }),
    },
  ]);
}

async function expectNoAxeViolations(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).include('main').analyze();
  expect(results.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);
}

test('signs in as operator, downgrades an organization with a preview and books an add-on', async ({
  page,
}) => {
  const calls = await mockOperator(page);
  await page.addInitScript(() => {
    if (!window.sessionStorage.getItem('reticora-operator-session')) {
      window.sessionStorage.setItem(
        'reticora-auth-transaction',
        JSON.stringify({
          state: 'op-state',
          codeVerifier: 'verifier',
          returnTo: '/operator',
          kind: 'operator',
        }),
      );
    }
  });

  // Sign-in through the operator callback, not the tenant one.
  await page.goto('/auth/callback?code=op-code&state=op-state');
  await expect(page.getByRole('heading', { name: 'Organisationen', level: 1 })).toBeVisible();
  expect(calls.some((c) => c.method === 'POST' && c.path === '/admin/auth/callback')).toBe(true);
  expect(calls.some((c) => c.path === '/auth/callback')).toBe(false);
  await expect(page.getByText('Angemeldet als Olga Operator')).toBeVisible();

  // The list with plan and CI usage.
  const acme = page.getByRole('row').filter({ hasText: 'Acme GmbH' });
  await expect(acme).toContainText('Pro');
  await expect(acme).toContainText('3 von 10000');
  await expectNoAxeViolations(page);

  // The detail with rows, usage and history.
  await page.getByRole('link', { name: 'Acme GmbH' }).click();
  await expect(page.getByRole('heading', { name: 'Organisation', level: 1 })).toBeVisible();
  await expect(page.getByRole('list', { name: 'Verlauf' })).toContainText(
    'Organisation angelegt durch op-1',
  );
  await expectNoAxeViolations(page);

  // Downgrade pro → essential with the preview and a reason.
  await page.getByRole('button', { name: 'Paket wechseln' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Neues Paket').selectOption('essential');
  const preview = dialog.getByRole('region', { name: 'Vorschau' });
  await expect(preview).toContainText(
    'Gesperrt (Zeilen bleiben erhalten): Monitoring, Workflows und Formulare',
  );
  await expect(preview).toContainText('Benutzer: 100 → 5');
  await dialog.getByLabel(/Grund/).fill('Kündigung Pro zum Monatsende');
  await dialog.getByRole('button', { name: 'Paket wechseln' }).click();
  await expect(page.getByText('Das Paket ist jetzt Essential.')).toBeVisible();
  const change = calls.find((c) => c.method === 'PUT' && c.path === `/admin/orgs/${ORG}/plan`);
  expect(change?.body).toEqual({ plan: 'essential', reason: 'Kündigung Pro zum Monatsende' });

  // Book an add-on.
  await page.getByRole('button', { name: 'Add-on buchen' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Buchen' }).click();
  await expect(page.getByText('IGA wurde gebucht.')).toBeVisible();
  expect(calls.some((c) => c.method === 'POST' && c.path === `/admin/orgs/${ORG}/addons/iga`)).toBe(
    true,
  );
});
