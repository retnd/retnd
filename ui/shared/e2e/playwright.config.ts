/**
 * Playwright configuration for the browser specs that sit beside the shared
 * UI (issue #985). They drive the real screens served by `vite` in
 * development, which is the composition that picks the in-process mock API
 * (app/createApp.tsx), so a spec here exercises real layout, real theme and
 * real event ordering against fixtures instead of props.
 *
 * Run them in a container, with `@playwright/test` installed on top of
 * `npm ci` (nothing here is a dependency of the product build):
 *
 *   npx playwright test -c e2e/playwright.config.ts
 */
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts/,
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  outputDir: "../test-results",
  use: {
    baseURL: "http://127.0.0.1:5199",
    viewport: { width: 1280, height: 900 },
    video: process.env.E2E_VIDEO ? { mode: "on", size: { width: 1280, height: 900 } } : "off",
    trace: "off"
  },
  webServer: {
    command: "npx vite --host 127.0.0.1 --port 5199 --strictPort",
    cwd: "..",
    url: "http://127.0.0.1:5199",
    reuseExistingServer: false,
    timeout: 120_000
  }
});
