import { defineConfig } from '@playwright/test';

// The demo path runs against a deployed URL, so unlike playwright.config.ts it starts no local
// server. It lives outside tests/ so a builder's e2e gate never writes to production.
export default defineConfig({
  testDir: '.',
  testMatch: 'demo.spec.ts',
  globalSetup: './helpers/global-setup.ts',
  timeout: 90_000,
  workers: 1,
  use: {
    baseURL: process.env.BASE_URL ?? 'https://twofeet.highclip.dev',
    viewport: { width: 390, height: 844 },
    trace: 'retain-on-failure',
  },
});
