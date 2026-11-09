import { test, expect, type APIRequestContext } from '@playwright/test';
import { api, stamp } from './support/stack';
import {
  ok,
  openTenant,
  rows,
  treeRow,
  waitForApi,
  type List,
  type Location,
  type Tenant,
} from './support/locations-racks';

/*
 * The client administration on the real stack (WP-855, UI-14), nothing mocked:
 * UI → REST API → PostgreSQL.
 *
 * One organization is created through the self-signup UI; its administrator holds
 * user:manage (org_admin).
 *
 * Test → requirement IDs proven:
 *  C1 the page "Kunden" creates a client with the short name suggested from the name
 *     (POST /clients 201), a taken short name is refused at its field (409), the client
 *     is renamed (PATCH) and an unused client deleted (DELETE 204, GET 404) ...... UI-14
 *  C2 a site is created for the client created on the page; the site dialog creates a
 *     further client, selects it, and the site created then belongs to it (client_id of
 *     the location) ......................................................... UI-14, LOC-03
 */

interface Client {
  id: string;
  name: string;
  slug: string;
}

let rq: APIRequestContext;
let t: Tenant;
let s: string;

test.describe('client administration (UI-14, WP-855)', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    rq = await playwright.request.newContext();
    t = await openTenant(browser, rq, 'clientadmin');
    s = stamp();
  });

  test.afterAll(async () => {
    await t?.page.context().close();
    await rq?.dispose();
  });

  async function clientNamed(name: string): Promise<Client | undefined> {
    const list = await ok<List<Client>>(rq, t.token, 'GET', '/clients?limit=200');
    return rows(list).find((c) => c.name === name);
  }

  test('C1 creates, refuses a taken short name, renames and deletes a client', async () => {
    const page = t.page;
    const name = `CA Kunde ${s}`;
    await page.goto('/clients');
    await expect(page.getByRole('heading', { name: 'Kunden', level: 1 })).toBeVisible();

    await page.getByRole('button', { name: 'Kunde anlegen' }).click();
    let dialog = page.getByRole('dialog', { name: 'Kunde anlegen' });
    await dialog.getByLabel('Name', { exact: true }).fill(name);
    await expect(dialog.getByLabel('Kürzel')).toHaveValue(`ca-kunde-${s}`);
    let created = waitForApi(page, '/clients', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
    let res = await created;
    expect(res.status(), `POST /clients: ${await res.text()}`).toBe(201);
    const client = (await res.json()) as Client;
    await expect(dialog).toBeHidden();
    await expect(page.locator('#main-content tr').filter({ hasText: name })).toBeVisible();
    expect(await clientNamed(name)).toMatchObject({ id: client.id, slug: `ca-kunde-${s}` });

    // The same short name again: 409 at the field, nothing created.
    await page.getByRole('button', { name: 'Kunde anlegen' }).click();
    dialog = page.getByRole('dialog', { name: 'Kunde anlegen' });
    await dialog.getByLabel('Name', { exact: true }).fill(`${name} Zwei`);
    await dialog.getByLabel('Kürzel').fill(`ca-kunde-${s}`);
    created = waitForApi(page, '/clients', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
    res = await created;
    expect(res.status(), `POST /clients (taken slug): ${await res.text()}`).toBe(409);
    await expect(dialog.getByText('Dieses Kürzel ist bereits vergeben.')).toBeVisible();
    await dialog.getByRole('button', { name: 'Abbrechen' }).click();
    expect(await clientNamed(`${name} Zwei`)).toBeUndefined();

    // Rename.
    await page.getByRole('button', { name: `Bearbeiten: ${name}` }).click();
    dialog = page.getByRole('dialog', { name: `„${name}“ bearbeiten` });
    await dialog.getByLabel('Name', { exact: true }).fill(`${name} Neu`);
    const patched = waitForApi(page, `/clients/${client.id}`, 'PATCH');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    expect((await patched).status()).toBe(200);
    await expect(dialog).toBeHidden();
    expect(await clientNamed(`${name} Neu`)).toMatchObject({ id: client.id });

    // Delete an unused client after confirmation.
    const unused = await ok<Client>(rq, t.token, 'POST', '/clients', {
      name: `CA Leer ${s}`,
      slug: `ca-leer-${s}`,
    });
    await page.reload();
    await page.getByRole('button', { name: `Löschen: ${unused.name}` }).click();
    dialog = page.getByRole('dialog', { name: `Kunde „${unused.name}“ löschen?` });
    const deleted = waitForApi(page, `/clients/${unused.id}`, 'DELETE');
    await dialog.getByRole('button', { name: 'Löschen' }).click();
    expect((await deleted).status()).toBe(204);
    await expect(dialog).toBeHidden();
    await expect(page.locator('#main-content tr').filter({ hasText: unused.name })).toHaveCount(0);
    expect((await api(rq, t.token, 'GET', `/clients/${unused.id}`)).status).toBe(404);
  });

  test('C2 creates sites for a client of the page and for one created in the site dialog', async () => {
    const page = t.page;
    const client = await clientNamed(`CA Kunde ${s} Neu`);
    expect(client, 'client of C1').toBeDefined();

    const createSite = async (siteName: string, chooseClient: () => Promise<void>) => {
      await page.goto('/locations');
      await page.getByRole('button', { name: 'Standort anlegen' }).click();
      const dialog = page.getByRole('dialog', { name: 'Standort anlegen' });
      await dialog.getByLabel('Name', { exact: true }).fill(siteName);
      await chooseClient();
      const created = waitForApi(page, '/locations', 'POST');
      await dialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
      const res = await created;
      expect(res.status(), `POST /locations (site): ${await res.text()}`).toBe(201);
      await expect(dialog).toBeHidden();
      await expect(treeRow(page, siteName)).toBeVisible();
      return (await res.json()) as Location;
    };

    // A site for the client created on the page.
    const site = await createSite(`CA Standort ${s}`, async () => {
      await page
        .getByRole('dialog', { name: 'Standort anlegen' })
        .getByRole('combobox')
        .selectOption({ label: client!.name });
    });
    expect(site.client_id).toBe(client!.id);

    // A site for a client created from the site dialog: it is selected afterwards.
    const newName = `CA Dialog Kunde ${s}`;
    let fromDialog: Client | undefined;
    const second = await createSite(`CA Standort Zwei ${s}`, async () => {
      const dialog = page.getByRole('dialog', { name: 'Standort anlegen' });
      await dialog.getByRole('button', { name: 'Neuer Kunde' }).click();
      const clientDialog = page.getByRole('dialog', { name: 'Kunde anlegen' });
      await clientDialog.getByLabel('Name', { exact: true }).fill(newName);
      const created = waitForApi(page, '/clients', 'POST');
      await clientDialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
      const res = await created;
      expect(res.status(), `POST /clients (site dialog): ${await res.text()}`).toBe(201);
      fromDialog = (await res.json()) as Client;
      await expect(clientDialog).toBeHidden();
      await expect(dialog.getByRole('combobox').locator('option:checked')).toHaveText(newName);
    });
    expect(fromDialog?.slug).toBe(`ca-dialog-kunde-${s}`);
    expect(second.client_id).toBe(fromDialog?.id);
    const stored = await ok<Location>(rq, t.token, 'GET', `/locations/${second.id}`);
    expect(stored.client_id).toBe(fromDialog?.id);
  });
});
