import type { Page } from '@playwright/test';
import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';
import type { Handler } from './support/backend';

// IPM-09, UI-14 (WP-922): import and export of the IPAM page. The action
// Import/Export opens a dialog; the import creates the missing profile
// "phpIPAM Subnetze" and leads into the import assistant with that profile
// (/imports?profile=<id>), where a dry run of a phpIPAM subnet file shows
// its row report. The export takes the active VRF filter of the page into
// filters.ipam and creates a CSV job. Requests and mocks are checked
// against api/openapi.yaml (support/contract.ts).

test.use({ locale: 'de-DE' });

const USER = '00000000-0000-0000-0000-000000000001';
const SECTION = 'a9220000-0000-4000-8000-000000000001';
const VRF = 'a9220000-0000-4000-8000-000000000002';
const VRF_LAB = 'a9220000-0000-4000-8000-000000000003';
const PROFILE = 'a9220000-0000-4000-8000-000000000004';
const DOCUMENT = 'a9220000-0000-4000-8000-000000000005';
const DRY_JOB = 'a9220000-0000-4000-8000-000000000006';
const EXPORT_JOB = 'a9220000-0000-4000-8000-000000000007';
const CSV =
  'section,subnet,mask,description,vrf\ndefault,10.92.0.0,24,office,global\nnope,10.92.1.0,24,lab,global\n';

const vrf = (id: string, name: string, isDefault: boolean) => ({
  id,
  organization_id: ORG_ID,
  name,
  is_default: isDefault,
  import_targets: [],
  export_targets: [],
  section_ids: [],
  created_at: AT,
  updated_at: AT,
});

function document(scan: string) {
  return {
    id: DOCUMENT,
    organization_id: ORG_ID,
    title: 'subnets',
    file_name: 'subnets.csv',
    file_size: CSV.length,
    mime_type: 'text/csv',
    storage_key: '',
    version: 1,
    category: 'general',
    tags: [],
    uploaded_by: USER,
    created_at: AT,
    updated_at: AT,
    scan_status: scan,
  };
}

