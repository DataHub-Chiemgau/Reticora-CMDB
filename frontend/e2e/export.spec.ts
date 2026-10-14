import { expect, test } from '@playwright/test';
import { list, mockBackend, signIn } from './support/backend';

// TST-03 (WP-201): an export job is started, completes and offers its file
// with the manifest of omitted fields (EXP-01). Runs in every browser project.

test.use({ locale: 'de-DE' });

test('starts an export job and downloads the file', async ({ page }) => {
  const jobs: Record<string, unknown>[] = [];
  const calls = await mockBackend(page, [
    { path: '/exports', reply: () => ({ body: list(jobs) }) },
    {
      method: 'POST',
      path: '/exports',
      reply: (request) => {
        const { format } = request.postDataJSON() as { format: string };
        const job = {
          id: 'job-1',
          organization_id: 'org-1',
          format,
          status: 'completed',
          filters: {},
          row_count: 30,
          download_url: '/files/export-job-1.csv',
          manifest: {
            format,
            fields: ['id', 'name', 'status'],
            omitted: [{ field: 'serial_number', reason: 'pii' }],
          },
          created_at: '2026-10-14T08:00:00Z',
          updated_at: '2026-10-14T08:00:05Z',
        };
        jobs.unshift(job);
        return { status: 202, body: job };
      },
    },
  ]);
  await page.route('**/files/export-job-1.csv', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'text/csv',
      headers: { 'Content-Disposition': 'attachment; filename="cis.csv"' },
      body: '﻿id;name;status\nci-1;core-sw-01;active\n',
    }),
  );
  await signIn(page);
  await page.goto('/export');

  await page.getByLabel('Format').selectOption('csv');
  await page.getByRole('button', { name: 'Auftrag erstellen' }).click();
  expect(calls.find((c) => c.method === 'POST' && c.path === '/exports')?.body).toEqual({
    format: 'csv',
  });

  const download = page.getByRole('link', { name: 'Herunterladen' });
  await expect(download).toBeVisible();
  await expect(download).toHaveAttribute('href', '/files/export-job-1.csv');
  await expect(page.getByRole('cell', { name: /^30/ })).toBeVisible();
  await expect(page.getByText('1 Felder nicht enthalten')).toBeVisible();
  // The link serves the file (fetched in the page, through the mock).
  const file = await page.evaluate(async () => (await fetch('/files/export-job-1.csv')).text());
  expect(file).toContain('id;name;status');
});
