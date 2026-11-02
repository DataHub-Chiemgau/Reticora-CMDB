import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { test, expect, type APIRequestContext, type Page, type Response } from '@playwright/test';
import { api, signupTenant, uiLogin, BASE, DATABASE_URL } from './support/stack';

/*
 * ABN-01 (e), the conflict scenario [B], on the REAL stack (WP-456, finding F-298): an
 * override is set on a CI, discovery reports a different value, the override_conflict is
 * visible in the review inbox, one conflict is accepted (override removed, reported value
 * taken) and one dismissed (override kept). Every step is checked in the UI, through the
 * API and in PostgreSQL. Nothing is mocked: the discovery records go through the ingest
 * of the stack (POST /api/v1/ingest/bulk → ingest.> → reconciliation), the database is
 * read with psql (E2E_DATABASE_URL, read-only queries).
 *
 * The scenario data are the frozen data of the protocol docs/acceptance/abn-01.md (e):
 * docs/acceptance/artefakte/abn-01-e-konflikt.json (srv-abn-01/model with the override
 * R-2U-manual, accept; srv-abn-02/os_name, dismiss). This spec, the artefact and the
 * mocked protocol run frontend/e2e/conflict.spec.ts are frozen in
 * docs/acceptance/artefakte/SHA256SUMS; frontend/e2e-real/discovery-override.spec.ts
 * (D03–D05, simulated fleet) is the general proof and stays a separate file.
 *
 * After a green run the spec writes the artefact anew from what the run observed (ids,
 * type, organization and times of the run, the scenario values read back from the API)
 * to abn-01-e-konflikt.json in its test output directory (frontend/test-results/…,
 * uploaded by install-smoke.yml as artifact e2e-real) and attaches it to the report. The
 * scenario values must equal the frozen ones; docs/acceptance/artefakte/README.md
 * describes how the file replaces the frozen one.
 *
 * Protocol step → checks
 *  0 first discovery: srv-abn-01 with the discovered model, srv-abn-02 ... API, DB
 *  1 override set in the "Override setzen" dialog (srv-abn-01/model); the
 *    override of srv-abn-02/os_name through the same endpoint ......... UI, API, DB
 *  2 deviating discovery: both fields reported with other values, the
 *    overrides stay, one open override_conflict per field ............ UI, API, DB
 *  3 override_conflict visible in the review inbox with the diff ...... UI
 *  4 accept: override removed, reported value taken .................. UI, API, DB
 *  5 dismiss: override kept, inbox empty, both resolutions persisted .. UI, API, DB
 *
 * Requirements: ABN-01 (e); OVR-01, REC-12, UI-16 as the scenario uses them.
 */

/** The frozen data of the scenario (docs/acceptance/artefakte/abn-01-e-konflikt.json). */
interface Frozen {
  szenario: string;
  eingefroren: string;
  lauf?: Run;
  ci: {
    id: string;
    name: string;
    ci_type_id: string;
    organization_id: string;
    model_discovered: string;
    discovered_source: string;
  };
  override: { field: string; value: string; reason: string };
  reviews: FrozenReview[];
}

interface FrozenReview {
  id: string;
  kind: string;
  payload: { name: string; field: string; override: string; observed: string };
  created_at: string;
  action: 'accept' | 'dismiss';
}

/** The run an artefact was generated from (GitHub Actions; null outside of it). */
interface Run {
  spec: string;
  workflow: string | null;
  job: string | null;
  run_id: string | null;
  run_attempt: string | null;
  commit: string | null;
  url: string | null;
}

const ARTEFACT = 'docs/acceptance/artefakte/abn-01-e-konflikt.json';
const SPEC = 'frontend/e2e-real/abn-01-konflikt.spec.ts';
const frozen = JSON.parse(
  readFileSync(new URL(`../../${ARTEFACT}`, import.meta.url), 'utf8'),
) as Frozen;

function frozenReview(action: FrozenReview['action']): FrozenReview {
  const review = frozen.reviews.find((r) => r.action === action);
  if (!review) throw new Error(`${ARTEFACT} has no review with action ${action}`);
  return review;
}

/** srv-abn-01/model: the conflict with the override of step 1, accepted. */
const ACCEPT = frozenReview('accept');
/** srv-abn-02/os_name: the conflict that is dismissed. */
const DISMISS = frozenReview('dismiss');

