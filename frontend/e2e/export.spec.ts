import { readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// TST-03 (WP-201), EXP-01: an export job is started on the export page,
// completes and offers its file with the manifest of omitted fields; the
// file is downloaded. The export buttons of the CI list queue the same job
// (API-05, WP-468), follow it and download the file over its signed URL.
// Runs in every browser project; the requests and answers follow
// api/openapi.yaml (WP-530).

test.use({ locale: 'de-DE' });

const JOB = '55555555-5555-4555-8555-000000000001';
// The signed URL of the object store (outside /api/v1, without the bearer token).
const SIGNED = `http://minio.e2e.test:9000/exports/${ORG_ID}/${JOB}.csv?X-Amz-Signature=e2e`;
const CSV = '﻿id;name;status\nci-1;core-sw-01;active\n';

type Job = Record<string, unknown> & { status: string };

/** exportJobs answers POST /exports (pending) and completes the job on the next read. */
function exportJobs() {
  const jobs: Job[] = [];
  const complete = (job: Job) => {
    if (job.status === 'pending') {
      Object.assign(job, {
        status: 'completed',
        row_count: 30,
        completed_at: AT,
        expires_at: '2026-10-15T08:00:00Z',
        download_url: SIGNED,
        manifest: {
          format: job.format,
          fields: ['id', 'name', 'status'],
          omitted: [{ field: 'serial_number', reason: 'pii' }],
        },
      });
    }
    return job;
  };
  const handlers = [
    { path: '/exports', reply: () => ({ body: list(jobs.map(complete)) }) },
    {
      path: `/exports/${JOB}`,
      reply: () => ({ body: complete(jobs.find((j) => j.id === JOB)!) }),
    },
    {
      method: 'POST',
      path: '/exports',
      reply: (request: { postDataJSON: () => unknown }) => {
        const { format, filters } = request.postDataJSON() as {
          format: string;
          filters?: Record<string, string>;
        };
        const job: Job = {
          id: JOB,
          organization_id: ORG_ID,
          format,
          status: 'pending',
          filters: filters ?? {},
          created_at: AT,
          updated_at: AT,
        };
        jobs.unshift(job);
        return { status: 202, body: job };
      },
    },
  ];
  return handlers;
}

async function serveSignedFile(page: Page) {
  const requests: Record<string, string>[] = [];
  await page.route('http://minio.e2e.test:9000/**', (route) => {
    requests.push(route.request().headers());
    return route.fulfill({
      status: 200,
      contentType: 'text/csv',
      headers: {
        'Content-Disposition': `attachment; filename="${JOB}.csv"`,
        'Access-Control-Allow-Origin': '*',
      },
      body: CSV,
    });
  });
  return requests;
}

test('starts an export job and downloads the file', async ({ page }) => {
  const calls = await mockBackend(page, exportJobs());
  const fileRequests = await serveSignedFile(page);
  await signIn(page);
  await page.goto('/export');

  await page.getByLabel('Format').selectOption('csv');
  await page.getByRole('button', { name: 'Auftrag erstellen' }).click();
  await expect
    .poll(() =>
      calls.filter((c) => c.method === 'POST' && c.path === '/exports').map((c) => c.body),
    )
    .toEqual([{ format: 'csv' }]);

  const link = page.getByRole('link', { name: 'Herunterladen' });
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute('href', SIGNED);
  await expect(page.getByRole('cell', { name: /^30/ })).toBeVisible();
  await expect(page.getByText('1 Felder nicht enthalten')).toBeVisible();

  const [download] = await Promise.all([page.waitForEvent('download'), link.click()]);
  expect(readFileSync((await download.path())!, 'utf8')).toBe(CSV);
  expect(fileRequests).toHaveLength(1);
  expect(fileRequests[0]!.authorization).toBeUndefined();
});

test('exports the CI list with its filters as a job and downloads the file', async ({ page }) => {
  const ci = {
    id: '11111111-1111-4111-8111-111111111111',
    organization_id: ORG_ID,
    ci_type_id: '5e1b0c3a-7d2f-4a10-9c01-000000000002',
    name: 'core-sw-01',
    status: 'active',
    attributes: {},
    is_manual: true,
    created_at: AT,
    updated_at: AT,
  };
  const calls = await mockBackend(page, [
    { path: '/cis', reply: () => ({ body: list([ci]) }) },
    ...exportJobs(),
  ]);
  const fileRequests = await serveSignedFile(page);
  await signIn(page);
  await page.goto('/cmdb');
  await expect(page.getByRole('link', { name: 'core-sw-01' })).toBeVisible();
  await page.getByRole('combobox', { name: 'CI-Status filtern' }).selectOption('active');

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'CSV exportieren' }).click(),
  ]);
  await expect(page.getByText('CSV-Export heruntergeladen (30 Zeilen)')).toBeVisible();
  expect(download.suggestedFilename()).toBe('cis_export.csv');
  expect(readFileSync((await download.path())!, 'utf8')).toBe(CSV);

  // The job carries the list filter; the page followed the job to completed
  // and fetched the signed URL without the session's bearer token.
  expect(
    calls.filter((c) => c.method === 'POST' && c.path === '/exports').map((c) => c.body),
  ).toEqual([{ format: 'csv', filters: { status: 'active' } }]);
  expect(calls.some((c) => c.method === 'GET' && c.path === `/exports/${JOB}`)).toBe(true);
  expect(fileRequests).toHaveLength(1);
  expect(fileRequests[0]!.authorization).toBeUndefined();
});
