/**
 * WP-1080 (PRB-01): the ProfileEditor of one level and probe type. An empty
 * field shows the inherited value with its source, a client profile
 * inherits from the org profile, "Zurücksetzen" sends PATCH without the
 * field and deletes the profile with its last field, a 422 appears at its
 * field and a timeout not below the interval is marked before sending.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/monitoring/ProfileEditor.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import i18n from '../../i18n';
import type { MonitoringProfile, ProbeType } from '../../api/client';
import { ProfileEditor } from './ProfileEditor';

type Json = Record<string, unknown>;

const ICMP: ProbeType = {
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
  max_parallel_per_target: 1,
  max_parallel_per_collector: 256,
  max_rate_per_collector: 1000,
  max_probes_per_target: 4,
  max_payload_bytes: 1472,
  max_rows: 1,
  credential_namespaces: [],
  capabilities: ['icmp'],
  params: {
    type: 'object',
    properties: {
      count: { type: 'integer', minimum: 1, maximum: 5, default: 3 },
      family: { type: 'string', enum: ['auto', 'v4', 'v6'], default: 'auto' },
      rtt_warn_ms: { type: 'number', minimum: 1 },
    },
  },
};

const PROFILE_ID = 'a1000000-0000-4000-8000-000000000001';
const CLIENT = 'c0000000-0000-4000-8000-000000000001';

const profile = (params: Json, extra: Json = {}): MonitoringProfile =>
  ({
    id: PROFILE_ID,
    organization_id: 'o',
    level: 'client',
    level_id: CLIENT,
    probe_type: 'icmp',
    params,
    updated_at: '2026-01-01T00:00:00Z',
    ...extra,
  }) as MonitoringProfile;

function stubApi(answer?: (method: string, body: Json) => Response | undefined) {
  const calls: { method: string; path: string; body?: Json }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = init?.method ?? 'GET';
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      calls.push({ method, path, body });
      const custom = answer?.(method, body ?? {});
      if (custom) return custom;
      if (method === 'DELETE') return new Response(null, { status: 204 });
      return new Response(JSON.stringify(profile((body?.params as Json) ?? {})), {
        status: method === 'POST' ? 201 : 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }),
  );
  return calls;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('ProfileEditor', () => {
  it('leeres Feld zeigt den geerbten Wert 30 s mit Quelle Standard', () => {
    stubApi();
    renderWithProviders(
      <ProfileEditor probeType={ICMP} level="org" inherited={[]} readOnly={false} />,
    );
    const interval = screen.getByLabelText('Intervall (s)');
    expect(interval).toHaveValue('');
    expect(interval).toHaveAttribute('placeholder', '30');
    expect(screen.getByTestId('hint-interval_s')).toHaveTextContent('Geerbt: 30 s (Standard)');
    expect(screen.getByTestId('hint-interval_s')).toHaveTextContent('Grenzen 10–3600 s');
    expect(screen.getByTestId('hint-params.count')).toHaveTextContent('Geerbt: 3 (Standard)');
  });

  it('Kundenprofil erbt vom Org-Profil', () => {
    stubApi();
    renderWithProviders(
      <ProfileEditor
        probeType={ICMP}
        level="client"
        levelId={CLIENT}
        inherited={[{ source: 'org', params: { interval_s: 60, thresholds: { rtt_warn_ms: 80 } } }]}
        readOnly={false}
      />,
    );
    expect(screen.getByLabelText('Intervall (s)')).toHaveAttribute('placeholder', '60');
    expect(screen.getByTestId('hint-interval_s')).toHaveTextContent('Geerbt: 60 s (Org)');
    expect(screen.getByTestId('hint-thresholds.rtt_warn_ms')).toHaveTextContent(
      'Geerbt: 80 ms (Org)',
    );
    // Not set above: the type default stays the source.
    expect(screen.getByTestId('hint-timeout_s')).toHaveTextContent('Geerbt: 10 s (Standard)');
  });

  it('creates a profile of the level with only the set fields', async () => {
    const calls = stubApi();
    renderWithProviders(
      <ProfileEditor
        probeType={ICMP}
        level="client"
        levelId={CLIENT}
        inherited={[]}
        readOnly={false}
      />,
    );
    fireEvent.change(screen.getByLabelText('Intervall (s)'), { target: { value: '120' } });
    fireEvent.change(screen.getByLabelText('rtt_warn_ms (ms)'), { target: { value: '150' } });
    fireEvent.click(screen.getByRole('button', { name: 'Profil speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      level: 'client',
      level_id: CLIENT,
      probe_type: 'icmp',
      params: { interval_s: 120, thresholds: { rtt_warn_ms: 150 } },
    });
  });

  it('Zurücksetzen sendet PATCH ohne das Feld, letztes Feld löscht das Profil', async () => {
    const calls = stubApi();
    const { rerender } = renderWithProviders(
      <ProfileEditor
        probeType={ICMP}
        level="client"
        levelId={CLIENT}
        profile={profile({ interval_s: 60, count: 4 })}
        inherited={[]}
        readOnly={false}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Intervall zurücksetzen' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.path).toBe(`/monitoring/profiles/${PROFILE_ID}`);
    expect(patch?.body).toEqual({ params: { count: 4 } });

    rerender(
      <ProfileEditor
        key="second"
        probeType={ICMP}
        level="client"
        levelId={CLIENT}
        profile={profile({ count: 4 })}
        inherited={[]}
        readOnly={false}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'count zurücksetzen' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true));
    expect(calls.find((c) => c.method === 'DELETE')?.path).toBe(
      `/monitoring/profiles/${PROFILE_ID}`,
    );
  });

  it('keeps keys the form does not show and flat thresholds on PATCH', async () => {
    const calls = stubApi();
    renderWithProviders(
      <ProfileEditor
        probeType={ICMP}
        level="client"
        levelId={CLIENT}
        profile={profile({ interval_s: 60, headers: { 'X-Probe': '1' }, rtt_warn_ms: 200 })}
        inherited={[]}
        readOnly={false}
      />,
    );
    // The flat threshold is shown as the own value of its field.
    expect(screen.getByLabelText('rtt_warn_ms (ms)')).toHaveValue('200');
    fireEvent.click(screen.getByRole('button', { name: 'Intervall zurücksetzen' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({
      params: { headers: { 'X-Probe': '1' }, thresholds: { rtt_warn_ms: 200 } },
    });
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
  });

  it('keeps the profile while a hidden key remains', async () => {
    const calls = stubApi();
    renderWithProviders(
      <ProfileEditor
        probeType={ICMP}
        level="client"
        levelId={CLIENT}
        profile={profile({ count: 4, headers: { 'X-Probe': '1' } })}
        inherited={[]}
        readOnly={false}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'count zurücksetzen' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({
      params: { headers: { 'X-Probe': '1' } },
    });
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
  });

  it('writes http cert_warn_days flat in params', async () => {
    const calls = stubApi();
    const HTTP: ProbeType = {
      ...ICMP,
      key: 'http',
      params: {
        type: 'object',
        properties: {
          cert_warn_days: { type: 'integer', minimum: 1, default: 21 },
          rtt_warn_ms: { type: 'number', minimum: 1 },
        },
      },
    };
    renderWithProviders(
      <ProfileEditor probeType={HTTP} level="org" inherited={[]} readOnly={false} />,
    );
    fireEvent.change(screen.getByLabelText('cert_warn_days'), { target: { value: '30' } });
    fireEvent.change(screen.getByLabelText('rtt_warn_ms (ms)'), { target: { value: '500' } });
    fireEvent.click(screen.getByRole('button', { name: 'Profil speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    expect(calls.find((c) => c.method === 'POST')?.body).toMatchObject({
      probe_type: 'http',
      params: { cert_warn_days: 30, thresholds: { rtt_warn_ms: 500 } },
    });
  });

  it('422 interval_s erscheint am Feld', async () => {
    stubApi(
      () =>
        new Response(
          JSON.stringify({
            type: 'about:blank',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'interval must be at least 60 s (plan minimum)',
            field: 'interval_s',
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    );
    renderWithProviders(
      <ProfileEditor probeType={ICMP} level="org" inherited={[]} readOnly={false} />,
    );
    const interval = screen.getByLabelText('Intervall (s)');
    fireEvent.change(interval, { target: { value: '20' } });
    fireEvent.click(screen.getByRole('button', { name: 'Profil speichern' }));
    await waitFor(() => expect(interval).toHaveAttribute('aria-invalid', 'true'));
    expect(screen.getByText('interval must be at least 60 s (plan minimum)')).toBeInTheDocument();
  });

  it('Timeout ≥ Intervall wird vor dem Senden markiert', () => {
    const calls = stubApi();
    renderWithProviders(
      <ProfileEditor
        probeType={{ ...ICMP, max_timeout_s: 60 }}
        level="org"
        inherited={[]}
        readOnly={false}
      />,
    );
    fireEvent.change(screen.getByLabelText('Intervall (s)'), { target: { value: '20' } });
    fireEvent.change(screen.getByLabelText('Timeout (s)'), { target: { value: '20' } });
    fireEvent.click(screen.getByRole('button', { name: 'Profil speichern' }));
    expect(screen.getByLabelText('Timeout (s)')).toHaveAttribute('aria-invalid', 'true');
    expect(
      screen.getByText('Der Timeout muss kleiner als das Intervall sein.'),
    ).toBeInTheDocument();
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);
  });

  it('is read only without write access', () => {
    stubApi();
    renderWithProviders(
      <ProfileEditor
        probeType={ICMP}
        level="org"
        profile={profile({ interval_s: 60 }, { level: 'org', level_id: null })}
        inherited={[]}
        readOnly
      />,
    );
    expect(screen.getByLabelText('Intervall (s)')).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Profil speichern' })).toBeNull();
    expect(screen.queryByRole('button', { name: /zurücksetzen/ })).toBeNull();
  });
});
