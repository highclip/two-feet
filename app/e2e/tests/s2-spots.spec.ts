import { execFileSync } from 'node:child_process';

import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { type Browser, type BrowserContext, expect, type Page, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

// A printed code's slug exists only in the database until S11's page prints it, so read it from the
// local Postgres the way scripts/shoot.sh reaches it.
function qrSlug(spot: string): string {
  const database = new URL(process.env.DATABASE_URL ?? '').pathname.slice(1);
  return execFileSync('docker', [
    'compose', '-f', '../../infra/local/compose.yaml', 'exec', '-T', 'postgres',
    'psql', '-U', 'twofeet', '-d', database, '-At',
    '-c', `SELECT qr_slug FROM spot WHERE name = '${spot}'`,
  ]).toString().trim();
}

// A datetime-local value in the event's timezone, whatever the machine's own is.
function denverMinutesAgo(minutes: number): string {
  const moment = new Date(Date.now() - minutes * 60_000);
  return moment.toLocaleString('sv-SE', { timeZone: 'America/Denver' }).replace(' ', 'T').slice(0, 16);
}

async function liveConnected(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    const body = document.body as HTMLElement & { 'htmx-internal-data'?: { sseEventSource?: EventSource } };
    return body['htmx-internal-data']?.sseEventSource?.readyState === EventSource.OPEN;
  });
}

async function signInFrom(browser: Browser, path: string, email: string, name: string): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  await setupClerkTestingToken({ page });
  await page.goto(path);
  await page.getByRole('link', { name: 'Sign in to check in' }).click();
  await expect(page).toHaveURL(/\/sign-in/);
  await enterEmailAndCode(page, email);
  await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
  await page.getByLabel('Your name').fill(name);
  await page.getByRole('button', { name: 'Continue' }).click();
  return { context, page };
}

