import { appendFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { createClerkUser, deleteClerkUser, enterEmailAndCode, testEmail } from '../helpers/signin';

// Signs in as a temporary Clerk test user and screenshots each path, for scripts/shoot.sh --signed-in.
const paths = (process.env.SHOOT_PATHS ?? '').split('\n').filter(Boolean);
const role = process.env.SHOOT_ROLE ?? '';
const out = process.env.SHOOT_OUT ?? '.';
const size = process.env.SHOOT_SIZE ?? '390x844';
const theme = process.env.SHOOT_THEME ?? 'light';

test('signed-in screenshots', async ({ page }) => {
  const email = testEmail('shoot');
  try {
    if (role === 'organizer') await createClerkUser(email, { role: 'organizer' });
    // SHOOT_ROLE is "sponsor_rep" or "sponsor_rep:<sponsor name>".
    if (role.startsWith('sponsor_rep')) {
      await createClerkUser(email, { role: 'sponsor_rep', sponsor: role.split(':')[1] ?? 'Dash0' });
    }
    await page.goto('/sign-in?return=%2Fe%2Fcnh');
    await enterEmailAndCode(page, email);
    await page.waitForURL(/\/e\/cnh/, { timeout: 30_000 });
    if (page.url().includes('/onboarding')) {
      await page.getByLabel('Your name').fill('Sample Person');
      await page.getByRole('button', { name: 'Continue' }).click();
      await expect(page).toHaveURL(/\/e\/cnh$/);
    }
    for (const path of paths) {
      await page.goto(path);
      await page.waitForTimeout(800);
      const name = path.replace(/^\//, '').replace(/[^A-Za-z0-9]+/g, '-') || 'index';
      const file = `${out}/${name}-${size}-${theme}-signed-in${role ? `-${role.split(':')[0]}` : ''}.png`;
      await page.screenshot({ path: file, fullPage: true });
      appendFileSync(`${out}/signed-in-shots.txt`, `${file}\n`);
    }
  } finally {
    await deleteClerkUser(email);
  }
});
