/**
 * WP-801 (UI-11 [P2]): the rule catalog /rules with the click builder for
 * validations and conditions.
 *
 *   List      GET /rules with the filters (kind, target, attribute_key from
 *             ?attribute_key=) and "load more" over next_cursor; key,
 *             attribute, kind, target, condition, state and version per
 *             rule; system rules without edit and delete
 *   Write     POST of a required rule with a condition in the condition
 *             grammar on a type; POST of a validation whose ast passes
 *             parseRule; a 422 with the path of the parser at the node; 403
 *             and 409 as notes
 *   Draft     with an active draft the write carries draft={id}, the 202
 *             asks for nothing by an id, the rule of an item carries "im
 *             Draft" and a created rule appears as a placeholder row
 *   Builder   the condition and the validation mode emit only nodes of
 *             their format, every output passes parseCondition or
 *             parseRule, the preview evaluates with evaluateCondition and
 *             evaluateRule
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/RulesPage.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/utils';
import { useAuthStore } from '../auth/authStore';
import { useMetamodelDraftStore } from '../stores/metamodelDraft';
import { RulesPage, conditionSummary } from './RulesPage';
import { RuleBuilder } from '../components/rules/RuleBuilder';
import {
  CONDITION_MODE,
  VALIDATION_MODE,
  checkCondition,
  checkValidation,
  conditionFieldCatalog,
  conditionNodeAtPath,
  fromCondition,
  fromValidation,
  toCondition,
  toValidation,
  type ConditionCheck,
  type ValidationCheck,
} from '../rules/builderModes';
import { compareOps, parseCondition, parseRule } from '../rules/rules';
import i18n from '../i18n';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  body: Json | undefined;
}

const AT = '2026-01-01T00:00:00Z';
const DRAFT = 'draft-1';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, body: Json) {
  return new Response(JSON.stringify({ status, title: 'Problem', ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

const NET = {
  id: 't-net',
  key: 'network',
  name: 'Netzwerk',
  display_name: { de: 'Netzwerk', en: 'Network' },
  is_builtin: false,
  is_system: false,
  is_active: true,
  is_logical: false,
  is_observable: false,
  schema_version: 1,
  version: 1,
  capabilities: {},
  allowed_relationship_types: [],
  ui_schema: {},
  created_at: AT,
  updated_at: AT,
};

function definition(key: string, data_type: string, extra: Json = {}): Json {
  return {
    id: `d-${key}`,
    key,
    scope: 'type',
    ci_type_id: 't-net',
    display_name: { de: key, en: key },
    data_type,
    required: false,
    multi_value: false,
    searchable: false,
    unique: false,
    unique_scope: 'org',
    inheritable: true,
    order: 0,
    is_system: false,
    applies_to: 'all',
    applies_to_categories: [],
    applies_to_type_ids: [],
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const DEFINITIONS = [
  definition('vendor', 'text'),
  definition('ports', 'integer'),
  definition('role', 'enum', { enum_values: ['core', 'edge'] }),
];

const SET = {
  id: 's-net',
  organization_id: 'org-1',
  key: 'net_set',
  display_name: { de: 'Netzset', en: 'Net set' },
  is_system: false,
  is_active: true,
  created_at: AT,
  updated_at: AT,
};

function rule(id: string, key: string, extra: Json = {}): Json {
  return {
    id,
    organization_id: 'org-1',
    key,
    attribute_key: 'vendor',
    kind: 'required',
    ci_type_id: 't-net',
    condition: { op: 'eq', field: 'role', value: 'core' },
    ast: null,
    message: null,
    is_system: false,
    is_active: true,
    version: 3,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const OWN = rule('r-own', 'vendor_required');
const SYSTEM = rule('r-sys', 'system_rule', {
  organization_id: '',
  is_system: true,
  kind: 'visible',
  attribute_set_id: 's-net',
  ci_type_id: undefined,
  condition: null,
});
const OLD = rule('r-old', 'old_rule', { kind: 'readonly', is_active: false, version: 1 });

interface Backend {
  permissions?: string[];
  rules?: Json[];
  post?: (body: Json) => Response;
  patch?: (body: Json) => Response;
  remove?: () => Response;
  draftItems?: Json[];
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  const rules = b.rules ?? [OWN, SYSTEM, OLD];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const q = url.searchParams;
      calls.push({ method, path, query: q, body });
      if (q.has('draft') && method !== 'GET') {
        // The capture of WP-720: the item instead of the rule.
        return json(
          {
            id: 'item-new',
            kind: 'rule',
            op: method === 'POST' ? 'create' : method === 'PATCH' ? 'update' : 'delete',
            target_id: null,
            target_key: null,
            payload: body ?? {},
            base: null,
            schema_versions: {},
            route: `${method} /api/v1${path}`,
            created_by: 'u1',
            created_at: AT,
          },
          202,
        );
      }
      if (path === '/me/permissions') {
        return json({ user_id: 'u1', permissions: b.permissions ?? ['ci:read', 'citype:manage'] });
      }
      if (path === '/modules') return json({ data: [] });
      if (path === '/ci-types') {
        return json({ data: [NET], total: 1, limit: 200, offset: 0, has_more: false });
      }
      if (path === '/attribute-definitions') {
        if (q.get('applicable_to') === 't-net') return json({ data: DEFINITIONS });
        if (q.get('attribute_set_id') === 's-net')
          return json({ data: [definition('vlan', 'integer')] });
        return json({ data: [] });
      }
      if (path === '/attribute-sets') return json({ data: [SET], has_more: false });
      if (path === '/templates') return json({ data: [], has_more: false });
      if (path === `/metamodel/drafts/${DRAFT}`) {
        return json({ id: DRAFT, name: 'Umbau', status: 'draft', item_count: 0 });
      }
      if (path === `/metamodel/drafts/${DRAFT}/items`) {
        return json({ data: b.draftItems ?? [], has_more: false });
      }
      if (path === '/rules' && method === 'GET') {
        const kind = q.get('kind');
        const all = rules.filter((r) => !kind || r.kind === kind);
        if (q.get('cursor') === 'c2') return json({ data: all.slice(2) });
        return all.length > 2
          ? json({ data: all.slice(0, 2), next_cursor: 'c2' })
          : json({ data: all });
      }
      if (path === '/rules' && method === 'POST') {
        if (b.post) return b.post(body!);
        return json(rule('r-new', String(body!.key), body!), 201);
      }
      const one = /^\/rules\/([^/]+)$/.exec(path);
      if (one && method === 'PATCH') {
        if (b.patch) return b.patch(body!);
        return json({ ...rules.find((r) => r.id === one[1]), ...body });
      }
      if (one && method === 'DELETE') {
        if (b.remove) return b.remove();
        return new Response(null, { status: 204 });
      }
      return json({ data: [] });
    }),
  );
  return calls;
}

function renderAt(route = '/rules') {
  return renderWithProviders(
    <Routes>
      <Route path="/rules" element={<RulesPage />} />
    </Routes>,
    { route },
  );
}

const rulesTable = () => screen.getByRole('table', { name: 'Regeln' });
const rowOf = (key: string) => rulesTable().querySelector<HTMLElement>(`[data-rule-key="${key}"]`)!;

/** Opens the create dialog and chooses the type Netzwerk and the attribute. */
async function openCreate(user: ReturnType<typeof userEvent.setup>, attribute = 'vendor') {
  await screen.findByText('vendor_required');
  await user.click(screen.getAllByRole('button', { name: 'Regel anlegen' })[0]!);
  const dialog = await screen.findByRole('dialog');
  const d = within(dialog);
  await user.type(d.getByLabelText('Schlüssel'), 'new_rule');
  await waitFor(() =>
    expect(d.getByRole('combobox', { name: 'Ziel' }).querySelectorAll('option').length).toBe(2),
  );
  await user.selectOptions(d.getByRole('combobox', { name: 'Ziel' }), 't-net');
  await waitFor(() =>
    expect(
      d.getByRole('combobox', { name: 'Attribut' }).querySelector(`option[value="${attribute}"]`),
    ).not.toBeNull(),
  );
  await user.selectOptions(d.getByRole('combobox', { name: 'Attribut' }), attribute);
  return d;
}

