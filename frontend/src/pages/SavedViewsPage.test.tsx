/**
 * Regression tests for creating and deleting personal saved views.
 *
 * The backend has always exposed POST and DELETE /saved-views, and a
 * `useSaveView` hook existed, but no component ever called it. There was no
 * create and no delete affordance anywhere in the UI, so "Meine Ansichten" was
 * permanently empty and its empty-state string was unconditionally true.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { SavedViewsPage } from './SavedViewsPage';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const preset = {
  id: '',
  organization_id: '',
  name: 'Assets with warranty expiring within 90 days',
  entity_kind: 'asset',
  filter_spec: { warranty_within_days: 90 },
  shared: true,
};

const ownView = {
  id: 'view-1',
  organization_id: 'org-1',
  name: 'My servers',
  entity_kind: 'ci',
  filter_spec: { status: 'active' },
  shared: false,
};

/** Routes each request by URL/method so a single mock serves the whole page. */
function mockApi(overrides: { views?: unknown[] } = {}) {
  return vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    if (url.includes('/saved-views/presets')) return json({ data: [preset] });
    if (url.includes('/search/query')) return json({ data: [], total: 0 });
    if (url.includes('/saved-views') && method === 'POST') return json({ id: 'new' }, 201);
    if (url.includes('/saved-views') && method === 'DELETE')
      return new Response(null, { status: 204 });
    return json({ data: overrides.views ?? [], total: 0, limit: 50, offset: 0, has_more: false });
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('SavedViewsPage', () => {
  it('saves the currently running filter as a personal view', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    vi.spyOn(window, 'prompt').mockReturnValue('Warranty watch');

    renderWithProviders(<SavedViewsPage />);

    // Nothing is running yet, so there is nothing to save.
    expect(screen.queryByRole('button', { name: /speichern|save as view/i })).toBeNull();

    fireEvent.click(await screen.findByRole('button', { name: new RegExp(preset.name, 'i') }));

    // entity_kind is a sibling column of filter_spec, so it must be folded
    // into the query; otherwise an asset view runs against the ci table and
    // its asset-only predicates are silently dropped.
    await waitFor(() => {
      const q = fetchMock.mock.calls.find((call) => String(call[0]).includes('/search/query'));
      expect(q).toBeTruthy();
      expect(JSON.parse(String(q![1]!.body)).entity_kind).toBe('asset');
    });

    const saveButton = await screen.findByRole('button', { name: /speichern|save as view/i });
    fireEvent.click(saveButton);

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        (call) => call[1]?.method === 'POST' && String(call[0]).includes('/saved-views'),
      );
      expect(post).toBeTruthy();
      const body = JSON.parse(String(post![1]!.body));
      expect(body.name).toBe('Warranty watch');
      // entity_kind is lifted out of the spec into its own column.
      expect(body.entity_kind).toBe('asset');
      expect(body.filter_spec).toEqual({ warranty_within_days: 90 });
      expect(body.filter_spec.entity_kind).toBeUndefined();
    });
  });

  it('does not save when the name prompt is cancelled', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    vi.spyOn(window, 'prompt').mockReturnValue(null);

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: new RegExp(preset.name, 'i') }));
    fireEvent.click(await screen.findByRole('button', { name: /speichern|save as view/i }));

    await waitFor(() => expect(window.prompt).toHaveBeenCalled());
    expect(
      fetchMock.mock.calls.some(
        (call) => call[1]?.method === 'POST' && String(call[0]).includes('/saved-views'),
      ),
    ).toBe(false);
  });

  it('deletes a personal view', async () => {
    const fetchMock = mockApi({ views: [ownView] });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<SavedViewsPage />);

    // The delete control carries an explicit aria-label ("Löschen: My
    // servers") so it is distinguishable from the run button.
    const del = await screen.findByRole('button', {
      name: new RegExp(`(löschen|delete): ${ownView.name}`, 'i'),
    });
    fireEvent.click(del);

    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) => c[1]?.method === 'DELETE');
      expect(call).toBeTruthy();
      expect(String(call![0])).toContain(`/saved-views/${ownView.id}`);
    });
  });
});
