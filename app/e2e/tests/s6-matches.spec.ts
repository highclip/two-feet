import { execFileSync } from 'node:child_process';

import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { expect, test, type Page } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

// Signed-in screens can't be shot with scripts/shoot.sh, so the tests save them for design review.
const shots = '../../.harness/shots/S6';

async function signInAndOnboard(page: Page, email: string, name: string): Promise<void> {
  await setupClerkTestingToken({ page });
  await page.goto('/sign-in?return=%2Fe%2Fcnh');
  await enterEmailAndCode(page, email);
  await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
  await page.getByLabel('Your name').fill(name);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { name: `Hi, ${name}` })).toBeVisible();
}

test.describe('S6 matching with reasons', () => {
  const email = testEmail('s6-asker');
  const searcher = testEmail('s6-searcher');

  test.beforeAll(() => {
    // The real seed subcommand, against the server this run started (D8, D21).
    execFileSync('./.bin/twofeet', ['seed-demo'], { stdio: 'inherit' });
  });

  test.afterAll(async () => {
    // The sample spaces are committed and hold their spots for the next hour, which would refuse
    // spaces made by the Go tests that share this database.
    execFileSync('./.bin/twofeet', ['clear-demo'], { stdio: 'inherit' });
    await deleteClerkUser(email);
    await deleteClerkUser(searcher);
  });

  test('a post gets a space, a person and a sponsor, each with reasons', async ({ page }) => {
    await signInAndOnboard(page, email, 'Sam');

    await page.getByLabel('Your post, in one line').fill('our OTel collector drops spans under load on k8s');
    await page.getByRole('button', { name: 'Post it' }).click();
    await expect(page).toHaveURL(/\/e\/cnh\/posts\/[0-9a-f-]{36}$/);

    const space = page.locator('article.match-space').first();
    await expect(space).toContainText('Collector tuning', { timeout: 30_000 });
    await expect(page.locator('article.match-person').first()).toContainText('Priya (sample)');
    await expect(page.locator('article.match-sponsor').first()).toContainText('Dash0');
    await expect(space).toContainText(/starts in \d+ min/);
    await expect(space).toContainText('of 8 going');

    for (const card of await page.locator('article.deal').all()) {
      const reasons = await card.locator('.reason').count();
      expect(reasons).toBeGreaterThanOrEqual(1);
      expect(reasons).toBeLessThanOrEqual(3);
    }

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth).toBeLessThanOrEqual(390);
    for (const target of await page.locator('.matches a, .matches button, .matches summary').all()) {
      if (!(await target.isVisible())) continue;
      const box = await target.boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
    }
    await page.screenshot({ path: `${shots}/matches.png`, fullPage: true });
  });

  test('the home screen searches the room, grouped by kind', async ({ page }) => {
    await signInAndOnboard(page, searcher, 'Noor');

    await page.getByRole('searchbox').fill('collector pipelines');
    await page.getByRole('button', { name: 'Search', exact: true }).click();

    await expect(page).toHaveURL(/\/e\/cnh\/search\?q=/);
    await expect(page.getByRole('heading', { name: 'People' })).toBeVisible({ timeout: 30_000 });
    await expect(page.locator('article.match-person').first()).toContainText('Priya (sample)');
    await page.screenshot({ path: `${shots}/search.png`, fullPage: true });
  });
});