/** Adds a comparison to the root group of the condition and fills it. */
async function addComparison(
  user: ReturnType<typeof userEvent.setup>,
  d: ReturnType<typeof within>,
  position: number,
  field: string,
  op: string,
  value?: string,
) {
  const condition = d.getByRole('group', { name: /^Gruppe/ });
  await user.click(
    within(condition).getAllByRole('button', { name: 'Bedingung hinzufügen' }).pop()!,
  );
  const node = d.getByRole('group', { name: `Bedingung ${position}` });
  await user.selectOptions(within(node).getByRole('combobox', { name: 'Feld' }), field);
  await user.selectOptions(within(node).getByRole('combobox', { name: 'Operator' }), op);
  if (value !== undefined) await user.type(within(node).getByLabelText('Wert'), value);
  return node;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    user: { sub: 'u1' },
  });
});

afterEach(() => {
  act(() => useMetamodelDraftStore.getState().clear());
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('RulesPage list (WP-801)', () => {
  it('lists the rules with their columns, filters and loads more over next_cursor', async () => {
    const user = userEvent.setup();
    const calls = stubBackend();
    renderAt();

    const own = await screen.findByText('vendor_required');
    const row = own.closest('tr')!;
    expect(within(row).getByText('vendor')).toBeTruthy();
    expect(within(row).getByText('Pflicht')).toBeTruthy();
    expect(within(row).getByText('CI-Typ: Netzwerk')).toBeTruthy();
    expect(within(row).getByText('role ist gleich "core"')).toBeTruthy();
    expect(within(row).getByText('aktiv')).toBeTruthy();
    expect(within(row).getByText('3')).toBeTruthy();
    await waitFor(() =>
      expect(within(rowOf('system_rule')).getByText('Attribut-Set: Netzset')).toBeTruthy(),
    );
    expect(within(rowOf('system_rule')).getByText('immer')).toBeTruthy();
    expect(screen.queryByText('old_rule')).toBeNull();

    // Load more: the second page over the cursor.
    await user.click(screen.getByRole('button', { name: 'Weitere laden' }));
    expect(await screen.findByText('old_rule')).toBeTruthy();
    expect(calls.some((c) => c.path === '/rules' && c.query.get('cursor') === 'c2')).toBe(true);
    expect(screen.queryByRole('button', { name: 'Weitere laden' })).toBeNull();

    // A filter goes to the server.
    await user.selectOptions(screen.getByRole('combobox', { name: 'Regelart' }), 'readonly');
    await waitFor(() =>
      expect(calls.some((c) => c.path === '/rules' && c.query.get('kind') === 'readonly')).toBe(
        true,
      ),
    );
    await user.selectOptions(screen.getByRole('combobox', { name: 'CI-Typ' }), 't-net');
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.path === '/rules' &&
            c.query.get('ci_type_id') === 't-net' &&
            c.query.get('kind') === 'readonly',
        ),
      ).toBe(true),
    );
    // The catalog is organization-wide: no client or site scope.
    expect(calls.filter((c) => c.path === '/rules').every((c) => !c.query.has('client_id'))).toBe(
      true,
    );
  });

  it('filters by ?attribute_key in advance', async () => {
    const calls = stubBackend();
    renderAt('/rules?attribute_key=vendor');
    await screen.findByText('vendor_required');
    expect(calls.find((c) => c.path === '/rules')!.query.get('attribute_key')).toBe('vendor');
    expect((screen.getByLabelText('Attribut-Schlüssel') as HTMLInputElement).value).toBe('vendor');
  });

  it('offers no edit or delete for a system rule', async () => {
    stubBackend();
    renderAt();
    await screen.findByText('system_rule');
    const system = rowOf('system_rule');
    expect(within(system).getByText('System')).toBeTruthy();
    expect(within(system).getByText('nur lesbar')).toBeTruthy();
    expect(within(system).queryByRole('button')).toBeNull();
    expect(
      within(rowOf('vendor_required')).getByRole('button', {
        name: 'Regel vendor_required bearbeiten',
      }),
    ).toBeTruthy();
  });

  it('offers no writes without citype:manage', async () => {
    stubBackend({ permissions: ['ci:read'] });
    renderAt();
    await screen.findByText('vendor_required');
    expect(screen.queryByRole('button', { name: 'Regel anlegen' })).toBeNull();
    expect(within(rowOf('vendor_required')).queryByRole('button')).toBeNull();
  });
});

