import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { api, stamp } from './support/stack';
import {
  childRow,
  dbRows,
  label,
  ok,
  openTenant,
  rows,
  treeRow,
  uuid,
  waitForApi,
  type CI,
  type List,
  type Location,
  type Problem,
  type Rack,
  type RackLayout,
  type RackMount,
  type Tenant,
} from './support/locations-racks';

/*
 * Phase-1 acceptance audit – locations and racks end-to-end (real stack, nothing mocked).
 *
 * One organization is created through the self-signup UI. Its administrator builds the
 * location tree in the location tree page (UI → POST /locations → PostgreSQL → tree after
 * reload), the rules of the tree and of rack placement are then driven through the REST API
 * the SPA uses (the UI offers no move or mount form) and every result is read back through
 * the API, read-only database probes (psql, E2E_DATABASE_URL, default
 * postgres://reticora:reticora@localhost:55432/e2e) and the pages that show it (location
 * tree, racks, room plan, CI detail history).
 *
 * Test → WP / requirement IDs proven:
 *  L01 site → building → room → rack in the tree UI ........ WP-026, WP-053, WP-108, WP-109
 *                                                              (LOC-07, LOC-10, LOC-06, TEN-01, TEN-02)
 *  L02 names unique per level (409 naming the field), rack
 *      height 1–60, explicit 0 → 422 ...................... WP-107, WP-108 (LOC-03, LOC-04, LOC-05, LOC-06)
 *  L03 cycle and parent-matrix violations are refused ...... WP-026, WP-053 (LOC-10, LOC-07)
 *  L04 non-empty location cannot be deleted (tree UI/API);
 *      an empty leaf can ................................... WP-055 (LOC-11)
 *  L05 DELETE /sites|/buildings|/rooms of a node with
 *      child locations → 409, subtree untouched ............ WP-055 (LOC-11)
 *  L06 mount a CI (PUT mounts/{ci_id}); rack page and room
 *      plan show it after reload; CI location and scope
 *      fields derived ....................................... WP-175, WP-078, WP-054, WP-025
 *                                                              (RCK-02, RCK-03, DB-05, LOC-10, TEN-02)
 *  L07 overlapping, face-aware, out-of-range and composition
 *      child mounts are refused ............................ WP-126, WP-175 (RCK-01, RCK-02)
 *  L08 move/unmount: ci_change, audit, unified history and
 *      the CI detail page ................................... WP-078, WP-171, WP-175 (RCK-03, CI-06, AUD-03, DB-05)
 *  L09 rack with a mounted CI cannot be deleted ............ WP-055 (LOC-11)
 *  L10 moving a room re-derives path/site of the subtree,
 *      CIs and mounts; location_change and audit ........... WP-055, WP-026, WP-109, WP-025
 *                                                              (LOC-11, LOC-10, TEN-01, TEN-02)
 *
 * The fixtures of L02–L10 are built through the REST API the UI uses (clients have no UI
 * form, mounts and moves no UI form). Each test builds its own hierarchy, so a failing test
 * does not influence the others (Playwright restarts the worker and beforeAll signs up a new
 * organization).
 */

interface AuditEntry {
  id: string;
  actor_id?: string;
  action: string;
  resource_type: string;
  resource_id?: string;
  changes?: Record<string, unknown>;
}

interface HistoryChange {
  id: string;
  change_type: string;
  field_name?: string;
  old_value?: unknown;
  new_value?: unknown;
  created_at: string;
  source?: string;
  site_id?: string;
}

interface CIChange {
  id: string;
  change_type: string;
  field_name?: string;
  old_value?: unknown;
  new_value?: unknown;
  actor_id?: string;
  created_at: string;
}

interface Hierarchy {
  site: Location;
  building: Location;
  room: Location;
  rack: Location;
}

interface World {
  rq: APIRequestContext;
  t: Tenant;
  s: string;
  serverType: string;
  client: { id: string; name: string };
  client2: { id: string; name: string };
}

let w: World;

/** at reads a nested value of an untyped JSON object. */
function at(value: unknown, ...keys: string[]): unknown {
  let cur = value;
  for (const k of keys) {
    if (cur === null || typeof cur !== 'object') return undefined;
    cur = (cur as Record<string, unknown>)[k];
  }
  return cur;
}

function violationFields(p: unknown): string[] {
  return ((p as Problem)?.violations ?? []).map((v) => v.field);
}

async function hierarchy(tag: string, clientId = w.client.id): Promise<Hierarchy> {
  const post = (body: Record<string, unknown>) =>
    ok<Location>(w.rq, w.t.token, 'POST', '/locations', body);
  const site = await post({ kind: 'site', name: `${tag} Site ${w.s}`, client_id: clientId });
  const building = await post({
    kind: 'building',
    name: `${tag} Building ${w.s}`,
    parent_id: site.id,
  });
  const room = await post({ kind: 'room', name: `${tag} Room ${w.s}`, parent_id: building.id });
  const rack = await post({ kind: 'rack', name: `${tag} Rack ${w.s}`, parent_id: room.id });
  return { site, building, room, rack };
}

function newCI(name: string, extra: Record<string, unknown> = {}) {
  return ok<CI>(w.rq, w.t.token, 'POST', '/cis', {
    ci_type_id: w.serverType,
    name: `${name}-${w.s}`,
    client_id: w.client.id,
    ...extra,
  });
}

function putMount(rackId: string, ciId: string, body: Record<string, unknown>) {
  return api<RackMount & Problem>(w.rq, w.t.token, 'PUT', `/racks/${rackId}/mounts/${ciId}`, body);
}

async function layout(rackId: string): Promise<RackLayout> {
  return ok<RackLayout>(w.rq, w.t.token, 'GET', `/racks/${rackId}/layout`);
}

async function getLocation(id: string) {
  return ok<Location>(w.rq, w.t.token, 'GET', `/locations/${id}`);
}

/** mountRows reads the rack_mount rows of a rack from the database. */
function mountRows(rackId: string) {
  return dbRows<{
    ci_id: string;
    position_u: number;
    height_u: number;
    face: string;
    client_id: string | null;
    site_id: string | null;
  }>(
    `SELECT ci_id::text, position_u, height_u, face, client_id::text, site_id::text
       FROM rack_mount WHERE organization_id = '${uuid(w.t.orgId)}' AND rack_id = '${uuid(rackId)}'
      ORDER BY position_u, face`,
  );
}

