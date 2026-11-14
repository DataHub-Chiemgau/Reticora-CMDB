import AxeBuilder from '@axe-core/playwright';
import { AT, CI_TYPES, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// SEC-03, SEC-04 (WP-1135): on the compliance page a rule is created from a
// template (inactive), its condition changed in the click builder and a
// control assigned; the evaluation runs as a job, the score is grouped by
// client; on the security page a PDF report is generated as a job and
// downloaded over the presigned URL of its document. Requests and mocks are
// checked against api/openapi.yaml (support/contract.ts); axe finds no
// violations on both pages.

test.use({ locale: 'de-DE' });

const USER = '00000000-0000-0000-0000-000000000001';
const RULE_ID = 'c1135000-0000-4000-8000-000000000001';
const CONTROL_ID = 'c1135000-0000-4000-8000-0000000000c1';
const JOB_ID = 'c1135000-0000-4000-8000-0000000000a1';
const REPORT_JOB_ID = 'c1135000-0000-4000-8000-0000000000a2';
const DOCUMENT_ID = 'c1135000-0000-4000-8000-0000000000d1';
const CLIENT_ID = 'c1135000-0000-4000-8000-0000000000e1';
const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

function job(id: string, kind: string, status: string, result?: Record<string, unknown>) {
  return {
    id,
    organization_id: ORG_ID,
    kind,
    status,
    progress: status === 'succeeded' ? 100 : 0,
    attempts: 1,
    max_attempts: 3,
    created_at: AT,
    updated_at: AT,
    cancellable: status === 'queued',
    ...(result ? { result } : {}),
  };
}

test('creates a rule from a template, evaluates, groups the score and downloads a PDF report', async ({
  page,
}) => {
  const rule: Record<string, unknown> = {
    id: RULE_ID,
    organization_id: ORG_ID,
    name: 'Firmware gepflegt',
    description: 'Vorlage firmware_maintained v1',
    severity: 'medium',
    category: 'security',
    expression: { op: 'not_empty', field: 'firmware_version' },
    applies_when: null,
    remediation_hint: 'Firmware eintragen',
    active: false,
    created_at: AT,
    updated_at: AT,
  };
  const rules: Record<string, unknown>[] = [];
  let controlIds: string[] = [];
  const control = {
    id: CONTROL_ID,
    standard: 'iso27001',
    control_id: '8.8',
    title_de: 'Technische Schwachstellen',
    title_en: 'Technical vulnerabilities',
    sort_order: 1,
    rule_count: 0,
    active_rule_count: 0,
  };
  let evaluations = 0;
  let reportStatus = 'queued';
  const score = (groupBy: string | null) => ({
    overall: { passed: 3, failed: 1, not_applicable: 0, score: 75 },
    by_ci_type: [],
    results: [],
    ...(groupBy
      ? {
          group_by: groupBy,
          groups: [
            {
              key: CLIENT_ID,
              label: groupBy === 'client' ? 'Kunde Nord' : 'Server',
              passed: 3,
              failed: 1,
              not_applicable: 0,
              score: 75,
            },
          ],
        }
      : {}),
  });
  const calls = await mockBackend(page, [
    {
      path: '/me/permissions',
      reply: () => ({
        body: {
          user_id: USER,
          permissions: ['compliance:read', 'compliance:write', 'document:read'],
        },
      }),
    },
    {
      path: '/modules',
      reply: () => ({
        body: {
          data: [
            { key: 'compliance', phase: 'P3', nav: ['compliance', 'findings'], enabled: true },
          ],
        },
      }),
    },
    { path: '/ci-types', reply: () => ({ body: list(CI_TYPES) }) },
    { path: '/compliance/rules', reply: () => ({ body: list([...rules]) }) },
    {
      path: `/compliance/rules/${RULE_ID}`,
      reply: () => ({ body: { ...rule, control_ids: controlIds } }),
    },
    {
      method: 'PATCH',
      path: `/compliance/rules/${RULE_ID}`,
      reply: (request) => {
        Object.assign(rule, request.postDataJSON());
        return { body: rule };
      },
    },
    {
      method: 'PUT',
      path: `/compliance/rules/${RULE_ID}/controls`,
      reply: (request) => {
        controlIds = (request.postDataJSON() as { control_ids: string[] }).control_ids;
        return { body: { rule_id: RULE_ID, control_ids: controlIds } };
      },
    },
    {
      path: '/compliance/rule-templates',
      reply: () => ({
        body: {
          data: [
            {
              key: 'firmware_maintained',
              version: 1,
              name: 'Firmware gepflegt',
              description: 'Die Firmware-Version ist gepflegt.',
              severity: 'medium',
              category: 'security',
              expression: { op: 'not_empty', field: 'firmware_version' },
              remediation_hint: 'Firmware eintragen',
              controls: [{ standard: 'iso27001', control_id: '8.8' }],
            },
          ],
        },
      }),
    },
    {
      method: 'POST',
      path: '/compliance/rule-templates/firmware_maintained/instantiate',
      reply: () => {
        rules.push(rule);
        return { status: 201, body: { ...rule, control_ids: controlIds } };
      },
    },
    { path: '/compliance/controls', reply: () => ({ body: list([control]) }) },
    { path: '/compliance/controls/coverage', reply: () => ({ body: list([control]) }) },
    { path: '/compliance/results', reply: () => ({ body: list([]) }) },
    {
      path: '/compliance/score',
      reply: (_request, url) => ({ body: score(url.searchParams.get('group_by')) }),
    },
    {
      path: '/compliance/score/history',
      reply: (_request, url) => ({
        body: {
          group_by: 'overall',
          from: url.searchParams.get('from'),
          to: url.searchParams.get('to'),
          data: [
            {
              day: '2026-10-13',
              key: '',
              label: '',
              passed: 2,
              failed: 2,
              passed_weight: 4,
              failed_weight: 4,
              score: 50,
            },
            {
              day: '2026-10-14',
              key: '',
              label: '',
              passed: 3,
              failed: 1,
              passed_weight: 6,
              failed_weight: 2,
              score: 75,
            },
          ],
        },
      }),
    },
    {
      method: 'POST',
      path: '/compliance/evaluations',
      reply: () => {
        evaluations++;
        return { status: 202, body: job(JOB_ID, 'compliance.evaluate', 'queued') };
      },
    },
    {
      path: `/jobs/${JOB_ID}`,
      reply: () => ({ body: job(JOB_ID, 'compliance.evaluate', 'succeeded') }),
    },
    {
      path: '/clients',
      reply: () => ({
        body: list([
          {
            id: CLIENT_ID,
            organization_id: ORG_ID,
            name: 'Kunde Nord',
            slug: 'kunde-nord',
            status: 'active',
            external_ref: '',
            timezone: 'Europe/Berlin',
            settings: {},
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    {
      path: '/compliance/report',
      reply: () => ({
        body: {
          generated_at: AT,
          standard: 'iso27001',
          audit_integrity: { intact: true, checked: 12 },
          compliance_score: { passed: 3, failed: 1, not_applicable: 0, score: 75 },
          failures_by_severity: {},
          findings: [],
        },
      }),
    },
    // The attributes the rule editor offers (attributes.<key>).
    { path: '/attribute-definitions', reply: () => ({ body: list([]) }) },
    {
      // The report enters the list once its job is done, its scan clean.
      path: '/compliance/reports',
      reply: () => ({
        body: list(
          reportStatus === 'succeeded'
            ? [
                {
                  document_id: DOCUMENT_ID,
                  title: 'ISO 27001',
                  file_name: 'iso27001.pdf',
                  standard: 'iso27001',
                  format: 'pdf',
                  scope: 'organization',
                  scan_status: 'clean',
                  created_at: AT,
                },
              ]
            : [],
        ),
      }),
    },
    {
      method: 'POST',
      path: '/compliance/reports',
      reply: () => ({ status: 202, body: job(REPORT_JOB_ID, 'compliance.report', 'queued') }),
    },
    {
      path: `/jobs/${REPORT_JOB_ID}`,
      reply: () => {
        const body = job(
          REPORT_JOB_ID,
          'compliance.report',
          reportStatus,
          reportStatus === 'succeeded' ? { document_id: DOCUMENT_ID } : undefined,
        );
        reportStatus = 'succeeded';
        return { body };
      },
    },
    {
      path: `/documents/${DOCUMENT_ID}/content`,
      reply: () => ({ body: { download_url: 'https://blob.example/report.pdf?sig=1135' } }),
    },
    {
      path: '/privacy/retention',
      reply: () => ({
        body: {
          id: 'c1135000-0000-4000-8000-0000000000f1',
          organization_id: ORG_ID,
          retention_days: 0,
          mode: 'anonymize',
          created_at: AT,
          updated_at: AT,
        },
      }),
    },
  ]);
  await page.addInitScript(() => {
    (window as unknown as { __opened: string[] }).__opened = [];
    window.open = (url?: string | URL) => {
      (window as unknown as { __opened: string[] }).__opened.push(String(url));
      return null;
    };
  });
  await signIn(page);
  await page.goto('/compliance');

  // Template → inactive rule → editor.
  await page.getByRole('button', { name: 'Aus Vorlage anlegen' }).click();
  await page.getByRole('button', { name: 'Vorlage Firmware gepflegt anlegen' }).click();
  const editor = page.getByRole('dialog', { name: 'Compliance-Regel bearbeiten' });
  await expect(editor).toBeVisible();
  await expect(editor.getByRole('checkbox', { name: 'Aktiv' })).not.toBeChecked();

  // Change the condition: firmware_version equals 7.2.
  const condition = editor.getByTestId('rule-expression');
  const node = condition.getByRole('group', { name: 'Bedingung 1' });
  await node.getByRole('combobox', { name: 'Operator' }).selectOption('eq');
  await node.getByLabel('Wert').fill('7.2');

  // Assign the control 8.8 of ISO 27001.
  await editor.getByText('ISO 27001').click();
  await editor.getByRole('checkbox', { name: /8\.8/ }).check();

  const dialogScan = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze();
  expect(dialogScan.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);

  await editor.getByRole('button', { name: 'Speichern' }).click();
  await expect(editor).toBeHidden();
  expect(JSON.stringify(rule.expression)).toContain('"value":"7.2"');
  expect(controlIds).toEqual([CONTROL_ID]);

  // Evaluate as a job.
  await page.getByRole('button', { name: 'Bewerten' }).click();
  await expect(page.getByTestId('evaluation-job')).toContainText('Erfolgreich');
  expect(evaluations).toBe(1);

  // Score by client.
  await page.getByLabel('Gruppieren nach').selectOption('client');
  await expect(page.getByTestId('score-groups').getByText('Kunde Nord')).toBeVisible();

  const pageScan = await new AxeBuilder({ page }).withTags(WCAG_TAGS).include('main').analyze();
  expect(pageScan.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);

  // A PDF report as a job, downloaded once ready.
  await page.goto('/security');
  await page.getByRole('button', { name: 'Report erzeugen' }).click();
  await page.getByRole('button', { name: 'Herunterladen', exact: true }).click();
  await expect
    .poll(() => page.evaluate(() => (window as unknown as { __opened: string[] }).__opened))
    .toEqual(['https://blob.example/report.pdf?sig=1135']);
  const report = calls.find((c) => c.method === 'POST' && c.path === '/compliance/reports');
  expect(report?.body).toEqual({ standard: 'iso27001', format: 'pdf' });

  const securityScan = await new AxeBuilder({ page }).withTags(WCAG_TAGS).include('main').analyze();
  expect(securityScan.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);
});
