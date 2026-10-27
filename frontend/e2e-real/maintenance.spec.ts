import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { api, expectModuleServed, stamp } from './support/stack';
import {
  dbRows,
  ok,
  openTenant,
  rows,
  uuid,
  waitForApi,
  type CI,
  type List,
  type Tenant,
} from './support/locations-racks';

/*
 * Maintenance announcement end to end on the real stack (WP-541, nothing mocked):
 * maintenance page → POST /maintenance-windows → POST /maintenance-windows/{id}/notify →
 * PostgreSQL (maintenance_notification) → GET /maintenance-windows/{id}/notifications → the
 * maintenance page after a reload. WP-063 was proven only by repository tests and a mocked
 * probe; WP-540 made the announcement idempotent and lets the page show the stored status.
 *
 * The module maintenance is a late module (P3) and off in the G1 default (E-03): this spec
 * runs only in the module run of install-smoke.yml against the server restarted with
 * RETICORA_MODULES=maintenance; playwright.real.config.ts leaves it out of the G1 run
 * (README.md, "Module run"). Against a G1 default stack it fails in beforeAll (module not
 * served), it does not skip.
 *
 * Organization A (self-signup UI) has the clients 1 and 2 with one CI each; organization B is
 * a second self-signup.
 *
 * Test → requirement IDs proven:
 *  M1 A plans a window in the UI with the CI of client 1 and the CI of client 2 and clicks
 *     „Kunden benachrichtigen“; after a reload the window's notifications show one row per
 *     client with the status „ausstehend“; the database holds exactly two
 *     maintenance_notification rows of the window (one per client) with status pending and
 *     sent_at NULL; a second click adds no row ............... MGT-04, NTF-05, NFR-04
 *                                                              (WP-063, WP-540, F-235, F-236)
 *  M2 a principal of organization B does not see A's window: GET /maintenance-windows/{id}
 *     answers 404, neither GET /maintenance-windows nor B's maintenance page lists it
 *     ........................................................ MGT-04, NFR-04 (WP-063)
 *
 * A failing test is a product finding: the assertions encode the requirement texts in
 * docs/spec/katalog-v3 (MGT-04, NTF-05, NFR-04) and the acceptance criteria of WP-541.
 */

interface MaintenanceWindow {
  id: string;
  organization_id: string;
  title: string;
  status: string;
  ci_ids?: string[];
}

interface NotificationRow {
  id: string;
  client_id: string;
  channel: string;
  status: string;
  sent_at: string | null;
}

interface World {
  rq: APIRequestContext;
  a: Tenant;
  b: Tenant;
  s: string;
  clients: { id: string; name: string }[];
  /** One CI per client, in the order of clients. */
  cis: CI[];
}

let w: World;

