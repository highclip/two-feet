import { defineConfig } from '@playwright/test';

// Used only by scripts/shoot.sh --signed-in. Not part of the e2e gate, whose testDir is ./tests.
const [width, height] = (process.env.SHOOT_SIZE ?? '390x844').split('x').map(Number);

export default defineConfig({
  testDir: '.',
  testMatch: 'shoot.spec.ts',
  globalSetup: '../helpers/global-setup.ts',
  timeout: 120_000,
  workers: 1,
  reporter: 'line',
  outputDir: process.env.SHOOT_OUT ? `${process.env.SHOOT_OUT}/playwright` : undefined,
  use: {
    baseURL: process.env.SHOOT_BASE,
    viewport: { width, height },
    colorScheme: process.env.SHOOT_THEME === 'dark' ? 'dark' : 'light',
    reducedMotion: 'reduce',
  },
});