async function mockIpamIO(page: Page) {
  const profiles: Record<string, unknown>[] = [];
  const handlers: Handler[] = [
    {
      path: '/me/permissions',
      reply: () => ({
        body: {
          user_id: USER,
          permissions: ['ipam:read', 'ipam:write', 'import:run', 'import:manage', 'export:run'],
        },
      }),
    },
    {
      path: '/ipam-sections',
      reply: () => ({
        body: list([
          {
            id: SECTION,
            organization_id: ORG_ID,
            name: 'default',
            strict_mode: false,
            show_vlan: true,
            show_vrf: true,
            is_default: true,
            acl_enabled: false,
            ptr_mode: 'off',
            vrf_ids: [],
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    {
      path: '/vrfs',
      reply: () => ({ body: list([vrf(VRF, 'global', true), vrf(VRF_LAB, 'lab', false)]) }),
    },
    { path: '/subnets', reply: () => ({ body: list([]) }) },
    { path: '/import-profiles', reply: () => ({ body: list(profiles) }) },
    {
      method: 'POST',
      path: '/import-profiles',
      reply: (request) => {
        const created = {
          id: PROFILE,
          organization_id: ORG_ID,
          description: null,
          sheet: null,
          created_by: USER,
          created_at: AT,
          updated_at: AT,
          ...(request.postDataJSON() as Record<string, unknown>),
        };
        profiles.push(created);
        return { status: 201, body: created };
      },
    },
    {
      method: 'POST',
      path: '/documents',
      reply: () => ({ status: 201, body: document('pending_scan') }),
    },
    {
      method: 'PUT',
      path: `/documents/${DOCUMENT}/content`,
      reply: () => ({ body: document('pending_scan') }),
    },
    { path: `/documents/${DOCUMENT}`, reply: () => ({ body: document('clean') }) },
    {
      method: 'POST',
      path: '/imports/preview',
      reply: () => ({
        body: {
          format: 'csv',
          sheets: [],
          columns: ['section', 'subnet', 'mask', 'description', 'vrf'],
          sample_rows: [
            ['default', '10.92.0.0', '24', 'office', 'global'],
            ['nope', '10.92.1.0', '24', 'lab', 'global'],
          ],
        },
      }),
    },
    {
      method: 'POST',
      path: '/imports',
      reply: () => ({
        status: 202,
        body: { job_id: DRY_JOB, status: 'queued', kind: 'import.run' },
      }),
    },
    {
      path: `/jobs/${DRY_JOB}`,
      reply: () => ({
        body: {
          id: DRY_JOB,
          organization_id: ORG_ID,
          kind: 'import.run',
          status: 'succeeded',
          progress: 100,
          result: {
            report_key: `imports/${DRY_JOB}/report.csv`,
            dry_run: true,
            total: 2,
            created: 1,
            updated: 0,
            skipped: 0,
            failed: 1,
          },
          created_by: USER,
          attempts: 1,
          max_attempts: 3,
          created_at: AT,
          updated_at: AT,
          started_at: AT,
          finished_at: AT,
          cancellable: false,
        },
      }),
    },
    {
      path: `/imports/${DRY_JOB}`,
      reply: () => ({
        body: {
          id: DRY_JOB,
          kind: 'import.run',
          status: 'succeeded',
          progress: 100,
          source: 'document',
          document_id: DOCUMENT,
          document_version: 1,
          row_count: null,
          profile_id: PROFILE,
          mode: 'both',
          dry_run: true,
          sheet: null,
          rows_total: 2,
          rows_failed: 1,
          error_code: null,
          error: null,
          cancellable: false,
          created_by: USER,
          created_at: AT,
          started_at: AT,
          finished_at: AT,
        },
      }),
    },
    {
      path: `/imports/${DRY_JOB}/errors`,
      reply: () => ({
        body: {
          data: [
            { line: 3, field: 'section', code: 'invalid_value', message: 'unknown section nope' },
          ],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        },
      }),
    },
    {
      method: 'POST',
      path: '/exports',
      reply: (request) => {
        const body = request.postDataJSON() as { format: string; filters: unknown };
        return {
          status: 202,
          body: {
            id: EXPORT_JOB,
            organization_id: ORG_ID,
            format: body.format,
            status: 'pending',
            filters: body.filters,
            created_at: AT,
            updated_at: AT,
          },
        };
      },
    },
  ];
  return mockBackend(page, handlers);
}

test('creates the phpIPAM profile and dry-runs a subnet file with its row report', async ({
  page,
}) => {
  const calls = await mockIpamIO(page);
  await signIn(page);
  await page.goto('/ipam');

  const action = page.getByRole('button', { name: 'Import/Export' });
  await action.focus();
  await action.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Import und Export' });
  await expect(
    dialog.getByText('Das Profil „phpIPAM Subnetze“ wird mit den phpIPAM-Spalten angelegt.'),
  ).toBeVisible();
  const go = dialog.getByRole('button', { name: 'Zum Importassistenten' });
  await go.focus();
  await go.press('Enter');
  await expect(page).toHaveURL(new RegExp(`/imports\\?profile=${PROFILE}$`));
  await expect(page.getByTestId('import-preset')).toHaveText(
    'Vorgewählt: Profil „phpIPAM Subnetze“ (Subnetze).',
  );

  await page.getByLabel('Importdatei hochladen').setInputFiles({
    name: 'subnets.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(CSV),
  });
  await page.getByRole('button', { name: 'Weiter zur Zuordnung' }).click();
  await expect(page.getByLabel('Profil', { exact: true })).toHaveValue(PROFILE);
  await page.getByRole('button', { name: 'Weiter zum Prüfen' }).click();
  await page.getByRole('button', { name: 'Dry Run starten' }).click();
  const report = page.getByRole('region', { name: 'Bericht' });
  await expect(report.getByText('Teilerfolg: 1 von 2 Zeilen fehlerhaft')).toBeVisible();
  await expect(report.getByText('unknown section nope')).toBeVisible();

  const created = calls.find((c) => c.method === 'POST' && c.path === '/import-profiles');
  expect(created?.body).toMatchObject({
    name: 'phpIPAM Subnetze',
    entity: 'subnet',
    format: 'csv',
    mode: 'both',
    identity_keys: ['vrf', 'cidr'],
  });
  expect(
    calls.filter((c) => c.method === 'POST' && c.path === '/imports').map((c) => c.body),
  ).toEqual([{ document_id: DOCUMENT, profile_id: PROFILE, mode: 'both', dry_run: true }]);
});

test('exports the subnets with the active VRF filter as a CSV job', async ({ page }) => {
  const calls = await mockIpamIO(page);
  await signIn(page);
  await page.goto('/ipam');

  await page.getByLabel('VRF', { exact: true }).selectOption({ label: 'lab' });
  const action = page.getByRole('button', { name: 'Import/Export' });
  await action.focus();
  await action.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Import und Export' });
  const tab = dialog.getByRole('tab', { name: 'Export' });
  await tab.focus();
  await tab.press('Enter');
  await expect(dialog.getByTestId('ipam-io-filters')).toContainText('VRF:lab');
  const start = dialog.getByRole('button', { name: 'Export starten' });
  await start.focus();
  await start.press('Enter');
  await expect(dialog.getByRole('link', { name: 'Im Jobcenter ansehen' })).toHaveAttribute(
    'href',
    '/jobs',
  );

  expect(
    calls.filter((c) => c.method === 'POST' && c.path === '/exports').map((c) => c.body),
  ).toEqual([
    {
      format: 'csv',
      filters: { entity: 'subnet', ipam: { section_id: SECTION, vrf_id: VRF_LAB } },
    },
  ]);
});