/** The scenario CIs are servers; the artefact names the type of its run by id. */
const CI_TYPE = 'server';

/**
 * A device as discovery reports it: the identity (serial number, MAC, address from
 * TEST-NET-1) is fixed, so the second discovery matches the CI of the first.
 */
interface Device {
  name: string;
  ip: string;
  serial: string;
  mac: string;
}

const ACCEPT_DEVICE: Device = {
  name: ACCEPT.payload.name,
  ip: '192.0.2.101',
  serial: 'ABN-01-E-1',
  mac: '02:ab:01:0e:00:01',
};
const DISMISS_DEVICE: Device = {
  name: DISMISS.payload.name,
  ip: '192.0.2.102',
  serial: 'ABN-01-E-2',
  mac: '02:ab:01:0e:00:02',
};

interface CI {
  id: string;
  name: string;
  ci_type_id: string;
  organization_id: string;
  discovery_source?: string;
  [field: string]: unknown;
}

interface List<T> {
  data: T[] | null;
  total: number;
}

interface Provenance {
  field: string;
  value: unknown;
  source: string;
  reason: string;
  override_author?: string;
  override_reason?: string;
  protected?: boolean;
  discovered_value?: unknown;
  discovered_source?: string;
  diverged: boolean;
}

interface ReviewItem {
  id: string;
  kind: string;
  status: 'open' | 'resolved' | 'dismissed';
  payload: Record<string, unknown>;
  action?: string;
  resolved_by?: string;
  resolved_at?: string;
  created_at: string;
}

interface OverrideSaved {
  field_name: string;
  override_value: unknown;
  override_reason: string;
}

interface World {
  rq: APIRequestContext;
  page: Page;
  token: string;
  me: string;
}

let w: World;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

