import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { expect, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

test.describe('S14 sign out', () => {
  const email = testEmail('s14-leaver');

  test.afterAll(async () => {
    await deleteClerkUser(email);
  });

  test('signing out from home lands on the public page and ends the session', async ({ page }) => {
    await setupClerkTestingToken({ page });
    await page.goto('/e/cnh');
    await page.getByRole('link', { name: 'Sign in with your email' }).click();
    await enterEmailAndCode(page, email);
    await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
    await page.getByLabel('Your name').fill('Sam');
    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page.getByRole('heading', { name: 'Hi, Sam' })).toBeVisible();

    await page.waitForFunction(() => (window as unknown as { Clerk?: { loaded: boolean } }).Clerk?.loaded);
    await page.getByRole('button', { name: 'Sign out' }).click();

    await expect(page.getByRole('link', { name: 'Sign in with your email' })).toBeVisible({ timeout: 30_000 });
    await expect(page).toHaveURL(/\/e\/cnh$/);

    await page.goto('/e/cnh/feed');
    await expect(page).toHaveURL(/\/sign-in/);
  });
});
