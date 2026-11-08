import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import Ajv2020 from 'ajv/dist/2020.js';
import { parse } from 'yaml';
import { FEATURES, expect, list, mockBackend, signIn, test } from './support/backend';
import type { Call, Handler, Reply } from './support/backend';

// ABN-01 (c), metamodel scenario [P2] (WP-330, E-136). In the order of the
// catalog (docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:97) an
// org_admin (1) creates a type with a parent, (2) assigns an attribute set,
// (3) combines two templates in a defined order, (4) defines a rule in the
// click builder, (5) creates a layout for the role engineer in a draft,
// (6) previews the form as engineer with one of the templates without a
// write and (7) publishes the draft (draft → test → publish); (8) a normal
// user with the role engineer creates a CI through the guided creation and
// is refused a write on the metamodel; (9) the org_admin sets an override
// and migrates a template version, and the override stays. Each step checks
// the page and the requests it sent. The data is frozen in
// docs/acceptance/artefakte/abn-01-c-metamodell.json (ids of the seed of
// WP-325, md5(key)::uuid), the protocol is docs/acceptance/abn-01.md. Runs
// in every browser project; the strict mock checks every request and answer
// against api/openapi.yaml (WP-530). The effect in the backend is proven by
// the integration tests the protocol names per step.

test.use({ locale: 'de-DE' });

type Json = Record<string, unknown>;

interface FrozenRequest {
  method: string;
  path: string;
  query?: Record<string, string>;
  if_match?: string;
  body?: Json;
}

interface Step {
  titel: string;
  anfrage: FrozenRequest;
  antwort: { status: number; body: Json };
  [more: string]: unknown;
}

interface Session {
  user_id: string;
  name: string;
  permissions: string[];
}

interface Frozen {
  ids: Record<string, { schluessel: string; id: string; quelle: string }>;
  sitzungen: Record<'org_admin' | 'engineer', Session>;
  bestand: {
    ci_types: Json[];
    attribute_definitions: Json[];
    attribute_sets: Json[];
    set_attributes: Record<string, Json[]>;
    type_sets: Record<string, string[]>;
    templates: Json[];
    template_versions: Record<string, Json[]>;
    roles: Json[];
    referenz_ci: Json;
  };
  schritte: Record<string, Step>;
}

interface MigrationStep extends Step {
  override: { field: string; value: string; reason: string };
  herkunft_vorher: { data: Json[] };
  override_antwort: Json;
  herkunft_nachher: { data: Json[] };
  felder_nachher: { data: Json[] };
  templates_nachher: { data: Json[] };
  migration: {
    template_id: string;
    probelauf: Json;
    lauf: Json;
    job_probelauf: string;
    job_lauf: string;
    bericht_probelauf: Json;
    bericht: Json;
  };
}

const frozen = JSON.parse(
  readFileSync(
    new URL('../../docs/acceptance/artefakte/abn-01-c-metamodell.json', import.meta.url),
    'utf8',
  ),
) as Frozen;

const B = frozen.bestand;
const id = (name: string) => frozen.ids[name]!.id;
const step = (n: number) => frozen.schritte[String(n)]!;
const s9 = step(9) as MigrationStep;

const ORG = id('organisation');
const BLADE = id('typ_blade_server');
const NEW_TYPE = id('typ_neu');
const REF_CI = id('referenz_ci');
const T2 = id('template_storage');
const ROLE_ENGINEER = id('rolle_engineer');
const DRAFT = id('draft');
const TEST_JOB = id('job_test');
const AT = '2026-10-14T08:00:00Z';

