/**
 * Regression tests for RFC 7807 problem-detail parsing.
 *
 * The API answers field-metadata validation failures with HTTP 422 and a
 * `violations` array. The client used to throw `new Error(detail)`, which
 * discarded the array and made per-field form errors impossible, so these
 * tests pin the parsed shape.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';

const futureExpiry = '2999-01-01T00:00:00Z';

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

async function importClientWithSession(token = 'token') {
  vi.resetModules();
  const { useAuthStore } = await import('../auth/authStore');
  useAuthStore.getState().setSession({
    token,
    expiresAt: futureExpiry,
    user: { sub: 'user-1', email: 'user@example.com' },
  });
  return import('./client');
}

afterEach(() => {
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.resetModules();
});

describe('ApiError', () => {
  it('exposes per-field violations from a 422 problem detail', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(
          {
            type: 'https://reticora.io/problems/422',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'ram_gb: field is required; ilo_ip: must be a valid IP address',
            violations: [
              { field: 'ram_gb', detail: 'field is required' },
              { field: 'ilo_ip', detail: 'must be a valid IP address' },
            ],
          },
          422,
        ),
      ),
    );
    const { fetchAPI, ApiError } = await importClientWithSession();

    const error = await fetchAPI('/cis').catch((e: unknown) => e);

    expect(error).toBeInstanceOf(ApiError);
    const apiError = error as InstanceType<typeof ApiError>;
    expect(apiError.status).toBe(422);
    expect(apiError.hasViolations).toBe(true);
    expect(apiError.violations).toHaveLength(2);
    expect(apiError.violationsByField()).toEqual({
      ram_gb: 'field is required',
      ilo_ip: 'must be a valid IP address',
    });
    // The summary message still carries the human-readable detail.
    expect(apiError.message).toContain('field is required');
  });

  it('keeps the detail as the message when there are no violations', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ title: 'Conflict', status: 409, detail: 'already exists' }, 409),
        ),
    );
    const { fetchAPI, ApiError } = await importClientWithSession();

    const error = (await fetchAPI('/cis').catch((e: unknown) => e)) as InstanceType<
      typeof ApiError
    >;

    expect(error).toBeInstanceOf(ApiError);
    expect(error.status).toBe(409);
    expect(error.message).toBe('already exists');
    expect(error.hasViolations).toBe(false);
    expect(error.violationsByField()).toEqual({});
  });

  it('falls back to the status text when the body is not JSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('boom', { status: 500, statusText: 'Server Error' })),
    );
    const { fetchAPI, ApiError } = await importClientWithSession();

    const error = (await fetchAPI('/cis').catch((e: unknown) => e)) as InstanceType<
      typeof ApiError
    >;

    expect(error).toBeInstanceOf(ApiError);
    expect(error.status).toBe(500);
    expect(error.message).toBe('Server Error');
    // A 500 must never be mistaken for a validation failure.
    expect(error.hasViolations).toBe(false);
  });

  it('ignores malformed violation entries instead of throwing', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(
          {
            status: 422,
            detail: 'invalid',
            violations: [null, 'nope', { detail: 'no field name' }, { field: 'ok' }],
          },
          422,
        ),
      ),
    );
    const { fetchAPI, ApiError } = await importClientWithSession();

    const error = (await fetchAPI('/cis').catch((e: unknown) => e)) as InstanceType<
      typeof ApiError
    >;

    expect(error).toBeInstanceOf(ApiError);
    expect(error.violations).toEqual([{ field: 'ok', detail: '' }]);
  });
});

describe('relationshipApi', () => {
  it('PATCHes verification state to the relationship endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ id: 'rel-1' }));
    vi.stubGlobal('fetch', fetchMock);
    const { relationshipApi } = await importClientWithSession();

    await relationshipApi.update('rel-1', { verification_state: 'verified' });

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/relationships/rel-1');
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe('PATCH');
    expect(JSON.parse(fetchMock.mock.calls[0]?.[1]?.body as string)).toEqual({
      verification_state: 'verified',
    });
  });

  it('creates and deletes relationships on the collection endpoint', async () => {
    const fetchMock = vi.fn().mockImplementation(() => jsonResponse({ id: 'rel-2' }));
    vi.stubGlobal('fetch', fetchMock);
    const { relationshipApi } = await importClientWithSession();

    await relationshipApi.create({
      source_ci_id: 'a',
      target_ci_id: 'b',
      rel_type: 'depends_on',
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/relationships');
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe('POST');

    await relationshipApi.delete('rel-2');
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/relationships/rel-2');
    expect(fetchMock.mock.calls[1]?.[1]?.method).toBe('DELETE');
  });
});
