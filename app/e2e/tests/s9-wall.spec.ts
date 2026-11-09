import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { type Browser, type BrowserContext, expect, type Page, test } from '@playwright/test';

import { deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

const WALL = '/e/cnh/wall';
const SPOTS = [
  'Picnic table · Patio · seats 8',
  'Couches · Lounge · seats 8',
  'Window table · Main room · seats 6',
  'Kitchen island · Kitchen · seats 6',
  'Long table · Main room · seats 10',
];

// A form time some minutes from now, in the event's timezone.
function denverIn(minutes: number): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'America/Denver', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }).formatToParts(new Date(Date.now() + minutes * 60_000));
  const get = (type: string) => parts.find((p) => p.type === type)!.value;
  return `${get('year')}-${get('month')}-${get('day')}T${get('hour')}:${get('minute')}`;
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

// Starts a space soon, on the first spot that's free, so a kept database can't fail the test.
async function startSpace(page: Page, title: string): Promise<string> {
  for (const spot of SPOTS) {
    await page.goto('/e/cnh/spaces/new');
    await page.getByLabel("What's it about?").fill(title);
    await page.getByLabel('Where').selectOption({ label: spot });
    await page.getByLabel('Starts').fill(denverIn(20));
    await page.getByText('30 min').click();
    await page.getByLabel('How many people').fill('4');
    await page.getByRole('button', { name: 'Start this space' }).click();
    const heading = page.getByRole('heading', { name: title });
    await expect(heading.or(page.getByRole('alert'))).toBeVisible();
    if (await heading.isVisible()) return page.url();
  }
  throw new Error('every spot is taken');
}

async function openWall(browser: Browser): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({ viewport: { width: 1920, height: 1080 } });
  const page = await context.newPage();
  await page.goto(WALL);
  await page.waitForFunction(() => {
    const body = document.body as HTMLElement & { 'htmx-internal-data'?: { sseEventSource?: EventSource } };
    return body['htmx-internal-data']?.sseEventSource?.readyState === EventSource.OPEN;
  });
  return { context, page };
}

test.describe('S9 the Wall', () => {
  test.describe.configure({ timeout: 180_000 });

  const emails = { host: testEmail('s9-host'), guest: testEmail('s9-guest') };

  test.afterAll(async () => {
    await Promise.all(Object.values(emails).map(deleteClerkUser));
  });

  test('fits a 1080p screen, updates within 2 seconds, shows no names and survives a drop', async ({ browser }) => {
    const title = `Wall check ${Date.now()}: a title long enough that it has to be cut to two lines`.slice(0, 80);
    const host = await signedIn(browser, emails.host, 'Zelda Quimby');
    const guest = await signedIn(browser, emails.guest, 'Ansel Rosario');
    const spaceUrl = await startSpace(host.page, title);

    const wall = await openWall(browser);
    const note = wall.page.locator('.wall-note', { hasText: title.slice(0, 12) });
    await expect(note).toBeVisible();
    await expect(note).toContainText('1 of 4');

    // The next 2 hours of every zone fit without scrolling, with titles readable from 5 meters.
    const fit = await wall.page.evaluate(() => ({
      width: document.documentElement.scrollWidth,
      height: document.documentElement.scrollHeight,
      slots: document.querySelectorAll('[data-slot]').length,
      rows: document.querySelectorAll('.wall-spot').length,
      bottom: document.querySelector('.wall-foot')!.getBoundingClientRect().bottom,
      lanes: Array.from(document.querySelectorAll('.wall-note')).every((n) => {
        const box = n.getBoundingClientRect();
        return box.top >= 0 && box.bottom <= window.innerHeight && box.right <= window.innerWidth;
      }),
    }));
    expect(fit.width).toBeLessThanOrEqual(1920);
    expect(fit.height).toBeLessThanOrEqual(1080);
    expect(fit.slots).toBe(8);
    expect(fit.rows).toBe(SPOTS.length);
    expect(fit.bottom).toBeLessThanOrEqual(1080);
    expect(fit.lanes).toBe(true);

    const titleEl = note.locator('.wall-note-title');
    const type = await titleEl.evaluate((el) => {
      const style = getComputedStyle(el);
      return { size: parseFloat(style.fontSize), line: parseFloat(style.lineHeight), height: el.getBoundingClientRect().height };
    });
    expect(type.size).toBeGreaterThanOrEqual(28);
    expect(type.height).toBeLessThanOrEqual(type.line * 2 + 1); // D30: two lines at most

    // A signup anywhere moves the count on the Wall within 2 seconds, with no reload.
    await guest.page.goto(spaceUrl);
    await guest.page.waitForLoadState('load');
    await guest.page.getByRole('button', { name: /^Join/ }).click();
    await expect(guest.page.locator('.note-live').getByText("You're in!")).toBeVisible();
    await expect(note).toContainText('2 of 4', { timeout: 2_000 });

    // Titles, spots and counts only.
    const text = await wall.page.locator('body').innerText();
    for (const name of ['Zelda', 'Quimby', 'Ansel', 'Rosario']) expect(text).not.toContain(name);

    // A dropped connection comes back on its own and the Wall catches up.
    await wall.context.setOffline(true);
    await wall.page.evaluate(() => {
      const body = document.body as HTMLElement & { 'htmx-internal-data': { sseEventSource: EventSource } };
      body['htmx-internal-data'].sseEventSource.close();
      body['htmx-internal-data'].sseEventSource.onerror?.(new Event('error'));
    });
    await expect(wall.page.getByRole('status').filter({ hasText: 'Reconnecting' })).toBeVisible({ timeout: 8_000 });

    await guest.page.getByRole('button', { name: 'Leave', exact: true }).click();
    await expect(guest.page.getByRole('status').getByText("You've left")).toBeVisible();

    await wall.context.setOffline(false);
    await expect(note).toContainText('1 of 4', { timeout: 10_000 });
    await expect(wall.page.getByRole('status').filter({ hasText: 'Reconnecting' })).toBeHidden();

    // A cancelled space leaves the Wall.
    await host.page.goto(spaceUrl);
    await host.page.getByRole('button', { name: 'Cancel space' }).click();
    await expect(note).toHaveCount(0, { timeout: 2_000 });

    await wall.context.close();
    await host.context.close();
    await guest.context.close();
  });

  test('memory stays flat over hours of refreshes', async ({ browser }) => {
    const wall = await openWall(browser);
    const cdp = await wall.context.newCDPSession(wall.page);
    await cdp.send('HeapProfiler.enable');

    const refreshes = async (count: number) => {
      for (let i = 0; i < count; i++) {
        const done = wall.page.waitForResponse((r) => r.url().includes('/wall/board'));
        await wall.page.evaluate(() => {
          (window as unknown as { htmx: { trigger: (el: Element, name: string) => void } }).htmx
            .trigger(document.getElementById('wall-board')!, 'sse:wall-changed');
        });
        await done;
      }
      await wall.page.waitForTimeout(250);
      await cdp.send('HeapProfiler.collectGarbage');
    };
    const counters = () => cdp.send('Memory.getDOMCounters');

    // The first refreshes warm htmx's and the JIT's own caches. Four hours at 15 seconds is 960, so
    // 200 after warm-up shows the slope.
    await refreshes(20);
    const before = await counters();
    await refreshes(200);
    const after = await counters();

    expect(after.jsEventListeners - before.jsEventListeners).toBeLessThanOrEqual(2);
    expect(after.nodes - before.nodes).toBeLessThanOrEqual(50);
    await wall.context.close();
  });
});
