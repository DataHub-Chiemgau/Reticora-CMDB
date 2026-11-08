import type { Page } from '@playwright/test';
import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';
import type { Handler } from './support/backend';
import { IMPORT_PROFILE, mePermissions } from './support/phase2';

// TST-03 (WP-322), UI-11, UI-14: the import assistant. A CSV file is
// uploaded as a document (POST /documents, PUT /documents/{id}/content),
// waited for until its scan is clean, previewed (POST /imports/preview),
// mapped in a new profile (POST /import-profiles), checked in a dry run and
// imported (POST /imports, GET /jobs/{id}, GET /imports/{id}) with the
// report of each run and its row errors page by page (GET
// /imports/{id}/errors); an infected file aborts the assistant. WP-324: a
// saved profile is changed (PATCH /import-profiles/{id}) and reused for the
// next file after a reload, and the report with the row errors is
// downloaded through its signed link (GET /imports/{id}/report). Every
// request and answer follows api/openapi.yaml (WP-530).

test.use({ locale: 'de-DE' });

const USER_ID = '00000000-0000-0000-0000-000000000001';
const DOCUMENT_ID = '7a000000-0000-4000-8000-000000000001';
const PROFILE_ID = '7b000000-0000-4000-8000-000000000001';
const DRY_JOB = '7c000000-0000-4000-8000-000000000001';
const RUN_JOB = '7c000000-0000-4000-8000-000000000002';
const CSV = 'Hostname;Seriennummer\nsrv-01;SN-1\nsrv-02;SN-2\nsrv-03;SN-3\n';
const ERRORS_CURSOR = 'eyJvIjoxfQ.c2lnbmVk';

function document(scan: string) {
  return {
    id: DOCUMENT_ID,
    organization_id: ORG_ID,
    title: 'server',
    file_name: 'server.csv',
    file_size: CSV.length,
    mime_type: 'text/csv',
    storage_key: '',
    version: 1,
    category: 'general',
    tags: [],
    uploaded_by: USER_ID,
    created_at: AT,
    updated_at: AT,
    scan_status: scan,
  };
}

function job(id: string, created: number, updated: number, dryRun: boolean) {
  return {
    id,
    organization_id: ORG_ID,
    kind: 'import.run',
    status: 'succeeded',
    progress: 100,
    result: {
      report_key: `imports/${id}/report.csv`,
      dry_run: dryRun,
      total: 3,
      created,
      updated,
      skipped: 0,
      failed: 1,
    },
    created_by: USER_ID,
    attempts: 1,
    max_attempts: 3,
    created_at: AT,
    updated_at: AT,
    started_at: AT,
    finished_at: AT,
    cancellable: false,
  };
}

function run(id: string, dryRun: boolean) {
  return {
    id,
    kind: 'import.run',
    status: 'succeeded',
    progress: 100,
    source: 'document',
    document_id: DOCUMENT_ID,
    document_version: 1,
    row_count: null,
    profile_id: PROFILE_ID,
    mode: 'both',
    dry_run: dryRun,
    sheet: null,
    rows_total: 3,
    rows_failed: 1,
    error_code: null,
    error: null,
    cancellable: false,
    created_by: USER_ID,
    created_at: AT,
    started_at: AT,
    finished_at: AT,
  };
}