/** seedID is ID of backend/cmd/seed/seed.go: md5(key)::uuid. */
function seedID(key: string) {
  const h = createHash('md5').update(key).digest('hex');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

/** The schema of a job result is the kind's; the template migration answers this one. */
function migrationReportErrors(report: unknown) {
  const doc = parse(readFileSync(new URL('../../api/openapi.yaml', import.meta.url), 'utf8')) as {
    components: { schemas: Record<string, object> };
  };
  const ajv = new Ajv2020({ strict: false, allErrors: true });
  const validate = ajv.compile(doc.components.schemas.TemplateMigrationReport!);
  return validate(report) ? [] : (validate.errors ?? []);
}

const fixed =
  (body: unknown, status = 200) =>
  (): Reply => ({ status, body });

/** writes are the calls that change something. */
const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET');
const query = (call: Call) => Object.fromEntries(new URLSearchParams(call.search));

test('the frozen ids are those of the seed (md5(key)::uuid)', () => {
  for (const [name, entry] of Object.entries(frozen.ids)) {
    expect(entry.id, name).toBe(seedID(entry.schluessel));
  }
  expect(frozen.ids.organisation!.schluessel).toBe('/org/demo');
  expect(migrationReportErrors(s9.migration.bericht_probelauf)).toEqual([]);
  expect(migrationReportErrors(s9.migration.bericht)).toEqual([]);
});

test('ABN-01 (c): metamodel from the type to the template migration', async ({ page }) => {
  test.slow();
  let session: Session = frozen.sitzungen.org_admin;

  // The state of the mocked server, changed by the writes of the steps.
  const types: Json[] = [...B.ci_types];
  const setsOf = (keys: string[]) =>
    keys.map((key, i) => ({
      attribute_set_id: B.attribute_sets.find((s) => s.key === key)!.id as string,
      position: i + 1,
    }));
  const typeSets: Record<string, { attribute_set_id: string; position: number }[]> = {
    [BLADE]: setsOf(B.type_sets.blade_server ?? []),
  };
  let ci: Json = B.referenz_ci;
  let assigned: Json[] = [];
  const templatesIfMatch: (string | undefined)[] = [];
  const rules: Json[] = [];
  const layouts: Json[] = [];
  const draftItems: Json[] = [];
  const createdCIs: Json[] = [];
  let draftCreated = false;
  let draftStatus = 'draft';
  let overridden = false;
  const s5 = step(5);
  const testReport = step(7).test_report as Json;
  const draft = () => ({
    ...(s5.draft as Json),
    status: draftStatus,
    version: draftItems.length + 1,
    item_count: draftItems.length,
    test_report: draftStatus === 'draft' ? null : testReport,
    created_at: AT,
    updated_at: AT,
    published_at: draftStatus === 'published' ? AT : null,
  });
  const job = (jobId: string, kind: string) => ({
    id: jobId,
    organization_id: ORG,
    kind,
    status: 'succeeded',
    progress: 100,
    result: {},
    created_by: session.user_id,
    attempts: 1,
    max_attempts: 1,
    created_at: AT,
    updated_at: AT,
    started_at: AT,
    finished_at: AT,
    cancellable: false,
  });

  const handlers: Handler[] = [
    // The session (org_admin, from step 8 on the engineer), the module
    // metamodel and the licensed features, ahead of the DEFAULTS.
    {
      path: '/me/permissions',
      reply: () => ({ body: { user_id: session.user_id, permissions: session.permissions } }),
    },
    {
      path: '/modules',
      reply: fixed({
        data: [{ key: 'metamodel', phase: 'P2', nav: ['metamodel', 'rules'], enabled: true }],
      }),
    },
    {
      path: '/entitlements',
      reply: fixed(
        list(
          FEATURES.map((feature_key) => ({
            organization_id: ORG,
            feature_key,
            plan: 'enterprise',
            enabled: true,
            limits: {},
            source: 'manual',
          })),
        ),
      ),
    },

    // Types, attribute definitions and sets (steps 1, 2 and 8).
    { path: '/ci-types', reply: () => ({ body: list(types) }) },
    {
      path: /^\/ci-types\/[0-9a-f-]{36}$/,
      reply: (_request, url) => {
        const found = types.find((t) => url.pathname.endsWith(`/${t.id as string}`));
        return found ? { body: found } : { status: 404 };
      },
    },
    { path: '/ci-types/templates', reply: fixed(list([])) },
    {
      method: 'POST',
      path: '/ci-types',
      reply: () => {
        types.push(step(1).antwort.body);
        return step(1).antwort;
      },
    },
    { path: '/attribute-sets', reply: fixed({ data: B.attribute_sets }) },
    {
      path: /^\/ci-types\/[0-9a-f-]{36}\/attribute-sets$/,
      reply: (_request, url) => {
        const typeId = url.pathname.split('/').at(-2)!;
        return { body: { ci_type_id: typeId, attribute_sets: typeSets[typeId] ?? [] } };
      },
    },
    {
      method: 'PUT',
      path: `/ci-types/${NEW_TYPE}/attribute-sets`,
      reply: () => {
        const answer = step(2).antwort;
        typeSets[NEW_TYPE] = answer.body.attribute_sets as {
          attribute_set_id: string;
          position: number;
        }[];
        return answer;
      },
    },
    {
      path: '/attribute-definitions',
      reply: (_request, url) => {
        const q = url.searchParams;
        const setId = q.get('attribute_set_id');
        if (setId) {
          const set = B.attribute_sets.find((s) => s.id === setId);
          return { body: { data: set ? (B.set_attributes[set.key as string] ?? []) : [] } };
        }
        const typeId = q.get('ci_type_id') ?? q.get('applicable_to');
        return { body: { data: typeId === BLADE ? B.attribute_definitions : [] } };
      },
    },
    { path: '/field-permissions/sensitive-fields', reply: fixed(list([])) },
    {
      method: 'PATCH',
      path: /^\/attribute-definitions\/[0-9a-f-]{36}$/,
      reply: () => (step(8).verweigert as { antwort: Reply }).antwort,
    },

    // The reference CI of the seed with its templates and provenance (steps 3 and 9).
    { path: `/cis/${REF_CI}`, reply: () => ({ body: ci }) },
    { path: `/cis/${REF_CI}/templates`, reply: () => ({ body: { data: assigned } }) },
    {
      method: 'PUT',
      path: `/cis/${REF_CI}/templates`,
      reply: (request) => {
        templatesIfMatch.push(request.headers()['if-match']);
        const answer = step(3).antwort;
        ci = answer.body;
        assigned = ci.templates as Json[];
        return answer;
      },
    },
    {
      path: `/cis/${REF_CI}/provenance`,
      reply: () => ({ body: overridden ? s9.herkunft_nachher : s9.herkunft_vorher }),
    },
    {
      path: /^\/topology\/cis\/[^/]+\/neighbors$/,
      reply: fixed({ nodes: [], edges: [] }),
    },
    {
      path: '/templates',
      reply: (_request, url) => {
        const typeId = url.searchParams.get('ci_type_id');
        return { body: list(B.templates.filter((t) => !typeId || t.ci_type_id === typeId)) };
      },
    },
    {
      path: /^\/templates\/[0-9a-f-]{36}\/versions$/,
      reply: (_request, url) => {
        const tpl = B.templates.find((t) => url.pathname.includes(`/${t.id as string}/`));
        return { body: { data: tpl ? B.template_versions[tpl.key as string] : [] } };
      },
    },

    // The rule catalog (step 4).
    { path: '/rules', reply: () => ({ body: { data: rules } }) },
    {
      method: 'POST',
      path: '/rules',
      reply: () => {
        rules.push(step(4).antwort.body);
        return step(4).antwort;
      },
    },

    // Roles, resolved forms and layouts (steps 5, 6 and 8). The frozen
    // answers: the preview of the draft as engineer with the template
    // (step 6) and the form of the engineer with the template after the
    // publication (step 8); every other read of the form (the copy the
    // builder starts with, the preview before the role is chosen) is the
    // standard form of step 5 for the requested role and template.
    { path: '/roles', reply: fixed(list(B.roles)) },
    {
      path: `/ci-types/${BLADE}/form`,
      reply: (_request, url) => {
        const q = url.searchParams;
        const templateId = q.get('template_id');
        const roleId = q.get('role_id');
        if (templateId === T2 && roleId && q.get('draft')) return step(6).antwort;
        if (templateId === T2 && !roleId && !q.get('draft')) {
          return { body: step(8).formular };
        }
        const { role_id: _role, ...standard } = s5.formular_vorher as Json;
        void _role;
        return {
          body: {
            ...standard,
            ...(roleId ? { role_id: roleId } : {}),
            ...(templateId ? { template_id: templateId } : {}),
          },
        };
      },
    },
    {
      path: '/layouts',
      reply: (_request, url) => {
        const typeId = url.searchParams.get('ci_type_id');
        return { body: { data: layouts.filter((l) => l.ci_type_id === typeId), has_more: false } };
      },
    },
    {
      method: 'POST',
      path: '/layouts',
      reply: () => {
        draftItems.push(s5.antwort.body);
        return s5.antwort;
      },
    },

    // The draft work area (steps 5 and 7).
    {
      method: 'POST',
      path: '/metamodel/drafts',
      reply: () => {
        draftCreated = true;
        return { status: 201, body: draft() };
      },
    },
    {
      path: '/metamodel/drafts',
      reply: () => ({ body: { data: draftCreated ? [draft()] : [], has_more: false } }),
    },
    { path: `/metamodel/drafts/${DRAFT}`, reply: () => ({ body: draft() }) },
    {
      path: `/metamodel/drafts/${DRAFT}/items`,
      reply: () => ({ body: { data: draftItems, has_more: false } }),
    },
    {
      method: 'POST',
      path: `/metamodel/drafts/${DRAFT}/test`,
      reply: () => {
        draftStatus = 'tested';
        return {
          status: 202,
          body: { job_id: TEST_JOB, status: 'queued', kind: 'metamodel.test' },
        };
      },
    },
    { path: `/jobs/${TEST_JOB}`, reply: () => ({ body: job(TEST_JOB, 'metamodel.test') }) },
    {
      path: `/metamodel/drafts/${DRAFT}/test`,
      reply: () => ({ body: { ...testReport, data: [], total: 0, has_more: false } }),
    },
    {
      method: 'POST',
      path: `/metamodel/drafts/${DRAFT}/publish`,
      reply: () => {
        draftStatus = 'published';
        layouts.push(step(7).layout as Json);
        return { body: { draft: draft(), ...step(7).antwort.body } };
      },
    },

    // The CI list and the guided creation (step 8).
    {
      path: '/cis',
      reply: (_request, url) => {
        const suggestion = [...url.searchParams.keys()].some((k) => k.startsWith('filter['));
        return { body: list(suggestion ? [] : createdCIs) };
      },
    },
    {
      method: 'POST',
      path: '/cis',
      reply: () => {
        createdCIs.push(step(8).antwort.body);
        return step(8).antwort;
      },
    },
    { path: '/locations/tree', reply: fixed({ data: [] }) },

    // The override and the migration (step 9); the dialog offers saved views.
    {
      method: 'PUT',
      path: `/cis/${REF_CI}/fields/${s9.override.field}/override`,
      reply: () => {
        overridden = true;
        return { body: s9.override_antwort };
      },
    },
    { path: `/cis/${REF_CI}/fields`, reply: () => ({ body: s9.felder_nachher }) },
    { path: '/saved-views', reply: fixed(list([])) },
    {
      method: 'POST',
      path: `/templates/${T2}/migrations`,
      reply: (request) => {
        const dryRun = (request.postDataJSON() as { dry_run?: boolean }).dry_run === true;
        if (!dryRun) assigned = s9.templates_nachher.data;
        return {
          status: 202,
          body: {
            job_id: dryRun ? s9.migration.job_probelauf : s9.migration.job_lauf,
            status: 'queued',
            kind: 'template.migrate',
          },
        };
      },
    },
    {
      path: /^\/jobs\/[0-9a-f-]{36}$/,
      reply: (_request, url) => ({
        body: job(url.pathname.split('/').at(-1)!, 'template.migrate'),
      }),
    },
    {
      path: /^\/jobs\/[0-9a-f-]{36}\/result$/,
      reply: (_request, url) => ({
        body: url.pathname.includes(s9.migration.job_probelauf)
          ? s9.migration.bericht_probelauf
          : s9.migration.bericht,
      }),
    },
  ];
  const calls = await mockBackend(page, handlers);
  const signInAs = (role: 'org_admin' | 'engineer') => {
    session = frozen.sitzungen[role];
    return signIn(page, {
      sub: session.user_id,
      name: session.name,
      permissions: session.permissions,
    });
  };
  await signInAs('org_admin');

  // --- 1. Typ mit Elterntyp anlegen (WP-248).
  const s1 = step(1);
  const created = s1.antwort.body;
  const createdLabel = created.display_name as { de: string; en: string };
  await page.goto('/ci-types');
  await page.getByRole('button', { name: 'Typ anlegen' }).click();
  const typeDialog = page.getByRole('dialog');
  await typeDialog.getByLabel('Name', { exact: true }).fill(created.name as string);
  await typeDialog.getByLabel('Schlüssel').fill(created.key as string);
  await typeDialog.getByLabel('Anzeigename (Deutsch)').fill(createdLabel.de);
  await typeDialog.getByLabel('Anzeigename (Englisch)').fill(createdLabel.en);
  await typeDialog.getByLabel('Kategorie').fill(created.category as string);
  await typeDialog.getByLabel('Elterntyp').selectOption({ label: 'Blade-Server' });
  await typeDialog.getByRole('button', { name: 'Anlegen' }).click();
  await expect(typeDialog).toBeHidden();
  await expect(page.getByRole('treeitem', { name: createdLabel.de })).toBeVisible();
  const detail = page.locator('#type-detail');
  await expect(detail.getByText('Elterntyp: Blade-Server')).toBeVisible();
  // The attributes of the parent are inherited.
  await expect(detail.getByText('geerbt von Blade-Server').first()).toBeVisible();
  expect(writes(calls).map((c) => [c.method, c.path, c.body])).toEqual([
    [s1.anfrage.method, s1.anfrage.path, s1.anfrage.body],
  ]);

  // --- 2. Attribute Set zuordnen (WP-250, WP-797).
  const s2 = step(2);
  const sets = detail.getByRole('group', { name: 'Attribut-Sets dieses Typs' });
  await sets.getByLabel('Aktives Set').selectOption({ label: 'Stromversorgung' });
  await sets.getByRole('button', { name: 'Zuordnen' }).click();
  await expect(sets.getByRole('button', { name: 'Set entfernen: Stromversorgung' })).toBeVisible();
  expect(
    writes(calls)
      .slice(1)
      .map((c) => [c.method, c.path, c.body]),
  ).toEqual([[s2.anfrage.method, s2.anfrage.path, s2.anfrage.body]]);

  // --- 3. Zwei Templates in definierter Reihenfolge kombinieren (WP-710, WP-800).
  const s3 = step(3);
  await page.goto(`/cmdb/${REF_CI}`);
  await expect(
    page.getByRole('heading', { level: 1, name: B.referenz_ci.name as string }),
  ).toBeVisible();
  await page.locator('summary', { hasText: 'Templates' }).click();
  await expect(page.getByText('Dem CI ist kein Template zugeordnet.')).toBeVisible();
  const addTemplate = page.getByLabel('Template hinzufügen');
  const addVersion = page.getByLabel('Version', { exact: true });
  await addTemplate.selectOption({ label: 'Blade-Server (Compute)' });
  await expect(addVersion).toHaveValue('2');
  await page.getByRole('button', { name: 'Hinzufügen', exact: true }).click();
  await addTemplate.selectOption({ label: 'Blade-Server (Storage)' });
  await addVersion.selectOption('1');
  await page.getByRole('button', { name: 'Hinzufügen', exact: true }).click();
  await page.getByRole('button', { name: 'Templates speichern' }).click();
  const order = page.getByRole('list', { name: 'Templates des CI' });
  await expect(order.locator('[data-template-key]')).toHaveText([
    /Blade-Server \(Compute\)\s*Version 2 · Position 1/,
    /Blade-Server \(Storage\)\s*Version 1 · Position 2/,
  ]);
  await expect(page.getByRole('button', { name: 'Auf Version 2 migrieren' })).toBeVisible();
  expect(
    writes(calls)
      .filter((c) => c.path === s3.anfrage.path)
      .map((c) => c.body),
  ).toEqual([s3.anfrage.body]);
  expect(templatesIfMatch).toEqual([s3.anfrage.if_match]);

  // --- 4. Regel im Klick-Builder definieren (WP-801, WP-315).
  const s4 = step(4);
  const rule = s4.antwort.body;
  const condition = rule.condition as { field: string; op: string; value: string };
  await page.goto('/rules');
  await page.getByRole('button', { name: 'Regel anlegen' }).first().click();
  const ruleDialog = page.getByRole('dialog');
  await ruleDialog.getByLabel('Schlüssel').fill(rule.key as string);
  await ruleDialog.getByRole('combobox', { name: 'Regelart' }).selectOption('required');
  await ruleDialog
    .getByRole('combobox', { name: 'Ziel', exact: true })
    .selectOption({ label: 'Blade-Server' });
  await ruleDialog
    .getByRole('combobox', { name: 'Attribut', exact: true })
    .selectOption(rule.attribute_key as string);
  await ruleDialog
    .getByRole('group', { name: /^Gruppe/ })
    .getByRole('button', { name: 'Bedingung hinzufügen' })
    .click();
  const node = ruleDialog.getByRole('group', { name: 'Bedingung 1' });
  await node.getByRole('combobox', { name: 'Feld' }).selectOption(condition.field);
  await node.getByRole('combobox', { name: 'Operator' }).selectOption(condition.op);
  await node.getByLabel('Wert').selectOption(condition.value);
  await ruleDialog.getByLabel('Meldung Deutsch').fill((rule.message as { de: string }).de);
  await ruleDialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(ruleDialog).toBeHidden();
  await expect(
    page.getByRole('table', { name: 'Regeln' }).getByText(rule.key as string),
  ).toBeVisible();
  expect(
    writes(calls)
      .filter((c) => c.path === '/rules')
      .map((c) => c.body),
  ).toEqual([s4.anfrage.body]);

  // --- 5. Layout für die Rolle engineer erstellen, im Draft (WP-316, WP-259, WP-798).
  const draftName = (s5.draft as Json).name as string;
  await page.goto('/ci-types/drafts');
  await page.getByRole('button', { name: 'Draft anlegen' }).first().click();
  await page.getByLabel('Name des neuen Drafts').fill(draftName);
  await page.getByRole('dialog').getByRole('button', { name: 'Draft anlegen' }).click();
  await page.getByRole('button', { name: `Draft ${draftName} als aktiven Draft wählen` }).click();
  await expect(page.getByText('aktiver Draft')).toBeVisible();
  await page.goto(`/ci-types/${BLADE}/form-builder`);
  await expect(
    page.getByRole('heading', { name: 'Formular gestalten: Blade-Server' }),
  ).toBeVisible();
  await page.getByLabel('Rolle', { exact: true }).selectOption({ label: 'engineer' });
  await expect(page.getByText(`Änderungen landen im Draft ${draftName}.`)).toBeVisible();
  const layoutBody = s5.anfrage.body as {
    key: string;
    layout: { sections: { key: string; title: { de: string; en: string } }[] };
  };
  const section = layoutBody.layout.sections[0]!;
  await page.getByLabel(`Titel (DE) von ${section.key}`).fill(section.title.de);
  await page.getByLabel(`Titel (EN) von ${section.key}`).fill(section.title.en);
  await page.getByRole('button', { name: 'Im Draft speichern' }).click();
  await expect(page.getByText(`Im Draft ${draftName} gespeichert.`)).toBeVisible();
  expect(
    calls
      .filter((c) => c.method === 'POST' && c.path === '/layouts')
      .map((c) => [query(c), c.body]),
  ).toEqual([[s5.anfrage.query, s5.anfrage.body]]);

  // --- 6. Preview als engineer mit einem der beiden Templates, ohne Schreibwirkung (WP-803).
  const s6 = step(6);
  const writesBefore = writes(calls).length;
  const preview = page.getByRole('region', { name: 'Vorschau' });
  await preview.getByRole('button', { name: 'Vorschau öffnen' }).click();
  await expect(preview.getByRole('status')).toContainText(
    `Die Vorschau zeigt den Stand des Drafts ${draftName}.`,
  );
  await preview
    .getByLabel('Template der Vorschau')
    .selectOption({ label: 'Blade-Server (Storage)' });
  await preview.getByLabel('Rolle der Vorschau').selectOption({ label: 'engineer' });
  const previewForm = preview.getByRole('form', { name: 'Formularvorschau' });
  await expect(previewForm.getByText(section.title.de)).toBeVisible();
  await expect(previewForm.getByLabel(/^Speicherpool/)).toHaveValue('pool-a');
  await expect(previewForm.getByLabel(/^Servicelevel/)).toHaveValue('8x5');
  const asEngineer = calls.filter(
    (c) =>
      c.path === s6.anfrage.path &&
      query(c).role_id === ROLE_ENGINEER &&
      query(c).template_id === T2,
  );
  expect(asEngineer.length).toBeGreaterThan(0);
  expect(query(asEngineer.at(-1)!)).toEqual(s6.anfrage.query);
  expect(writes(calls)).toHaveLength(writesBefore);

  // --- 7. Publizieren: Draft → Test → Publish (WP-721, WP-261).
  await page.goto(`/ci-types/drafts/${DRAFT}`);
  await expect(page.getByRole('heading', { name: draftName, level: 1 })).toBeVisible();
  await expect(page.locator('#main-content').getByText(layoutBody.key).first()).toBeVisible();
  await page.getByRole('button', { name: 'Testen' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Test starten' }).click();
  await expect(page.getByText('Getestet').first()).toBeVisible();
  await page.getByRole('button', { name: 'Publizieren' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Publizieren' }).click();
  await expect(page.getByText('Publiziert').first()).toBeVisible();
  expect(
    writes(calls)
      .filter((c) => c.path.startsWith('/metamodel/drafts'))
      .map((c) => `${c.method} ${c.path}`),
  ).toEqual([
    'POST /metamodel/drafts',
    `POST /metamodel/drafts/${DRAFT}/test`,
    `POST /metamodel/drafts/${DRAFT}/publish`,
  ]);

  // --- 8. Anlage durch einen normalen Nutzer mit der Rolle engineer (WP-317, WP-710, E-106).
  const s8 = step(8);
  const form = s8.formular as Json;
  // The form of the engineer is the published layout of step 5 with the rule of step 4.
  expect(form.layout_id).toBe((step(7).layout as Json).id);
  expect((form.rules as Json[]).map((r) => r.key)).toContain(rule.key);
  for (const right of ['citype:manage', 'ci_attribute:manage', 'metamodel:publish']) {
    expect(frozen.sitzungen.engineer.permissions).not.toContain(right);
  }
  await signInAs('engineer');
  await page.goto('/cmdb');
  await page.getByRole('button', { name: 'CI anlegen' }).first().click();
  const create = page.getByRole('dialog');
  await create.getByRole('button', { name: 'Untertypen von Server anzeigen' }).click();
  await create
    .getByRole('list', { name: 'Untertypen von Server' })
    .getByRole('button', { name: 'Blade-Server', exact: true })
    .click();
  await create.getByRole('button', { name: 'Weiter' }).click();
  const post = s8.anfrage.body!;
  await create
    .getByRole('textbox', { name: 'Hersteller', exact: true })
    .fill(post.manufacturer as string);
  await create.getByRole('textbox', { name: 'Modell', exact: true }).fill(post.model as string);
  await create.getByRole('button', { name: 'Weiter' }).click();
  await expect(create.getByRole('heading', { name: 'Angaben' })).toBeFocused();
  await create.getByLabel('Ausführung *').selectOption({ label: 'Blade-Server (Storage)' });
  await create.getByLabel('Name *').fill(post.name as string);
  // The layout of step 5 and the template: its defaults, the storage pool required.
  const formSection = (form.sections as { title: { de: string } }[])[0]!;
  await expect(create.getByRole('group', { name: formSection.title.de })).toBeVisible();
  const pool = create.getByLabel('Speicherpool (vorbelegt) *');
  await expect(pool).toHaveValue('pool-a');
  await expect(pool).toHaveAttribute('required', '');
  await expect(create.getByLabel('Servicelevel (vorbelegt)')).toHaveValue('8x5');
  // The rule of step 4: a storage blade needs its slot.
  await expect(create.getByLabel('Einschub', { exact: true })).not.toHaveAttribute('required');
  await create.getByLabel('Rolle').selectOption(condition.value);
  const slot = create.getByLabel('Einschub *');
  await expect(slot).toHaveAttribute('required', '');
  await slot.fill(String((post.attributes as Json).blade_slot));
  // No metamodel terms for the normal user (UI-12).
  await expect(create).not.toContainText(/Template|Attribut-Set|Attribute Set/);
  await create.getByRole('button', { name: 'Anlegen' }).click();
  await expect(create).toBeHidden();
  const formReads = calls.filter((c) => c.path === `/ci-types/${BLADE}/form`).map(query);
  expect(formReads.at(-1)).toEqual({ template_id: T2 });
  expect(calls.filter((c) => c.method === 'POST' && c.path === '/cis').map((c) => c.body)).toEqual([
    post,
  ]);

  // A write on the metamodel is refused: 403 with a problem body, shown in the dialog.
  const refused = s8.verweigert as { anfrage: FrozenRequest; antwort: { body: Json } };
  await page.goto('/ci-types');
  await page
    .getByRole('treeitem', { name: 'Blade-Server' })
    .getByText('blade_server · v1', { exact: false })
    .click();
  await page.getByRole('button', { name: 'Bearbeiten: storage_pool' }).click();
  const editor = page.getByRole('dialog');
  await editor.getByLabel('Anzeigename (Deutsch)').fill('Speicherpool (geändert)');
  await editor.getByRole('button', { name: 'Speichern' }).click();
  await expect(editor.getByRole('alert')).toContainText(
    'Keine Berechtigung: Ihrer Sitzung fehlt das Recht „ci_attribute:manage“.',
  );
  expect(refused.antwort.body).toMatchObject({ status: 403, title: 'Forbidden' });
  expect(calls.filter((c) => c.method === 'PATCH').map((c) => `${c.method} ${c.path}`)).toEqual([
    `${refused.anfrage.method} ${refused.anfrage.path}`,
  ]);

  // --- 9. Template-Version migrieren, Overrides bleiben erhalten (WP-253, WP-712, WP-306).
  await signInAs('org_admin');
  await page.goto(`/cmdb/${REF_CI}`);
  await page.locator('summary', { hasText: 'Herkunft & Overrides' }).click();
  const row = page.locator('li', { has: page.getByText(s9.override.field, { exact: true }) });
  await row.getByRole('button', { name: 'Override setzen' }).click();
  const overrideDialog = page.getByRole('dialog');
  await overrideDialog.getByLabel('Wert').fill(s9.override.value);
  await overrideDialog.getByLabel('Begründung').fill(s9.override.reason);
  await overrideDialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(row.getByText('geschützt', { exact: true })).toBeVisible();
  expect(
    calls
      .filter((c) => c.method === 'PUT' && c.path.endsWith('/override'))
      .map((c) => [c.path, c.body]),
  ).toEqual([
    [
      `/cis/${REF_CI}/fields/${s9.override.field}/override`,
      { value: s9.override.value, reason: s9.override.reason, protected: true },
    ],
  ]);

  await page.locator('summary', { hasText: 'Templates' }).click();
  await page.getByRole('button', { name: 'Auf Version 2 migrieren' }).click();
  const migrate = page.getByRole('dialog', {
    name: 'Template migrieren: Blade-Server (Storage)',
  });
  await expect(migrate.getByText('Migriert werden nur die gewählten CIs (1).')).toBeVisible();
  await expect(migrate.getByLabel('Probelauf (schreibt nichts)')).toBeChecked();
  await migrate.getByRole('button', { name: 'Probelauf starten' }).click();
  const run = migrate.getByRole('region', { name: 'Lauf der Migration' });
  await expect(run.getByText('Bericht des Probelaufs')).toBeVisible();
  await run.getByRole('button', { name: 'Migration starten' }).click();
  await expect(run.getByText('Bericht der Migration', { exact: true })).toBeVisible();
  const result = run.getByRole('table');
  await expect(result.getByRole('cell', { name: 'Migriert' })).toBeVisible();
  await expect(result.getByRole('cell', { name: 'support_level' })).toBeVisible();
  await expect(result.getByRole('cell', { name: s9.override.field })).toBeVisible();
  expect(
    calls
      .filter((c) => c.method === 'POST' && c.path === `/templates/${T2}/migrations`)
      .map((c) => c.body),
  ).toEqual([s9.migration.probelauf, s9.migration.lauf]);
  const report = s9.migration.bericht as {
    items: { ci_id: string; outcome: string; defaults: string[]; overridden: string[] }[];
  };
  expect(report.items[0]).toMatchObject({ ci_id: REF_CI, outcome: 'migrated' });
  expect(report.items[0]!.defaults).not.toContain(s9.override.field);
  expect(report.items[0]!.overridden).toContain(s9.override.field);
  await migrate.getByRole('button', { name: 'Schließen' }).first().click();

  // After the migration: the template on version 2, the override protected and unchanged.
  await page.reload();
  await page.locator('summary', { hasText: 'Templates' }).click();
  await expect(order.locator('[data-template-key]')).toHaveText([
    /Version 2 · Position 1/,
    /Version 2 · Position 2/,
  ]);
  await page.locator('summary', { hasText: 'Herkunft & Overrides' }).click();
  await expect(row.getByText('geschützt', { exact: true })).toBeVisible();
  await expect(row.getByText(JSON.stringify(s9.override.value)).first()).toBeVisible();
  // GET /cis/{id}/fields: the field stays protected with its override value.
  const fields = await page.evaluate(async (ciId) => {
    const response = await fetch(`/api/v1/cis/${ciId}/fields`, {
      headers: { Authorization: 'Bearer header.payload.signature' },
    });
    return (await response.json()) as { data: Record<string, unknown>[] };
  }, REF_CI);
  expect(fields.data.find((f) => f.field_name === s9.override.field)).toMatchObject({
    protected: true,
    override_value: s9.override.value,
    override_reason: s9.override.reason,
  });
});