async function ok<T>(
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(w.rq, w.token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/** record is a DeviceRecord (api/devicerecord.schema.json) of the device observed now. */
function record(
  device: Device,
  classification: Record<string, string>,
  system: Record<string, string>,
) {
  return {
    record_id: randomUUID(),
    observed_at: new Date().toISOString(),
    source: frozen.ci.discovered_source,
    target_ip: device.ip,
    identity: { serial_number: device.serial, primary_mac: device.mac, hostname: device.name },
    classification: { ci_type: CI_TYPE, ...classification },
    system: { name: device.name, ...system },
  };
}

/** discover delivers one discovery run through the ingest of the stack. */
async function discover(records: ReturnType<typeof record>[]) {
  const res = await api(w.rq, w.token, 'POST', '/ingest/bulk', { records });
  expect(res.status, `POST /ingest/bulk: ${JSON.stringify(res.body)}`).toBe(202);
}

async function ciByName(name: string): Promise<CI | undefined> {
  const list = await ok<List<CI>>('GET', '/cis?limit=100');
  return (list.data ?? []).find((ci) => ci.name === name);
}

async function getCI(id: string): Promise<CI> {
  return ok<CI>('GET', `/cis/${id}`);
}

async function provField(id: string, field: string): Promise<Provenance | undefined> {
  const list = await ok<{ data: Provenance[] | null }>('GET', `/cis/${id}/provenance`);
  return (list.data ?? []).find((p) => p.field === field);
}

/** conflicts are the override_conflict items of one field of one CI. */
async function conflicts(ciId: string, field: string, status = ''): Promise<ReviewItem[]> {
  const q = new URLSearchParams({ kind: 'override_conflict', limit: '100' });
  if (status) q.set('status', status);
  const list = await ok<List<ReviewItem>>('GET', `/review-items?${q.toString()}`);
  return (list.data ?? []).filter(
    (item) => item.payload.ci_id === ciId && item.payload.field === field,
  );
}

// ─── Database (read-only) ───────────────────────────────────────────────────

/**
 * db runs ONE read-only query against the stack database with psql variables
 * (:'name') and returns its rows as JSON objects.
 */
function db<T>(select: string, vars: Record<string, string>): T[] {
  expect(/^\s*select\b/i.test(select), 'read-only query').toBe(true);
  const args = [DATABASE_URL, '-X', '-At', '-v', 'ON_ERROR_STOP=1'];
  for (const [name, value] of Object.entries(vars)) args.push('-v', `${name}=${value}`);
  args.push('-f', '-');
  const out = execFileSync('psql', args, {
    input: `SELECT COALESCE(json_agg(q), '[]'::json) FROM (${select}) q;`,
    encoding: 'utf8',
    timeout: 15_000,
  }).trim();
  return JSON.parse(out) as T[];
}

/** The stored state of one field of a CI: the CI column and its ci_field_value row. */
interface StoredField {
  ci_value: unknown;
  discovered_value: unknown;
  discovered_source: string | null;
  override_value: unknown;
  override_reason: string | null;
  override_author: string | null;
  overridden: boolean;
  protected: boolean | null;
}

function storedField(ci: CI, field: string): StoredField {
  const rows = db<StoredField>(
    `SELECT to_jsonb(c) -> :'field' AS ci_value, f.discovered_value, f.discovered_source,
            f.override_value, f.override_reason, f.override_author::text AS override_author,
            f.override_at IS NOT NULL AS overridden, f.protected
       FROM ci c LEFT JOIN ci_field_value f ON f.ci_id = c.id AND f.field_name = :'field'
      WHERE c.id = :'ci' AND c.organization_id = :'org'`,
    { ci: ci.id, org: ci.organization_id, field },
  );
  expect(rows, `database row of ${ci.name}/${field}`).toHaveLength(1);
  return rows[0]!;
}

interface StoredReview {
  id: string;
  status: string;
  action: string | null;
  payload: Record<string, unknown>;
  resolved_by: string | null;
}

function storedReview(ci: CI, id: string): StoredReview {
  const rows = db<StoredReview>(
    `SELECT id::text AS id, status, action, payload, resolved_by::text AS resolved_by
       FROM review_item
      WHERE id = :'id' AND organization_id = :'org' AND kind = 'override_conflict'`,
    { id, org: ci.organization_id },
  );
  expect(rows, `database row of review item ${id}`).toHaveLength(1);
  return rows[0]!;
}

// ─── UI ─────────────────────────────────────────────────────────────────────

async function openCI(page: Page, ci: CI) {
  await page.goto(`/cmdb/${ci.id}`);
  await expect(page.getByRole('heading', { level: 2, name: ci.name })).toBeVisible();
}

/** detailValue is the value of a row of the overview card ("Modell", …). */
function detailValue(page: Page, label: string) {
  return page
    .locator('dl > div')
    .filter({ has: page.locator('dt', { hasText: new RegExp(`^${label}$`) }) })
    .locator('dd')
    .first();
}

async function provenanceRow(page: Page, field: string) {
  const section = page.locator('details').filter({
    has: page.locator('summary', { hasText: 'Herkunft & Overrides' }),
  });
  if ((await section.getAttribute('open')) === null) {
    await section.locator('summary').click();
  }
  const row = section
    .locator('li')
    .filter({ has: page.getByRole('button', { name: `Herkunft von ${field}`, exact: true }) });
  await expect(row, `provenance row of ${field}`).toHaveCount(1);
  return row;
}

/** provenanceCell is the value of a column (Discovery / Override / Effektiv) of a row. */
function provenanceCell(row: ReturnType<Page['locator']>, column: string) {
  return row
    .locator('dl > div')
    .filter({ has: row.page().locator('dt', { hasText: new RegExp(`^${column}$`) }) })
    .locator('dd');
}

/** setOverrideUI sets a value in the "Override setzen" dialog and returns the PUT response. */
async function setOverrideUI(
  page: Page,
  field: string,
  value: string,
  reason: string,
): Promise<Response> {
  const row = await provenanceRow(page, field);
  await row.getByRole('button', { name: 'Override setzen' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('heading', { name: `Override setzen: ${field}` })).toBeVisible();
  await dialog.getByLabel('Wert').fill(value);
  await dialog.getByLabel('Begründung').fill(reason);
  const saved = page.waitForResponse(
    (r) => r.url().includes(`/fields/${field}/override`) && r.request().method() === 'PUT',
  );
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  const response = await saved;
  expect(response.status(), 'PUT override').toBe(200);
  await expect(dialog).toBeHidden();
  return response;
}

/** reviewEntry is the entry of an item in the review inbox list, named by its field. */
function reviewEntry(page: Page, field: string) {
  return page.locator('main ul li button', { hasText: field });
}

async function inboxFilter(page: Page, status: 'open' | 'resolved' | 'dismissed') {
  await page.getByRole('combobox', { name: 'Typ' }).selectOption('override_conflict');
  await page.getByRole('combobox', { name: 'Status' }).selectOption(status);
}

/** openReview selects the item of a field; the diff header names its CI. */
async function openReview(page: Page, field: string, ciName: string) {
  const entry = reviewEntry(page, field);
  await expect(entry).toHaveCount(1);
  await expect(entry).toContainText('Konflikt mit Override');
  await entry.click();
  await expect(page.locator('main table thead th').nth(2)).toHaveText(ciName);
  return page.locator('main table').locator('..');
}

// ─── The scenario ───────────────────────────────────────────────────────────

test('ABN-01 (e) with the frozen data: override, deviating discovery, override_conflict, accept and dismiss (UI, API, database)', async ({
  browser,
  request,
}, testInfo) => {
  test.setTimeout(8 * 60_000);
  // The frozen data are one scenario: the accepted conflict concerns the override.
  expect(ACCEPT.payload).toMatchObject({
    name: frozen.ci.name,
    field: frozen.override.field,
    override: frozen.override.value,
  });
  expect(DISMISS.payload.name).not.toBe(ACCEPT.payload.name);
  // The DeviceRecords below carry these fields: classification.model, system.os_name.
  expect([ACCEPT.payload.field, DISMISS.payload.field]).toEqual(['model', 'os_name']);

  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': realIp() },
  });
  const page = await ctx.newPage();
  const account = await signupTenant(page, request, 'abn01e');
  const token = await uiLogin(page, account);
  const me = await api<{ sub: string }>(request, token, 'GET', '/me');
  expect(me.status).toBe(200);
  w = { rq: request, page, token, me: me.body.sub };

  let first!: CI;
  let second!: CI;
  let discoveredSource = '';
  await test.step('0 first discovery: the CIs of the scenario with the discovered model', async () => {
    await discover([
      record(ACCEPT_DEVICE, { model: frozen.ci.model_discovered }, {}),
      record(DISMISS_DEVICE, {}, {}),
    ]);
    await expect
      .poll(
        async () => [
          (await ciByName(ACCEPT_DEVICE.name))?.model,
          Boolean(await ciByName(DISMISS_DEVICE.name)),
        ],
        { message: 'CIs of the first discovery', timeout: 120_000, intervals: [2_000] },
      )
      .toEqual([frozen.ci.model_discovered, true]);
    first = await getCI((await ciByName(ACCEPT_DEVICE.name))!.id);
    second = await getCI((await ciByName(DISMISS_DEVICE.name))!.id);
    expect(first.discovery_source).toBe(frozen.ci.discovered_source);
    const p = await provField(first.id, frozen.override.field);
    expect(p).toMatchObject({ value: frozen.ci.model_discovered, diverged: false });
    expect(p?.reason).not.toBe('override');
    discoveredSource = p?.source ?? '';
    expect(discoveredSource).toBe(frozen.ci.discovered_source);
    const stored = storedField(first, frozen.override.field);
    expect(stored).toMatchObject({
      ci_value: frozen.ci.model_discovered,
      discovered_value: frozen.ci.model_discovered,
      discovered_source: frozen.ci.discovered_source,
      overridden: false,
    });
    // srv-abn-02 reports no operating system yet: the later report deviates from the override.
    expect(second[DISMISS.payload.field] ?? '').toBe('');
  });

  let savedOverride!: OverrideSaved;
  await test.step('1 override set in the "Override setzen" dialog', async () => {
    const { field, value, reason } = frozen.override;
    await openCI(page, first);
    const response = await setOverrideUI(page, field, value, reason);
    savedOverride = (await response.json()) as OverrideSaved;
    expect(savedOverride).toMatchObject({
      field_name: field,
      override_value: value,
      override_reason: reason,
    });

    await page.reload();
    await expect(detailValue(page, 'Modell')).toHaveText(value);
    const row = await provenanceRow(page, field);
    await expect(row.getByText('geschützt', { exact: true })).toBeVisible();
    await expect(provenanceCell(row, 'Override')).toHaveText(JSON.stringify(value));
    await expect(provenanceCell(row, 'Discovery')).toHaveText(
      JSON.stringify(frozen.ci.model_discovered),
    );
    await expect(provenanceCell(row, 'Effektiv')).toHaveText(JSON.stringify(value));

    expect((await getCI(first.id))[field]).toBe(value);
    expect(await provField(first.id, field)).toMatchObject({
      value,
      source: 'manual_override',
      reason: 'override',
      override_reason: reason,
      override_author: w.me,
      protected: true,
      discovered_value: frozen.ci.model_discovered,
      discovered_source: frozen.ci.discovered_source,
    });
    expect(storedField(first, field)).toMatchObject({
      ci_value: value,
      override_value: value,
      override_reason: reason,
      override_author: w.me,
      overridden: true,
      protected: true,
      discovered_value: frozen.ci.model_discovered,
    });

    // The override of the dismissed conflict: same endpoint as the dialog. The field has
    // no discovered value yet, so the CI detail offers no row for it.
    await ok('PUT', `/cis/${second.id}/fields/${DISMISS.payload.field}/override`, {
      value: DISMISS.payload.override,
      reason,
      protected: true,
    });
    expect((await getCI(second.id))[DISMISS.payload.field]).toBe(DISMISS.payload.override);
    expect(storedField(second, DISMISS.payload.field)).toMatchObject({
      ci_value: DISMISS.payload.override,
      override_value: DISMISS.payload.override,
      overridden: true,
    });
  });

  let acceptItem!: ReviewItem;
  let dismissItem!: ReviewItem;
  const overrideAtConflict: Record<string, unknown> = {};
  await test.step('2 deviating discovery: the overrides stay, one override_conflict per field', async () => {
    await discover([
      record(ACCEPT_DEVICE, { model: ACCEPT.payload.observed }, {}),
      record(DISMISS_DEVICE, {}, { os_name: DISMISS.payload.observed }),
    ]);
    await expect
      .poll(
        async () =>
          (await conflicts(first.id, ACCEPT.payload.field, 'open')).length +
          (await conflicts(second.id, DISMISS.payload.field, 'open')).length,
        { message: 'two open override_conflict items', timeout: 120_000, intervals: [2_000] },
      )
      .toBe(2);
    for (const [ci, expected] of [
      [first, ACCEPT],
      [second, DISMISS],
    ] as const) {
      const items = await conflicts(ci.id, expected.payload.field);
      expect(items, `one item for ${ci.name}/${expected.payload.field}`).toHaveLength(1);
      const item = items[0]!;
      expect(item).toMatchObject({ kind: 'override_conflict', status: 'open' });
      expect(item.payload).toMatchObject({
        ci_id: ci.id,
        field: expected.payload.field,
        observed_value: expected.payload.observed,
        source: frozen.ci.discovered_source,
      });
      // OVR-01: the override is not overwritten; the deviation is kept beside it.
      expect((await getCI(ci.id))[expected.payload.field]).toBe(expected.payload.override);
      const p = await provField(ci.id, expected.payload.field);
      expect(p).toMatchObject({
        value: expected.payload.override,
        reason: 'override',
        discovered_value: expected.payload.observed,
        discovered_source: frozen.ci.discovered_source,
        diverged: true,
      });
      overrideAtConflict[item.id] = p?.value;
      expect(storedField(ci, expected.payload.field)).toMatchObject({
        ci_value: expected.payload.override,
        override_value: expected.payload.override,
        overridden: true,
        discovered_value: expected.payload.observed,
      });
      expect(storedReview(ci, item.id)).toMatchObject({
        status: 'open',
        action: null,
        payload: { field: expected.payload.field, observed_value: expected.payload.observed },
      });
      if (expected === ACCEPT) acceptItem = item;
      else dismissItem = item;
    }

    await openCI(page, first);
    await expect(detailValue(page, 'Modell')).toHaveText(frozen.override.value);
    const row = await provenanceRow(page, ACCEPT.payload.field);
    await expect(row.getByText('abweichend', { exact: true })).toBeVisible();
    await expect(provenanceCell(row, 'Discovery')).toHaveText(
      JSON.stringify(ACCEPT.payload.observed),
    );
    await expect(provenanceCell(row, 'Effektiv')).toHaveText(
      JSON.stringify(ACCEPT.payload.override),
    );
  });

  await test.step('3 override_conflict visible in the review inbox', async () => {
    await page.goto('/reviews');
    await inboxFilter(page, 'open');
    await expect(page.locator('main ul li button')).toHaveCount(2);
    const detail = await openReview(page, ACCEPT.payload.field, first.name);
    const diff = detail.locator('tbody tr').filter({ hasText: ACCEPT.payload.field });
    await expect(diff.locator('td').nth(1)).toHaveText(ACCEPT.payload.observed);
    await expect(diff.locator('td').nth(2)).toContainText(ACCEPT.payload.override);
    await expect(diff.getByText('abweichend')).toBeVisible();
    await expect(detail.getByRole('button', { name: 'Übernehmen' })).toBeVisible();
    await expect(detail.getByRole('button', { name: 'Verwerfen' })).toBeVisible();
  });

  let accepted!: ReviewItem;
  await test.step('4 accept: override removed, reported value taken', async () => {
    const detail = await openReview(page, ACCEPT.payload.field, first.name);
    const resolved = page.waitForResponse(
      (r) => r.url().includes(`/review-items/${acceptItem.id}/resolve`) && r.ok(),
    );
    await detail.getByRole('button', { name: 'Übernehmen' }).click();
    await resolved;
    await expect(reviewEntry(page, ACCEPT.payload.field)).toHaveCount(0);

    accepted = await ok<ReviewItem>('GET', `/review-items/${acceptItem.id}`);
    expect(accepted).toMatchObject({
      status: 'resolved',
      action: ACCEPT.action,
      resolved_by: w.me,
    });
    expect(accepted.resolved_at, 'resolution time').toBeTruthy();
    expect((await getCI(first.id))[ACCEPT.payload.field]).toBe(ACCEPT.payload.observed);
    const p = await provField(first.id, ACCEPT.payload.field);
    expect(p?.value).toBe(ACCEPT.payload.observed);
    expect(p?.reason, 'no override any more').not.toBe('override');
    expect(storedField(first, ACCEPT.payload.field)).toMatchObject({
      ci_value: ACCEPT.payload.observed,
      override_value: null,
      overridden: false,
      discovered_value: ACCEPT.payload.observed,
    });
    expect(storedReview(first, acceptItem.id)).toMatchObject({
      status: 'resolved',
      action: ACCEPT.action,
      resolved_by: w.me,
    });

    await openCI(page, first);
    await expect(detailValue(page, 'Modell')).toHaveText(ACCEPT.payload.observed);
    const row = await provenanceRow(page, ACCEPT.payload.field);
    await expect(provenanceCell(row, 'Effektiv')).toHaveText(
      JSON.stringify(ACCEPT.payload.observed),
    );
    await expect(row.getByRole('button', { name: 'Override setzen' })).toBeVisible();
    await expect(row.getByText('geschützt', { exact: true })).toHaveCount(0);
  });

  let dismissed!: ReviewItem;
  await test.step('5 dismiss: override kept; both resolutions persist', async () => {
    await page.goto('/reviews');
    await inboxFilter(page, 'open');
    const detail = await openReview(page, DISMISS.payload.field, second.name);
    const diff = detail.locator('tbody tr').filter({ hasText: DISMISS.payload.field });
    await expect(diff.locator('td').nth(1)).toHaveText(DISMISS.payload.observed);
    await expect(diff.locator('td').nth(2)).toContainText(DISMISS.payload.override);
    const resolved = page.waitForResponse(
      (r) => r.url().includes(`/review-items/${dismissItem.id}/resolve`) && r.ok(),
    );
    await detail.getByRole('button', { name: 'Verwerfen' }).click();
    await resolved;
    await expect(page.getByText('Keine Review-Items')).toBeVisible();

    dismissed = await ok<ReviewItem>('GET', `/review-items/${dismissItem.id}`);
    expect(dismissed).toMatchObject({
      status: 'dismissed',
      action: DISMISS.action,
      resolved_by: w.me,
    });
    expect((await getCI(second.id))[DISMISS.payload.field]).toBe(DISMISS.payload.override);
    expect(await provField(second.id, DISMISS.payload.field)).toMatchObject({
      value: DISMISS.payload.override,
      reason: 'override',
      protected: true,
      discovered_value: DISMISS.payload.observed,
    });
    expect(storedField(second, DISMISS.payload.field)).toMatchObject({
      ci_value: DISMISS.payload.override,
      override_value: DISMISS.payload.override,
      overridden: true,
      discovered_value: DISMISS.payload.observed,
    });
    expect(storedReview(second, dismissItem.id)).toMatchObject({
      status: 'dismissed',
      action: DISMISS.action,
      resolved_by: w.me,
    });

    // Persisted: after a reload the inbox lists both under their new status.
    await page.reload();
    await inboxFilter(page, 'open');
    await expect(page.getByText('Keine Review-Items')).toBeVisible();
    await inboxFilter(page, 'resolved');
    await expect(reviewEntry(page, ACCEPT.payload.field)).toContainText('Erledigt');
    await inboxFilter(page, 'dismissed');
    await expect(reviewEntry(page, DISMISS.payload.field)).toContainText('Verworfen');

    await openCI(page, second);
    const row = await provenanceRow(page, DISMISS.payload.field);
    await expect(row.getByText('geschützt', { exact: true })).toBeVisible();
    await expect(provenanceCell(row, 'Override')).toHaveText(
      JSON.stringify(DISMISS.payload.override),
    );
    await expect(provenanceCell(row, 'Discovery')).toHaveText(
      JSON.stringify(DISMISS.payload.observed),
    );
  });

  // The artefact anew from this run: what the API returned, in the frozen format.
  const env = process.env;
  const run: Run = {
    spec: SPEC,
    workflow: env.GITHUB_WORKFLOW ?? null,
    job: env.GITHUB_JOB ?? null,
    run_id: env.GITHUB_RUN_ID ?? null,
    run_attempt: env.GITHUB_RUN_ATTEMPT ?? null,
    commit: env.GITHUB_SHA ?? null,
    url:
      env.GITHUB_SERVER_URL && env.GITHUB_REPOSITORY && env.GITHUB_RUN_ID
        ? `${env.GITHUB_SERVER_URL}/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}`
        : null,
  };
  const review = (item: ReviewItem, ci: CI, resolution: ReviewItem): FrozenReview => ({
    id: item.id,
    kind: item.kind,
    payload: {
      name: ci.name,
      field: String(item.payload.field),
      override: String(overrideAtConflict[item.id]),
      observed: String(item.payload.observed_value),
    },
    created_at: item.created_at,
    action: resolution.action as FrozenReview['action'],
  });
  const regenerated: Frozen = {
    szenario: frozen.szenario,
    eingefroren: new Date().toISOString().slice(0, 10),
    lauf: run,
    ci: {
      id: first.id,
      name: first.name,
      ci_type_id: first.ci_type_id,
      organization_id: first.organization_id,
      model_discovered: String(first.model),
      discovered_source: discoveredSource,
    },
    override: {
      field: savedOverride.field_name,
      value: String(savedOverride.override_value),
      reason: savedOverride.override_reason,
    },
    reviews: [review(acceptItem, first, accepted), review(dismissItem, second, dismissed)],
  };
  // The run reproduced the frozen scenario: only ids, times and the run differ.
  const scenario = (a: Frozen) => ({
    szenario: a.szenario,
    ci: {
      name: a.ci.name,
      model_discovered: a.ci.model_discovered,
      discovered_source: a.ci.discovered_source,
    },
    override: a.override,
    reviews: a.reviews.map((r) => ({ kind: r.kind, payload: r.payload, action: r.action })),
  });
  expect(scenario(regenerated), `the run reproduces ${ARTEFACT}`).toEqual(scenario(frozen));
  const out = testInfo.outputPath('abn-01-e-konflikt.json');
  writeFileSync(out, `${JSON.stringify(regenerated, null, 2)}\n`);
  await testInfo.attach('abn-01-e-konflikt.json', { path: out, contentType: 'application/json' });
  console.log(`ABN-01 (e): artefact of this run written to ${out}`);

  await ctx.close();
});