/** local formats a date for an <input type="datetime-local"> (local time, minutes). */
function local(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** windowCard is the card of the maintenance page that shows the window `title`. */
function windowCard(page: Page, title: string) {
  return page
    .locator('#main-content section')
    .filter({ has: page.getByRole('heading', { name: title, exact: true }) });
}

/** storedNotifications reads the notifications of one window from the stack database. */
function storedNotifications(windowId: string): NotificationRow[] {
  return dbRows<NotificationRow>(
    `SELECT id::text, client_id::text, channel, status, sent_at
       FROM maintenance_notification
      WHERE maintenance_window_id = '${uuid(windowId)}'
      ORDER BY client_id`,
  );
}

test.describe('maintenance announcement (MGT-04, NTF-05, NFR-04)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const a = await openTenant(browser, rq, 'maint-a');
    await expectModuleServed(rq, a.token, 'maintenance');
    const s = stamp();
    const types = await ok<List<{ id: string; key: string }>>(
      rq,
      a.token,
      'GET',
      '/ci-types?limit=200',
    );
    const serverType = rows(types).find((x) => x.key === 'server')?.id ?? '';
    expect(serverType, 'catalog type server').not.toBe('');
    const clients: { id: string; name: string }[] = [];
    const cis: CI[] = [];
    for (const n of [1, 2]) {
      const client = await ok<{ id: string; name: string }>(rq, a.token, 'POST', '/clients', {
        name: `Wartung Kunde ${n} ${s}`,
        slug: `wartung-kunde-${n}-${s}`,
      });
      clients.push(client);
      cis.push(
        await ok<CI>(rq, a.token, 'POST', '/cis', {
          ci_type_id: serverType,
          name: `wartung-ci-${n}-${s}`,
          client_id: client.id,
        }),
      );
    }
    const b = await openTenant(browser, rq, 'maint-b');
    w = { rq, a, b, s, clients, cis };
  });

  test.afterAll(async () => {
    await w?.a.page.context().close();
    await w?.b.page.context().close();
    await w?.rq.dispose();
  });

  test('M1 a window planned in the UI for CIs of two clients queues one pending notification per client, shown after reload, once', async () => {
    const page = w.a.page;
    const title = `Wartung UI ${w.s}`;
    const start = new Date(Date.now() + 7 * 24 * 3600_000);
    const end = new Date(start.getTime() + 2 * 3600_000);

    await page.goto('/maintenance');
    await expect(
      page.getByText('Wartungen planen und betroffene Kunden benachrichtigen.'),
    ).toBeVisible();
    await page.getByRole('button', { name: 'Wartung planen' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Titel', { exact: true }).fill(title);
    await dialog.getByLabel('Beginn', { exact: true }).fill(local(start));
    await dialog.getByLabel('Ende', { exact: true }).fill(local(end));
    for (const ci of w.cis) {
      await dialog.getByLabel('Betroffene CIs suchen').fill(ci.name);
      await dialog.getByRole('button', { name: `${ci.name} hinzufügen` }).click();
      await expect(
        dialog.getByRole('list', { name: 'Ausgewählte CIs' }).getByText(ci.name),
      ).toBeVisible();
    }
    const created = waitForApi(page, '/maintenance-windows', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen', exact: true }).click();
    const res = await created;
    expect(res.status(), `POST /maintenance-windows: ${await res.text()}`).toBe(201);
    const win = (await res.json()) as MaintenanceWindow;
    expect([...(win.ci_ids ?? [])].sort(), 'the window holds the two chosen CIs').toEqual(
      w.cis.map((ci) => ci.id).sort(),
    );
    const stored = await ok<MaintenanceWindow>(
      w.rq,
      w.a.token,
      'GET',
      `/maintenance-windows/${uuid(win.id)}`,
    );
    expect(stored.title).toBe(title);
    expect(stored.status).toBe('scheduled');

    // Announce: two clients are affected, nothing is delivered yet (pending).
    const card = windowCard(page, title);
    await expect(card).toHaveCount(1);
    const notified = waitForApi(page, `/maintenance-windows/${win.id}/notify`, 'POST');
    await card.getByRole('button', { name: 'Kunden benachrichtigen' }).click();
    const first = await notified;
    expect(first.status(), `POST notify: ${await first.text()}`).toBe(200);
    expect(await first.json()).toMatchObject({ notified: 0, queued: 2 });
    await expect(
      page.getByText('2 Benachrichtigungen vorgemerkt – Versand ausstehend', { exact: true }),
    ).toBeVisible();

    // After a reload the page shows the stored status per client.
    await page.reload();
    const reloaded = windowCard(page, title);
    await reloaded.getByRole('button', { name: 'Benachrichtigungen', exact: true }).click();
    const notifications = reloaded.getByTestId('maintenance-notification');
    await expect(notifications).toHaveCount(2);
    for (const client of w.clients) {
      const row = notifications.filter({ hasText: client.name });
      await expect(row, `notification row of ${client.name}`).toHaveCount(1);
      await expect(row.getByRole('cell').nth(2)).toHaveText('ausstehend');
    }

    // The database: exactly one pending, undelivered row per client.
    const rowsAfterFirst = storedNotifications(win.id);
    expect(rowsAfterFirst, JSON.stringify(rowsAfterFirst)).toHaveLength(2);
    expect(rowsAfterFirst.map((n) => n.client_id).sort()).toEqual(
      w.clients.map((c) => c.id).sort(),
    );
    for (const n of rowsAfterFirst) {
      expect(n.status, `status of ${n.id}`).toBe('pending');
      expect(n.sent_at, `sent_at of ${n.id}`).toBeNull();
    }

    // A second announcement queues nothing new (WP-540, F-235).
    const again = waitForApi(page, `/maintenance-windows/${win.id}/notify`, 'POST');
    await reloaded.getByRole('button', { name: 'Kunden benachrichtigen' }).click();
    const second = await again;
    expect(second.status(), `second POST notify: ${await second.text()}`).toBe(200);
    const rowsAfterSecond = storedNotifications(win.id);
    expect(rowsAfterSecond, JSON.stringify(rowsAfterSecond)).toHaveLength(2);
    expect(rowsAfterSecond.map((n) => n.id).sort()).toEqual(rowsAfterFirst.map((n) => n.id).sort());
    for (const n of rowsAfterSecond) {
      expect(n.status, `status of ${n.id} after the second click`).toBe('pending');
      expect(n.sent_at, `sent_at of ${n.id} after the second click`).toBeNull();
    }
    await page.reload();
    const final = windowCard(page, title);
    await final.getByRole('button', { name: 'Benachrichtigungen', exact: true }).click();
    await expect(final.getByTestId('maintenance-notification')).toHaveCount(2);
  });

  test('M2 a principal of organization B does not see the window of organization A', async () => {
    const title = `Wartung A ${w.s}`;
    const start = new Date(Date.now() + 14 * 24 * 3600_000);
    const win = await ok<MaintenanceWindow>(w.rq, w.a.token, 'POST', '/maintenance-windows', {
      title,
      starts_at: start.toISOString(),
      ends_at: new Date(start.getTime() + 3600_000).toISOString(),
      ci_ids: w.cis.map((ci) => ci.id),
    });
    expect(win.organization_id).toBe(w.a.orgId);
    // Control: A reads its window.
    expect(
      (await api(w.rq, w.a.token, 'GET', `/maintenance-windows/${uuid(win.id)}`)).status,
      'A reads its own window',
    ).toBe(200);

    await expectModuleServed(w.rq, w.b.token, 'maintenance');
    const detail = await api(w.rq, w.b.token, 'GET', `/maintenance-windows/${win.id}`);
    expect(
      detail.status,
      `GET /maintenance-windows/{id} as B: ${JSON.stringify(detail.body)}`,
    ).toBe(404);
    const list = await api<List<MaintenanceWindow>>(
      w.rq,
      w.b.token,
      'GET',
      '/maintenance-windows?limit=200',
    );
    expect(list.status, `GET /maintenance-windows as B: ${JSON.stringify(list.body)}`).toBe(200);
    expect(rows(list.body).map((x) => x.id)).not.toContain(win.id);
    expect(rows(list.body).map((x) => x.title)).not.toContain(title);

    // B's maintenance page does not list it either.
    const page = w.b.page;
    const loaded = waitForApi(page, '/maintenance-windows');
    await page.goto('/maintenance');
    expect((await loaded).status()).toBe(200);
    await expect(
      page.getByText('Wartungen planen und betroffene Kunden benachrichtigen.'),
    ).toBeVisible();
    await expect(page.getByText(title, { exact: true })).toHaveCount(0);
  });
});
