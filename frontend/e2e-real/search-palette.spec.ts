import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { stamp } from './support/stack';
import {
  ok,
  openTenant,
  rows,
  treeRow,
  type List,
  type Location,
  type Tenant,
} from './support/locations-racks';

/*
 * Search hits of locations and contacts lead to their entity (real stack, nothing mocked; WP-594,
 * SRC-01, binding retest of F-334): UI → REST API → PostgreSQL → UI.
 *
 * Before WP-594 the index rows of locations and contacts carried the constant urls '/locations'
 * and '/contacts' (migration 000132): a location hit opened the root of the tree, a contact hit a
 * route the SPA does not have (its catch-all showed the CI list). Since migration 000182 a
 * location hit links to /locations?node=<id>, which opens the path to that node, marks it and
 * focuses it; a contact has no page in phase 1, its hit has an empty url and carries e-mail,
 * phone and role, and the command palette shows the contact in a card instead of navigating.
 *
 * One organization is created through the self-signup UI; its administrator builds a location
 * tree and two contacts through the REST API the SPA uses and searches them in the command
 * palette (Ctrl+K).
 *
 * Test → requirement IDs proven:
 *  S1 a rack three levels below its site (collapsed by default) is found by its name; the hit
 *     links to /locations?node=<id>; choosing it in the palette opens the tree with the path
 *     expanded and exactly this node marked and focused, its neighbour not ........ SRC-01 (F-334)
 *  S2 two contacts share a search word; the hit of one has an empty url and its e-mail, phone
 *     and role; choosing it in the palette shows a card with exactly this contact's name,
 *     e-mail, phone and role (none of the other's), the page does not change, Escape closes
 *     the palette ................................................................ SRC-01 (F-334)
 *
 * A failing test is a product finding: the assertions encode SRC-01 in docs/spec/katalog-v3 and
 * the acceptance criteria of WP-594.
 */

interface SearchHit {
  entity_type: string;
  entity_id: string;
  title: string;
  url: string;
  metadata?: Record<string, string>;
}

interface World {
  rq: APIRequestContext;
  t: Tenant;
  s: string;
  client: { id: string; name: string };
}

let w: World;

