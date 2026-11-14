/**
 * InstanceFieldsSection shows the metamodel only to users who may manage
 * instance fields (UI-12, WP-804).
 *
 * - Defining and deleting instance fields (PUT/DELETE
 *   /cis/{id}/field-definitions) needs ci_instance_attribute:manage. Before
 *   WP-804 every user saw the data type, 'Löschen' and 'Instanzfeld
 *   hinzufügen'. Without the permission (GET /me/permissions, fail closed)
 *   the section is called 'Weitere Angaben' and shows only the values; it is
 *   left out when the CI has no instance fields.
 * - With the permission the administration stays as it was.
 *
 * LifecycleSection (LCY-02, WP-724) offers the targets of GET
 * /cis/{id}/lifecycle-transitions, sends the transition with its required
 * fields to POST /cis/{id}/lifecycle-transitions and renders nothing for a
 * CI type without lifecycle model (422). For an asset (WP-809) a 422
 * lifecycle-guard shows the translated guard and every missing reference
 * with its way out (assign, reserve, disposal), invalid-transition shows
 * "Übergang nicht erlaubt"; the CI path keeps the raw message.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/cmdb/CIDetailSections.test.tsx
 */
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithProviders, stubFetchRoutes } from '../../test/utils';
import i18n from '../../i18n';
import { CIMonitoringSection, InstanceFieldsSection, LifecycleSection } from './CIDetailSections';

const CI_ID = 'ci-1';

const ILO_FIELD = { name: 'ilo_ip', label: 'iLO-IP', data_type: 'ip' };

function stub(permissions: string[], fields: unknown[] = [ILO_FIELD]) {
  return stubFetchRoutes({
    '/me/permissions': { user_id: 'user-1', permissions },
    [`/cis/${CI_ID}/field-definitions`]: { data: fields },
  });
}

function renderSection(attributes: Record<string, unknown> = { ilo_ip: '10.0.0.1' }) {
  return renderWithProviders(
    <InstanceFieldsSection ciId={CI_ID} attributes={attributes} onChanged={() => {}} />,
  );
}

