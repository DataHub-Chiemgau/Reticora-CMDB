import { test, expect } from '@playwright/test';

test('app loads and shows login or dashboard', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveTitle(/Reticora/);
});