function ciRow(ciId: string) {
  return dbRows<{
    location_id: string | null;
    site_id: string | null;
    room_id: string | null;
    client_id: string | null;
  }>(
    `SELECT location_id::text, site_id::text, room_id::text, client_id::text
       FROM ci WHERE organization_id = '${uuid(w.t.orgId)}' AND id = '${uuid(ciId)}'`,
  )[0];
}

async function auditOf(resourceId: string): Promise<AuditEntry[]> {
  const res = await ok<List<AuditEntry>>(
    w.rq,
    w.t.token,
    'GET',
    `/audit-log?resource_id=${resourceId}&limit=200`,
  );
  return rows(res);
}

/** openRack selects the rack on the racks page and waits for its layout. */
async function openRack(page: Page, rack: { id: string; name: string }, heightUnits = 42) {
  const select = page.getByLabel('Rack auswählen');
  await expect(select.locator('option', { hasText: rack.name })).toHaveCount(1);
  const current = await select.inputValue();
  if (current !== rack.id) {
    const loaded = waitForApi(page, `/racks/${rack.id}/layout`);
    await select.selectOption({ label: `${rack.name} (${heightUnits} HE)` });
    await loaded;
  }
  await expect(page.getByRole('heading', { name: `${rack.name} · Vorderseite` })).toBeVisible();
}

/** mountLine is the entry of a CI in the mount list of the racks page. */
function mountLine(page: Page, ciName: string) {
  return page
    .locator('#main-content li')
    .filter({ has: page.getByRole('button', { name: ciName, exact: true }) });
}

