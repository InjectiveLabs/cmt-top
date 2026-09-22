import { defineConfig, devices } from "@playwright/test";
const baseURL = process.env.CAPACITY_BASE_URL || "http://127.0.0.1:18768";
const target = new URL(baseURL);
if (
  target.protocol !== "http:" ||
  target.hostname !== "127.0.0.1" ||
  target.username ||
  target.password ||
  target.pathname !== "/" ||
  target.search ||
  target.hash
)
  throw new Error(
    "Browser capacity tests require a plain http://127.0.0.1:<port> loopback origin.",
  );
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 60000,
  expect: { timeout: 15000 },
  reporter: [["list"]],
  outputDir: process.env.CAPACITY_BROWSER_RESULTS || "./test-results",
  use: {
    ...devices["Desktop Chrome"],
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
});
