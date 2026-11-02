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
 * Retest:
 *   cd frontend && npx vitest run src/components/cmdb/CIDetailSections.test.tsx
 */
import { screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithProviders, stubFetchRoutes } from '../../test/utils';
import { InstanceFieldsSection } from './CIDetailSections';

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
