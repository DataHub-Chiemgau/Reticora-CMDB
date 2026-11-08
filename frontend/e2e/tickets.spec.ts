import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';
import {
  BREACHED_TICKET,
  BUSINESS_CALENDAR,
  RUNNING_TICKET,
  SLA_HANDLERS,
  mePermissions,
  ticketDetailHandlers,
  ticketSLA,
} from './support/phase2';

// TST-03 [P2], UI-14 (WP-324): the ticket flows of WP-814 and WP-815. A
// review item of the inbox opens a ticket (POST /review-items/{id}/ticket)
// and stays open, linked to its ticket. The SLA of a ticket counts down its
// remaining business minutes (GET /tickets/{id}/sla, read every minute) and
// shows the breach of a deadline, on the detail page and in the list of
// breaches of the SLA page (GET /slas/breaches) beside the business
// calendar of the policy; the ticket list leads to the detail. The data come
// from support/phase2.ts; the strict mock checks every request and answer
// against api/openapi.yaml.

test.use({ locale: 'de-DE' });

const REVIEW_ID = '7d000000-0000-4000-8000-0000000000b1';

test('opens a ticket from a review item of the inbox and follows its link', async ({ page }) => {
  const item: Record<string, unknown> = {
    id: REVIEW_ID,
    organization_id: ORG_ID,
    kind: 'serial_mismatch',
    status: 'open',
    payload: { name: 'srv-07', serial_number: 'SN-NEW' },
    escalated: false,
    created_at: AT,
    updated_at: AT,
  };
  const created = { ...RUNNING_TICKET, title: 'Review: Seriennummer weicht ab (srv-07)' };
  const calls = await mockBackend(page, [
    mePermissions(['ci:read', 'review:resolve', 'ticket:read', 'ticket:write']),
    { path: '/review-items', reply: () => ({ body: list([item]) }) },
    {
      method: 'POST',
      path: `/review-items/${REVIEW_ID}/ticket`,
      reply: () => {
        item.tickets = [
          { id: created.id, ticket_number: created.ticket_number, status: created.status },
        ];
        return { status: 201, body: created };
      },
    },
    { path: `/tickets/${created.id}`, reply: () => ({ body: created }) },
    {
      path: `/tickets/${created.id}/sla`,
      reply: () => ({ body: ticketSLA(created.id, 115, 470) }),
    },
    ...ticketDetailHandlers(created.id),
  ]);
  await signIn(page);
  await page.goto('/reviews');

  await page.getByRole('button', { name: /srv-07/ }).click();
  await page.getByRole('button', { name: 'Ticket erstellen' }).click();
  const notice = page.getByRole('status').filter({ hasText: 'Ticket #57 erstellt.' });
  await expect(notice).toBeVisible();
  // Opening a ticket resolves nothing: the item stays open with its ticket.
  await expect(page.getByRole('link', { name: '#57' }).first()).toBeVisible();
  expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/resolve'))).toBe(false);
  expect(
    calls.filter((c) => c.method === 'POST' && c.path === `/review-items/${REVIEW_ID}/ticket`),
  ).toHaveLength(1);

  await notice.getByRole('link', { name: 'Ticket #57 erstellt.' }).click();
  await expect(page).toHaveURL(new RegExp(`/tickets/${created.id}$`));
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(`#57${created.title}`);
  await expect(page.getByText('noch 1 h 55 min')).toBeVisible();
});

test('counts the SLA down and shows its breach in the detail and the list of breaches', async ({
  page,
}) => {
  // The server counts in business minutes: once the test lets a minute
  // pass, the running ticket has a minute less left.
  let minutes = 0;
  await page.clock.install();
  await mockBackend(page, [
    mePermissions(['ci:read', 'ticket:read', 'ticket:write', 'sla:read']),
    { path: '/tickets', reply: () => ({ body: list([RUNNING_TICKET, BREACHED_TICKET]) }) },
    { path: `/tickets/${RUNNING_TICKET.id}`, reply: () => ({ body: RUNNING_TICKET }) },
    {
      path: `/tickets/${RUNNING_TICKET.id}/sla`,
      reply: () => ({ body: ticketSLA(RUNNING_TICKET.id, 90 - minutes, 390 - minutes) }),
    },
    { path: `/tickets/${BREACHED_TICKET.id}`, reply: () => ({ body: BREACHED_TICKET }) },
    {
      path: `/tickets/${BREACHED_TICKET.id}/sla`,
      reply: () => ({
        body: ticketSLA(BREACHED_TICKET.id, null, -15, { first_response_at: AT }),
      }),
    },
    ...ticketDetailHandlers(RUNNING_TICKET.id),
    ...ticketDetailHandlers(BREACHED_TICKET.id),
    ...SLA_HANDLERS,
  ]);
  await signIn(page);
  await page.goto('/tickets');

  // The list leads to the detail of the running ticket: its countdown.
  await page.getByRole('link', { name: RUNNING_TICKET.title }).click();
  const sla = page.locator('#main-content dl').filter({ hasText: 'Reaktion' });
  const response = sla.locator('div').filter({ hasText: /^Reaktion/ });
  const resolution = sla.locator('div').filter({ hasText: /^Lösung/ });
  await expect(response).toContainText('noch 1 h 30 min');
  await expect(resolution).toContainText('noch 6 h 30 min');
  await expect(sla.getByText('verletzt')).toHaveCount(0);
  // A minute later the page reads the SLA again and counts down.
  minutes = 1;
  await page.clock.fastForward('01:05');
  await expect(response).toContainText('noch 1 h 29 min');
  await expect(resolution).toContainText('noch 6 h 29 min');

  // The breached ticket: the response is met, the resolution overdue.
  await page.goto('/tickets');
  await page.getByRole('link', { name: BREACHED_TICKET.title }).click();
  const breached = page.locator('#main-content dl').filter({ hasText: 'Reaktion' });
  await expect(breached.locator('div').filter({ hasText: /^Reaktion/ })).toContainText('erfüllt');
  const overdue = breached.locator('div').filter({ hasText: /^Lösung/ });
  await expect(overdue).toContainText('verletzt');
  await expect(overdue).toContainText('überfällig seit 0 h 15 min');

  // The list of breaches of the SLA page names the ticket, beside the
  // business calendar of the policy.
  await page.goto('/slas');
  const breaches = page.getByRole('region', { name: 'Verletzungen' });
  const row = breaches.locator('tbody tr').filter({ hasText: BREACHED_TICKET.id });
  await expect(row).toContainText('Verletzt');
  await expect(page.getByText(BUSINESS_CALENDAR.name).first()).toBeVisible();
});