test.describe('locations and racks (LOC-*, RCK-*)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const t = await openTenant(browser, rq, 'locrack');
    const s = stamp();
    const types = await ok<List<{ id: string; key: string }>>(
      rq,
      t.token,
      'GET',
      '/ci-types?limit=200',
    );
    const serverType = rows(types).find((x) => x.key === 'server')?.id ?? '';
    expect(serverType, 'catalog type server').not.toBe('');
    const client = await ok<{ id: string; name: string }>(rq, t.token, 'POST', '/clients', {
      name: `LR Client ${s}`,
      slug: `lr-client-${s}`,
    });
    const client2 = await ok<{ id: string; name: string }>(rq, t.token, 'POST', '/clients', {
      name: `LR Client Two ${s}`,
      slug: `lr-client-two-${s}`,
    });
    w = { rq, t, s, serverType, client, client2 };
  });

  test.afterAll(async () => {
    await w?.t.page.context().close();
    await w?.rq.dispose();
  });

  test('L01 the tree UI builds site → building → room → rack with derived client, site and path', async () => {
    const page = w.t.page;
    const n = {
      site: `LR UI Site ${w.s}`,
      building: `LR UI Gebäude ${w.s}`,
      room: `LR UI Raum ${w.s}`,
      rack: `LR UI Rack ${w.s}`,
    };
    await page.goto('/locations');
    await expect(page.getByRole('heading', { name: 'Standorte & Lager' })).toBeVisible();

    // Root: a site needs a client (LOC-03, LOC-10).
    await page.getByRole('button', { name: 'Standort anlegen' }).click();
    let dialog = page.getByRole('dialog');
    await dialog.getByLabel('Name', { exact: true }).fill(n.site);
    await dialog.getByLabel('Mandant').selectOption({ label: w.client.name });
    let created = waitForApi(page, '/locations', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    let res = await created;
    expect(res.status(), `POST /locations (site): ${await res.text()}`).toBe(201);
    const site = (await res.json()) as Location;
    await expect(dialog).toBeHidden();
    await expect(treeRow(page, n.site)).toBeVisible();

    // Children: each node offers exactly the kinds of the parent matrix (LOC-10).
    const addChild = async (
      parent: string,
      kind: string,
      name: string,
      offered: string[],
    ): Promise<Location> => {
      await treeRow(page, parent).getByRole('button', { name: '+ Unterpunkt' }).click();
      dialog = page.getByRole('dialog', { name: `Unterpunkt in ${parent}` });
      await expect(dialog).toBeVisible();
      const typ = dialog.getByLabel('Typ');
      const options = (await typ.locator('option').allTextContents()).map((o) =>
        o.trim().split(' ').slice(1).join(' '),
      );
      expect(options, `kinds offered below ${parent}`).toEqual(offered);
      await typ.selectOption(kind);
      await dialog.getByLabel('Name', { exact: true }).fill(name);
      created = waitForApi(page, '/locations', 'POST');
      await dialog.getByRole('button', { name: 'Anlegen' }).click();
      res = await created;
      expect(res.status(), `POST /locations (${kind}): ${await res.text()}`).toBe(201);
      await expect(dialog).toBeHidden();
      return (await res.json()) as Location;
    };
    const building = await addChild(n.site, 'building', n.building, ['Gebäude', 'Lager']);
    await expect(childRow(page, n.site, n.building)).toBeVisible();
    const room = await addChild(n.building, 'room', n.room, ['Raum']);
    await expect(childRow(page, n.building, n.room)).toBeVisible();
    // The room (depth 2) starts collapsed once it has children.
    const rack = await addChild(n.room, 'rack', n.rack, ['Rack']);
    const roomToggle = treeRow(page, n.room).getByRole('button', { name: 'expand' });
    if (await roomToggle.isVisible()) await roomToggle.click();
    await expect(childRow(page, n.room, n.rack)).toBeVisible();
    // A rack is a leaf of the matrix: no "add child".
    await expect(treeRow(page, n.rack).getByRole('button', { name: '+ Unterpunkt' })).toHaveCount(
      0,
    );

    // UI → API: parents, derived client/site and ltree path (LOC-10, TEN-02).
    const expected: [Location, string | undefined, string, string][] = [
      [site, undefined, 'site', label(site.id)],
      [building, site.id, 'building', `${label(site.id)}.${label(building.id)}`],
      [room, building.id, 'room', `${label(site.id)}.${label(building.id)}.${label(room.id)}`],
      [
        rack,
        room.id,
        'rack',
        `${label(site.id)}.${label(building.id)}.${label(room.id)}.${label(rack.id)}`,
      ],
    ];
    for (const [loc, parent, kind, path] of expected) {
      const got = await getLocation(loc.id);
      expect(got, `${kind} via API`).toMatchObject({
        organization_id: w.t.orgId,
        kind,
        client_id: w.client.id,
        site_id: site.id,
        path,
      });
      expect(got.parent_id || undefined, `${kind} parent`).toBe(parent);
    }

    // The tree endpoint returns every level (WP-053 BuildTree regression).
    const tree = await ok<List<Location & { children: (Location & { children: unknown[] })[] }>>(
      w.rq,
      w.t.token,
      'GET',
      '/locations/tree',
    );
    const siteNode = rows(tree).find((x) => x.id === site.id);
    expect(siteNode, 'site is a root of the tree').toBeTruthy();
    const bNode = siteNode!.children.find((x) => x.id === building.id) as
      (Location & { children: (Location & { children: Location[] })[] }) | undefined;
    expect(bNode, 'building below the site').toBeTruthy();
    const rNode = bNode!.children.find((x) => x.id === room.id);
    expect(rNode, 'room below the building').toBeTruthy();
    expect(rNode!.children.map((x) => x.id)).toEqual([rack.id]);

    // API → DB: location rows and their 1:1 specialist rows (LOC-10, LOC-03..06).
    const ids = [site.id, building.id, room.id, rack.id].map((id) => `'${uuid(id)}'`).join(',');
    const db = dbRows<{
      id: string;
      kind: string;
      parent_id: string | null;
      client_id: string;
      site_id: string;
      path: string;
    }>(
      `SELECT id::text, kind, parent_id::text, client_id::text, site_id::text, path::text
         FROM location WHERE organization_id = '${uuid(w.t.orgId)}' AND id IN (${ids})`,
    );
    expect(db).toHaveLength(4);
    for (const [loc, parent, kind, path] of expected) {
      expect(db.find((r) => r.id === loc.id)).toEqual({
        id: loc.id,
        kind,
        parent_id: parent ?? null,
        client_id: w.client.id,
        site_id: site.id,
        path,
      });
    }
    const specialist = dbRows<{ t: string; parent: string; name: string }>(
      `SELECT 'site' AS t, client_id::text AS parent, name FROM site WHERE id = '${uuid(site.id)}'
       UNION ALL SELECT 'building', site_id::text, name FROM building WHERE id = '${uuid(building.id)}'
       UNION ALL SELECT 'room', building_id::text, name FROM room WHERE id = '${uuid(room.id)}'
       UNION ALL SELECT 'rack', room_id::text, name FROM rack WHERE id = '${uuid(rack.id)}'`,
    );
    expect(specialist.sort((a, b) => a.t.localeCompare(b.t))).toEqual([
      { t: 'building', parent: site.id, name: n.building },
      { t: 'rack', parent: room.id, name: n.rack },
      { t: 'room', parent: building.id, name: n.room },
      { t: 'site', parent: w.client.id, name: n.site },
    ]);

    // A rack created in the tree is a rack with the defaults of LOC-06.
    const rackRes = await ok<Rack>(w.rq, w.t.token, 'GET', `/racks/${rack.id}`);
    expect(rackRes).toMatchObject({
      room_id: room.id,
      name: n.rack,
      height_units: 42,
      width_mm: 600,
      depth_mm: 1000,
    });

    // DB → UI after reload: nested tree; the rack is offered on the racks page.
    await page.reload();
    await expect(treeRow(page, n.site)).toBeVisible();
    await expect(childRow(page, n.site, n.building)).toBeVisible();
    await expect(childRow(page, n.building, n.room)).toBeVisible();
    await treeRow(page, n.room).getByRole('button', { name: 'expand' }).click();
    await expect(childRow(page, n.room, n.rack)).toBeVisible();
    await expect(treeRow(page, n.rack)).toContainText('Rack');
    await page.goto('/racks');
    await expect(
      page.getByLabel('Rack auswählen').locator('option', { hasText: `${n.rack} (42 HE)` }),
    ).toHaveCount(1);
  });

  test('L02 names are unique per parent level and the rack height is 1–60 (explicit 0 → 422)', async () => {
    const page = w.t.page;
    const h = await hierarchy('L02');

    // Site name unique per (org, client) – through the tree UI (LOC-03).
    await page.goto('/locations');
    await page.getByRole('button', { name: 'Standort anlegen' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Name', { exact: true }).fill(h.site.name);
    await dialog.getByLabel('Mandant').selectOption({ label: w.client.name });
    const created = waitForApi(page, '/locations', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    const res = await created;
    const body = await res.text();
    expect(res.status(), `duplicate site name in one client: ${body}`).toBe(409);
    // The dialog stays open and tells the user what is wrong.
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('[role="alert"], .text-red-600').first()).toBeVisible();
    await dialog.getByRole('button', { name: 'Abbrechen' }).click();
    // The conflict names the field (WP-107: "Konflikte liefern 409 mit Feldbezug"), on the
    // tree endpoint the dialog uses as on the site resource itself. Soft, so that the
    // remaining uniqueness rules are still checked when this one fails.
    expect
      .soft(body, 'POST /locations: the 409 of a duplicate site names the field name')
      .toMatch(/"field"\s*:\s*"name"/);
    const viaSites = await api<Problem>(w.rq, w.t.token, 'POST', '/sites', {
      client_id: w.client.id,
      name: h.site.name,
    });
    expect(viaSites.status, `POST /sites duplicate: ${JSON.stringify(viaSites.body)}`).toBe(409);
    expect
      .soft(JSON.stringify(viaSites.body), 'POST /sites: the 409 names the field name')
      .toMatch(/"field"\s*:\s*"name"/);
    expect(
      dbRows(
        `SELECT id FROM site WHERE organization_id = '${uuid(w.t.orgId)}' AND name = '${h.site.name.replace(/'/g, "''")}'`,
      ),
      'one site of that name',
    ).toHaveLength(1);
    // The same name in another client is allowed (unique per (org, client_id)).
    const other = await api<Location>(w.rq, w.t.token, 'POST', '/locations', {
      kind: 'site',
      name: h.site.name,
      client_id: w.client2.id,
    });
    expect(other.status, `same site name, other client: ${JSON.stringify(other.body)}`).toBe(201);

    // Building unique per site, room per building, rack per room (LOC-04..06).
    const dupes: [string, string, string][] = [
      ['building', h.building.name, h.site.id],
      ['room', h.room.name, h.building.id],
      ['rack', h.rack.name, h.room.id],
    ];
    for (const [kind, name, parent] of dupes) {
      const dup = await api<Problem>(w.rq, w.t.token, 'POST', '/locations', {
        kind,
        name,
        parent_id: parent,
      });
      expect(
        [409, 422],
        `duplicate ${kind} name: ${dup.status} ${JSON.stringify(dup.body)}`,
      ).toContain(dup.status);
    }
    const rackDup = await api<Problem>(w.rq, w.t.token, 'POST', '/racks', {
      room_id: h.room.id,
      name: h.rack.name,
    });
    expect([409, 422], `duplicate rack via /racks: ${rackDup.status}`).toContain(rackDup.status);
    const counts = dbRows<{ kind: string; n: number }>(
      `SELECT kind, count(*)::int AS n FROM location
        WHERE organization_id = '${uuid(w.t.orgId)}' AND site_id = '${uuid(h.site.id)}' GROUP BY kind ORDER BY kind`,
    );
    expect(counts).toEqual([
      { kind: 'building', n: 1 },
      { kind: 'rack', n: 1 },
      { kind: 'room', n: 1 },
      { kind: 'site', n: 1 },
    ]);
    // The same building name below another site is fine.
    const h2site = await ok<Location>(w.rq, w.t.token, 'POST', '/locations', {
      kind: 'site',
      name: `L02 Second Site ${w.s}`,
      client_id: w.client.id,
    });
    const sameName = await api<Location>(w.rq, w.t.token, 'POST', '/locations', {
      kind: 'building',
      name: h.building.name,
      parent_id: h2site.id,
    });
    expect(sameName.status, 'same building name below another site').toBe(201);

    // height_units: 1–60, default 42; 0 and 61 are 422 naming the field (LOC-06, WP-108).
    for (const height of [0, 61, -1]) {
      const bad = await api<Problem>(w.rq, w.t.token, 'POST', '/racks', {
        room_id: h.room.id,
        name: `L02 Rack h${height} ${w.s}`,
        height_units: height,
      });
      expect(bad.status, `POST /racks height_units ${height}`).toBe(422);
      expect(JSON.stringify(bad.body)).toContain('height_units');
    }
    const tall = await api<Rack>(w.rq, w.t.token, 'POST', '/racks', {
      room_id: h.room.id,
      name: `L02 Rack 60 ${w.s}`,
      height_units: 60,
    });
    expect(tall.status).toBe(201);
    expect(tall.body.height_units).toBe(60);
    const dflt = await ok<Rack>(w.rq, w.t.token, 'POST', '/racks', {
      room_id: h.room.id,
      name: `L02 Rack default ${w.s}`,
    });
    expect(dflt).toMatchObject({ height_units: 42, width_mm: 600, depth_mm: 1000 });
    for (const height of [0, 61]) {
      const bad = await api<Problem>(w.rq, w.t.token, 'PATCH', `/racks/${dflt.id}`, {
        height_units: height,
      });
      expect(bad.status, `PATCH /racks height_units ${height}`).toBe(422);
    }
    expect(
      dbRows<{ height_units: number }>(
        `SELECT height_units FROM rack WHERE id = '${uuid(dflt.id)}'`,
      ),
    ).toEqual([{ height_units: 42 }]);
    // The rack created via /racks is a node of the canonical tree below its room.
    const asNode = await getLocation(dflt.id);
    expect(asNode).toMatchObject({ kind: 'rack', parent_id: h.room.id, site_id: h.site.id });
  });

  test('L03 a cycle or a parent-matrix violation is refused with 422 on parent_id and changes nothing', async () => {
    const h = await hierarchy('L03');
    const before = await Promise.all(
      [h.site, h.building, h.room, h.rack].map((l) => getLocation(l.id)),
    );

    const attempts: [string, 'PATCH' | 'POST', string, Record<string, unknown>][] = [
      [
        'building below its own room (cycle)',
        'PATCH',
        `/locations/${h.building.id}`,
        { parent_id: h.room.id },
      ],
      [
        'building below its own rack (cycle)',
        'PATCH',
        `/locations/${h.building.id}`,
        { parent_id: h.rack.id },
      ],
      ['room below itself', 'PATCH', `/locations/${h.room.id}`, { parent_id: h.room.id }],
      ['site below its building', 'PATCH', `/locations/${h.site.id}`, { parent_id: h.building.id }],
      ['rack below a building', 'PATCH', `/locations/${h.rack.id}`, { parent_id: h.building.id }],
      [
        'room directly below a site',
        'POST',
        '/locations',
        { kind: 'room', name: `L03 bad room ${w.s}`, parent_id: h.site.id },
      ],
      [
        'building without parent',
        'POST',
        '/locations',
        { kind: 'building', name: `L03 orphan ${w.s}` },
      ],
      [
        'rack below a rack',
        'POST',
        '/locations',
        { kind: 'rack', name: `L03 rack in rack ${w.s}`, parent_id: h.rack.id },
      ],
    ];
    for (const [what, method, path, data] of attempts) {
      const res = await api<Problem>(w.rq, w.t.token, method, path, data);
      expect(res.status, `${what}: ${JSON.stringify(res.body)}`).toBe(422);
      // RFC 7807 with the offending field (WP-053).
      expect(violationFields(res.body), `${what}: field`).toEqual(['parent_id']);
      expect((res.body as Problem).status, `${what}: problem status`).toBe(422);
    }
    const siteWithoutClient = await api<Problem>(w.rq, w.t.token, 'POST', '/locations', {
      kind: 'site',
      name: `L03 no client ${w.s}`,
    });
    expect(siteWithoutClient.status).toBe(422);
    expect(violationFields(siteWithoutClient.body)).toEqual(['client_id']);

    // Nothing changed: API, database, no location_change rows, tree UI.
    const after = await Promise.all(
      [h.site, h.building, h.room, h.rack].map((l) => getLocation(l.id)),
    );
    expect(after.map((l) => [l.id, l.parent_id ?? '', l.path, l.site_id])).toEqual(
      before.map((l) => [l.id, l.parent_id ?? '', l.path, l.site_id]),
    );
    expect(
      dbRows(
        `SELECT id FROM location_change WHERE organization_id = '${uuid(w.t.orgId)}'
           AND location_id IN ('${uuid(h.building.id)}','${uuid(h.room.id)}','${uuid(h.site.id)}','${uuid(h.rack.id)}')`,
      ),
      'no move was recorded',
    ).toEqual([]);
    const refused = ['L03 bad room', 'L03 orphan', 'L03 rack in rack', 'L03 no client']
      .map((n) => `'${n} ${w.s}'`)
      .join(',');
    expect(
      dbRows(
        `SELECT id FROM location WHERE organization_id = '${uuid(w.t.orgId)}' AND name IN (${refused})`,
      ),
      'no invalid node was created',
    ).toEqual([]);
    const page = w.t.page;
    await page.goto('/locations');
    await expect(childRow(page, h.site.name, h.building.name)).toBeVisible();
    await expect(childRow(page, h.building.name, h.room.name)).toBeVisible();
  });

  test('L04 a non-empty location cannot be deleted in the tree UI (409), an empty leaf can', async () => {
    const page = w.t.page;
    const h = await hierarchy('L04');
    const empty = await ok<Location>(w.rq, w.t.token, 'POST', '/locations', {
      kind: 'building',
      name: `L04 Empty ${w.s}`,
      parent_id: h.site.id,
    });

    await page.goto('/locations');
    // Site with children → refused, message, still there after reload.
    const del = waitForApi(page, `/locations/${h.site.id}`, 'DELETE');
    await treeRow(page, h.site.name).getByRole('button', { name: 'Löschen' }).click();
    const res = await del;
    const text = await res.text();
    expect(res.status(), `DELETE site with children: ${text}`).toBe(409);
    expect((JSON.parse(text) as Problem).dependencies).toContain('location');
    await expect(
      page.getByRole('alert').filter({ hasText: 'Löschen nicht möglich' }),
    ).toBeVisible();
    // Room with a rack → refused as well (API).
    const roomDel = await api<Problem>(w.rq, w.t.token, 'DELETE', `/locations/${h.room.id}`);
    expect(roomDel.status).toBe(409);
    expect(roomDel.body.dependencies).toContain('location');
    await page.reload();
    await expect(childRow(page, h.site.name, h.building.name)).toBeVisible();
    await expect(childRow(page, h.building.name, h.room.name)).toBeVisible();
    expect(
      dbRows(
        `SELECT id FROM location WHERE organization_id = '${uuid(w.t.orgId)}' AND site_id = '${uuid(h.site.id)}'`,
      ),
    ).toHaveLength(5);

    // An empty leaf is deleted through the UI, recorded in the audit and gone after reload.
    const leafDel = waitForApi(page, `/locations/${empty.id}`, 'DELETE');
    await treeRow(page, empty.name).getByRole('button', { name: 'Löschen' }).click();
    expect((await leafDel).status()).toBe(204);
    await expect(treeRow(page, empty.name)).toHaveCount(0);
    await page.reload();
    await expect(treeRow(page, h.site.name)).toBeVisible();
    await expect(treeRow(page, empty.name)).toHaveCount(0);
    expect((await api(w.rq, w.t.token, 'GET', `/locations/${empty.id}`)).status).toBe(404);
    expect(dbRows(`SELECT id FROM building WHERE id = '${uuid(empty.id)}'`)).toEqual([]);
    const audit = await auditOf(empty.id);
    expect(audit.map((a) => a.action)).toContain('location.deleted');
  });

  test('L05 deleting a site, building or room with child locations through /sites, /buildings or /rooms is refused (409)', async () => {
    // LOC-11: "Locations mit ... Kindern können nicht gelöscht werden (409 conflict)" – for
    // every delete path of the API, not only /locations. One hierarchy per path, so that a
    // delete that wrongly succeeds does not hide the result of the others.
    const cases: [string, (h: Hierarchy) => string][] = [
      ['rooms', (h) => h.room.id],
      ['buildings', (h) => h.building.id],
      ['sites', (h) => h.site.id],
    ];
    for (const [resource, target] of cases) {
      const h = await hierarchy(`L05 ${resource}`);
      const res = await api<Problem>(w.rq, w.t.token, 'DELETE', `/${resource}/${target(h)}`);
      expect
        .soft(
          res.status,
          `DELETE /${resource}/{id} with child locations: ${JSON.stringify(res.body)}`,
        )
        .toBe(409);
      if (res.status === 409) {
        expect.soft(res.body.dependencies, `/${resource}: dependency kinds`).toContain('location');
      }
      const ids = [h.site.id, h.building.id, h.room.id, h.rack.id].map((id) => `'${uuid(id)}'`);
      expect
        .soft(
          dbRows<{ kind: string }>(
            `SELECT kind FROM location WHERE organization_id = '${uuid(w.t.orgId)}'
               AND id IN (${ids.join(',')}) ORDER BY kind`,
          ).map((r) => r.kind),
          `DELETE /${resource}/{id}: the subtree is untouched`,
        )
        .toEqual(['building', 'rack', 'room', 'site']);
    }
  });

  test('L06 a CI mounted at a U position shows in the rack layout and room plan after reload; location and scope are derived', async () => {
    const page = w.t.page;
    const h = await hierarchy('L06');
    const ci = await newCI('lr-mount', { location_id: h.room.id });

    const put = await putMount(h.rack.id, ci.id, { position_u: 10, height_u: 2, face: 'front' });
    expect(put.status, `PUT mounts/{ci_id}: ${JSON.stringify(put.body)}`).toBe(201);
    expect(put.body).toMatchObject({
      rack_id: h.rack.id,
      ci_id: ci.id,
      position_u: 10,
      height_u: 2,
      face: 'front',
    });

    // API: layout with free units per face (RCK-02).
    const lay = await layout(h.rack.id);
    expect(lay.rack.id).toBe(h.rack.id);
    expect(lay.mounts.map((m) => [m.ci_id, m.position_u, m.height_u, m.face])).toEqual([
      [ci.id, 10, 2, 'front'],
    ]);
    expect(lay.free.front).not.toContain(10);
    expect(lay.free.front).not.toContain(11);
    expect(lay.free.front).toContain(9);
    expect(lay.free.front).toContain(12);
    expect(lay.free.front).toHaveLength(40);
    expect(lay.free.rear).toHaveLength(42);

    // The CI's location is the rack; site and room are derived (RCK-03, DB-05).
    const got = await ok<CI>(w.rq, w.t.token, 'GET', `/cis/${ci.id}`);
    expect(got).toMatchObject({
      location_id: h.rack.id,
      site_id: h.site.id,
      room_id: h.room.id,
      client_id: w.client.id,
    });
    // DB: mount row with the CI's client and site (TEN-02), CI row.
    expect(mountRows(h.rack.id)).toEqual([
      {
        ci_id: ci.id,
        position_u: 10,
        height_u: 2,
        face: 'front',
        client_id: w.client.id,
        site_id: h.site.id,
      },
    ]);
    expect(ciRow(ci.id)).toEqual({
      location_id: h.rack.id,
      site_id: h.site.id,
      room_id: h.room.id,
      client_id: w.client.id,
    });

    // UI: racks page after reload.
    await page.goto('/racks');
    await openRack(page, h.rack);
    await page.reload();
    await openRack(page, h.rack);
    await expect(mountLine(page, ci.name)).toContainText('Position 10 · 2 HE · front');
    await expect(page.locator('#main-content svg text', { hasText: ci.name })).toHaveCount(1);
    // The rear face is empty.
    await page.getByRole('button', { name: 'Rückseite', exact: true }).click();
    await expect(page.getByText('Keine Einbauten in diesem Rack')).toBeVisible();

    // UI: room plan of the rack's room lists the CI after reload.
    await page.goto('/roomplan');
    const pickRoom = async () => {
      await page
        .getByRole('combobox', { name: 'Gebäude' })
        .selectOption({ label: h.building.name });
      await page.getByRole('combobox', { name: 'Raum' }).selectOption({ label: h.room.name });
    };
    await pickRoom();
    const plan = page.getByTestId('room-plan');
    await expect(plan.getByRole('button', { name: new RegExp(`^${ci.name},`) })).toBeVisible();
    await page.reload();
    await pickRoom();
    await expect(plan.getByRole('button', { name: new RegExp(`^${ci.name},`) })).toBeVisible();
  });

  test('L07 overlapping, face-colliding, out-of-range and composition-child mounts are refused', async () => {
    const h = await hierarchy('L07');
    const a = await newCI('lr-a');
    const b = await newCI('lr-b');
    const c = await newCI('lr-c');
    const first = await putMount(h.rack.id, a.id, { position_u: 10, height_u: 2, face: 'front' });
    expect(first.status).toBe(201);

    // Collisions and height overflow are 409 (RCK-01, RCK-02).
    const conflicts: [string, Record<string, unknown>][] = [
      ['same unit', { position_u: 10, face: 'front' }],
      ['upper unit of the 2U mount', { position_u: 11, face: 'front' }],
      ['2U ending in the mount', { position_u: 9, height_u: 2, face: 'front' }],
      ['both faces over a front mount', { position_u: 10, face: 'both' }],
      ['beyond the top (42 + 2 − 1 > 42)', { position_u: 42, height_u: 2, face: 'front' }],
      ['position 43 of a 42U rack', { position_u: 43, face: 'front' }],
    ];
    for (const [what, body] of conflicts) {
      const res = await putMount(h.rack.id, b.id, body);
      expect(res.status, `${what}: ${JSON.stringify(res.body)}`).toBe(409);
    }
    // Below the first unit / non-positive height: refused (position_u ≥ 1, height_u ≥ 1).
    for (const [what, body] of [
      ['position 0', { position_u: 0, face: 'front' }],
      ['position −1', { position_u: -1, face: 'front' }],
      ['height −1', { position_u: 30, height_u: -1, face: 'front' }],
      ['unknown face', { position_u: 30, face: 'side' }],
    ] as [string, Record<string, unknown>][]) {
      const res = await putMount(h.rack.id, b.id, body);
      expect([400, 409, 422], `${what}: ${res.status} ${JSON.stringify(res.body)}`).toContain(
        res.status,
      );
    }
    expect(
      mountRows(h.rack.id).map((m) => m.ci_id),
      'only A is mounted',
    ).toEqual([a.id]);
    expect(ciRow(b.id).location_id, 'B was not placed').toBeNull();

    // Face-aware: the rear of the same units is free (RCK-01 face_range).
    const rear = await putMount(h.rack.id, b.id, { position_u: 10, face: 'rear' });
    expect(rear.status, `rear at U10: ${JSON.stringify(rear.body)}`).toBe(201);
    // Now U10 is taken on both faces: "both" collides, and moving B onto A's front too.
    const both = await putMount(h.rack.id, c.id, { position_u: 10, face: 'both' });
    expect(both.status).toBe(409);
    const moveOnto = await putMount(h.rack.id, b.id, { position_u: 11, face: 'front' });
    expect(moveOnto.status, 'moving B onto A').toBe(409);
    // A free unit on both faces is fine.
    const bothOk = await putMount(h.rack.id, c.id, { position_u: 20, face: 'both' });
    expect(bothOk.status).toBe(201);

    // A child CI of a composition (AST-05) is not mounted on its own (RCK-01, WP-126).
    const child = await newCI('lr-child', { parent_ci_id: a.id });
    const childMount = await putMount(h.rack.id, child.id, { position_u: 30, face: 'front' });
    expect(
      [409, 422],
      `composition child: ${childMount.status} ${JSON.stringify(childMount.body)}`,
    ).toContain(childMount.status);
    // Unknown rack → 404.
    const noRack = await putMount('00000000-0000-4000-8000-000000000000', c.id, { position_u: 1 });
    expect(noRack.status).toBe(404);

    // Exactly the accepted placements are stored.
    expect(mountRows(h.rack.id).map((m) => [m.ci_id, m.position_u, m.height_u, m.face])).toEqual([
      [a.id, 10, 2, 'front'],
      [b.id, 10, 1, 'rear'],
      [c.id, 20, 1, 'both'],
    ]);
    const lay = await layout(h.rack.id);
    expect(lay.mounts).toHaveLength(3);
    expect(lay.free.rear).not.toContain(10);
    expect(lay.free.rear).not.toContain(20);
    expect(lay.free.front).not.toContain(20);
  });

  test('L08 moving a mounted CI is recorded in ci_change, audit and the unified history shown on the CI page', async () => {
    const page = w.t.page;
    const h = await hierarchy('L08');
    const rack2 = await ok<Rack>(w.rq, w.t.token, 'POST', '/racks', {
      room_id: h.room.id,
      name: `L08 Rack Two ${w.s}`,
    });
    const ci = await newCI('lr-move', { location_id: h.room.id });

    expect((await putMount(h.rack.id, ci.id, { position_u: 10, face: 'front' })).status).toBe(201);
    const moved = await putMount(h.rack.id, ci.id, { position_u: 20, face: 'front' });
    expect(moved.status, `move: ${JSON.stringify(moved.body)}`).toBe(200);
    expect(moved.body).toMatchObject({ rack_id: h.rack.id, ci_id: ci.id, position_u: 20 });
    let lay = await layout(h.rack.id);
    expect(lay.mounts.map((m) => [m.ci_id, m.position_u])).toEqual([[ci.id, 20]]);
    expect(lay.free.front).toContain(10);
    expect(mountRows(h.rack.id).map((m) => m.position_u)).toEqual([20]);

    // ci_change: the move carries old and new placement (RCK-03).
    const changes = rows(
      await ok<List<CIChange>>(w.rq, w.t.token, 'GET', `/cis/${ci.id}/changes?limit=100`),
    );
    const moveChange = changes.find(
      (c) => at(c.old_value, 'position_u') === 10 && at(c.new_value, 'position_u') === 20,
    );
    expect(moveChange, `ci_change of the move in ${JSON.stringify(changes)}`).toBeTruthy();
    expect(at(moveChange!.new_value, 'rack_id')).toBe(h.rack.id);
    const mountChange = changes.find(
      (c) =>
        (c.old_value === null || c.old_value === undefined) && at(c.new_value, 'position_u') === 10,
    );
    expect(mountChange, 'ci_change of the mount').toBeTruthy();
    expect(
      changes.some((c) => c.old_value === h.room.id && c.new_value === h.rack.id),
      'ci_change of the location (room → rack)',
    ).toBe(true);

    // Audit: mount and move with before/after and the acting user (RCK-03, AUD-03).
    const audit = await auditOf(ci.id);
    const moveAudit = audit.find(
      (a) =>
        at(a.changes, 'before', 'position_u') === 10 && at(a.changes, 'after', 'position_u') === 20,
    );
    expect(
      moveAudit,
      `audit of the move in ${JSON.stringify(audit.map((a) => a.action))}`,
    ).toBeTruthy();
    expect(moveAudit!.resource_type).toBe('ci');
    expect(moveAudit!.actor_id, 'the move names the user').toBeTruthy();
    expect(
      audit.some(
        (a) => at(a.changes, 'before') === null && at(a.changes, 'after', 'position_u') === 10,
      ),
      'audit of the mount',
    ).toBe(true);

    // Unified history (CI-06/WP-171): ci_change and audit entries with the site at the time.
    const history = rows(
      await ok<List<HistoryChange>>(w.rq, w.t.token, 'GET', `/history/ci/${ci.id}`),
    );
    const histMove = history.find(
      (e) =>
        e.source === 'ci_change' &&
        at(e.old_value, 'position_u') === 10 &&
        at(e.new_value, 'position_u') === 20,
    );
    expect(histMove, 'move in the unified history').toBeTruthy();
    expect(histMove!.site_id, 'site at the time of the move').toBe(h.site.id);
    expect(
      history.some((e) => e.source === 'audit'),
      'audit entries in the unified history',
    ).toBe(true);

    // UI: CI detail page after reload shows the move with time and site.
    await page.goto(`/cmdb/${ci.id}`);
    await page.reload();
    // The history is a collapsed section (details/summary) of the detail page.
    await page.locator('#main-content summary', { hasText: 'Historie' }).click();
    const list = page.getByRole('list', { name: 'Historie' });
    await expect(list).toBeVisible();
    const entry = list.locator('li').filter({
      has: page.locator(`time[datetime="${histMove!.created_at}"]`),
    });
    await expect(entry.first()).toBeVisible();
    await expect(
      list
        .locator('li', { hasText: `Site ${h.site.id}` })
        .filter({ hasText: histMove!.change_type }),
    ).not.toHaveCount(0);
    // UI: the racks page shows the new position after reload.
    await page.goto('/racks');
    await openRack(page, h.rack);
    await expect(mountLine(page, ci.name)).toContainText('Position 20');

    // Move to another rack, then unmount: the location follows (rack → rack → room).
    const toRack2 = await putMount(rack2.id, ci.id, { position_u: 5, face: 'front' });
    expect(toRack2.status, `move to rack 2: ${JSON.stringify(toRack2.body)}`).toBe(200);
    expect(ciRow(ci.id).location_id).toBe(rack2.id);
    lay = await layout(h.rack.id);
    // The first rack is empty again: no mount and all 42 units free on both faces.
    expect(lay.mounts ?? [], 'no mount left in the first rack').toEqual([]);
    expect(lay.free.front).toHaveLength(42);
    expect(lay.free.rear).toHaveLength(42);
    // API contract (openapi.yaml RackLayout: mounts is a required array, not nullable).
    expect
      .soft(
        Array.isArray(lay.mounts),
        `layout of an empty rack: mounts = ${JSON.stringify(lay.mounts)}`,
      )
      .toBe(true);
    const unmount = await api(w.rq, w.t.token, 'DELETE', `/racks/${rack2.id}/mounts/${ci.id}`);
    expect(unmount.status).toBe(204);
    expect(mountRows(rack2.id)).toEqual([]);
    expect(ciRow(ci.id), 'unmount puts the CI into the rack’s room').toMatchObject({
      location_id: h.room.id,
      room_id: h.room.id,
      site_id: h.site.id,
    });
    const after = rows(
      await ok<List<CIChange>>(w.rq, w.t.token, 'GET', `/cis/${ci.id}/changes?limit=100`),
    );
    expect(
      after.some(
        (c) =>
          at(c.old_value, 'rack_id') === rack2.id &&
          (c.new_value === null || c.new_value === undefined),
      ),
      'ci_change of the unmount',
    ).toBe(true);
    const auditAfter = await auditOf(ci.id);
    expect(
      auditAfter.some(
        (a) => at(a.changes, 'before', 'rack_id') === rack2.id && at(a.changes, 'after') === null,
      ),
      'audit of the unmount',
    ).toBe(true);
  });

  test('L09 a rack with a mounted CI and its room cannot be deleted (409); empty again, the rack can', async () => {
    const page = w.t.page;
    const h = await hierarchy('L09');
    const ci = await newCI('lr-keep');
    expect((await putMount(h.rack.id, ci.id, { position_u: 1, face: 'front' })).status).toBe(201);

    const viaRacks = await api<Problem>(w.rq, w.t.token, 'DELETE', `/racks/${h.rack.id}`);
    expect(viaRacks.status, JSON.stringify(viaRacks.body)).toBe(409);
    expect(viaRacks.body.dependencies).toEqual(expect.arrayContaining(['rack_mount']));
    const viaTree = await api<Problem>(w.rq, w.t.token, 'DELETE', `/locations/${h.rack.id}`);
    expect(viaTree.status).toBe(409);
    expect(viaTree.body.dependencies).toEqual(expect.arrayContaining(['ci', 'rack_mount']));
    const viaRooms = await api<Problem>(w.rq, w.t.token, 'DELETE', `/rooms/${h.room.id}`);
    expect(viaRooms.status).toBe(409);

    // Tree UI: refused with a message, rack and mount still there after reload.
    await page.goto('/locations');
    await treeRow(page, h.room.name).getByRole('button', { name: 'expand' }).click();
    const del = waitForApi(page, `/locations/${h.rack.id}`, 'DELETE');
    await treeRow(page, h.rack.name).getByRole('button', { name: 'Löschen' }).click();
    expect((await del).status()).toBe(409);
    await expect(
      page.getByRole('alert').filter({ hasText: 'Löschen nicht möglich' }),
    ).toBeVisible();
    await page.reload();
    await treeRow(page, h.room.name).getByRole('button', { name: 'expand' }).click();
    await expect(childRow(page, h.room.name, h.rack.name)).toBeVisible();
    expect(mountRows(h.rack.id).map((m) => m.ci_id)).toEqual([ci.id]);

    // Unmounted, the CI sits in the room: the empty rack can go, the room cannot.
    expect(
      (await api(w.rq, w.t.token, 'DELETE', `/racks/${h.rack.id}/mounts/${ci.id}`)).status,
    ).toBe(204);
    const roomDel = await api<Problem>(w.rq, w.t.token, 'DELETE', `/locations/${h.room.id}`);
    expect(roomDel.status).toBe(409);
    expect(roomDel.body.dependencies).toEqual(expect.arrayContaining(['ci']));
    const rackDel = await api(w.rq, w.t.token, 'DELETE', `/racks/${h.rack.id}`);
    expect(rackDel.status).toBe(204);
    expect(dbRows(`SELECT id FROM location WHERE id = '${uuid(h.rack.id)}'`)).toEqual([]);
  });

  test('L10 moving a room re-derives path and site of its subtree, CIs and mounts; location_change and audit record it', async () => {
    const page = w.t.page;
    const h1 = await hierarchy('L10a');
    const h2 = await hierarchy('L10b');
    const ci = await newCI('lr-subtree');
    expect((await putMount(h1.rack.id, ci.id, { position_u: 3, face: 'front' })).status).toBe(201);

    const res = await api<Location>(w.rq, w.t.token, 'PATCH', `/locations/${h1.room.id}`, {
      parent_id: h2.building.id,
    });
    expect(res.status, `move room: ${JSON.stringify(res.body)}`).toBe(200);
    const roomPath = `${label(h2.site.id)}.${label(h2.building.id)}.${label(h1.room.id)}`;
    expect(res.body).toMatchObject({
      id: h1.room.id,
      parent_id: h2.building.id,
      site_id: h2.site.id,
      client_id: w.client.id,
      path: roomPath,
    });
    // Subtree: the rack follows (path, site).
    expect(await getLocation(h1.rack.id)).toMatchObject({
      parent_id: h1.room.id,
      site_id: h2.site.id,
      path: `${roomPath}.${label(h1.rack.id)}`,
    });
    // The specialist row is in step.
    expect(
      dbRows(`SELECT building_id::text AS b FROM room WHERE id = '${uuid(h1.room.id)}'`),
    ).toEqual([{ b: h2.building.id }]);
    // CI in the rack: location kept, site re-derived; the mount's scope follows (TEN-02).
    expect(await ok<CI>(w.rq, w.t.token, 'GET', `/cis/${ci.id}`)).toMatchObject({
      location_id: h1.rack.id,
      site_id: h2.site.id,
      room_id: h1.room.id,
    });
    expect(mountRows(h1.rack.id).map((m) => [m.ci_id, m.site_id])).toEqual([[ci.id, h2.site.id]]);

    // location_change: one row for the moved node with old/new parent and path (LOC-11).
    const lc = dbRows<{
      location_id: string;
      old_parent_id: string;
      new_parent_id: string;
      old_path: string;
      new_path: string;
      changed_by: string | null;
      site_id: string;
    }>(
      `SELECT location_id::text, old_parent_id::text, new_parent_id::text, old_path, new_path, changed_by, site_id::text
         FROM location_change WHERE organization_id = '${uuid(w.t.orgId)}' AND location_id = '${uuid(h1.room.id)}'`,
    );
    expect(lc).toHaveLength(1);
    expect(lc[0]).toMatchObject({
      old_parent_id: h1.building.id,
      new_parent_id: h2.building.id,
      old_path: `${label(h1.site.id)}.${label(h1.building.id)}.${label(h1.room.id)}`,
      new_path: roomPath,
      site_id: h2.site.id,
    });
    expect(lc[0].changed_by, 'the move names the user').toBeTruthy();
    // Audit of the move.
    const audit = await auditOf(h1.room.id);
    const moveAudit = audit.find((a) => at(a.changes, 'parent_id', 'new') === h2.building.id);
    expect(
      moveAudit,
      `audit of the move in ${JSON.stringify(audit.map((a) => a.action))}`,
    ).toBeTruthy();
    expect(at(moveAudit!.changes, 'parent_id', 'old')).toBe(h1.building.id);

    // UI after reload: the room hangs below the new building, not the old one.
    await page.goto('/locations');
    await expect(childRow(page, h2.building.name, h1.room.name)).toBeVisible();
    await expect(childRow(page, h1.building.name, h1.room.name)).toHaveCount(0);
    // The room plan of the new building lists the room with the mounted CI.
    await page.goto('/roomplan');
    await page.getByRole('combobox', { name: 'Gebäude' }).selectOption({ label: h2.building.name });
    await page.getByRole('combobox', { name: 'Raum' }).selectOption({ label: h1.room.name });
    await expect(
      page.getByTestId('room-plan').getByRole('button', { name: new RegExp(`^${ci.name},`) }),
    ).toBeVisible();
  });
});
