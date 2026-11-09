import { execFileSync } from 'node:child_process';

import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { type BrowserContext, expect, type Page, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

function sql(statement: string): void {
  const database = new URL(process.env.DATABASE_URL ?? '').pathname.slice(1);
  execFileSync('docker', [
    'compose', '-f', '../../infra/local/compose.yaml', 'exec', '-T', 'postgres',
    'psql', '-U', 'twofeet', '-d', database, '-At', '-c', statement,
  ]);
}

// This test has to book real times today, so what it leaves behind would collide with other runs.
function removeOurSpaces(): void {
  sql(`DELETE FROM space WHERE title LIKE 'Near me %'`);
}

// A datetime-local value in the event's timezone, whatever the machine's own is.
function denverIn(minutes: number): string {
  const moment = new Date(Date.now() + minutes * 60_000);
  return moment.toLocaleString('sv-SE', { timeZone: 'America/Denver' }).replace(' ', 'T').slice(0, 16);
}

async function liveConnected(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    const body = document.body as HTMLElement & { 'htmx-internal-data'?: { sseEventSource?: EventSource } };
    return body['htmx-internal-data']?.sseEventSource?.readyState === EventSource.OPEN;
  });
}

test.describe('S8 near-me feed', () => {
  test.describe.configure({ mode: 'serial', timeout: 120_000 });

  const email = testEmail('s8-ana');
  let context: BrowserContext;
  let home: Page;

  test.beforeAll(async ({ browser }) => {
    removeOurSpaces(); // a run that was killed can't clean up after itself
    context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    home = await context.newPage();
    await setupClerkTestingToken({ page: home });
    await home.goto('/sign-in?return=%2Fe%2Fcnh');
    await enterEmailAndCode(home, email);
    await expect(home).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
    await home.getByLabel('Your name').fill('Ana');
    await home.getByRole('button', { name: 'Continue' }).click();
    await expect(home).toHaveURL(/\/e\/cnh$/);
  });

  test.afterAll(async () => {
    removeOurSpaces();
    await context?.close();
    await deleteClerkUser(email);
  });

  test('offers the scan before a check-in, and a new space reaches an open feed within 2 seconds', async () => {
    await expect(home.getByText("Scan a table code to see what's close")).toBeVisible();
    await liveConnected(home);

    const other = await context.newPage();
    await setupClerkTestingToken({ page: other });
    await other.goto('/e/cnh/spaces/new');
    await other.getByLabel("What's it about?").fill('Near me first');
    await other.getByLabel('Where').selectOption({ label: 'North lounge · Lounge · seats 6' });
    await other.getByLabel('Starts').fill(denverIn(12));
    await other.getByText('30 min').click();
    await other.getByRole('button', { name: 'Start this space' }).click();
    await expect(other.getByRole('heading', { name: 'Near me first' })).toBeVisible();

    const card = home.locator('article.match', { hasText: 'Near me first' });
    await expect(card).toBeVisible({ timeout: 2_000 });
    await expect(card).toContainText('North lounge');
    await expect(card).toContainText(/starts in 1[12] min/);
    await expect(card).toContainText('1 of 6 going');
    await other.close();
  });

  test('checking in adds the walk, and the feed fits a phone', async () => {
    await home.getByRole('button', { name: /Kitchen/ }).click();
    await expect(home).toHaveURL(/\/e\/cnh$/);
    await expect(home.getByText("Scan a table code to see what's close")).toHaveCount(0);

    const card = home.locator('article.match', { hasText: 'Near me first' });
    await expect(card).toContainText('1 min walk');
    await expect(card).toContainText("You're hosting");

    await home.waitForLoadState('load'); // sizes mean nothing until the stylesheet has applied
    expect(await home.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    const title = await card.getByRole('link', { name: 'Near me first' }).boundingBox();
    expect(title?.height).toBeGreaterThanOrEqual(44);
  });
});
