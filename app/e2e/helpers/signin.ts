import { setupClerkTestingToken } from '@clerk/testing/playwright';
import { expect, type Page } from '@playwright/test';

// A fresh Clerk test address. The +clerk_test suffix makes Clerk accept the fixed code 424242.
export function testEmail(name: string): string {
  return `${name}-${Date.now()}+clerk_test@example.com`;
}

// Signs in through Clerk's real UI from the page the browser is already on (a /sign-in page).
export async function enterEmailAndCode(page: Page, email: string): Promise<void> {
  await setupClerkTestingToken({ page });
  const identifier = page.locator('input[name="identifier"]');
  await expect(identifier).toBeVisible({ timeout: 30_000 });
  await identifier.fill(email);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await expect(page.getByText(/verification code|Check your email/i).first()).toBeVisible({ timeout: 30_000 });
  await page.keyboard.type('424242', { delay: 50 });
}

// Deletes the Clerk user a test created: the development instance has a cap on users, and every
// builder's gate runs would otherwise fill it.
export async function deleteClerkUser(email: string): Promise<void> {
  const headers = { Authorization: `Bearer ${process.env.CLERK_SECRET_KEY}` };
  const list = await fetch(
    `https://api.clerk.com/v1/users?email_address=${encodeURIComponent(email)}`,
    { headers },
  );
  if (!list.ok) return;
  const users = (await list.json()) as { id: string }[];
  await Promise.all(
    users.map((u) => fetch(`https://api.clerk.com/v1/users/${u.id}`, { method: 'DELETE', headers })),
  );
}
