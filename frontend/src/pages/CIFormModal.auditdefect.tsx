/**
 * Phase-1 acceptance audit, group T6-epic-b – DEFECT test (fails until fixed).
 *
 * WP-120 (CI-01, CI-03), WP-117 (CI-03), WP-119 (MET-04)
 * Findings:
 *   - P1 "Manual CI creation in the UI always fails: CIFormModal sends a
 *     hard-coded type key instead of the CI type UUID" (CIFormModal.tsx:30
 *     ciTypeValues, used as option value and posted as ci_type_id; the backend
 *     stores ci_type_id as a uuid → 400).
 *   - P2 "MET-04: new CI types cannot be used in the UI creation dialog without
 *     a code release" (org types created at runtime never appear).
 * Gaps (high): the dialog lists API types including org types; the option
 * values are the type ids from the API; the POST body carries the chosen id.
 *
 * Retest:
 *   cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/CIFormModal.auditdefect.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { CIFormModal } from './CIFormModal';

const SYSTEM_TYPE_ID = '11111111-1111-4111-8111-111111111111';
const ORG_TYPE_ID = '22222222-2222-4222-8222-222222222222';

function ciType(id: string, key: string, name: string, system: boolean) {
  return {
    id,
    key,
    name,
    display_name: { de: name, en: name },
    is_builtin: system,
    is_system: system,
    is_active: true,
    is_logical: false,
    is_observable: true,
    schema_version: 1,
    version: 1,
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** Stubs GET /ci-types (system + org type) and records POST /cis bodies. */
function stubBackend() {
  const posted: Array<Record<string, unknown>> = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    const method = (init?.method ?? 'GET').toUpperCase();
    if (url.includes('/ci-types')) {
      return json({
        data: [
          ciType(SYSTEM_TYPE_ID, 'server', 'Server', true),
          ciType(ORG_TYPE_ID, 'audit-custom-pdu', 'Audit Custom PDU', false),
        ],
        total: 2,
        limit: 200,
        offset: 0,
      });
    }
    if (url.includes('/cis') && method === 'POST') {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
      posted.push(body);
      return json({ id: '33333333-3333-4333-8333-333333333333', version: 1, ...body }, 201);
    }
    return json({ detail: `unexpected request: ${method} ${url}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return posted;
}

function typeSelect(): HTMLSelectElement {
  return screen.getByLabelText(/typ|type/i) as HTMLSelectElement;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('CIFormModal CI types come from the metamodel API (audit defect)', () => {
  it('offers the org type created at runtime (MET-04)', async () => {
    stubBackend();
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await waitFor(
      () => {
        const labels = Array.from(typeSelect().options).map((o) => o.textContent ?? '');
        expect(labels.some((l) => l.includes('Audit Custom PDU'))).toBe(true);
      },
      { timeout: 2000 },
    );
  });

  it('uses the API type ids as option values and posts the selected id (CI-03)', async () => {
    const posted = stubBackend();
    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    await waitFor(
      () => {
        const values = Array.from(typeSelect().options)
          .map((o) => o.value)
          .filter((v) => v !== '');
        expect(values.sort()).toEqual([SYSTEM_TYPE_ID, ORG_TYPE_ID].sort());
      },
      { timeout: 2000 },
    );

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'srv-audit-01' } });
    fireEvent.change(typeSelect(), { target: { value: ORG_TYPE_ID } });
    fireEvent.click(screen.getByRole('button', { name: /anlegen|create/i }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]?.ci_type_id).toBe(ORG_TYPE_ID);
  });
});
