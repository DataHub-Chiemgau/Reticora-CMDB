import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { AT, ORG_ID, expect, list, mockBackend, problem, signIn, test } from './support/backend';

// UI-14 (WP-856): credentials are created and deleted in the credentials
// section of the Discovery page. The create form asks for the fields of the
// kind; secret fields are password fields without autocompletion, the
// secret is sent once and never shown, and no response carries it. A new
// credential is offered in the scope form at once; deleting asks first.
// Without credential:manage the section is read-only.

test.use({ locale: 'de-DE' });

const USER = '00000000-0000-0000-0000-000000000001';
const CLIENT = 'c0000000-0000-4000-8000-000000000856';
const COLLECTOR = 'c1000000-0000-4000-8000-000000000856';
const CRED_ORG = 'ce000000-0000-4000-8000-000000000001';
const CRED_NEW = 'ce000000-0000-4000-8000-000000000002';
const SECRET = 'community-nur-schreibbar';
const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

interface StoredCredential {
  id: string;
  organization_id: string;
  client_id?: string;
  name: string;
  kind: string;
  key_version: number;
  created_at: string;
  updated_at: string;
}

function credential(id: string, name: string, kind: string, clientId?: string): StoredCredential {
  return {
    id,
    organization_id: ORG_ID,
    ...(clientId ? { client_id: clientId } : {}),
    name,
    kind,
    key_version: 1,
    created_at: AT,
    updated_at: AT,
  };
}

