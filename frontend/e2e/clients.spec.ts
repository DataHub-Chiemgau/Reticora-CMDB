import { AT, ORG_ID, expect, list, mockBackend, problem, signIn, test } from './support/backend';
import type { Page } from '@playwright/test';
import type { Handler } from './support/backend';

// UI-14 (WP-855): the client administration in a real browser against the
// strict mocked backend; every request and answer is checked against
// api/openapi.yaml (support/contract.ts). An administrator (location:read,
// user:manage) creates a client with the short name suggested from the
// name, sees a taken short name (409) at its field, renames and deletes a
// client and creates the client of a new site from the site dialog; a
// reader (location:read) sees the list without write actions.

test.use({ locale: 'de-DE' });

const USER = '00000000-0000-0000-0000-000000000001';
const ACME = 'c0000000-0000-4000-8000-000000000001';

interface Client {
  id: string;
  organization_id: string;
  name: string;
  slug: string;
  external_ref: string | null;
  timezone: string | null;
  settings: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

function client(id: string, name: string, slug: string): Client {
  return {
    id,
    organization_id: ORG_ID,
    name,
    slug,
    external_ref: null,
    timezone: null,
    settings: {},
    created_at: AT,
    updated_at: AT,
  };
}

function permissions(perms: string[]): Handler {
  return {
    path: '/me/permissions',
    reply: () => ({ body: { user_id: USER, permissions: perms } }),
  };
}

/** clientStore answers /clients like the server: a taken slug is 409 naming the field. */
function clientStore(clients: Client[]): Handler[] {
  let next = 2;
  return [
    { path: '/clients', reply: () => ({ body: { ...list(clients), limit: 200 } }) },
    {
      method: 'POST',
      path: '/clients',
      reply: (request) => {
        const body = request.postDataJSON() as { name: string; slug: string };
        if (clients.some((c) => c.slug === body.slug)) {
          return {
            status: 409,
            body: problem(409, 'slug: the value is already taken', { field: 'slug' }),
          };
        }
        const created = client(
          `c0000000-0000-4000-8000-00000000000${next++}`,
          body.name,
          body.slug,
        );
        clients.push(created);
        return { status: 201, body: created };
      },
    },
    {
      method: 'PATCH',
      path: /^\/clients\/[0-9a-f-]{36}$/,
      reply: (request, url) => {
        const found = clients.find((c) => url.pathname.endsWith(c.id));
        if (!found) return { status: 404, body: problem(404, 'client not found') };
        Object.assign(found, request.postDataJSON() as Partial<Client>);
        return { body: found };
      },
    },
    {
      method: 'DELETE',
      path: /^\/clients\/[0-9a-f-]{36}$/,
      reply: (_request, url) => {
        const index = clients.findIndex((c) => url.pathname.endsWith(c.id));
        if (index < 0) return { status: 404, body: problem(404, 'client not found') };
        clients.splice(index, 1);
        return { status: 204 };
      },
    },
  ];
}

const row = (page: Page, name: string) =>
  page.locator('#main-content tr').filter({ hasText: name });

test('an administrator creates, renames and deletes clients', async ({ page }) => {
  const clients = [client(ACME, 'Acme GmbH', 'acme')];
  const calls = await mockBackend(page, [
    permissions(['location:read', 'user:manage']),
    ...clientStore(clients),
  ]);
  await signIn(page);
  await page.goto('/clients');
  await expect(page.getByRole('heading', { name: 'Kunden', level: 1 })).toBeVisible();
  await expect(row(page, 'Acme GmbH')).toContainText('acme');

  // Create: the short name follows the name.
  await page.getByRole('button', { name: 'Kunde anlegen' }).click();
  let dialog = page.getByRole('dialog', { name: 'Kunde anlegen' });
  await dialog.getByLabel('Name', { exact: true }).fill('Müller & Söhne GmbH');
  await expect(dialog.getByLabel('Kürzel')).toHaveValue('mueller-soehne-gmbh');
  await dialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(row(page, 'Müller & Söhne GmbH')).toContainText('mueller-soehne-gmbh');

  // A taken short name is shown at its field; the dialog stays open.
  await page.getByRole('button', { name: 'Kunde anlegen' }).click();
  dialog = page.getByRole('dialog', { name: 'Kunde anlegen' });
  await dialog.getByLabel('Name', { exact: true }).fill('Acme Zwei');
  await dialog.getByLabel('Kürzel').fill('acme');
  await dialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
  await expect(dialog.getByText('Dieses Kürzel ist bereits vergeben.')).toBeVisible();
  await expect(dialog.getByLabel('Kürzel')).toHaveAttribute('aria-invalid', 'true');
  await dialog.getByRole('button', { name: 'Abbrechen' }).click();
  await expect(dialog).toBeHidden();

  // Rename: only the changed field is sent.
  await page.getByRole('button', { name: 'Bearbeiten: Acme GmbH' }).click();
  dialog = page.getByRole('dialog', { name: '„Acme GmbH“ bearbeiten' });
  await dialog.getByLabel('Name', { exact: true }).fill('Acme Holding GmbH');
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(dialog).toBeHidden();
  await expect(row(page, 'Acme Holding GmbH')).toBeVisible();

  // Delete after confirmation.
  await page.getByRole('button', { name: 'Löschen: Acme Holding GmbH' }).click();
  dialog = page.getByRole('dialog', { name: 'Kunde „Acme Holding GmbH“ löschen?' });
  await expect(dialog).toContainText('seine Standorte mit ihm');
  await dialog.getByRole('button', { name: 'Löschen' }).click();
  await expect(dialog).toBeHidden();
  await expect(row(page, 'Acme Holding GmbH')).toHaveCount(0);

  const writes = calls.filter((c) => c.method !== 'GET' && c.path.startsWith('/clients'));
  expect(writes.map((c) => [c.method, c.path, c.body])).toEqual([
    ['POST', '/clients', { name: 'Müller & Söhne GmbH', slug: 'mueller-soehne-gmbh' }],
    ['POST', '/clients', { name: 'Acme Zwei', slug: 'acme' }],
    ['PATCH', `/clients/${ACME}`, { name: 'Acme Holding GmbH' }],
    ['DELETE', `/clients/${ACME}`, undefined],
  ]);
});

test('the site dialog creates the client of a new site and selects it', async ({ page }) => {
  const clients: Client[] = [];
  const sites: Record<string, unknown>[] = [];
  const calls = await mockBackend(page, [
    permissions(['location:read', 'user:manage']),
    ...clientStore(clients),
    { path: '/locations/tree', reply: () => ({ body: { data: sites } }) },
    {
      method: 'POST',
      path: '/locations',
      reply: (request) => {
        const body = request.postDataJSON() as { name: string; client_id: string };
        const id = '51000000-0000-4000-8000-0000000000a1';
        const site = {
          id,
          organization_id: ORG_ID,
          client_id: body.client_id,
          site_id: id,
          kind: 'site',
          name: body.name,
          path: id.replace(/-/g, '_'),
          created_at: AT,
          updated_at: AT,
        };
        sites.push({ ...site, children: [] });
        return { status: 201, body: site };
      },
    },
  ]);
  await signIn(page);
  await page.goto('/locations');

  await page.getByRole('button', { name: 'Standort anlegen' }).click();
  const siteDialog = page.getByRole('dialog', { name: 'Standort anlegen' });
  await siteDialog.getByRole('button', { name: 'Neuer Kunde' }).click();
  const clientDialog = page.getByRole('dialog', { name: 'Kunde anlegen' });
  await clientDialog.getByLabel('Name', { exact: true }).fill('Nordwind AG');
  await clientDialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
  await expect(clientDialog).toBeHidden();

  const select = siteDialog.getByRole('combobox');
  await expect(select.locator('option:checked')).toHaveText('Nordwind AG');
  await siteDialog.getByLabel('Name', { exact: true }).fill('Werk Nord');
  await siteDialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
  await expect(siteDialog).toBeHidden();
  await expect(page.getByText('Werk Nord', { exact: true })).toBeVisible();

  const posts = calls.filter((c) => c.method === 'POST');
  expect(posts.map((c) => [c.path, c.body])).toEqual([
    ['/clients', { name: 'Nordwind AG', slug: 'nordwind-ag' }],
    ['/locations', { kind: 'site', name: 'Werk Nord', client_id: clients[0]?.id }],
  ]);
});

test('a reader sees the clients without write actions', async ({ page }) => {
  await mockBackend(page, [
    permissions(['location:read']),
    ...clientStore([client(ACME, 'Acme GmbH', 'acme')]),
  ]);
  await signIn(page);
  await page.goto('/clients');

  await expect(row(page, 'Acme GmbH')).toContainText('acme');
  await expect(page.getByText(/erfordern das Recht user:manage/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Kunde anlegen' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^Bearbeiten:/ })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^Löschen:/ })).toHaveCount(0);
});
