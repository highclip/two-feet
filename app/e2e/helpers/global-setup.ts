import { clerkSetup } from '@clerk/testing/playwright';

// Fetches the testing token that lets Playwright through Clerk's bot protection.
export default async function globalSetup() {
  await clerkSetup();
}