describe('RulesPage writes (WP-801)', () => {
  it('creates a required rule with a condition on a type', async () => {
    const user = userEvent.setup();
    const calls = stubBackend();
    renderAt();
    const d = await openCreate(user);
    await user.selectOptions(d.getByRole('combobox', { name: 'Regelart' }), 'required');
    await addComparison(user, d, 1, 'role', 'in');
    const node = d.getByRole('group', { name: 'Bedingung 1' });
    await user.selectOptions(
      within(node).getByRole('combobox', { name: 'Wert hinzufügen' }),
      'core',
    );
    await addComparison(user, d, 2, 'ports', 'gte', '24');
    await user.type(d.getByLabelText('Meldung Deutsch'), 'Hersteller fehlt');
    await user.click(d.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    const post = calls.find((c) => c.method === 'POST' && c.path === '/rules')!;
    expect(post.body).toEqual({
      key: 'new_rule',
      attribute_key: 'vendor',
      kind: 'required',
      ci_type_id: 't-net',
      is_active: true,
      condition: {
        op: 'all',
        rules: [
          { op: 'in', field: 'role', value: ['core'] },
          { op: 'gte', field: 'ports', value: 24 },
        ],
      },
      message: { de: 'Hersteller fehlt' },
    });
    expect(() => parseCondition(post.body!.condition)).not.toThrow();
    expect(post.body).not.toHaveProperty('ast');
  });

  it('creates a validation whose ast passes parseRule', async () => {
    const user = userEvent.setup();
    const calls = stubBackend();
    renderAt();
    const d = await openCreate(user);
    await user.selectOptions(d.getByRole('combobox', { name: 'Regelart' }), 'validation');
    // A validation without a value rule does not go out.
    await user.click(d.getByRole('button', { name: 'Speichern' }));
    expect(d.getByText('Eine Validierung braucht mindestens eine Wertregel.')).toBeTruthy();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);

    await user.click(d.getByRole('button', { name: 'Muster hinzufügen' }));
    fireEvent.change(d.getByLabelText('Regulärer Ausdruck'), { target: { value: '^[A-Z]+$' } });
    await user.click(d.getByRole('button', { name: 'Bereich hinzufügen' }));
    await user.type(d.getByLabelText('Maximum'), '32');
    await user.click(d.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST')!;
    expect(post.body!.kind).toBe('validation');
    expect(post.body!.ast).toEqual({
      op: 'all',
      rules: [
        { op: 'pattern', pattern: '^[A-Z]+$' },
        { op: 'range', of: 'length', max: 32 },
      ],
    });
    expect(() => parseRule(post.body!.ast)).not.toThrow();
    expect(post.body).not.toHaveProperty('condition');
  });

  it('shows a 422 with the path of the parser at the node', async () => {
    const user = userEvent.setup();
    stubBackend({
      post: () =>
        problem(422, {
          detail: 'rules[1].field: eq requires a field',
          field: 'condition.rules[1].field',
        }),
    });
    renderAt();
    const d = await openCreate(user);
    await addComparison(user, d, 1, 'vendor', 'eq', 'acme');
    await addComparison(user, d, 2, 'name', 'not_empty');
    await user.click(d.getByRole('button', { name: 'Speichern' }));

    const second = await waitFor(() => {
      const node = d.getByRole('group', { name: 'Bedingung 2' });
      expect(node.getAttribute('data-invalid')).toBe('true');
      return node;
    });
    expect(within(second).getByText(/Vom Server abgelehnt: rules\[1\]\.field/)).toBeTruthy();
    expect(d.getByRole('group', { name: 'Bedingung 1' }).getAttribute('data-invalid')).toBeNull();
    expect(screen.getByRole('dialog')).toBeTruthy();
  });

  it('shows a 422 on attribute_key at the attribute (visible on a required attribute)', async () => {
    const user = userEvent.setup();
    stubBackend({
      post: () =>
        problem(422, {
          detail: 'the type requires the attribute statically',
          field: 'attribute_key',
        }),
    });
    renderAt();
    const d = await openCreate(user);
    await user.selectOptions(d.getByRole('combobox', { name: 'Regelart' }), 'visible');
    await user.click(d.getByRole('button', { name: 'Speichern' }));
    expect(await d.findByText('the type requires the attribute statically')).toBeTruthy();
  });

  it('shows a 409 of a published template version as a note', async () => {
    const user = userEvent.setup();
    stubBackend({
      patch: () => problem(409, { detail: 'the template version is published' }),
    });
    renderAt();
    await screen.findByText('vendor_required');
    await user.click(screen.getByRole('button', { name: 'Regel vendor_required bearbeiten' }));
    const d = within(await screen.findByRole('dialog'));
    await waitFor(() =>
      expect((d.getByRole('combobox', { name: 'Attribut' }) as HTMLSelectElement).value).toBe(
        'vendor',
      ),
    );
    await user.click(d.getByRole('button', { name: 'Speichern' }));
    expect((await d.findByRole('alert')).textContent).toMatch(
      /Konflikt: the template version is published/,
    );
  });

  it('sends a PATCH with condition and ast as null and shows a 403 as a note', async () => {
    const user = userEvent.setup();
    const calls = stubBackend({ patch: () => problem(403, { detail: 'system rule' }) });
    renderAt();
    await screen.findByText('vendor_required');
    await user.click(screen.getByRole('button', { name: 'Regel vendor_required bearbeiten' }));
    const d = within(await screen.findByRole('dialog'));
    // The loaded condition is in the builder; deleting it sends null.
    const node = d.getByRole('group', { name: 'Bedingung 1' });
    expect((within(node).getByRole('combobox', { name: 'Feld' }) as HTMLSelectElement).value).toBe(
      'role',
    );
    await user.click(within(node).getByRole('button', { name: 'Bedingung 1 löschen' }));
    await user.click(d.getByRole('button', { name: 'Speichern' }));
    expect((await d.findByRole('alert')).textContent).toMatch(/Keine Berechtigung/);
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.path).toBe('/rules/r-own');
    expect(patch.body).toEqual({
      attribute_key: 'vendor',
      kind: 'required',
      condition: null,
      ast: null,
      message: null,
      is_active: true,
    });
  });

  it('deletes after a confirmation and shows a 403 as a note', async () => {
    const user = userEvent.setup();
    const calls = stubBackend({ remove: () => problem(403, { detail: 'forbidden' }) });
    renderAt();
    await screen.findByText('vendor_required');
    await user.click(screen.getByRole('button', { name: 'Regel vendor_required löschen' }));
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Löschen' }),
    );
    expect((await screen.findByRole('alert')).textContent).toMatch(
      /Nicht gelöscht: Keine Berechtigung/,
    );
    expect(calls.some((c) => c.method === 'DELETE' && c.path === '/rules/r-own')).toBe(true);
  });
});

