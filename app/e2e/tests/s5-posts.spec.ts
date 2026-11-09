import { expect, test, type Page } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

// Signed-in screens can't be shot with scripts/shoot.sh, so the tests save them for design review.
const shots = '../../.harness/shots/S5';

async function signInAndOnboard(page: Page, email: string, name: string): Promise<void> {
  await page.goto('/sign-in?return=%2Fe%2Fcnh');
  await enterEmailAndCode(page, email);
  await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
  await page.getByLabel('Your name').fill(name);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { name: `Hi, ${name}` })).toBeVisible();
}

test.describe('S5 posts', () => {
  const email = testEmail('s5-poster');

  test.afterAll(async () => {
    await deleteClerkUser(email);
  });

  test('an attendee posts a note on a phone and it shows as text', async ({ page }) => {
    await signInAndOnboard(page, email, 'Jun');

    await page.screenshot({ path: `${shots}/home-composer.png`, fullPage: true });

    const composer = page.locator('form.note');
    const background = () => composer.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(await background()).toBe('rgb(255, 243, 191)'); // --stuck-tint
    await page.locator('.kind', { hasText: 'Curious' }).click();
    await expect.poll(background).toBe('rgb(221, 240, 255)'); // --curious-tint
    await page.locator('.kind', { hasText: 'Stuck on' }).click();

    for (const target of await page.locator('form.note .kind-label, form.note button').all()) {
      const box = await target.boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
    }

    const text = '<b>our</b> OTel collector drops spans under load on k8s';
    await page.getByLabel('Your post, in one line').fill(text);
    await page.getByRole('button', { name: 'Post it' }).click();

    await expect(page).toHaveURL(/\/e\/cnh\/posts\/[0-9a-f-]{36}$/);
    await expect(page.locator('.post-text')).toHaveText(text);
    await expect(page.locator('.post-text b')).toHaveCount(0);
    await expect(page.locator('.pill')).toHaveText('Stuck on');
    await expect(page.locator('.tag', { hasText: 'otel-collector' })).toBeVisible();
    await expect(page.getByText("No one's on this yet.")).toBeVisible();

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth).toBeLessThanOrEqual(390);
    await page.screenshot({ path: `${shots}/post-page.png`, fullPage: true });

    await page.getByRole('button', { name: 'This is sorted' }).click();
    await expect(page.getByText('Sorted. This post no longer shows up')).toBeVisible();
  });

  test('a page behind sign-in keeps the composer away from visitors', async ({ page }) => {
    await page.goto('/e/cnh/posts/new');
    await expect(page).toHaveURL(/\/sign-in\?return=/);
  });
});
