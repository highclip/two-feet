import { defineConfig } from '@playwright/test';

// Runs the real binary on this worktree's PORT and DATABASE_URL. `migrate` also loads the venue.
const port = process.env.PORT ?? '8080';
// Clerk issues session tokens for this origin, so the browser has to use exactly this address.
const baseURL = process.env.PUBLIC_URL ?? `http://localhost:${port}`;
// Offset from this worktree's own PORT, so parallel worktrees don't collide.
const fakeNewsPort = Number(port) + 1000;
const fakeNewsURL = `http://127.0.0.1:${fakeNewsPort}`;

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
  webServer: [
    {
      // S12: the fake news source starts first, so the binary's fetch at startup finds it.
      command: 'node helpers/fake-hn.mjs',
      url: fakeNewsURL,
      env: { FAKE_HN_PORT: String(fakeNewsPort) },
      reuseExistingServer: false,
    },
    {
      command: 'go build -o .bin/twofeet ../cmd/twofeet && ./.bin/twofeet migrate && ./.bin/twofeet',
      url: `${baseURL}/readyz`,
      env: { TOPICS_SOURCE_URL: fakeNewsURL },
      reuseExistingServer: false,
      timeout: 120_000,
    },
  ],
});