describe('RulesPage with an active draft (WP-801, WP-798)', () => {
  it('writes with draft={id}, asks for nothing after the 202 and marks the rules', async () => {
    const user = userEvent.setup();
    const calls = stubBackend({
      draftItems: [
        {
          id: 'item-1',
          kind: 'rule',
          op: 'update',
          target_id: 'r-own',
          payload: { kind: 'readonly' },
          schema_versions: {},
          route: 'PATCH /api/v1/rules/r-own',
          created_at: AT,
        },
        {
          id: 'item-2',
          kind: 'rule',
          op: 'create',
          target_id: null,
          payload: {
            key: 'draft_rule',
            attribute_key: 'ports',
            kind: 'validation',
            ci_type_id: 't-net',
            condition: { op: 'not_empty', field: 'vendor' },
          },
          schema_versions: {},
          route: 'POST /api/v1/rules',
          created_at: AT,
        },
      ],
    });
    act(() => useMetamodelDraftStore.getState().setDraft({ id: DRAFT, name: 'Umbau' }));
    renderAt();

    await screen.findByText('vendor_required');
    expect(await within(rowOf('vendor_required')).findByText('im Draft: geändert')).toBeTruthy();
    const placeholder = (await screen.findByText('draft_rule')).closest('tr')!;
    expect(within(placeholder).getByText('im Draft: neu')).toBeTruthy();
    expect(within(placeholder).getByText('vendor ist gesetzt')).toBeTruthy();
    // The productive values stay: the changed rule is still required.
    expect(within(rowOf('vendor_required')).getByText('Pflicht')).toBeTruthy();

    const d = await openCreate(user);
    await user.click(d.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    const index = calls.findIndex((c) => c.method === 'POST');
    expect(calls[index]!.path).toBe('/rules');
    expect(calls[index]!.query.get('draft')).toBe(DRAFT);
    // The page adds nothing itself: the body carries no draft.
    expect(calls[index]!.body).not.toHaveProperty('draft');
    // No follow-up by an id of the item and no reread of the productive list.
    await new Promise((resolve) => setTimeout(resolve, 20));
    const after = calls.slice(index + 1);
    expect(after.some((c) => /^\/rules\/.+/.test(c.path))).toBe(false);
    expect(after.some((c) => c.path === '/rules')).toBe(false);
    expect(useMetamodelDraftStore.getState().itemRevision).toBeGreaterThan(0);
  });
});

describe('RuleBuilder condition and validation modes (WP-801)', () => {
  const fields = conditionFieldCatalog(
    DEFINITIONS as unknown as Parameters<typeof conditionFieldCatalog>[0],
  );

  it('emits only the condition grammar and passes parseCondition', () => {
    expect([...CONDITION_MODE.ops].sort()).toEqual([...compareOps].sort());
    expect(CONDITION_MODE.ops).not.toContain('like');
    expect(CONDITION_MODE.ops).not.toContain('required');
    const loaded = {
      op: 'any',
      rules: [
        { op: 'eq', field: 'role', value: 'core' },
        { op: 'not', rules: [{ op: 'empty', field: 'vendor' }] },
      ],
    };
    const root = fromCondition(loaded);
    expect(toCondition(root)).toEqual(loaded);
    const check = checkCondition(root);
    expect(check.ok && check.condition).toEqual(parseCondition(loaded));
    // A single comparison stays a comparison; an empty one is none.
    expect(toCondition(fromCondition({ op: 'empty', field: 'name' }))).toEqual({
      op: 'empty',
      field: 'name',
    });
    expect(toCondition(fromCondition(null))).toBeNull();
    // Value rules and filter nodes are refused.
    expect(() => fromCondition({ op: 'required' })).toThrow(/value rule/);
    expect(() => fromCondition({ field: 'name', op: 'like', value: 'a' })).toThrow();
    // A path of the server names the node.
    expect(conditionNodeAtPath(root, 'condition.rules[1].rules[0].field')).toBe(
      root.kind === 'group' && root.children[1]!.kind === 'not'
        ? root.children[1]!.child.id
        : 'none',
    );
    // The fields: the typed CI columns and the attributes of the target.
    expect(fields.map((f) => f.field)).toEqual(expect.arrayContaining(['name', 'vendor', 'ports']));
  });

  it('emits only the value rule AST and passes parseRule', () => {
    const loaded = {
      op: 'all',
      rules: [
        { op: 'required' },
        { op: 'range', of: 'value', min: 1, max: 48 },
        { op: 'enum', values: ['a', 'b'] },
      ],
    };
    const root = fromValidation(loaded);
    expect(toValidation(root)).toEqual(loaded);
    const check = checkValidation(root);
    expect(check.ok && check.ast).toEqual(parseRule(loaded));
    expect(toValidation(fromValidation({ op: 'pattern', pattern: '^a' }))).toEqual({
      op: 'pattern',
      pattern: '^a',
    });
    // Conditions are refused.
    expect(() => fromValidation({ op: 'eq', field: 'a', value: 1 })).toThrow();
    expect(() => fromValidation({ op: 'any', rules: [] })).toThrow();
    for (const kind of VALIDATION_MODE.nodeKinds) {
      expect(['all', 'required', 'pattern', 'range', 'enum']).toContain(kind);
    }
    // A pattern outside the common subset is caught before it goes out.
    expect(checkValidation(fromValidation({ op: 'pattern', pattern: 'a' })).ok).toBe(true);
    const lookaround = fromValidation({ op: 'pattern', pattern: 'a' });
    if (lookaround.kind === 'all' && lookaround.children[0]!.kind === 'pattern') {
      lookaround.children[0]!.pattern = '(?=a)';
    }
    expect(checkValidation(lookaround)).toMatchObject({ ok: false, issue: 'bad_pattern' });
  });

  it('previews a condition with evaluateCondition against a sample object', async () => {
    const user = userEvent.setup();
    const checks: ConditionCheck[] = [];
    renderWithProviders(
      <RuleBuilder
        mode={CONDITION_MODE}
        initialValue={{ op: 'gte', field: 'ports', value: 24 }}
        fields={fields}
        onChange={(c: ConditionCheck) => checks.push(c)}
      />,
    );
    const preview = screen.getByTestId('condition-preview');
    expect(preview.textContent).toMatch(/trifft nicht zu/);
    await user.type(screen.getByLabelText('Beispiel: Attribut: ports'), '48');
    expect(preview.textContent).toMatch(/trifft zu – die Regel greift/);
    await user.clear(screen.getByLabelText('Beispiel: Attribut: ports'));
    await user.type(screen.getByLabelText('Beispiel: Attribut: ports'), '8');
    expect(preview.textContent).toMatch(/trifft nicht zu/);
    expect(checks[checks.length - 1]).toEqual({
      ok: true,
      condition: { op: 'gte', field: 'ports', value: 24 },
    });
    // The operators of a number field: no like, no exists of the filter mode.
    const ops = [
      ...(screen.getByRole('combobox', { name: 'Operator' }) as HTMLSelectElement).options,
    ].map((o) => o.value);
    expect(ops).toEqual([
      'eq',
      'ne',
      'in',
      'not_in',
      'gt',
      'gte',
      'lt',
      'lte',
      'empty',
      'not_empty',
    ]);
  });

  it('previews a value rule with evaluateRule and shows the violation codes', async () => {
    const user = userEvent.setup();
    const checks: ValidationCheck[] = [];
    renderWithProviders(
      <RuleBuilder
        mode={VALIDATION_MODE}
        initialValue={{ op: 'pattern', pattern: '^[a-z]+$' }}
        attribute={fields.find((f) => f.field === 'vendor')}
        onChange={(c: ValidationCheck) => checks.push(c)}
      />,
    );
    const preview = screen.getByTestId('validation-preview');
    await user.type(screen.getByLabelText('Beispielwert'), 'ACME');
    expect(within(preview).getByText('pattern')).toBeTruthy();
    await user.clear(screen.getByLabelText('Beispielwert'));
    await user.type(screen.getByLabelText('Beispielwert'), 'acme');
    expect(preview.textContent).toMatch(/erfüllt die Regel/);
    // Adding a required rule wraps both into all.
    fireEvent.click(screen.getByRole('button', { name: 'Pflichtwert hinzufügen' }));
    expect(checks[checks.length - 1]).toEqual({
      ok: true,
      ast: { op: 'all', rules: [{ op: 'pattern', pattern: '^[a-z]+$' }, { op: 'required' }] },
    });
    await user.clear(screen.getByLabelText('Beispielwert'));
    expect(within(preview).getByText('required')).toBeTruthy();
  });

  it('summarizes a condition in the UI language', () => {
    const t = i18n.getFixedT('de-DE');
    expect(conditionSummary(null, t)).toBe('immer');
    expect(
      conditionSummary(
        {
          op: 'all',
          rules: [
            { op: 'eq', field: 'role', value: 'core' },
            {
              op: 'any',
              rules: [
                { op: 'gt', field: 'ports', value: 8 },
                { op: 'empty', field: 'vendor' },
              ],
            },
          ],
        },
        t,
      ),
    ).toBe('role ist gleich "core" UND (ports größer als 8 ODER vendor ist leer)');
  });
});
