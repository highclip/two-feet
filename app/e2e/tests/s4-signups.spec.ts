import { type ChildProcess, spawn } from 'node:child_process';
import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { type Browser, type BrowserContext, expect, type Page, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

// Each run takes its own day far in the future, so a kept database never collides with an old run.
function futureDay(): string {
  const days = 30 + Math.floor(Math.random() * 3000);
  const date = new Date(Date.now() + days * 86_400_000);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

async function signedIn(browser: Browser, email: string, name: string): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  await setupClerkTestingToken({ page });
  await page.goto('/e/cnh/spaces/new');
  await expect(page).toHaveURL(/\/sign-in/);
  await enterEmailAndCode(page, email);
  await expect(page).toHaveURL(/\/e\/cnh\/onboarding/, { timeout: 30_000 });
  await page.getByLabel('Your name').fill(name);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page).toHaveURL(/\/e\/cnh\/spaces\/new/);
  return { context, page };
}

async function createSpace(page: Page, title: string, spot: string, starts: string, capacity: string): Promise<string> {
  await page.goto('/e/cnh/spaces/new');
  await page.getByLabel("What's it about?").fill(title);
  await page.getByLabel('Where').selectOption({ label: spot });
  await page.getByLabel('Starts').fill(starts);
  await page.getByText('30 min').click();
  await page.getByLabel('How many people').fill(capacity);
  await page.getByRole('button', { name: 'Start this space' }).click();
  await expect(page.getByRole('heading', { name: title })).toBeVisible();
  return page.url();
}

// Waits until the page's own SSE connection is open, so a change made next can't be missed.
async function liveConnected(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    const body = document.body as HTMLElement & { 'htmx-internal-data'?: { sseEventSource?: EventSource } };
    return body['htmx-internal-data']?.sseEventSource?.readyState === EventSource.OPEN;
  });
}

