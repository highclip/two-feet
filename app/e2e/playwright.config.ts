import { defineConfig } from '@playwright/test';

// Runs the real binary on this worktree's PORT and DATABASE_URL. `migrate` also loads the venue.
const port = process.env.PORT ?? '8080';
// Clerk issues session tokens for this origin, so the browser has to use exactly this address.
const baseURL = process.env.PUBLIC_URL ?? `http://localhost:${port}`;

export default defineConfig({
  testDir: './tests',
  globalSetup: './helpers/global-setup.ts',
  timeout: 60_000,
  workers: 1,
  use: {
    baseURL,
    viewport: { width: 390, height: 844 },
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'go build -o .bin/twofeet ../cmd/twofeet && ./.bin/twofeet migrate && ./.bin/twofeet',
    url: `${baseURL}/readyz`,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
