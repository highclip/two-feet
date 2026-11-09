import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { expect, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

const clerkScript = /clerk\.browser\.js/;

test.describe('S1 sign-in page while Clerk loads', () => {
  test('the area under the lede shows a card outline until Clerk mounts', async ({ page }) => {
    await page.route(clerkScript, () => {}); // held open, as on slow venue wifi
    await page.goto('/sign-in', { waitUntil: 'domcontentloaded' });

    const outline = page.locator('.clerk-mount .card');
    await expect(outline).toBeVisible();
    const box = await outline.boundingBox();
    expect(box?.height).toBeGreaterThan(100);
    expect(await outline.evaluate((el) => getComputedStyle(el).borderTopWidth)).toBe('1px');
  });

  test('a Clerk script that fails says so and offers a reload that recovers', async ({ page }) => {
    await page.route(clerkScript, (route) => route.abort());
    await page.goto('/sign-in', { waitUntil: 'domcontentloaded' });

    await expect(page.getByText("Sign-in didn't load. Reload to try again.")).toBeVisible({ timeout: 15_000 });
    const reload = page.getByRole('button', { name: 'Reload' });
    await expect(reload).toBeVisible();

    await page.unroute(clerkScript);
    await reload.click();
    await expect(page.locator('input[name="identifier"]')).toBeVisible({ timeout: 30_000 });
    await expect(page.getByText("Sign-in didn't load")).toHaveCount(0);
  });

  test('a Clerk script that never finishes still ends in the reload message', async ({ page }) => {
    await page.route(clerkScript, () => {});
    await page.goto('/sign-in', { waitUntil: 'domcontentloaded' });

    await expect(page.getByText("Sign-in didn't load. Reload to try again.")).toBeVisible({ timeout: 15_000 });
  });

  test('the mounted widget uses the design tokens and has no card shadow', async ({ page }) => {
    await page.goto('/sign-in');
    const input = page.locator('input[name="identifier"]');
    await expect(input).toBeVisible({ timeout: 30_000 });

    await expect(page.locator('.clerk-placeholder')).toHaveCount(0);
    expect(await input.evaluate((el) => getComputedStyle(el).fontFamily)).toContain('Atkinson Hyperlegible');
    for (const part of ['.cl-cardBox', '.cl-card']) {
      expect(await page.locator(part).first().evaluate((el) => getComputedStyle(el).boxShadow), part).toBe('none');
    }
    const button = page.locator('.cl-formButtonPrimary');
    expect(await button.evaluate((el) => getComputedStyle(el).backgroundImage)).toBe('none');
    expect(await button.evaluate((el) => getComputedStyle(el, '::after').display)).toBe('none');
    const color = await page.locator('.cl-headerTitle, .cl-formFieldLabel').first().evaluate((el) => getComputedStyle(el).color);
    expect(color).toBe('rgb(22, 25, 28)'); // --ink
  });
});

test.describe('S1 sign-in', () => {
  const email = testEmail('s1-newcomer');

  test.afterAll(async () => {
    await deleteClerkUser(email);
  });

  test('a visitor signs in with an emailed code, lands on onboarding, then home', async ({ page }) => {
    await setupClerkTestingToken({ page }); // before Clerk's first request, which starts on page load
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
