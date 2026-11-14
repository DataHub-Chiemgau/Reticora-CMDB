/**
 * WFL-01 (WP-1133): the forms page renders the chosen definition, lists
 * its submissions grouped by schema version, each with the schema of its
 * version, and offers "Bearbeiten" and "Neu" only with form:write.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import { FormsPage } from './FormsPage';
import { renderWithProviders } from '../test/utils';

const AT = '2026-01-01T00:00:00Z';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

const FORM = {
  id: 'form-1',
  organization_id: 'org-1',
  name: 'Access request',
  schema: {
    properties: { email: { type: 'string', title: 'E-Mail' } },
    required: ['email'],
  },
  ui_hints: { fields: { email: { label: { de: 'E-Mail', en: 'E-mail' } } } },
  active: true,
  version: 3,
  schema_version: 2,
  current_version_id: 'v2',
  created_at: AT,
  updated_at: AT,
};

const VERSIONS = [
  {
    id: 'v2',
    organization_id: 'org-1',
    form_id: 'form-1',
    version: 2,
    schema: FORM.schema,
    ui_hints: FORM.ui_hints,
    created_at: AT,
  },
  {
    id: 'v1',
    organization_id: 'org-1',
    form_id: 'form-1',
    version: 1,
    schema: { properties: { mail: { type: 'string', title: 'Postfach' } } },
    ui_hints: {},
    created_at: AT,
  },
];

const submission = (id: string, version: number, values: Record<string, unknown>) => ({
  id,
  organization_id: 'org-1',
  form_id: 'form-1',
  form_version_id: `v${version}`,
  form_version: version,
  values,
  status: 'submitted',
  created_at: AT,
  updated_at: AT,
});

function stub(permissions: string[]) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const { pathname } = new URL(String(input), 'http://localhost');
      const path = pathname.replace(/^\/api\/v1/, '');
      const ok = (body: unknown) =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      if (path === '/me/permissions') return ok({ user_id: 'u', permissions });
      if (path === '/forms') return ok(paginated([FORM]));
      if (path === '/forms/form-1/versions') return ok(paginated(VERSIONS));
      if (path === '/form-submissions') {
        return ok(
          paginated([
            submission('s1', 1, { mail: 'old@example.net' }),
            submission('s2', 2, { email: 'new@example.net' }),
            submission('s3', 2, { email: 'second@example.net' }),
          ]),
        );
      }
      return new Response(JSON.stringify({ detail: 'unexpected' }), { status: 404 });
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('FormsPage', () => {
  it('renders a form definition and fields', async () => {
    stub(['form:read']);
    renderWithProviders(<FormsPage />, { route: '/forms' });
    expect(await screen.findByText('Access request')).toBeInTheDocument();
    expect(screen.getByLabelText('E-Mail *')).toBeRequired();
  });

  it('groups the submissions by version with the schema of each version', async () => {
    stub(['form:read', 'form:write']);
    renderWithProviders(<FormsPage />, { route: '/forms' });

    const v2 = await screen.findByTestId('submissions-v2');
    const v1 = screen.getByTestId('submissions-v1');
    expect(within(v2).getByRole('heading', { name: 'Version 2' })).toBeInTheDocument();
    expect(within(v2).getByText('new@example.net')).toBeInTheDocument();
    expect(within(v2).getByText('second@example.net')).toBeInTheDocument();
    expect(within(v2).getAllByText('E-Mail')).toHaveLength(2);
    // Version 1 has its own field and label.
    expect(within(v1).getByText('Postfach')).toBeInTheDocument();
    expect(within(v1).getByText('old@example.net')).toBeInTheDocument();
    // Newest version first.
    const groups = screen.getAllByTestId(/^submissions-v/);
    expect(groups.map((g) => g.getAttribute('data-testid'))).toEqual([
      'submissions-v2',
      'submissions-v1',
    ]);
    expect(screen.getByRole('button', { name: 'Bearbeiten' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Neu' })).toBeInTheDocument();
  });

  it('offers no edit and no new form without form:write', async () => {
    stub(['form:read']);
    renderWithProviders(<FormsPage />, { route: '/forms' });

    await screen.findByTestId('submissions-v2');
    expect(screen.queryByRole('button', { name: 'Bearbeiten' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Neu' })).not.toBeInTheDocument();
  });
});
