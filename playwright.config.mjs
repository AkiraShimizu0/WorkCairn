import { defineConfig, devices } from "@playwright/test";

// Each test owns its own workcairn-daemon subprocess, temporary Vault, and
// mock Provider HTTP server on ephemeral ports (see support/harness.mjs),
// so tests are already fully isolated -- fullyParallel + a worker pool is
// safe. workers is a fixed, measured value (not e.g. "50%") because it was
// chosen empirically for this harness's actual bottleneck: at workers=8 on
// an 8-core machine, concurrent Chromium + daemon + mock-server processes
// contend for CPU badly enough to produce real timeouts and test failures
// (confirmed during the Test Speed round); workers=4 gave a measured ~3.6x
// wall-clock speedup with zero failures. Re-measure before raising this.
const BROWSER_GATE_WORKERS = 4;

export default defineConfig({
  testDir: "./tests/browser",
  fullyParallel: true,
  workers: BROWSER_GATE_WORKERS,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  use: {
    actionTimeout: 15_000,
    navigationTimeout: 20_000,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure"
  },
  projects: [
    {
      // Canonical business-logic suite: every test file, every tag. This
      // project is the source of truth for product behavior.
      name: "chromium-desktop",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } }
    },
    {
      // WebKit-engine and mobile-layout risk only -- not a second full
      // pass over business logic already proven on chromium-desktop.
      // @mobile marks tests with real per-viewport/per-engine behavior
      // (responsive layout, composer pinned, viewport overflow, native
      // disclosure, touch-reachable archive/menu controls). @critical
      // marks the small set of core product flows (happy path, incremental
      // clarification, archive lifecycle, deliverable viewer, FailureEnvelope
      // restore, detail pane lifecycle, composer-copy dedupe) worth a WebKit
      // smoke pass specifically to catch engine-specific regressions in the
      // most important paths, even where they carry no mobile-specific
      // assertions of their own.
      name: "webkit-iphone",
      grep: /@mobile|@critical/,
      use: { ...devices["iPhone 13"] }
    }
  ],
  outputDir: "test-results/browser"
});
