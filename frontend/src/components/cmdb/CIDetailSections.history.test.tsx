/**
 * Tests for the history of the CI detail page (CI-06, E-78, WP-475): a
 * reader without audit:read sees the changes of the CI without an error and
 * a hint that the audit entries need audit:read.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { CIHistorySection } from './CIDetailSections';

const CI_ID = '11111111-1111-1111-1111-111111111111';
const ORG_ID = '22222222-2222-2222-2222-222222222222';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const change = {
  id: 'c1',
  organization_id: ORG_ID,
  entity_type: 'ci',
  entity_id: CI_ID,
  change_type: 'update',
  field_name: 'name',
  created_at: '2026-10-20T10:00:00Z',
  source: 'ci_change',
};
const move = {
  id: 'l1',
  organization_id: ORG_ID,
  entity_type: 'ci',
  entity_id: CI_ID,
  change_type: 'location_change',
  field_name: 'parent_id',
  created_at: '2026-10-21T10:00:00Z',
  source: 'location_change',
};
const auditEntry = {
  id: 'a1',
  organization_id: ORG_ID,
  entity_type: 'ci',
  entity_id: CI_ID,
  change_type: 'ci.update',
  created_at: '2026-10-20T10:00:01Z',
  source: 'audit',
};

function page(data: unknown[], auditIncluded: boolean) {
  return {
    data,
    total: data.length,
    limit: 50,
    offset: 0,
    has_more: false,
    audit_included: auditIncluded,
  };
}

const errorText = /historie konnte nicht geladen werden|history could not be loaded/i;
const hintText = /audit:read/i;

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('CIHistorySection', () => {
  it('shows the changes without an error and the audit hint to a reader without audit:read', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => json(page([move, change], false)));
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIHistorySection ciId={CI_ID} />);

    expect(await screen.findByText('update · name')).toBeTruthy();
    expect(screen.getByText('location_change · parent_id')).toBeTruthy();
    expect(screen.getByText(/standortänderung|location change/i)).toBeTruthy();
    expect(screen.getByText(hintText)).toBeTruthy();
    expect(screen.queryByText(errorText)).toBeNull();
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(`/api/v1/history/ci/${CI_ID}`);
  });

  it('shows the hint also for a history without changes', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json(page([], false))),
    );

    renderWithProviders(<CIHistorySection ciId={CI_ID} />);

    expect(await screen.findByText(/noch keine änderungen|no changes yet/i)).toBeTruthy();
    expect(screen.getByText(hintText)).toBeTruthy();
  });

  it('lists the audit entries without the hint for a reader with audit:read', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json(page([move, auditEntry, change], true))),
    );

    renderWithProviders(<CIHistorySection ciId={CI_ID} />);

    expect(await screen.findByText('ci.update')).toBeTruthy();
    expect(screen.getByText('update · name')).toBeTruthy();
    expect(screen.queryByText(hintText)).toBeNull();
    expect(screen.queryByText(errorText)).toBeNull();
  });

  it('shows a change set with three field changes as one entry with three field lines (AUD-03)', async () => {
    const changeSet = {
      id: 'c3',
      organization_id: ORG_ID,
      entity_type: 'ci',
      entity_id: CI_ID,
      change_type: 'update',
      created_at: '2026-10-22T10:00:00Z',
      source: 'ci_change',
      change_kind: 'updated',
      change_set_id: '33333333-3333-3333-3333-333333333333',
      change_source: 'ui',
      request_id: 'req-1',
      field_changes: [
        { field: 'model', old: 'H1', new: 'H2' },
        { field: 'name', old: 'old-name', new: 'new-name' },
        { field: 'os_name', old: null, new: 'Linux' },
      ],
    };
    // A row written before the change sets: no change set, shown as before.
    const legacy = {
      ...change,
      id: 'c0',
      old_value: 'a',
      new_value: 'b',
      change_kind: 'updated',
      field_changes: [{ field: 'name', old: 'a', new: 'b' }],
    };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json(page([changeSet, legacy], true))),
    );

    renderWithProviders(<CIHistorySection ciId={CI_ID} />);

    const history = await screen.findByRole('list', { name: /historie|history/i });
    const entries = within(history)
      .getAllByRole('listitem')
      .filter((li) => li.parentElement === history);
    expect(entries).toHaveLength(2);
    const lines = within(entries[0]!).getAllByRole('listitem');
    expect(lines.map((li) => li.textContent)).toEqual([
      'model: H1 → H2',
      'name: old-name → new-name',
      'os_name: — → Linux',
    ]);
    expect(within(entries[1]!).getByText('update · name')).toBeTruthy();
    expect(within(entries[1]!).queryAllByRole('listitem')).toHaveLength(0);
  });

  it('still reports a failed load', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockImplementation(async () =>
          json(
            { title: 'Forbidden', status: 403, detail: 'missing required permission: ci:read' },
            403,
          ),
        ),
    );

    renderWithProviders(<CIHistorySection ciId={CI_ID} />);

    expect(await screen.findByText(errorText)).toBeTruthy();
    expect(screen.queryByText(hintText)).toBeNull();
  });
});
