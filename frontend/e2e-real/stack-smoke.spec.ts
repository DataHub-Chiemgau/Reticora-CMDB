import { test, expect } from '@playwright/test';
import { api, signupTenant, uiLogin } from './support/stack';

// Audit precondition: the helpers reach the real stack – a fresh tenant is
// created through the self-signup UI and its administrator signs in.
test('a fresh tenant signs up, signs in and sees its own empty organization', async ({
  page,
  request,
}) => {
  const account = await signupTenant(page, request, 'smoke');
  const token = await uiLogin(page, account);
  const org = await api<{ name: string }>(request, token, 'GET', '/org');
  expect(org.status).toBe(200);
  expect(org.body.name).toBe(account.org);
  const cis = await api<{ total: number }>(request, token, 'GET', '/cis?limit=1');
  expect(cis.status).toBe(200);
  expect(cis.body.total).toBe(0);
});
