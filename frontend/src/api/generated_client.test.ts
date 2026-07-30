import { describe, expect, it, vi } from 'vitest';
import { buildQuery, createApiClient, renderPath } from './generated/client';

describe('generated client helpers', () => {
  it('renders path parameters', () => {
    expect(renderPath('/api/v1/cis/{id}', { id: 'abc' })).toBe('/api/v1/cis/abc');
    expect(renderPath('/api/v1/cis')).toBe('/api/v1/cis');
  });

  it('encodes path parameters', () => {
    expect(renderPath('/api/v1/cis/{id}', { id: 'a/b c' })).toBe('/api/v1/cis/a%2Fb%20c');
  });

  it('throws when a path parameter is missing', () => {
    expect(() => renderPath('/api/v1/cis/{id}', {})).toThrow(/missing path parameter/);
  });

  it('skips empty query values', () => {
    expect(buildQuery({ limit: 10, search: '', cursor: undefined })).toBe('?limit=10');
    expect(buildQuery(undefined)).toBe('');
  });
});

describe('createApiClient', () => {
  it('issues typed requests through the transport', async () => {
    const transport = vi.fn().mockResolvedValue({ data: [], total: 0, limit: 50, offset: 0, has_more: false });
    const client = createApiClient(transport);

    await client.get('/api/v1/cis', { query: { limit: 25, cursor: 'abc' } });

    expect(transport).toHaveBeenCalledWith(
      '/api/v1/cis?limit=25&cursor=abc',
      expect.objectContaining({ method: 'GET' }),
    );
  });

  it('serialises request bodies', async () => {
    const transport = vi.fn().mockResolvedValue({});
    const client = createApiClient(transport);

    await client.patch('/api/v1/cis/{id}', { params: { id: 'ci-1' }, body: { name: 'renamed' } });

    expect(transport).toHaveBeenCalledWith(
      '/api/v1/cis/ci-1',
      expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ name: 'renamed' }) }),
    );
  });
});