test.describe('S4 live signups', () => {
  test.describe.configure({ timeout: 180_000 });

  const emails = {
    host: testEmail('s4-host'),
    ana: testEmail('s4-ana'),
    ben: testEmail('s4-ben'),
  };

  test.afterAll(async () => {
    await Promise.all(Object.values(emails).map(deleteClerkUser));
  });

  test('join, waitlist, promotion, overlap and Undo across browsers, and pages catch up after a gap', async ({ browser }) => {
    const day = futureDay();
    const host = await signedIn(browser, emails.host, 'Host');
    const ana = await signedIn(browser, emails.ana, 'Ana');
    const ben = await signedIn(browser, emails.ben, 'Ben');

    const spaceUrl = await createSpace(host.page, 'Collector tuning', 'Long table · Main room · seats 10', `${day}T14:00`, '2');
    const otherUrl = await createSpace(host.page, 'Tail sampling', 'Corner couches · Lounge · seats 6', `${day}T14:15`, '4');

    // Two browsers on the same space: one joins, the other's count moves without a reload.
    await host.page.goto(spaceUrl);
    await expect(host.page.getByText('1 of 2 going')).toBeVisible();
    await expect(host.page.getByRole('button', { name: 'Leave' })).toHaveCount(0); // hosts cancel instead
    await liveConnected(host.page);
    await ana.page.goto(spaceUrl);
    await ana.page.waitForLoadState('load');
    await expect(ana.page.getByRole('button', { name: 'Join · 1 seat left' })).toBeVisible();
    const joinBox = await ana.page.getByRole('button', { name: 'Join · 1 seat left' }).boundingBox();
    expect(joinBox?.height).toBeGreaterThanOrEqual(44);
    expect(await ana.page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await liveConnected(ana.page);
    await ana.page.getByRole('button', { name: 'Join · 1 seat left' }).click();
    await expect(ana.page.getByText("You're in!")).toBeVisible();
    await expect(host.page.getByText('2 of 2 going')).toBeVisible({ timeout: 2_000 });

    // Full: Ben goes on the waitlist, then gets the seat when Ana leaves.
    await ben.page.goto(spaceUrl);
    await liveConnected(ben.page);
    await ben.page.getByRole('button', { name: 'Join the waitlist' }).click();
    await expect(ben.page.getByText("Full. You're 1st on the waitlist; we'll move you in if a seat opens.")).toBeVisible();

    // The answer arrives late, so the live refetch this leave sets off is the first to come back. It
    // must not cost Ana her toast.
    await ana.page.route('**/leave', async (route) => {
      const response = await route.fetch();
      await new Promise((resolve) => setTimeout(resolve, 500));
      await route.fulfill({ response });
    });
    await ana.page.getByRole('button', { name: 'Leave', exact: true }).click();
    await expect(ana.page.getByRole('status').getByText('your seat went to the next person')).toBeVisible();
    await expect(ana.page.getByRole('button', { name: 'Undo' })).toHaveCount(0); // D2: the seat has gone
    await expect(ben.page.getByRole('status').getByText("You're in!")).toBeVisible({ timeout: 2_000 });
    await expect(ben.page.locator('.note-live').getByText("You're in!")).toBeVisible();

    // Undo puts someone back while nobody has taken the seat.
    await ben.page.getByRole('button', { name: 'Leave', exact: true }).click();
    const toast = ben.page.locator('.toasts .toast');
    await expect(toast.getByText("You've left.")).toBeVisible();
    await toast.getByRole('button', { name: 'Undo' }).click();
    await expect(ben.page.locator('.note-live').getByText("You're in!")).toBeVisible();

    // Overlap: Ben is going, and joining another space at the same time asks, on the page.
    await ben.page.goto(otherUrl);
    await liveConnected(ben.page);
    await ben.page.getByRole('button', { name: /^Join/ }).click();
    await expect(ben.page.getByRole('alert')).toContainText('Collector tuning');
    await ben.page.getByRole('button', { name: 'Leave it and join' }).click();
    await expect(ben.page.locator('.note-live').getByText("You're in!")).toBeVisible();
    await expect(host.page.getByText('1 of 2 going')).toBeVisible({ timeout: 2_000 });

    // A page that loses its connection says so after 5 seconds and catches up when it's back.
    const watcher = await browser.newContext({ viewport: { width: 390, height: 844 } });
    const watching = await watcher.newPage();
    await watching.goto(spaceUrl);
    await expect(watching.getByText('1 of 2 going')).toBeVisible();
    await liveConnected(watching);

    await watcher.setOffline(true);
    await watching.evaluate(() => {
      const body = document.body as HTMLElement & { 'htmx-internal-data': { sseEventSource: EventSource } };
      body['htmx-internal-data'].sseEventSource.close();
      body['htmx-internal-data'].sseEventSource.onerror?.(new Event('error'));
    });
    await expect(watching.getByRole('status').filter({ hasText: 'Reconnecting' })).toBeVisible({ timeout: 8_000 });

    await ana.page.goto(spaceUrl);
    await liveConnected(ana.page);
    await ana.page.getByRole('button', { name: /^Join/ }).click();
    await expect(ana.page.locator('.note-live').getByText("You're in!")).toBeVisible();

    await watcher.setOffline(false);
    await expect(watching.getByText('2 of 2 going')).toBeVisible({ timeout: 10_000 });
    await expect(watching.getByRole('status').filter({ hasText: 'Reconnecting' })).toBeHidden();

    // A tab coming back to the foreground refetches once, within 2 seconds.
    await ana.page.getByRole('button', { name: 'Leave', exact: true }).click();
    await expect(watching.getByText('1 of 2 going')).toBeVisible({ timeout: 2_000 });
    await watching.waitForTimeout(1_100); // refetches within a second of each other are merged
    await watching.evaluate(() => {
      // Hide the live section's real value, then show that a return to the foreground restores it.
      document.querySelector('.note-live .seat-line')!.textContent = 'stale';
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await expect(watching.getByText('1 of 2 going')).toBeVisible({ timeout: 2_000 });

    await watcher.close();
    await host.context.close();
    await ana.context.close();
    await ben.context.close();
  });

  test('a page reconnects and shows current data within 10 seconds of a server restart', async ({ browser }) => {
    const email = testEmail('s4-restart');
    const joinerEmail = testEmail('s4-restart-joiner');
    const host = await signedIn(browser, email, 'Rae');
    try {
      const spaceUrl = await createSpace(host.page, 'Restart check', 'Window bench · Main room · seats 6', `${futureDay()}T09:00`, '4');
      const path = new URL(spaceUrl).pathname;

      // A second copy of the real binary on its own port, so the test can stop and start it.
      const port = String(20_000 + Math.floor(Math.random() * 20_000));
      const start = (): ChildProcess => spawn('./.bin/twofeet', [], { env: { ...process.env, PORT: port }, stdio: 'ignore' });
      const ready = async () => {
        await expect(async () => {
          const res = await fetch(`http://localhost:${port}/readyz`);
          expect(res.status).toBe(200);
        }).toPass({ timeout: 30_000 });
      };
      let server = start();
      try {
        await ready();
        const viewer = await browser.newContext({ viewport: { width: 390, height: 844 } });
        const page = await viewer.newPage();
        await page.goto(`http://localhost:${port}${path}`);
        await expect(page.getByText('1 of 4 going')).toBeVisible();
        await liveConnected(page);

        server.kill('SIGTERM');
        await new Promise((resolve) => server.once('exit', resolve));

        // A change while it's down, made through the main server, which shares the database.
        const other = await signedIn(browser, joinerEmail, 'Joiner');
        await other.page.goto(spaceUrl);
        await liveConnected(other.page);
        await other.page.getByRole('button', { name: /^Join/ }).click();
        await expect(other.page.locator('.note-live').getByText("You're in!")).toBeVisible();

        server = start();
        await expect(page.getByText('2 of 4 going')).toBeVisible({ timeout: 10_000 });
        await viewer.close();
        await other.context.close();
      } finally {
        server.kill('SIGTERM');
      }
    } finally {
      await Promise.all([deleteClerkUser(email), deleteClerkUser(joinerEmail)]);
    }
  });
});