/** mockDiscovery answers the Discovery page with one collector and the credential store. */
async function mockDiscovery(page: Page, rights: string[]) {
  const stored: StoredCredential[] = [credential(CRED_ORG, 'SNMP Organisation', 'snmp_v2c')];
  const calls = await mockBackend(page, [
    {
      path: '/me/permissions',
      reply: () => ({ body: { user_id: USER, permissions: rights } }),
    },
    {
      path: '/clients',
      reply: () => ({
        body: list([
          {
            id: CLIENT,
            organization_id: ORG_ID,
            name: 'Kunde Nord',
            slug: 'kunde-nord',
            external_ref: null,
            timezone: null,
            settings: {},
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    {
      path: '/collectors',
      reply: () => ({
        body: list([
          {
            id: COLLECTOR,
            organization_id: ORG_ID,
            client_id: CLIENT,
            name: 'collector-nord',
            version: '1.4.0',
            status: 'active',
            last_heartbeat_at: AT,
            config: {},
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    { path: '/discovery-scopes', reply: () => ({ body: list([]) }) },
    { path: '/vrfs', reply: () => ({ body: list([]) }) },
    { path: '/credentials', reply: () => ({ body: list(stored) }) },
    {
      method: 'POST',
      path: '/credentials',
      reply: (request) => {
        const body = request.postDataJSON() as { name: string; kind: string; client_id?: string };
        const created = credential(CRED_NEW, body.name, body.kind, body.client_id);
        stored.push(created);
        // Like the server: metadata only, never the secret.
        return { status: 201, body: created };
      },
    },
    {
      method: 'DELETE',
      path: /^\/credentials\/[0-9a-f-]{36}$/,
      reply: (_request, url) => {
        const id = url.pathname.split('/').pop();
        const at = stored.findIndex((c) => c.id === id);
        if (at < 0) return { status: 404, body: problem(404, 'credential not found') };
        stored.splice(at, 1);
        return { status: 204 };
      },
    },
  ]);
  return calls;
}

function section(page: Page) {
  return page
    .locator('section')
    .filter({ has: page.getByRole('heading', { name: 'Zugangsdaten', exact: true }) });
}

async function expectNoAxeViolations(page: Page, selector: string) {
  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).include(selector).analyze();
  expect(results.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);
}

test('creates a credential, offers it in the scope form and deletes it after a confirmation', async ({
  page,
}) => {
  const calls = await mockDiscovery(page, [
    'discovery:read',
    'discovery:manage',
    'credential:read',
    'credential:manage',
  ]);
  await signIn(page);
  await page.goto('/discovery');

  const credentials = section(page);
  await expect(credentials.getByText('SNMP Organisation')).toBeVisible();
  await expect(credentials.getByText('Alle Kunden (organisationsweit)')).toBeVisible();
  await expectNoAxeViolations(page, '#credentials');

  await credentials.getByRole('button', { name: 'Zugangsdaten anlegen' }).click();
  const dialog = page.getByRole('dialog', { name: 'Zugangsdaten anlegen' });
  await dialog.getByLabel('Name', { exact: true }).fill('SNMP Nord');
  await dialog.getByLabel('Kunde', { exact: true }).selectOption({ label: 'Kunde Nord' });
  const community = dialog.getByLabel('Community', { exact: true });
  await expect(community).toHaveAttribute('type', 'password');
  await expect(community).toHaveAttribute('autocomplete', 'off');
  await community.fill(SECRET);
  await expectNoAxeViolations(page, '[role="dialog"]');

  const created = page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/credentials' && r.request().method() === 'POST',
  );
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  const res = await created;
  expect(res.status()).toBe(201);
  expect(JSON.stringify(await res.json())).not.toContain('secret');
  await expect(dialog).toBeHidden();
  expect(calls.filter((c) => c.method === 'POST').map((c) => c.body)).toEqual([
    { name: 'SNMP Nord', kind: 'snmp_v2c', client_id: CLIENT, secret: { community: SECRET } },
  ]);

  // The list is current: name, kind and client of the new credential.
  const row = credentials.getByRole('row').filter({ hasText: 'SNMP Nord' });
  await expect(row).toContainText('SNMP v2c');
  await expect(row).toContainText('Kunde Nord');
  await expect(page.getByText(SECRET)).toHaveCount(0);

  // The scope form of the collector of Kunde Nord offers the new credential
  // at once (the client's and the organization-wide ones apply, E-67).
  await page.getByRole('button', { name: 'Scope anlegen' }).click();
  const scopeDialog = page.getByRole('dialog');
  await expect(scopeDialog.getByLabel(/SNMP Nord/)).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(scopeDialog).toBeHidden();

  // Deleting asks first; cancel keeps the credential.
  const remove = credentials.getByRole('button', { name: 'Zugangsdaten SNMP Nord löschen' });
  await remove.click();
  const confirm = page.getByRole('dialog', { name: 'Zugangsdaten löschen?' });
  await confirm.getByRole('button', { name: 'Abbrechen' }).click();
  await expect(confirm).toBeHidden();
  expect(calls.filter((c) => c.method === 'DELETE')).toEqual([]);

  await remove.click();
  await confirm.getByRole('button', { name: 'Endgültig löschen' }).click();
  await expect(credentials.getByText('SNMP Nord')).toHaveCount(0);
  expect(calls.filter((c) => c.method === 'DELETE').map((c) => c.path)).toEqual([
    `/credentials/${CRED_NEW}`,
  ]);
});

test('asks for the fields of the kind and keeps secrets out of the page', async ({ page }) => {
  const calls = await mockDiscovery(page, [
    'discovery:read',
    'credential:read',
    'credential:manage',
  ]);
  await signIn(page);
  await page.goto('/discovery');

  await section(page).getByRole('button', { name: 'Zugangsdaten anlegen' }).click();
  const dialog = page.getByRole('dialog', { name: 'Zugangsdaten anlegen' });
  await dialog.getByLabel('Name', { exact: true }).fill('SNMPv3 Kern');
  await dialog.getByLabel('Art', { exact: true }).selectOption('snmp_v3');
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  for (const field of ['Benutzer', 'Auth-Passwort', 'Privacy-Passwort']) {
    await expect(dialog.getByText(`${field} ist erforderlich.`)).toBeVisible();
  }
  for (const field of ['Auth-Passwort', 'Privacy-Passwort']) {
    await expect(dialog.getByLabel(field, { exact: true })).toHaveAttribute('type', 'password');
  }
  await expectNoAxeViolations(page, '[role="dialog"]');

  await dialog.getByLabel('Art', { exact: true }).selectOption('api_token');
  await expect(dialog.getByLabel('Token', { exact: true })).toHaveAttribute('type', 'password');
  await expect(dialog.getByLabel('Benutzer', { exact: true })).toHaveCount(0);
  expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);
});

test('is read-only without credential:manage', async ({ page }) => {
  await mockDiscovery(page, ['discovery:read', 'credential:read']);
  await signIn(page);
  await page.goto('/discovery');

  const credentials = section(page);
  await expect(credentials.getByText('SNMP Organisation')).toBeVisible();
  await expect(credentials.getByRole('button')).toHaveCount(0);
});
