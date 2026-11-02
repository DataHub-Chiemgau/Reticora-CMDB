/**
 * Tests for the override hooks (F-100, F-021; CI-10, API-07). Setting or
 * clearing an override changes the effective value of the CI and raises its
 * version (WP-412), so both hooks mark the CI, the CI lists and the field
 * values stale: the CI detail shows the new value without a reload and the
 * edit form sends the new version as If-Match. Other CIs are not refetched.
 */
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { useCI, useCIList } from './hooks';
import { useClearOverride, useFieldProvenance, useFieldValues, useSetOverride } from './cmdbHooks';
import { createTestQueryClient } from '../test/utils';

const CI_ID = '11111111-1111-1111-1111-111111111111';
const OTHER_ID = '22222222-2222-2222-2222-222222222222';
const OBSERVED = 'C9300-48P';

function json(body: unknown, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

/**
 * stubAPI answers like the server: the CI holds its effective model and
 * version, PUT …/override sets the manual value, DELETE returns the field to
 * the observed one, each raising the version. Every request is recorded as
 * "METHOD path".
 */
function stubAPI(initial: { model: string; version: number }) {
  const ci = { id: CI_ID, name: 'sw-core-01', status: 'active', ci_type_id: 'switch', ...initial };
  const other = { id: OTHER_ID, name: 'sw-core-02', status: 'active', model: OBSERVED, version: 1 };
  const requests: string[] = [];
  const provenance = () => ({
    field: 'model',
    value: ci.model,
    source: ci.model === OBSERVED ? 'snmp' : 'manual_override',
    rank: ci.model === OBSERVED ? 80 : 100,
    reason: ci.model === OBSERVED ? 'source_rank' : 'override',
    diverged: false,
  });
  // The stored field value (GET …/fields) behind the provenance.
  const fieldValue = () => ({
    id: 'fv-1',
    ci_id: CI_ID,
    field_name: 'model',
    discovered_value: OBSERVED,
    discovered_source: 'snmp',
    ...(ci.model === OBSERVED ? {} : { override_value: ci.model }),
    protected: ci.model !== OBSERVED,
    effective_value: ci.model,
    diverged: ci.model !== OBSERVED,
  });
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = (init?.method ?? 'GET').toUpperCase();
    const path = String(input).replace(/^\/api\/v1/, '');
    requests.push(`${method} ${path}`);
    if (method === 'PUT' && path === `/cis/${CI_ID}/fields/model/override`) {
      ci.model = (JSON.parse(String(init?.body)) as { value: string }).value;
      ci.version += 1;
      return json(provenance(), { ETag: `"${ci.version}"` });
    }
    if (method === 'DELETE' && path === `/cis/${CI_ID}/fields/model/override`) {
      ci.model = OBSERVED;
      ci.version += 1;
      return json(provenance(), { ETag: `"${ci.version}"` });
    }
    if (method === 'GET' && path === `/cis/${CI_ID}`) return json({ ...ci });
    if (method === 'GET' && path === `/cis/${OTHER_ID}`) return json({ ...other });
    if (method === 'GET' && path.startsWith('/cis?')) {
      return json({ data: [{ ...ci }, { ...other }], total: 2 });
    }
    if (method === 'GET' && path === `/cis/${CI_ID}/fields`) return json({ data: [fieldValue()] });
    if (method === 'GET' && path === `/cis/${CI_ID}/provenance`) {
      return json({ data: [provenance()] });
    }
    return new Response(JSON.stringify({ detail: `unexpected request: ${method} ${path}` }), {
      status: 404,
      headers: { 'Content-Type': 'application/json' },
    });
  });
  vi.stubGlobal('fetch', fetchMock);
  return { requests };
}

/** renderCIPage mounts the queries of an open CI detail page and both override hooks. */
async function renderCIPage() {
  const client = createTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const view = renderHook(
    () => ({
      ci: useCI(CI_ID),
      other: useCI(OTHER_ID),
      list: useCIList({ limit: 50 }),
      fields: useFieldValues(CI_ID),
      provenance: useFieldProvenance(CI_ID),
      setOverride: useSetOverride(CI_ID),
      clearOverride: useClearOverride(CI_ID),
    }),
    { wrapper },
  );
  await waitFor(() => {
    const r = view.result.current;
    expect(r.ci.isSuccess && r.other.isSuccess && r.list.isSuccess).toBe(true);
    expect(r.fields.isSuccess && r.provenance.isSuccess).toBe(true);
  });
  return { ...view, client };
}

/** count returns how often a request was sent. */
function count(requests: string[], request: string) {
  return requests.filter((r) => r === request).length;
}

const reads = [
  `GET /cis/${CI_ID}`,
  'GET /cis?limit=50',
  `GET /cis/${CI_ID}/fields`,
  `GET /cis/${CI_ID}/provenance`,
];

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('override hooks', () => {
  it('setting an override refreshes the CI detail, the CI lists and the field values (CI-10, API-07)', async () => {
    const { requests } = stubAPI({ model: OBSERVED, version: 1 });
    const { result, client } = await renderCIPage();
    for (const read of [...reads, `GET /cis/${OTHER_ID}`]) {
      expect(count(requests, read), read).toBe(1);
    }

    await act(() =>
      result.current.setOverride.mutateAsync({
        field: 'model',
        value: 'manual-model',
        reason: 'Typenschild geprüft',
      }),
    );

    // The detail shows the manual value with the version the PUT raised, so
    // the edit form sends If-Match "2" (F-021).
    await waitFor(() => expect(result.current.ci.data?.model).toBe('manual-model'));
    expect(result.current.ci.data?.version).toBe(2);
    await waitFor(() => expect(result.current.list.data?.data[0]?.model).toBe('manual-model'));
    await waitFor(() =>
      expect(result.current.fields.data?.data[0]?.override_value).toBe('manual-model'),
    );
    await waitFor(() => expect(result.current.provenance.data?.data[0]?.reason).toBe('override'));
    for (const read of reads) expect(count(requests, read), read).toBe(2);
    // Another CI is not refetched.
    expect(count(requests, `GET /cis/${OTHER_ID}`)).toBe(1);
    expect(client.getQueryState(['ci', OTHER_ID])?.isInvalidated).toBe(false);
  });

  it('clearing an override shows the observed value in the CI detail without a reload (F-100)', async () => {
    const { requests } = stubAPI({ model: 'manual-model', version: 2 });
    const { result, client } = await renderCIPage();
    expect(result.current.ci.data?.model).toBe('manual-model');
    for (const read of [...reads, `GET /cis/${OTHER_ID}`]) {
      expect(count(requests, read), read).toBe(1);
    }

    await act(() => result.current.clearOverride.mutateAsync('model'));

    // The overview, the list and the provenance agree on the observed value.
    await waitFor(() => expect(result.current.ci.data?.model).toBe(OBSERVED));
    expect(result.current.ci.data?.version).toBe(3);
    await waitFor(() => expect(result.current.list.data?.data[0]?.model).toBe(OBSERVED));
    await waitFor(() =>
      expect(result.current.fields.data?.data[0]?.effective_value).toBe(OBSERVED),
    );
    await waitFor(() => expect(result.current.provenance.data?.data[0]?.value).toBe(OBSERVED));
    for (const read of reads) expect(count(requests, read), read).toBe(2);
    expect(count(requests, `GET /cis/${OTHER_ID}`)).toBe(1);
    expect(client.getQueryState(['ci', OTHER_ID])?.isInvalidated).toBe(false);
  });
});