test.describe('S2 spot pages', () => {
  test.describe.configure({ timeout: 180_000 });

  const emails = { ana: testEmail('s2-ana'), ben: testEmail('s2-ben') };

  test.afterAll(async () => {
    await Promise.all(Object.values(emails).map(deleteClerkUser));
  });

  test('scan, sign in, check in, start a space here, and watch it arrive and fill on another phone', async ({ browser }, testInfo) => {
    const spotPath = `/e/cnh?spot=${qrSlug('Couches')}`;

    // A visitor sees the spot with no names and no zone counts.
    const visitorContext = await browser.newContext({ viewport: { width: 390, height: 844 } });
    const visitor = await visitorContext.newPage();
    await visitor.goto(spotPath);
    await expect(visitor.getByRole('heading', { level: 1 })).toContainText('Couches');
    await expect(visitor.getByText('The couches around the low table')).toBeVisible();
    await expect(visitor.getByText("You're at")).toHaveCount(0);
    await expect(visitor.getByText(/people in the/)).toHaveCount(0);
    await liveConnected(visitor);

    // Signing in comes back to the same spot and checks you in.
    const ana = await signInFrom(browser, spotPath, emails.ana, 'Ana');
    await expect(ana.page).toHaveURL(new RegExp(`spot=${qrSlug('Couches')}`));
    await expect(ana.page.getByRole('heading', { level: 1 })).toContainText("You're at");
    await expect(ana.page.getByRole('heading', { level: 1 })).toContainText('Couches, Lounge');
    await expect(ana.page.getByText(/people in the Lounge/)).toBeVisible();

    // Start a space here opens the form with this spot and a free slot filled in.
    await ana.page.getByRole('link', { name: /Start a space here/ }).click();
    await expect(ana.page).toHaveURL(/\/spaces\/new/);
    const where = ana.page.getByLabel('Where');
    await expect(where.locator('option:checked')).toContainText('Couches');
    const starts = await ana.page.getByLabel('Starts').inputValue();
    expect(Number(starts.slice(-2)) % 15).toBe(0);
    const title = `Spot check ${Date.now()}`;
    await ana.page.getByLabel("What's it about?").fill(title);
    await ana.page.getByLabel('How many people').fill('4');
    await ana.page.getByRole('button', { name: 'Start this space' }).click();
    await expect(ana.page.getByRole('heading', { name: title })).toBeVisible();

    // The visitor's open page gets the new space without a reload, and it never names the host.
    await expect(visitor.getByText(title)).toBeVisible({ timeout: 2_000 });
    const row = visitor.locator('.next-row, .spot-now').filter({ hasText: title });
    await expect(row.getByText('1 of 4 going')).toBeVisible();
    await expect(row.getByText('Ana')).toHaveCount(0);
    const join = await row.getByRole('link', { name: 'Join' }).boundingBox();
    expect(join?.height).toBeGreaterThanOrEqual(44);
    expect(join?.width).toBeGreaterThanOrEqual(44);
    expect(await visitor.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await liveConnected(visitor);

    // Someone joins from their own phone, and the count moves on the open page.
    const ben = await signInFrom(browser, spotPath, emails.ben, 'Ben');
    await expect(ben.page.getByRole('heading', { level: 1 })).toContainText('Couches, Lounge');
    await ben.page.screenshot({ path: testInfo.outputPath('next-here.png'), fullPage: true });

    // A space under way is the featured card, and joining it stays on the spot page.
    const windowPath = `/e/cnh?spot=${qrSlug('Window table')}`;
    const underWay = `Under way ${Date.now()}`;
    await ana.page.goto('/e/cnh/spaces/new');
    await ana.page.getByLabel("What's it about?").fill(underWay);
    await ana.page.getByLabel('A line from you').fill('Bring your worst collector config.');
    await ana.page.getByLabel('Where').selectOption({ label: 'Window table · Main room · seats 6' });
    await ana.page.getByLabel('Starts').fill(denverMinutesAgo(1));
    await ana.page.getByText('15 min').click();
    await ana.page.getByLabel('How many people').fill('5');
    await ana.page.getByRole('button', { name: 'Start this space' }).click();
    await expect(ana.page.getByRole('heading', { name: underWay })).toBeVisible();
    const underWayUrl = ana.page.url();
    try {
      await ben.page.goto(windowPath);
      await liveConnected(ben.page);
      const card = ben.page.locator('.spot-now');
      await expect(card.getByRole('heading', { name: underWay })).toBeVisible();
      const status = card.getByText(/Happening now · until/).locator('visible=true');
      await expect(status).toHaveCount(1);
      await expect(card.getByText('Ana, hosting')).toBeVisible();
      await card.getByRole('button', { name: 'Join · 4 seats left' }).click();
      await expect(card.getByText("You're in!")).toBeVisible();
      await expect(card.getByText('2 of 5 going')).toBeVisible();
      await expect(status).toHaveCount(1); // the swap doesn't bring back the hidden copy
      await ben.page.screenshot({ path: testInfo.outputPath('happening-now.png'), fullPage: true });
      expect(ben.page.url()).toContain('spot=');
      await card.getByRole('button', { name: 'Leave', exact: true }).click();
      await expect(card.getByRole('button', { name: /^Join/ })).toBeVisible();
    } finally {
      await ana.page.goto(underWayUrl);
      await ana.page.getByRole('button', { name: 'Cancel space' }).click();
    }

    await ben.page.goto(spotPath);
    const benRow = ben.page.locator('.next-row').filter({ hasText: title });
    await benRow.getByRole('button', { name: 'Join' }).click();
    await expect(ben.page.getByText("You're in!")).toBeVisible();
    await expect(row.getByText('2 of 4 going')).toBeVisible({ timeout: 2_000 });

    // A code from nowhere says so.
    await ben.page.goto('/e/cnh?spot=not-a-real-code');
    await expect(ben.page.getByText("That code isn't from this event")).toBeVisible();

    // The home screen says where you are and lets you move.
    await ben.page.goto('/e/cnh');
    await expect(ben.page.getByText('You\'re in the Lounge')).toBeVisible();
    await ben.page.getByText('Moved?').click();
    await ben.page.getByRole('button', { name: /Patio/ }).click();
    await expect(ben.page.getByText('You\'re in the Patio')).toBeVisible();

    await visitorContext.close();
    await ana.context.close();
    await ben.context.close();
  });
});