/** importHandlers answer the assistant; scan is the status of the uploaded version. */
function importHandlers(scan: string): Handler[] {
  // The stored profile of support/phase2.ts and those the assistant saves.
  const profiles: Record<string, unknown>[] = [IMPORT_PROFILE];
  let starts = 0;
  return [
    mePermissions(['ci:read', 'import:run', 'import:manage']),
    {
      method: 'POST',
      path: '/documents',
      reply: () => ({ status: 201, body: document('pending_scan') }),
    },
    {
      method: 'PUT',
      path: `/documents/${DOCUMENT_ID}/content`,
      reply: () => ({ body: document('pending_scan') }),
    },
    { path: `/documents/${DOCUMENT_ID}`, reply: () => ({ body: document(scan) }) },
    {
      method: 'POST',
      path: '/imports/preview',
      reply: () => ({
        body: {
          format: 'csv',
          sheets: [],
          columns: ['Hostname', 'Seriennummer'],
          sample_rows: [
            ['srv-01', 'SN-1'],
            ['srv-02', 'SN-2'],
            ['srv-03', 'SN-3'],
          ],
        },
      }),
    },
    { path: '/import-profiles', reply: () => ({ body: list(profiles) }) },
    {
      method: 'POST',
      path: '/import-profiles',
      reply: (request) => {
        const body = request.postDataJSON() as Record<string, unknown>;
        const profile = {
          id: PROFILE_ID,
          organization_id: ORG_ID,
          description: null,
          created_by: USER_ID,
          created_at: AT,
          updated_at: AT,
          ...body,
        };
        profiles.push(profile);
        return { status: 201, body: profile };
      },
    },
    {
      method: 'PATCH',
      path: `/import-profiles/${PROFILE_ID}`,
      reply: (request) => {
        const index = profiles.findIndex((p) => p.id === PROFILE_ID);
        const profile = {
          ...profiles[index]!,
          ...(request.postDataJSON() as Record<string, unknown>),
        };
        profiles[index] = profile;
        return { body: profile };
      },
    },
    {
      path: new RegExp(`^/imports/(${DRY_JOB}|${RUN_JOB})/report$`),
      reply: (request, url) => ({
        body: {
          download_url: `${new URL(request.url()).origin}/e2e-downloads${url.pathname.replace('/api/v1', '')}.csv?signature=e2e`,
        },
      }),
    },
    { path: '/relationship-types', reply: () => ({ body: list([]) }) },
    {
      method: 'POST',
      path: '/imports',
      reply: () => {
        starts += 1;
        return {
          status: 202,
          body: { job_id: starts === 1 ? DRY_JOB : RUN_JOB, status: 'queued', kind: 'import.run' },
        };
      },
    },
    { path: `/jobs/${DRY_JOB}`, reply: () => ({ body: job(DRY_JOB, 2, 0, true) }) },
    { path: `/jobs/${RUN_JOB}`, reply: () => ({ body: job(RUN_JOB, 1, 1, false) }) },
    { path: `/imports/${DRY_JOB}`, reply: () => ({ body: run(DRY_JOB, true) }) },
    { path: `/imports/${RUN_JOB}`, reply: () => ({ body: run(RUN_JOB, false) }) },
    {
      path: new RegExp(`^/imports/(${DRY_JOB}|${RUN_JOB})/errors$`),
      reply: (_request, url) => ({ body: rowErrors(url.searchParams.get('cursor')) }),
    },
  ];
}

/** rowErrors is a page of GET /imports/{id}/errors: two pages of one error each. */
function rowErrors(cursor: string | null) {
  const second = cursor === ERRORS_CURSOR;
  return {
    data: [
      second
        ? { line: 4, field: 'serial_number', code: 'invalid_value', message: 'is required' }
        : { line: null, field: 'Typ', code: 'column_missing', message: 'Typ is missing' },
    ],
    total: 2,
    limit: 50,
    offset: second ? 1 : 0,
    has_more: !second,
    ...(second ? {} : { next_cursor: ERRORS_CURSOR }),
  };
}

async function upload(page: Page) {
  await page.getByLabel('Importdatei hochladen').setInputFiles({
    name: 'server.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(CSV),
  });
}

