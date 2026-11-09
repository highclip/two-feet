import { expect, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

test.describe('S1 sign-in', () => {
  const email = testEmail('s1-newcomer');

  test.afterAll(async () => {
    await deleteClerkUser(email);
  });

  test('a visitor signs in with an emailed code, lands on onboarding, then home', async ({ page }) => {
    await page.goto('/e/cnh');
    await page.getByRole('link', { name: 'Sign in with your email' }).click();
    await expect(page).toHaveURL(/\/sign-in/);

    await enterEmailAndCode(page, email);

    await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
    await page.getByLabel('Your name').fill('Priya');
    await page.getByLabel(/Up to 3 tags/).fill('otel, k8s');
    await page.getByRole('button', { name: 'Continue' }).click();

    await expect(page).toHaveURL(/\/e\/cnh$/);
    await expect(page.getByRole('heading', { name: 'Hi, Priya' })).toBeVisible();

    const toggle = page.getByRole('switch', { name: "Don't show me in matches" });
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    await toggle.click();
    await expect(page.getByRole('switch', { name: "Don't show me in matches" })).toHaveAttribute('aria-checked', 'true');

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth).toBeLessThanOrEqual(390);
  });

  test('a page behind sign-in sends a visitor to sign-in and keeps the return path', async ({ page }) => {
    await page.goto('/e/cnh/onboarding');
    await expect(page).toHaveURL(/\/sign-in\?return=%2Fe%2Fcnh%2Fonboarding/);
  });
});
