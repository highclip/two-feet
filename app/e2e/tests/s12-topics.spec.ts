import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { type BrowserContext, expect, type Page, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

// The source is the local fake in helpers/fake-hn.mjs; the binary fetches it at startup.
const GATEWAY = 'Kubernetes gateway API reaches a new milestone';
const SAMPLING = 'Tail sampling in the OTel collector, explained';
const SOURDOUGH = 'A cosy guide to sourdough starters';
const COUNT = /Hacker News/;

test.describe('S12 topics from online', () => {
  const email = testEmail('s12-host');
  let context: BrowserContext;
  let page: Page;

  test.beforeAll(async ({ browser }) => {
    test.setTimeout(120_000);
    context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    page = await context.newPage();
    await setupClerkTestingToken({ page });
    await page.goto('/e/cnh/spaces/new');
    await expect(page).toHaveURL(/\/sign-in/);
    await enterEmailAndCode(page, email);
    await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
    await page.getByLabel('Your name').fill('Topic Host');
    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page).toHaveURL(/\/e\/cnh\/spaces\/new/);
  });

  test.afterAll(async () => {
    await context.close();
    await deleteClerkUser(email);
  });

  test('tapping a suggestion fills in the title, and it stays editable', async () => {
    await page.goto('/e/cnh/spaces/new');
    const title = page.getByLabel("What's it about?");
    await expect(page.locator('.suggestion').nth(1)).toContainText(COUNT); // the first is the KubeCon lead
    await expect(page.getByText(SOURDOUGH)).toHaveCount(0);

    await page.getByRole('button', { name: new RegExp(SAMPLING) }).click();

    await expect(title).toHaveValue(SAMPLING);
    await expect(page).toHaveURL(/\/spaces\/new/); // filled in, not submitted

    // One tap chooses the spot and time too, and they can still be changed.
    const where = page.getByLabel('Where');
    await expect(page.locator('.quick-start')).toBeVisible();
    await expect(where).toBeHidden();
    await page.getByRole('button', { name: 'Change' }).click();
    await expect(where).toBeVisible();
    await expect(page.locator('.quick-start')).toBeHidden();

    await title.fill('Tail sampling, our way');
    await expect(title).toHaveValue('Tail sampling, our way');

    // Every suggestion is big enough for a thumb, and nothing scrolls sideways.
    const fit = await page.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      smallest: Math.min(...Array.from(document.querySelectorAll('.suggestion')).map((el) => el.getBoundingClientRect().height)),
    }));
    expect(fit.scroll).toBeLessThanOrEqual(390);
    expect(fit.smallest).toBeGreaterThanOrEqual(44);
    await page.screenshot({ path: test.info().outputPath('form.png'), fullPage: true });
  });

  test('the Wall shows the strip, with no names, and still fits a 1080p screen', async ({ browser }) => {
    const wallContext = await browser.newContext({ viewport: { width: 1920, height: 1080 } });
    const wall = await wallContext.newPage();
    await wall.goto('/e/cnh/wall');

    const strip = wall.locator('.wall-topics');
    await expect(strip).toContainText(GATEWAY);
    await expect(strip).toContainText(SAMPLING);
    await expect(strip).toContainText(COUNT);
    await expect(strip).not.toContainText(SOURDOUGH);
    await expect(strip).not.toContainText('somebody');
    expect(await strip.locator('.wall-topic').count()).toBeLessThanOrEqual(5);

    const fit = await wall.evaluate(() => ({
      width: document.documentElement.scrollWidth,
      height: document.documentElement.scrollHeight,
      bottom: document.querySelector('.wall-foot')!.getBoundingClientRect().bottom,
    }));
    expect(fit.width).toBeLessThanOrEqual(1920);
    expect(fit.height).toBeLessThanOrEqual(1080);
    expect(fit.bottom).toBeLessThanOrEqual(1080);
    await wall.screenshot({ path: test.info().outputPath('wall.png') });
    await wallContext.close();
  });
});
