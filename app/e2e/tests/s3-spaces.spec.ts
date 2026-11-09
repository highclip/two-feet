import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { expect, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

// A date far enough out, and random enough, that runs against a kept database never collide.
function futureSlot(): string {
  const days = 30 + Math.floor(Math.random() * 3000);
  const date = new Date(Date.now() + days * 86_400_000);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T14:00`;
}

test.describe('S3 spaces', () => {
  const email = testEmail('s3-host');

  test.afterAll(async () => {
    await deleteClerkUser(email);
  });

  test('a host creates a space, is refused an overlap, then cancels and undoes it', async ({ page }) => {
    await setupClerkTestingToken({ page }); // before Clerk's first request, which starts on page load
    await page.goto('/e/cnh/spaces/new');
    await expect(page).toHaveURL(/\/sign-in/);
    await enterEmailAndCode(page, email);

    await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
    await page.getByLabel('Your name').fill('Jun');
    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page).toHaveURL(/\/e\/cnh\/spaces\/new/);

    const slot = futureSlot();
    const fillForm = async () => {
      await page.getByLabel("What's it about?").fill('Collector tuning');
      await page.getByLabel('Where').selectOption({ label: 'Long table · Main room · seats 10' });
      await page.getByLabel('Starts').fill(slot);
      await page.getByText('30 min').click();
    };

    await fillForm();
    await page.getByLabel('How many people').fill('8');
    await page.getByRole('button', { name: 'Start this space' }).click();

    await expect(page.getByRole('heading', { name: 'Collector tuning' })).toBeVisible();
    await expect(page.getByText('Jun, hosting')).toBeVisible();
    await expect(page.getByText('1 of 8 going')).toBeVisible();
    const spacePage = page.url();

    await page.waitForLoadState('load'); // sizes mean nothing until the stylesheet has applied
    const box = await page.getByRole('link', { name: 'Edit' }).boundingBox();
    expect(box?.height).toBeGreaterThanOrEqual(44);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);

    const dotRows = await page.locator('.seats .seat').evaluateAll(
      (dots) => new Set(dots.map((dot) => dot.getBoundingClientRect().top)).size,
    );
    expect(dotRows, 'eight seats stay on one line').toBe(1);

    // The notice only reaches other people's pages, so measure its link against the real stylesheet.
    const noticeHeight = await page.evaluate(() => {
      const link = document.createElement('a');
      link.className = 'toast-text';
      link.textContent = 'Cancelled by host: Collector tuning';
      document.body.append(link);
      const height = link.getBoundingClientRect().height;
      link.remove();
      return height;
    });
    expect(noticeHeight).toBeGreaterThanOrEqual(44);

    await page.goto('/e/cnh/spaces/new');
    await fillForm();
    await page.getByRole('button', { name: 'Start this space' }).click();
    await expect(page.getByRole('alert')).toHaveText('Long table is taken until 2:30. Try Couches.');

    await page.goto(spacePage);
    await page.getByRole('button', { name: 'Cancel space' }).click();
    await expect(page.getByText('Cancelled by host')).toBeVisible();
    await page.getByRole('button', { name: 'Undo' }).click();
    await expect(page.getByText('1 of 8 going')).toBeVisible();
    await expect(page.getByText('Cancelled by host')).toHaveCount(0);
  });
});