test('imports a CSV file: file, mapping, dry run, run and report', async ({ page }) => {
  const calls = await mockBackend(page, importHandlers('clean'));
  await signIn(page);
  await page.goto('/imports');

  await upload(page);
  await page.getByRole('button', { name: 'Weiter zur Zuordnung' }).click();
  const preview = page.getByRole('region', { name: 'Spaltenvorschau' });
  await expect(preview.getByText('srv-03')).toBeVisible();

  await page.getByLabel('Name des Profils').fill('Server-Import');
  await page.getByLabel('Ziel von Hostname').selectOption('name');
  await page.getByLabel('Ziel von Seriennummer').selectOption('serial_number');
  await page.getByLabel('Transformation für Hostname hinzufügen').selectOption('trim');
  await page.getByRole('checkbox', { name: 'Seriennummer' }).check();
  await page.getByRole('button', { name: 'Profil anlegen' }).click();
  await expect(page.getByText('Profil „Server-Import“ gespeichert.')).toBeVisible();
  expect(
    calls.find((c) => c.method === 'POST' && c.path === '/import-profiles')?.body,
  ).toMatchObject({
    entity: 'ci',
    format: 'csv',
    mode: 'both',
    mapping: {
      columns: {
        Hostname: { target: 'name', transforms: [{ op: 'trim' }] },
        Seriennummer: { target: 'serial_number', transforms: [] },
      },
    },
    identity_keys: ['serial_number'],
  });

  await page.getByRole('button', { name: 'Weiter zum Prüfen' }).click();
  await page.getByRole('button', { name: 'Dry Run starten' }).click();
  const dryReport = page.getByRole('region', { name: 'Bericht' });
  await expect(dryReport.getByText('Teilerfolg: 1 von 3 Zeilen fehlerhaft')).toBeVisible();
  await expect(dryReport.getByText('Dry Run', { exact: true })).toBeVisible();
  // The row errors page by page (GET /imports/{id}/errors, next_cursor).
  const errors = dryReport.getByRole('region', { name: 'Fehlerliste' });
  await expect(errors.getByText('column_missing')).toBeVisible();
  await expect(errors.getByText('1–1 von 2 Fehlern')).toBeVisible();
  await errors.getByRole('button', { name: 'Weiter', exact: true }).click();
  await expect(errors.getByText('invalid_value')).toBeVisible();
  await expect(errors.getByText('2–2 von 2 Fehlern')).toBeVisible();
  expect(
    calls
      .filter((c) => c.path === `/imports/${DRY_JOB}/errors`)
      .map((c) => new URLSearchParams(c.search).get('cursor')),
  ).toEqual([null, ERRORS_CURSOR]);

  await page.getByRole('button', { name: 'Weiter zum Ausführen' }).click();
  await page.getByRole('button', { name: 'Import ausführen' }).click();
  const report = page.getByRole('region', { name: 'Bericht' });
  await expect(report.getByText('Teilerfolg: 1 von 3 Zeilen fehlerhaft')).toBeVisible();
  await expect(report.getByRole('button', { name: 'Bericht herunterladen' })).toBeVisible();

  expect(
    calls.filter((c) => c.method === 'POST' && c.path === '/imports').map((c) => c.body),
  ).toEqual([
    { document_id: DOCUMENT_ID, profile_id: PROFILE_ID, mode: 'both', dry_run: true },
    { document_id: DOCUMENT_ID, profile_id: PROFILE_ID, mode: 'both', dry_run: false },
  ]);
});

test('aborts the assistant when the file is infected', async ({ page }) => {
  const calls = await mockBackend(page, importHandlers('infected'));
  await signIn(page);
  await page.goto('/imports');

  await upload(page);
  await expect(
    page.getByText('Die Datei ist infiziert und in Quarantäne. Der Import ist abgebrochen.'),
  ).toBeVisible();
  await expect(page.getByRole('button', { name: 'Weiter zur Zuordnung' })).toHaveCount(0);
  expect(calls.some((c) => c.path === '/imports/preview')).toBe(false);
});

