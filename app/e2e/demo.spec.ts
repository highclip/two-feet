import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { type BrowserContext, expect, type Page, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from './helpers/signin';

// spec.demo_path, one test per step. Run with: npx playwright test -c demo.config.ts
// A step waiting on a slice is test.fixme with the slice id; remove the fixme when it merges.
test.describe.serial('demo path', () => {
  const email = testEmail('demo-path');
  const name = 'Demo path test';
  const problem = 'our OTel collector drops spans under load on k8s';

  let context: BrowserContext;
  let page: Page;
  let postUrl: string | undefined;

  test.beforeAll(async ({ browser }) => {
    context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    page = await context.newPage();
  });

  // The app is live for a real room, so the post is taken off the feed and the account removed.
  test.afterAll(async () => {
    if (postUrl) {
      await page.goto(postUrl);
      await page.getByRole('button', { name: 'This is sorted' }).click();
      await expect(page.getByText('Sorted. This post no longer shows up')).toBeVisible();
    }
    await context.close();
    await deleteClerkUser(email);
  });

  test.fixme('step 1: scan the Kitchen counter QR on a phone (S2: spot pages)', async () => {
    await page.goto('/e/cnh/s/kitchen-counter');
    await expect(page.getByRole('heading', { name: /Kitchen counter/ })).toBeVisible();
  });

  test('step 1: sign in with an email code', async () => {
    await setupClerkTestingToken({ page }); // before Clerk's first request, which starts on page load
    await page.goto('/sign-in?return=%2Fe%2Fcnh');
    await enterEmailAndCode(page, email);
    await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
    await page.getByLabel('Your name').fill(name);
    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page.getByRole('heading', { name: `Hi, ${name}` })).toBeVisible();
  });

  test('step 2: post the problem', async () => {
    await page.locator('.kind', { hasText: 'Stuck on' }).click();
    await page.getByLabel('Your post, in one line').fill(problem);
    await page.getByRole('button', { name: 'Post it' }).click();
    await expect(page).toHaveURL(/\/e\/cnh\/posts\/[0-9a-f-]{36}$/);
    postUrl = page.url();
    await expect(page.locator('.post-text')).toHaveText(problem);
  });

  test.fixme('step 3: see a space, a person and a sponsor with reasons (S6: matching)', async () => {
    await expect(page.getByText('Collector tuning')).toBeVisible();
    await expect(page.getByText(/1 min walk/)).toBeVisible();
    await expect(page.getByText(/starts in 6 min/)).toBeVisible();
    await expect(page.getByText('5 of 8 going')).toBeVisible();
    await expect(page.getByText(/collector pipelines/)).toBeVisible();
    await expect(page.getByText('Dash0')).toBeVisible();
  });

  test.fixme('step 4: tap Join and the Wall shows 6 of 8 within 2 seconds (S6: matching, S9: the Wall)', async ({ browser }) => {
    const projector = await browser.newContext({ viewport: { width: 1920, height: 1080 } });
    const wall = await projector.newPage();
    await wall.goto('/e/cnh/wall');
    await expect(wall.getByText('5 of 8')).toBeVisible();

    await page.getByRole('link', { name: /Collector tuning/ }).click();
    await page.getByRole('button', { name: /^Join/ }).click();
    await expect(wall.getByText('6 of 8')).toBeVisible({ timeout: 2_000 });
    await projector.close();
  });
});
