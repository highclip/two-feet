import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { expect, test, type BrowserContext, type Page } from '@playwright/test';

import { createClerkUser, deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

const cssPxPerCm = 96 / 2.54;

test('a visitor reads the pitch at 390px and can sign in', async ({ page }, testInfo) => {
  await page.goto('/e/cnh');

  await expect(page.getByText('Open spaces used to happen once a day')).toBeVisible();
  const signIn = page.getByRole('link', { name: 'Sign in with your email' });
  await expect(signIn).toBeVisible();

  await page.waitForLoadState('load');
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(390);
  for (const target of [signIn, page.getByRole('link', { name: 'See the Wall' })]) {
    const box = await target.boundingBox();
    expect(box?.height).toBeGreaterThanOrEqual(44);
  }
  await page.screenshot({ path: testInfo.outputPath('landing-390.png'), fullPage: true });
});

test.describe('S11 codes page', () => {
  const email = testEmail('s11-organizer');
  let context: BrowserContext;
  let page: Page;

  test.beforeAll(async ({ browser }) => {
    await createClerkUser(email, { role: 'organizer' });
    context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    page = await context.newPage();
    await setupClerkTestingToken({ page });
    await page.goto('/sign-in?return=/e/cnh/codes');
    await enterEmailAndCode(page, email);
    await expect(page).toHaveURL(/\/e\/cnh\/codes/, { timeout: 30_000 });
  });

  test.afterAll(async () => {
    await context?.close();
    await deleteClerkUser(email);
  });

  test('an organizer previews the signs at 390px with nothing scrolling sideways', async ({}, testInfo) => {
    await expect(page.getByRole('heading', { name: 'QR codes' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Print' })).toBeVisible();

    await page.waitForLoadState('load');
    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth).toBeLessThanOrEqual(390);
    const box = await page.getByRole('button', { name: 'Print' }).boundingBox();
    expect(box?.height).toBeGreaterThanOrEqual(44);
    await page.screenshot({ path: testInfo.outputPath('codes-390.png'), fullPage: true });
  });

  test('each spot prints as one letter page with a QR at least 12 cm wide', async ({}, testInfo) => {
    const signs = page.locator('[data-sign]');
    const count = await signs.count();
    expect(count).toBeGreaterThan(0);

    await page.emulateMedia({ media: 'print' });
    try {
      for (let i = 0; i < count; i++) {
        await expect(signs.nth(i)).toContainText("Open space here. Scan to see what's on or start one.");
        const printed = await signs.nth(i).locator('svg').evaluate((svg: SVGSVGElement) => ({
          width: svg.getBoundingClientRect().width,
          viewBox: svg.viewBox.baseVal.width,
        }));
        // The viewBox has a 4-module quiet zone on each side, which isn't part of the code's width.
        const modules = printed.viewBox - 8;
        expect((printed.width * modules) / printed.viewBox).toBeGreaterThanOrEqual(12 * cssPxPerCm);
      }
      await expect(page.getByRole('button', { name: 'Print' })).toBeHidden();

      const pdf = await page.pdf({ preferCSSPageSize: true, path: testInfo.outputPath('codes.pdf') });
      const raw = pdf.toString('latin1');
      const pages = raw.match(/\/Type\s*\/Page\b(?!s)/g) ?? [];
      expect(pages.length, 'one page per spot, then the sticker sheet').toBe(count + 1);
      const sizes = raw.match(/\/MediaBox\s*\[\s*0 0 612(\.\d+)? 79\d(\.\d+)?\s*\]/g) ?? [];
      expect(sizes.length, 'every page is letter size').toBe(count + 1);
    } finally {
      await page.emulateMedia({ media: 'screen' });
    }
  });
});