test('saves a profile, reuses it for the next file and downloads the error report', async ({
  page,
}) => {
  const calls = await mockBackend(page, importHandlers('clean'));
  // The signed link of the report: the browser opens it in a new tab.
  await page.context().route('**/e2e-downloads/**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'text/plain; charset=utf-8',
      body: 'Zeile;Feld;Code;Meldung\n;Typ;column_missing;Typ is missing\n4;serial_number;invalid_value;is required\n',
    }),
  );
  await signIn(page);
  await page.goto('/imports');

  // Save a new profile, then change and save it again.
  await upload(page);
  await page.getByRole('button', { name: 'Weiter zur Zuordnung' }).click();
  const profile = page.getByLabel('Profil', { exact: true });
  await expect(profile.locator('option')).toHaveText([
    'Neues Profil',
    `${IMPORT_PROFILE.name} (Assets)`,
  ]);
  await page.getByLabel('Name des Profils').fill('Server-Import');
  await page.getByLabel('Ziel von Hostname').selectOption('name');
  await page.getByRole('button', { name: 'Profil anlegen' }).click();
  await expect(page.getByText('Profil „Server-Import“ gespeichert.')).toBeVisible();
  await page.getByLabel('Ziel von Seriennummer').selectOption('serial_number');
  await page.getByRole('checkbox', { name: 'Seriennummer' }).check();
  await expect(
    page.getByText('Ungespeicherte Änderungen: vor dem Prüfen speichern.'),
  ).toBeVisible();
  await expect(page.getByRole('button', { name: 'Weiter zum Prüfen' })).toBeDisabled();
  await page.getByRole('button', { name: 'Profil speichern' }).click();
  await expect(page.getByText('Profil „Server-Import“ gespeichert.')).toBeVisible();
  expect(
    calls.find((c) => c.method === 'PATCH' && c.path === `/import-profiles/${PROFILE_ID}`)?.body,
  ).toMatchObject({
    name: 'Server-Import',
    mapping: {
      columns: {
        Hostname: { target: 'name' },
        Seriennummer: { target: 'serial_number' },
      },
    },
    identity_keys: ['serial_number'],
  });

  // The next file in a new session of the assistant takes the saved profile.
  await page.reload();
  await upload(page);
  await page.getByRole('button', { name: 'Weiter zur Zuordnung' }).click();
  await expect(profile.locator('option')).toHaveText([
    'Neues Profil',
    `${IMPORT_PROFILE.name} (Assets)`,
    'Server-Import (CIs)',
  ]);
  await profile.selectOption({ label: 'Server-Import (CIs)' });
  await expect(page.getByLabel('Name des Profils')).toHaveValue('Server-Import');
  await expect(page.getByLabel('Ziel von Hostname')).toHaveValue('name');
  await expect(page.getByLabel('Ziel von Seriennummer')).toHaveValue('serial_number');
  await expect(page.getByRole('checkbox', { name: 'Seriennummer' })).toBeChecked();
  await page.getByRole('button', { name: 'Weiter zum Prüfen' }).click();
  await page.getByRole('button', { name: 'Dry Run starten' }).click();
  const report = page.getByRole('region', { name: 'Bericht' });
  await expect(report.getByText('Teilerfolg: 1 von 3 Zeilen fehlerhaft')).toBeVisible();

  // The report with the row errors opens through its signed link.
  const opened = page.waitForEvent('popup');
  await report.getByRole('button', { name: 'Bericht herunterladen' }).click();
  const tab = await opened;
  await expect(tab.locator('body')).toContainText('Zeile;Feld;Code;Meldung');
  await expect(tab.locator('body')).toContainText('4;serial_number;invalid_value;is required');
  expect(tab.url()).toContain(`/e2e-downloads/imports/${DRY_JOB}/report.csv?signature=e2e`);
  await tab.close();

  expect(calls.filter((c) => c.method === 'POST' && c.path === '/import-profiles')).toHaveLength(1);
  expect(
    calls.filter((c) => c.method === 'POST' && c.path === '/imports').map((c) => c.body),
  ).toEqual([{ document_id: DOCUMENT_ID, profile_id: PROFILE_ID, mode: 'both', dry_run: true }]);
  expect(calls.filter((c) => c.path === `/imports/${DRY_JOB}/report`)).toHaveLength(1);
});