/** waitForRequests waits until both reads of the section were answered. */
async function waitForRequests(fetchMock: ReturnType<typeof stub>) {
  await waitFor(() => {
    const urls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(urls.some((url) => url.includes('/me/permissions'))).toBe(true);
    expect(urls.some((url) => url.includes('/field-definitions'))).toBe(true);
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('InstanceFieldsSection for a normal user (UI-12, WP-804)', () => {
  it('shows the values without metamodel terms', async () => {
    stub(['ci:read', 'ci:write']);
    renderSection();

    expect(await screen.findByText('Weitere Angaben')).toBeInTheDocument();
    expect(screen.getByLabelText('iLO-IP')).toHaveValue('10.0.0.1');
    expect(screen.queryByText('Instanzfelder')).not.toBeInTheDocument();
    expect(screen.queryByText(/Felder nur für dieses Gerät/)).not.toBeInTheDocument();
    expect(screen.queryByText('ip')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Löschen' })).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: /Instanzfeld hinzufügen/ }),
    ).not.toBeInTheDocument();
  });

  it('leaves the section out when the CI has no instance fields', async () => {
    const fetchMock = stub(['ci:read'], []);
    const { container } = renderSection({});

    await waitForRequests(fetchMock);
    await waitFor(() => expect(container).toBeEmptyDOMElement());
    expect(screen.queryByText('Weitere Angaben')).not.toBeInTheDocument();
    expect(screen.queryByText('Instanzfelder')).not.toBeInTheDocument();
  });

  it('fails closed when the permissions cannot be read', async () => {
    const fetchMock = stubFetchRoutes({
      '/me/permissions': new Response(JSON.stringify({ detail: 'unavailable' }), { status: 503 }),
      [`/cis/${CI_ID}/field-definitions`]: { data: [ILO_FIELD] },
    });
    renderSection();

    await waitForRequests(fetchMock);
    expect(await screen.findByText('Weitere Angaben')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Löschen' })).not.toBeInTheDocument();
  });
});

describe('InstanceFieldsSection with ci_instance_attribute:manage (UI-12, WP-804)', () => {
  it('keeps the administration of the instance fields', async () => {
    stub(['ci:read', 'ci_instance_attribute:manage']);
    renderSection();

    expect(await screen.findByText('Instanzfelder')).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Löschen' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Instanzfeld hinzufügen/ })).toBeInTheDocument();
    expect(screen.getByText('ip')).toBeInTheDocument();
    expect(screen.getByLabelText('iLO-IP')).toHaveValue('10.0.0.1');
    expect(screen.queryByText('Weitere Angaben')).not.toBeInTheDocument();
  });
});

describe('LifecycleSection (LCY-02, WP-724)', () => {
  const OPTIONS = {
    model: { id: 'model-1', key: 'software', name: 'Software' },
    current_state: 'approved',
    transitions: [
      {
        to_state: 'production',
        key: 'go_live',
        label: 'Go live',
        required_fields: ['change_ticket'],
      },
      { to_state: 'eol', key: 'retire', label: 'End of life', required_fields: [] },
    ],
  };

  /** stubLifecycle answers GET …/lifecycle-transitions with options and records the POSTs. */
  function stubLifecycle(options: Response | object) {
    const posts: unknown[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes(`/cis/${CI_ID}/lifecycle-transitions`) && init?.method === 'POST') {
        posts.push(JSON.parse(String(init.body)));
        return new Response(
          JSON.stringify({ entity_type: 'ci', entity_id: CI_ID, to_state: 'production' }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }
      if (url.includes(`/cis/${CI_ID}/lifecycle-transitions`)) {
        return options instanceof Response
          ? options.clone()
          : new Response(JSON.stringify(options), {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            });
      }
      return new Response(JSON.stringify({ detail: `unexpected request: ${url}` }), {
        status: 404,
      });
    });
    vi.stubGlobal('fetch', fetchMock);
    return { fetchMock, posts };
  }

  it('offers the allowed targets and sends the transition with its required fields', async () => {
    const { posts } = stubLifecycle(OPTIONS);
    const user = userEvent.setup();
    renderWithProviders(<LifecycleSection entityType="cis" entityId={CI_ID} />);

    const select = (await screen.findByLabelText('Übergang nach')) as HTMLSelectElement;
    expect(screen.getByText('approved')).toBeInTheDocument();
    expect(Array.from(select.options).map((o) => o.textContent)).toEqual([
      '—',
      'Go live',
      'End of life',
    ]);

    await user.selectOptions(select, 'production');
    await user.type(screen.getByLabelText(/change_ticket/), 'CHG-1');
    await user.type(screen.getByLabelText('Grund'), 'release 2');
    await user.click(screen.getByRole('button', { name: 'Anwenden' }));

    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({
      to_state: 'production',
      context: { change_ticket: 'CHG-1' },
      reason: 'release 2',
    });
  });

  it('renders nothing for a CI type without lifecycle model (422)', async () => {
    const { fetchMock } = stubLifecycle(
      new Response(
        JSON.stringify({
          type: 'urn:reticora:problem:invalid-field',
          status: 422,
          field: 'to_state',
        }),
        { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
      ),
    );
    const { container } = renderWithProviders(
      <LifecycleSection entityType="cis" entityId={CI_ID} />,
    );

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([input]) => String(input).includes('/lifecycle-transitions')),
      ).toBe(true),
    );
    await waitFor(() => expect(container).toBeEmptyDOMElement());
    expect(screen.queryByText('Lebenszyklus')).not.toBeInTheDocument();
    // The section no longer guesses a model from the model list.
    expect(
      fetchMock.mock.calls.some(([input]) => String(input).includes('/lifecycle-models')),
    ).toBe(false);
  });
});

describe('LifecycleSection of an asset: guard messages (WP-809)', () => {
  const ASSET_ID = 'asset-1';
  const OPTIONS = {
    model: { id: 'model-1', key: 'physical_asset', name: 'Physical asset' },
    current_state: 'in_stock',
    transitions: [
      { to_state: 'deployed', key: 'deploy', label: 'Deploy', required_fields: [] },
      { to_state: 'disposed', key: 'dispose', label: 'Dispose', required_fields: [] },
    ],
  };

  function stubRefusal(problem: object) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes(`/assets/${ASSET_ID}/lifecycle-transitions`) && init?.method === 'POST') {
        return new Response(JSON.stringify(problem), {
          status: 422,
          headers: { 'Content-Type': 'application/problem+json' },
        });
      }
      if (url.includes(`/assets/${ASSET_ID}/lifecycle-transitions`)) {
        return new Response(JSON.stringify(OPTIONS), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return new Response(JSON.stringify({ detail: `unexpected request: ${url}` }), {
        status: 404,
      });
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  async function apply(target: string) {
    const user = userEvent.setup();
    const select = (await screen.findByLabelText('Übergang nach')) as HTMLSelectElement;
    await user.selectOptions(select, target);
    await user.click(screen.getByRole('button', { name: 'Anwenden' }));
    return user;
  }

  it('names the failed guard and every missing reference with its action', async () => {
    stubRefusal({
      type: 'urn:reticora:problem:lifecycle-guard',
      title: 'Lifecycle guard failed',
      status: 422,
      detail: 'guard operations_location_or_holder failed',
      guard: 'operations_location_or_holder',
      missing: ['location_id', 'assignee'],
      violations: [
        { field: 'location_id', code: 'guard.operations_location_or_holder', message: 'x' },
        { field: 'assignee', code: 'guard.operations_location_or_holder', message: 'x' },
      ],
    });
    const onAssign = vi.fn();
    const onReserve = vi.fn();
    renderWithProviders(
      <LifecycleSection
        entityType="assets"
        entityId={ASSET_ID}
        onAssign={onAssign}
        onReserve={onReserve}
      />,
    );
    const user = await apply('deployed');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Für „Im Einsatz“ braucht das Asset einen Betriebsstandort');
    expect(alert).toHaveTextContent('Es fehlt ein passender Standort');
    expect(alert).toHaveTextContent('Es fehlt ein Halter (Person oder Team).');
    expect(alert).not.toHaveTextContent('guard operations_location_or_holder failed');
    await user.click(screen.getByRole('button', { name: 'Zuweisen' }));
    expect(onAssign).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('button', { name: 'Reservieren' })).not.toBeInTheDocument();
  });

  it('links a missing disposal record to the disposal page', async () => {
    stubRefusal({
      type: 'urn:reticora:problem:lifecycle-guard',
      status: 422,
      detail: 'guard disposal_record failed',
      guard: 'disposal_record',
      missing: ['disposal_record'],
    });
    renderWithProviders(<LifecycleSection entityType="assets" entityId={ASSET_ID} />);
    await apply('disposed');

    expect(await screen.findByText('Es fehlt ein Entsorgungsnachweis.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Zur Entsorgung' })).toHaveAttribute(
      'href',
      '/disposal',
    );
  });

  it('shows "Übergang nicht erlaubt" for invalid-transition', async () => {
    stubRefusal({
      type: 'urn:reticora:problem:invalid-transition',
      status: 422,
      detail: 'no transition from in_stock to disposed',
      from_state: 'in_stock',
      to_state: 'disposed',
    });
    renderWithProviders(<LifecycleSection entityType="assets" entityId={ASSET_ID} />);
    await apply('disposed');

    expect(await screen.findByRole('alert')).toHaveTextContent('Übergang nicht erlaubt.');
    expect(screen.queryByText(/no transition from/)).not.toBeInTheDocument();
  });
});

// WP-1080 (PRB-01): the section "Monitoring" of the CI detail loads only
// when opened, shows the origin ci after the override is saved and the
// hint without the feature monitoring (403).
describe('CIMonitoringSection', () => {
  const CI = 'd0000000-0000-4000-8000-000000000001';
  const PROBE = 'p0000000-0000-4000-8000-000000000001';
  const ICMP = {
    key: 'icmp',
    status: 'available',
    min_interval_s: 10,
    default_interval_s: 30,
    max_interval_s: 3600,
    min_timeout_s: 1,
    default_timeout_s: 10,
    max_timeout_s: 10,
    default_down_count: 5,
    default_up_count: 1,
    params: { type: 'object', properties: {} },
  };
  const field = (value: unknown, origin: string) => ({ value, origin, clamped: false });

  const GROUP = 'f0000000-0000-4000-8000-0000000000a1';
  const GROUP_LOW = 'f0000000-0000-4000-8000-0000000000a2';
  const GROUP_OUT = 'f0000000-0000-4000-8000-0000000000a3';

  function stubMonitoring({ forbidden = false, groups = false } = {}) {
    let override: Record<string, unknown> | undefined;
    const calls: { method: string; path: string; body?: Record<string, unknown> }[] = [];
    const answer = (body: unknown, status = 200) =>
      new Response(status === 204 ? null : JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const method = init?.method ?? 'GET';
        const path = url.pathname.replace(/^\/api\/v1/, '');
        const body = init?.body
          ? (JSON.parse(String(init.body)) as Record<string, unknown>)
          : undefined;
        calls.push({ method, path, body });
        if (path === '/me/permissions') {
          return answer({ user_id: 'u', permissions: ['monitoring:read', 'monitoring:write'] });
        }
        if (forbidden && path.startsWith('/monitoring/')) {
          return answer({ type: 'about:blank', title: 'Forbidden', status: 403 }, 403);
        }
        if (method === 'POST' && path === '/monitoring/profiles') {
          override = body?.params as Record<string, unknown>;
          return answer({ id: 'prof-1', organization_id: 'o', updated_at: 'x', ...body }, 201);
        }
        const list = (data: unknown[]) =>
          answer({ data, total: data.length, limit: 200, offset: 0, has_more: false });
        if (path === '/monitoring/probe-types') return list([ICMP]);
        const groupProfile = (id: string, params: Record<string, unknown>) => ({
          id: `prof-${id}`,
          organization_id: 'o',
          level: 'group',
          level_id: id,
          probe_type: 'icmp',
          params,
          updated_at: 'x',
        });
        const group = (id: string, name: string, priority: number) => ({
          id,
          organization_id: 'o',
          name,
          priority,
          created_at: 'x',
          updated_at: 'x',
        });
        if (groups && path === '/monitoring/groups') {
          return list([
            group(GROUP, 'Kern', 10),
            group(GROUP_LOW, 'Rand', 1),
            group(GROUP_OUT, 'Fremd', 99),
          ]);
        }
        if (groups && path.startsWith('/monitoring/groups/') && path.endsWith('/members')) {
          const id = path.split('/')[3];
          return list(id === GROUP_OUT ? [] : [{ group_id: id, ci_id: CI }]);
        }
        if (path === '/monitoring/profiles') {
          const level = url.searchParams.get('level');
          if (groups && level === 'group') {
            return list([
              groupProfile(GROUP, { interval_s: 120 }),
              groupProfile(GROUP_LOW, { interval_s: 90, timeout_s: 5 }),
              groupProfile(GROUP_OUT, { interval_s: 300 }),
            ]);
          }
          return list(
            level === 'ci' && override
              ? [
                  {
                    id: 'prof-1',
                    organization_id: 'o',
                    level: 'ci',
                    level_id: CI,
                    probe_type: 'icmp',
                    params: override,
                    updated_at: 'x',
                  },
                ]
              : [],
          );
        }
        if (path === '/monitoring/probes') {
          return list([
            {
              id: PROBE,
              organization_id: 'o',
              ci_id: CI,
              collector_group_id: 'g',
              type: 'icmp',
              target: '10.0.0.1',
              params: {},
              enabled: true,
              created_at: 'x',
              updated_at: 'x',
            },
          ]);
        }
        if (path === `/monitoring/probes/${PROBE}/effective`) {
          return answer({
            probe_id: PROBE,
            type: 'icmp',
            target: '10.0.0.1',
            interval_s: override ? field(override.interval_s, 'ci') : field(30, 'default'),
            timeout_s: field(10, 'default'),
            down_count: field(5, 'default'),
            up_count: field(1, 'default'),
            thresholds: {},
            params: groups ? { interfaces: field([{ name: 'xe-0/0/1' }], 'default') } : {},
          });
        }
        return list([]);
      }),
    );
    return calls;
  }

  it('loads only when opened and shows the origin ci after saving the override', async () => {
    await i18n.changeLanguage('de-DE');
    const calls = stubMonitoring();
    renderWithProviders(<CIMonitoringSection ciId={CI} />);

    const section = screen.getByTestId('ci-monitoring-section');
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(calls).toEqual([]);

    fireEvent.click(within(section).getByText('Monitoring'));
    (section as HTMLDetailsElement).open = true;
    fireEvent(section, new Event('toggle'));
    const interval = await within(section).findByLabelText('Intervall (s)');
    const row = await within(section).findByTestId('effective-interval_s');
    expect(row).toHaveTextContent('Standard');

    fireEvent.change(interval, { target: { value: '60' } });
    fireEvent.click(within(section).getByRole('button', { name: 'Profil speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      level: 'ci',
      level_id: CI,
      probe_type: 'icmp',
      params: { interval_s: 60 },
    });
    await waitFor(() =>
      expect(within(section).getByTestId('effective-interval_s')).toHaveTextContent('60CI'),
    );
  });

  it('inherits from the monitoring groups of the CI and shows lists as JSON', async () => {
    await i18n.changeLanguage('de-DE');
    stubMonitoring({ groups: true });
    renderWithProviders(<CIMonitoringSection ciId={CI} />);
    const section = screen.getByTestId('ci-monitoring-section');
    (section as HTMLDetailsElement).open = true;
    fireEvent(section, new Event('toggle'));

    // The group of the higher priority decides the interval, the other
    // member group still gives the timeout; a group without the CI counts not.
    const interval = await within(section).findByLabelText('Intervall (s)');
    await waitFor(() => expect(interval).toHaveAttribute('placeholder', '120'));
    expect(within(section).getByTestId('hint-interval_s')).toHaveTextContent(
      'Geerbt: 120 s (Gruppe)',
    );
    expect(within(section).getByTestId('hint-timeout_s')).toHaveTextContent('Geerbt: 5 s (Gruppe)');
    expect(await within(section).findByTestId('effective-params.interfaces')).toHaveTextContent(
      '[{"name":"xe-0/0/1"}]',
    );
  });

  it('shows the hint on 403 without the feature monitoring', async () => {
    await i18n.changeLanguage('de-DE');
    stubMonitoring({ forbidden: true });
    renderWithProviders(<CIMonitoringSection ciId={CI} />);
    const section = screen.getByTestId('ci-monitoring-section');
    (section as HTMLDetailsElement).open = true;
    fireEvent(section, new Event('toggle'));
    expect(
      await within(section).findByText(
        'Monitoring ist für diese Organisation nicht freigeschaltet.',
      ),
    ).toBeInTheDocument();
  });
});
