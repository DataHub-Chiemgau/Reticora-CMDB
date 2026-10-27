import { readFileSync } from 'node:fs';
import { expect, list, mockBackend, signIn, test } from './support/backend';

// ABN-01 (e), conflict scenario [B] (WP-212): an override is set on a CI,
// discovery reports a different value, the override_conflict review is
// visible, one conflict is accepted (the CI takes the observed value) and
// one dismissed (the override stays). The data is frozen in
// docs/acceptance/artefakte/abn-01-e-konflikt.json, the protocol is
// docs/acceptance/abn-01.md. Runs in every browser project. The strict mock
// checks every request and answer against api/openapi.yaml (WP-530).

test.use({ locale: 'de-DE' });

interface Frozen {
  ci: {
    id: string;
    name: string;
    ci_type_id: string;
    organization_id: string;
    model_discovered: string;
    discovered_source: string;
  };
  override: { field: string; value: string; reason: string };
  reviews: {
    id: string;
    kind: string;
    payload: Record<string, string>;
    created_at: string;
    action: 'accept' | 'dismiss';
  }[];
}

const frozen = JSON.parse(
  readFileSync(
    new URL('../../docs/acceptance/artefakte/abn-01-e-konflikt.json', import.meta.url),
    'utf8',
  ),
) as Frozen;
const CI = frozen.ci.id;
const [accepted, dismissed] = frozen.reviews;

test('override, deviating discovery, conflict accepted and dismissed', async ({ page }) => {
  let override: { value: string; reason: string } | undefined;
  const provenance = () => ({
    body: {
      data: [
        override
          ? {
              field: frozen.override.field,
              value: override.value,
              source: 'manual_override',
              rank: 100,
              reason: 'override',
              protected: true,
              override_reason: override.reason,
              discovered_value: frozen.ci.model_discovered,
              discovered_source: frozen.ci.discovered_source,
              diverged: true,
            }
          : {
              field: frozen.override.field,
              value: frozen.ci.model_discovered,
              source: frozen.ci.discovered_source,
              rank: 60,
              reason: 'source_rank',
              diverged: false,
            },
      ],
    },
  });
  const reviews = frozen.reviews.map((r) => ({
    id: r.id,
    kind: r.kind,
    status: 'open',
    organization_id: frozen.ci.organization_id,
    payload: r.payload,
    candidate_ci_ids: [CI],
    escalated: false,
    created_at: r.created_at,
    updated_at: r.created_at,
  }));
  const calls = await mockBackend(page, [
    {
      path: `/cis/${CI}`,
      reply: () => ({
        body: {
          id: CI,
          name: frozen.ci.name,
          model: override?.value ?? frozen.ci.model_discovered,
          status: 'active',
          ci_type_id: frozen.ci.ci_type_id,
          attributes: {},
          is_manual: false,
          version: 1,
          organization_id: frozen.ci.organization_id,
          created_at: '2026-10-14T08:00:00Z',
          updated_at: '2026-10-14T08:00:00Z',
        },
      }),
    },
    { path: `/cis/${CI}/provenance`, reply: provenance },
    // The detail page reads the neighbors of the CI as a graph.
    {
      path: /^\/topology\/cis\/[^/]+\/neighbors$/,
      reply: () => ({ body: { nodes: [], edges: [] } }),
    },
    {
      method: 'PUT',
      path: `/cis/${CI}/fields/${frozen.override.field}/override`,
      reply: (req) => {
        override = req.postDataJSON() as { value: string; reason: string };
        return {
          body: {
            id: '22222222-2222-4222-8222-0000000000f1',
            organization_id: frozen.ci.organization_id,
            ci_id: CI,
            field_name: frozen.override.field,
            discovered_value: frozen.ci.model_discovered,
            discovered_source: frozen.ci.discovered_source,
            override_value: override.value,
            override_reason: override.reason,
            protected: true,
            effective_value: override.value,
            diverged: true,
            created_at: '2026-10-14T08:00:00Z',
            updated_at: '2026-10-14T08:00:00Z',
          },
        };
      },
    },
    {
      path: '/review-items',
      reply: () => ({ body: list(reviews.filter((r) => r.status === 'open')) }),
    },
    {
      method: 'POST',
      path: /^\/review-items\/[^/]+\/resolve$/,
      reply: (_req, url) => {
        const item = reviews.find((r) => url.pathname.includes(`/${r.id}/`))!;
        item.status = 'resolved';
        return { body: item };
      },
    },
  ]);
  await signIn(page);

  // 1. Override setzen.
  await page.goto(`/cmdb/${CI}`);
  await page.getByText('Herkunft & Overrides').click();
  await page.getByRole('button', { name: 'Override setzen' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Wert').fill(frozen.override.value);
  await dialog.getByLabel('Begründung').fill(frozen.override.reason);
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(page.getByText('geschützt')).toBeVisible();
  expect(override).toEqual(
    expect.objectContaining({ value: frozen.override.value, reason: frozen.override.reason }),
  );

  // 2./3. Abweichende Discovery: der override_conflict ist sichtbar.
  await page.goto('/reviews');
  await expect(
    page.getByRole('button', {
      name: new RegExp(`Konflikt mit Override.*${accepted.payload.name}`),
    }),
  ).toBeVisible();

  // 4. accept: das CI übernimmt den beobachteten Wert.
  await page.getByRole('button', { name: new RegExp(accepted.payload.name) }).click();
  await page.getByRole('button', { name: 'Übernehmen' }).click();
  await expect(page.getByRole('button', { name: new RegExp(accepted.payload.name) })).toBeHidden();
  expect(calls.find((c) => c.path === `/review-items/${accepted.id}/resolve`)?.body).toEqual(
    expect.objectContaining({ action: accepted.action }),
  );

  // 5. dismiss: der Override bleibt.
  await page.getByRole('button', { name: new RegExp(dismissed.payload.name) }).click();
  await page.getByRole('button', { name: 'Verwerfen' }).click();
  await expect(page.getByText('Keine Review-Items')).toBeVisible();
  expect(calls.find((c) => c.path === `/review-items/${dismissed.id}/resolve`)?.body).toEqual(
    expect.objectContaining({ action: dismissed.action }),
  );
});