function escapeRe(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** hitOf reads the hits of a text through the API until the entity is one of them. */
async function hitOf(text: string, entityId: string): Promise<SearchHit> {
  let hit: SearchHit | undefined;
  await expect
    .poll(
      async () => {
        const res = await ok<List<SearchHit>>(
          w.rq,
          w.t.token,
          'GET',
          `/search?q=${encodeURIComponent(text)}`,
        );
        hit = rows(res).find((h) => h.entity_id === entityId);
        return hit !== undefined;
      },
      { message: `search finds ${entityId}`, timeout: 30_000 },
    )
    .toBe(true);
  return hit!;
}

/** palette opens the command palette on the dashboard and types text. */
async function palette(page: Page, text: string) {
  await page.goto('/dashboard');
  await page.locator('body').click();
  await page.keyboard.press('Control+k');
  const box = page.getByRole('combobox', { name: 'Befehle durchsuchen' });
  await expect(box).toBeFocused();
  await box.fill(text);
  return box;
}

test.describe('search hits lead to their entity (SRC-01, F-334)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const t = await openTenant(browser, rq, 'search');
    const s = stamp();
    const client = await ok<{ id: string; name: string }>(rq, t.token, 'POST', '/clients', {
      name: `SP Client ${s}`,
      slug: `sp-client-${s}`,
    });
    w = { rq, t, s, client };
  });

  test.afterAll(async () => {
    await w?.t.page.context().close();
    await w?.rq.dispose();
  });

  test('S1 a location hit opens the tree at exactly this node', async () => {
    const page = w.t.page;
    const post = (body: Record<string, unknown>) =>
      ok<Location>(w.rq, w.t.token, 'POST', '/locations', body);
    const word = `suchrack${w.s}`;
    const site = await post({ kind: 'site', name: `SP Site ${w.s}`, client_id: w.client.id });
    const building = await post({
      kind: 'building',
      name: `SP Building ${w.s}`,
      parent_id: site.id,
    });
    const room = await post({ kind: 'room', name: `SP Room ${w.s}`, parent_id: building.id });
    const rack = await post({ kind: 'rack', name: `SP Rack ${word}`, parent_id: room.id });
    const neighbour = await post({
      kind: 'rack',
      name: `SP Neighbour ${w.s}`,
      parent_id: room.id,
    });

    const hit = await hitOf(word, rack.id);
    expect(hit).toMatchObject({
      entity_type: 'location',
      title: rack.name,
      url: `/locations?node=${rack.id}`,
    });

    const box = await palette(page, word);
    const option = page.getByRole('option', {
      name: new RegExp(`${escapeRe(rack.name)} · location`),
    });
    await expect(option).toBeVisible();
    await option.click();
    await expect(page).toHaveURL(new RegExp(`/locations\\?node=${rack.id}$`));
    await expect(box).toBeHidden();

    // The room (depth 2) starts collapsed; the link opens the path to the rack.
    const marked = page.locator('#main-content [aria-current="true"]');
    await expect(marked).toHaveCount(1);
    await expect(treeRow(page, rack.name)).toHaveAttribute('aria-current', 'true');
    await expect(treeRow(page, rack.name)).toBeFocused();
    await expect(
      treeRow(page, room.name).getByRole('button', { name: 'collapse' }),
    ).toHaveAttribute('aria-expanded', 'true');
    await expect(treeRow(page, neighbour.name)).toBeVisible();
    await expect(treeRow(page, neighbour.name)).not.toHaveAttribute('aria-current', 'true');
  });

  test('S2 a contact hit shows exactly this contact in a card', async () => {
    const page = w.t.page;
    const word = `suchkontakt${w.s}`;
    const create = (body: Record<string, unknown>) =>
      ok<{ id: string }>(w.rq, w.t.token, 'POST', '/contacts', {
        client_id: w.client.id,
        ...body,
      });
    const gerda = {
      display_name: `Gerda ${word}`,
      email: `gerda-${w.s}@audit-e2e.de`,
      phone: '+49 861 4711',
      role: 'Netzbetrieb',
    };
    const hubert = {
      display_name: `Hubert ${word}`,
      email: `hubert-${w.s}@audit-e2e.de`,
      phone: '+49 30 815',
      role: 'Leitstelle',
    };
    const chosen = await create(gerda);
    await create(hubert);

    const hit = await hitOf(word, chosen.id);
    expect(hit).toMatchObject({
      entity_type: 'contact',
      title: gerda.display_name,
      url: '',
      metadata: { email: gerda.email, phone: gerda.phone, role: gerda.role },
    });

    await palette(page, word);
    const option = page.getByRole('option', {
      name: new RegExp(`${escapeRe(gerda.display_name)} · contact`),
    });
    await expect(option).toBeVisible();
    await expect(
      page.getByRole('option', { name: new RegExp(`${escapeRe(hubert.display_name)} · contact`) }),
    ).toBeVisible();
    await option.click();

    const card = page.getByRole('region', { name: gerda.display_name, exact: true });
    await expect(card).toBeVisible();
    await expect(card).toBeFocused();
    await expect(card.getByRole('link', { name: gerda.email })).toHaveAttribute(
      'href',
      `mailto:${gerda.email}`,
    );
    await expect(card.getByRole('link', { name: gerda.phone })).toHaveAttribute(
      'href',
      'tel:+498614711',
    );
    await expect(card).toContainText(gerda.role);
    for (const foreign of [hubert.display_name, hubert.email, hubert.phone, hubert.role]) {
      await expect(card).not.toContainText(foreign);
    }
    // No navigation: the palette shows the card on the page it was opened on.
    await expect(page).toHaveURL(/\/dashboard$/);

    await page.keyboard.press('Escape');
    await expect(card).toBeHidden();
    await expect(page).toHaveURL(/\/dashboard$/);
  });
});
