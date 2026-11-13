import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  workers: 1,
  timeout: 60000,
  use: {
    ignoreHTTPSErrors: true,
    actionTimeout: 10000,
    viewport: { width: 390, height: 844 },
    launchOptions: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? {
          executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH,
          args: JSON.parse(process.env.PLAYWRIGHT_CHROMIUM_ARGS || "[]"),
        }
      : {},
  },
  reporter: "list",
});
