import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { expect, test } from '@playwright/test';

import { createClerkUser, deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

test.describe('S7 sponsors', () => {
  const email = testEmail('s7-rep');

  test.beforeAll(async () => {
    await createClerkUser(email, { role: 'sponsor_rep', sponsor: 'Dash0' });
  });

  test.afterAll(async () => {
    await deleteClerkUser(email);
  });

  test('a sponsor rep edits their solves text from the home screen', async ({ page }, testInfo) => {
    await setupClerkTestingToken({ page }); // before Clerk's first request, which starts on page load
    await page.goto('/sign-in?return=/e/cnh');
    await enterEmailAndCode(page, email);
    await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
    await page.getByLabel('Your name').fill('Rae');
    await page.getByRole('button', { name: 'Continue' }).click();

    await page.getByRole('link', { name: 'Edit what your sponsor solves' }).click();
    await expect(page.getByRole('heading', { name: 'Dash0' })).toBeVisible();

    const text = `Tail sampling that holds up under load ${Date.now()}`;
    const solves = page.getByLabel('What you solve');
    await solves.fill(text);
    await page.getByRole('button', { name: 'Save' }).click();

    await expect(page.getByRole('status')).toContainText('Saved');
    await expect(page.getByLabel('What you solve')).toHaveValue(text);

    // Sizes come from tokens.css, which waits on its font @import; measure once it has applied.
    await page.waitForLoadState('load');
    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth).toBeLessThanOrEqual(390);
    for (const target of [page.getByRole('button', { name: 'Save' }), page.getByRole('link', { name: 'Back to home' })]) {
      const box = await target.boundingBox();
      expect(box?.height).toBeGreaterThanOrEqual(44);
    }
    await page.screenshot({ path: testInfo.outputPath('sponsor-edit-390.png'), fullPage: true });
  });
});
