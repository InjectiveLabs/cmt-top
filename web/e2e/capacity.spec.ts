import { test, expect, type Page } from "@playwright/test";
import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import { mkdtemp, writeFile, readFile, cp, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";
const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const external = Boolean(process.env.CAPACITY_BASE_URL);
const legacyRef = process.env.CAPACITY_LEGACY_REF || "52532c264714ce5fd7c10ab2e44c335e4a4fdbcc";
const base = process.env.CAPACITY_BASE_URL || "http://127.0.0.1:18768";
const fixture = process.env.CAPACITY_FIXTURE_URL || "http://127.0.0.1:18767";
for (const origin of [base, fixture]) {
  const url = new URL(origin);
  if (
    url.protocol !== "http:" ||
    url.hostname !== "127.0.0.1" ||
    url.username ||
    url.password ||
    url.pathname !== "/" ||
    url.search ||
    url.hash
  )
    throw new Error("Capacity browser origins must be plain literal loopback HTTP URLs.");
}
if (external && !process.env.CAPACITY_FIXTURE_URL)
  throw new Error("CAPACITY_FIXTURE_URL is required with an external local stack.");
let stackLog = "";
const launchErrors: string[] = [];
async function assertFreePorts() {
  for (const port of [18767, 18768, 18769])
    await new Promise<void>((resolve, reject) => {
      const server = createServer();
      server.once("error", reject);
      server.listen(port, "127.0.0.1", () =>
        server.close((error) => (error ? reject(error) : resolve())),
      );
    });
}
let work = "",
  app: ChildProcess | undefined,
  replay: ChildProcess | undefined;
let binary = "",
  legacyBinary = "",
  config = "",
  height = 1001;
const children = new Set<ChildProcess>();
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
async function stop(child: ChildProcess | undefined) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([
    new Promise<void>((resolve) => child.once("exit", () => resolve())),
    sleep(3000),
  ]);
  if (child.exitCode === null) child.kill("SIGKILL");
  children.delete(child);
}
function launch(command: string, args: string[]) {
  const child = spawn(command, args, {
    env: { PATH: process.env.PATH },
    stdio: ["ignore", "pipe", "pipe"],
  });
  children.add(child);
  const log = (data: Buffer) => {
    stackLog = (stackLog + `${path.basename(command)}: ${data}`).slice(-65536);
  };
  child.stderr?.on("data", log);
  child.stdout?.on("data", log);
  child.on("error", (error) => launchErrors.push(`${command}: ${error.message}`));
  return child;
}
async function ready() {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    if (
      launchErrors.length ||
      (!external &&
        [app, replay].some(
          (child) => child && (child.exitCode !== null || child.signalCode !== null),
        ))
    )
      throw new Error(
        `Synthetic stack exited before readiness: ${launchErrors.join("; ")}\n${stackLog}`,
      );
    let state: any, identity: any;
    try {
      identity = await (
        await fetch(`${fixture}/_fixture/stats`, {
          redirect: "error",
          signal: AbortSignal.timeout(1000),
        })
      ).json();
      state = await (
        await fetch(`${base}/api/state`, { redirect: "error", signal: AbortSignal.timeout(1000) })
      ).json();
    } catch {
      await sleep(100);
      continue;
    }
    expect(identity.identity, "Only the named synthetic replay may drive browser tests").toBe(
      "cmt-top-local-replay-v1",
    );
    expect(identity.profile).toBe("testnet5");
    if (!state.activeRPC || !state.chain?.network) {
      await sleep(100);
      continue;
    }
    expect(state.activeRPC).toBe(fixture);
    expect(state.chain.network).toBe("capacity-replay-1");
    height = state.height;
    const response = await fetch(`${base}/api/blocks/${height}/rounds`, {
      redirect: "error",
      signal: AbortSignal.timeout(1000),
    });
    const report = await response.json();
    if (report.rounds?.length === 3) return;
    await sleep(100);
  }
  throw new Error(`Synthetic stack did not observe all three fixture rounds.\n${stackLog}`);
}
async function restart(useLegacy = false) {
  if (external) throw new Error("Restart tests require the owned synthetic stack.");
  await stop(app);
  await stop(replay);
  replay = launch(path.join(work, "replay"), [
    "-listen",
    "127.0.0.1:18767",
    "-profile",
    "testnet5",
    "-rounds",
    "3",
    "-stalled",
  ]);
  app = launch(useLegacy ? legacyBinary : binary, ["--config", config]);
  await ready();
}
test.beforeAll(async () => {
  test.setTimeout(120000);
  if (external) {
    await ready();
    return;
  }
  await assertFreePorts();
  work = await mkdtemp(path.join(tmpdir(), "cmt-top-browser-"));
  binary = path.join(work, "app");
  legacyBinary = path.join(work, "legacy-app");
  config = path.join(work, "config.toml");
  execFileSync("pnpm", ["--dir", "web", "build"], { cwd: repo, stdio: "pipe" });
  execFileSync("go", ["build", "-mod=readonly", "-tags", "webui", "-o", binary, "./cmd/cmt-top"], {
    cwd: repo,
    stdio: "pipe",
  });
  execFileSync(
    "go",
    ["build", "-mod=readonly", "-o", path.join(work, "replay"), "./cmd/cmt-top-replay"],
    { cwd: repo, stdio: "pipe" },
  );
  const oldRepo = path.join(work, "legacy");
  await mkdir(oldRepo);
  const archive = execFileSync("git", ["archive", legacyRef], {
    cwd: repo,
    maxBuffer: 64 * 1024 * 1024,
  });
  const archivePath = path.join(work, "legacy.tar");
  await writeFile(archivePath, archive);
  execFileSync("tar", ["-xf", archivePath, "-C", oldRepo]);
  await cp(path.join(repo, "internal/web/dist"), path.join(oldRepo, "internal/web/dist"), {
    recursive: true,
  });
  execFileSync(
    "go",
    ["build", "-mod=readonly", "-tags", "webui", "-o", legacyBinary, "./cmd/cmt-top"],
    { cwd: oldRepo, stdio: "pipe" },
  );
  await writeFile(
    config,
    `[chain]\nname = "Synthetic browser lifecycle"\nlcd = "http://127.0.0.1:18767"\nmonitored_rpcs = []\nexplorer_url = ""\n[[chain.rpc]]\nurl = "http://127.0.0.1:18767"\nprimary = true\n[ui]\nmode = "web"\n[ui.web]\nlisten = "127.0.0.1:18768"\ntoken = ""\n[obs]\nmetrics_listen = "127.0.0.1:18769"\nlog_level = "warn"\n`,
  );
  await restart();
});
test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus && stackLog)
    await testInfo.attach("synthetic-stack.log", {
      body: Buffer.from(stackLog),
      contentType: "text/plain",
    });
});
test.afterAll(async () => {
  for (const child of children) await stop(child);
  if (work) await rm(work, { recursive: true, force: true });
});
async function investigate(page: Page) {
  await page.goto(`/blocks/${height}/rounds`);
  await expect(page.getByRole("heading", { name: "Observed rounds" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Export evidence JSON", exact: true }),
  ).toBeEnabled();
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
}
async function exportJSON(page: Page) {
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export evidence JSON", exact: true }).click();
  const file = await (await download).path();
  expect(file).not.toBeNull();
  return JSON.parse(await readFile(file!, "utf8"));
}
function observe(page: Page) {
  const requests: string[] = [],
    commands: any[] = [],
    frames: any[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.startsWith("/api/")) requests.push(r.url());
  });
  page.on("websocket", (socket) => {
    socket.on("framesent", ({ payload }) => {
      try {
        commands.push(JSON.parse(String(payload)));
      } catch {}
    });
    socket.on("framereceived", ({ payload }) => {
      try {
        frames.push(JSON.parse(String(payload)));
      } catch {}
    });
  });
  return { requests, commands, frames };
}
test("starts with one baseline and compact selected/reference details", async ({ page }) => {
  const observed = observe(page);
  await investigate(page);
  expect(observed.requests.filter((url) => new URL(url).pathname === "/api/state")).toHaveLength(0);
  expect(
    observed.frames.filter((frame) => frame.type === "state.snapshot" && frame.seq === 1),
  ).toHaveLength(1);
  await expect
    .poll(() =>
      observed.commands.some(
        (command) => command.type === "subscribe" && command.channels.includes("context"),
      ),
    )
    .toBe(true);
  await page.getByRole("button", { name: /^Select round 0,/ }).click();
  await expect(page.getByRole("heading", { name: "Round 0 · hash cohorts" })).toBeVisible();
  await page.getByLabel("Compare with round").selectOption("1");
  await expect
    .poll(() => observed.requests.some((url) => new URL(url).searchParams.get("compare") === "1"))
    .toBe(true);
  const raw = await (
    await page.request.get(`/api/blocks/${height}/rounds?view=compact&round=0&compare=1`)
  ).json();
  expect(raw.rounds).toHaveLength(3);
  expect(raw.details).toHaveLength(2);
  expect(
    observed.requests.every(
      (url) =>
        !new URL(url).pathname.endsWith("/rounds") ||
        new URL(url).searchParams.get("view") === "compact",
    ),
  ).toBe(true);
});
test("captures Pause once, exports locally across a real process restart, and resumes once", async ({
  page,
}) => {
  test.skip(external, "Process restart requires the test-owned stack");
  const observed = observe(page);
  await investigate(page);
  await page.getByRole("button", { name: "Pause view", exact: true }).click();
  await expect(page.getByRole("button", { name: "Resume", exact: true })).toBeVisible();
  expect(
    observed.requests.filter((url) => new URL(url).searchParams.get("capture") === "1"),
  ).toHaveLength(1);
  const original = await exportJSON(page);
  let failResume = true;
  await page.route(
    (url) => url.pathname.endsWith("/rounds") && url.searchParams.get("view") === "compact",
    async (route) => {
      if (failResume) {
        failResume = false;
        await route.fulfill({ status: 503, body: "busy" });
      } else await route.continue();
    },
  );
  await page.getByRole("button", { name: "Resume", exact: true }).click();
  await expect(
    page.getByText("Could not refresh the paused view. Retry when the dashboard is available.", {
      exact: true,
    }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Retry connection", exact: true }).click();
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Observed rounds" })).toBeVisible();
  expect((await exportJSON(page)).investigation).toEqual(original.investigation);

  await page.getByRole("button", { name: /^Select round 0,/ }).click();
  await expect(page.getByRole("heading", { name: "Round 0 · hash cohorts" })).toBeVisible();
  const pausedRequests = observed.requests.filter((url) =>
    new URL(url).pathname.endsWith("/rounds"),
  ).length;
  const baselinesBeforeRestart = observed.frames.filter(
    (frame) => frame.type === "state.snapshot",
  ).length;
  await restart();
  await expect
    .poll(() => observed.frames.filter((frame) => frame.type === "state.snapshot").length)
    .toBeGreaterThan(baselinesBeforeRestart);
  const after = await exportJSON(page);
  expect(after.investigation).toEqual(original.investigation);
  expect(observed.requests.filter((url) => new URL(url).pathname.endsWith("/rounds"))).toHaveLength(
    pausedRequests,
  );
  const start = observed.requests.length,
    repairs = observed.commands.filter((command) => command.type === "resync").length;
  await page.getByRole("button", { name: "Resume", exact: true }).click();
  await expect(page.getByRole("button", { name: "Pause view", exact: true })).toBeVisible();
  const resumed = observed.requests
    .slice(start)
    .filter((url) => new URL(url).pathname.endsWith("/rounds"));
  expect(resumed).toHaveLength(1);
  expect(new URL(resumed[0]).searchParams.get("view")).toBe("compact");
  expect(observed.commands.filter((command) => command.type === "resync")).toHaveLength(
    repairs + 1,
  );
});
test("suspends report traffic on visibility events and preserves manual pause", async ({
  page,
}) => {
  const observed = observe(page);
  await investigate(page);
  // Headless Chromium keeps pages visible. Deliver the browser visibility signal
  // explicitly while exercising the real compiled UI, timers and network stack.
  async function visibility(hidden: boolean) {
    await page.evaluate((value) => {
      Object.defineProperty(document, "hidden", { configurable: true, get: () => value });
      document.dispatchEvent(new Event("visibilitychange"));
    }, hidden);
  }
  await visibility(true);
  await expect(page.getByText("Background tab", { exact: true })).toBeVisible();
  const count = observed.requests.filter((url) => new URL(url).pathname.endsWith("/rounds")).length;
  await page.waitForTimeout(1500);
  expect(observed.requests.filter((url) => new URL(url).pathname.endsWith("/rounds"))).toHaveLength(
    count,
  );
  await visibility(false);
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Pause view", exact: true }).click();
  await expect(page.getByRole("button", { name: "Resume", exact: true })).toBeVisible();
  const pausedCount = observed.requests.filter((url) =>
    new URL(url).pathname.endsWith("/rounds"),
  ).length;
  await visibility(true);
  await visibility(false);
  await page.waitForTimeout(1200);
  await expect(page.getByRole("button", { name: "Resume", exact: true })).toBeVisible();
  expect(observed.requests.filter((url) => new URL(url).pathname.endsWith("/rounds"))).toHaveLength(
    pausedCount,
  );
});
test("retries transient bootstrap overload without a full-state probe", async ({ page }) => {
  let attempts = 0;
  const observed = observe(page);
  await page.route("**/api/session", async (route) => {
    if (++attempts === 1)
      await route.fulfill({ status: 503, headers: { "Retry-After": "1" }, body: "busy" });
    else await route.continue();
  });
  await investigate(page);
  expect(attempts).toBeGreaterThan(1);
  expect(observed.requests.filter((url) => new URL(url).pathname === "/api/state")).toHaveLength(0);
});
test("failed Pause capture preserves the usable live view and can be retried", async ({ page }) => {
  let captures = 0;
  await page.route(
    (url) => url.searchParams.get("capture") === "1",
    async (route) => {
      if (++captures === 1)
        await route.fulfill({ status: 503, headers: { "Retry-After": "1" }, body: "busy" });
      else await route.continue();
    },
  );
  await investigate(page);
  await page.getByRole("button", { name: "Pause view", exact: true }).click();
  await expect(
    page.getByText("The dashboard is busy. Retrying shortly.", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Pause view", exact: true })).toBeEnabled();
  await expect(page.getByRole("heading", { name: "Observed rounds" })).toBeVisible();
  expect(captures).toBe(1);
  await page.getByRole("button", { name: "Pause view", exact: true }).click();
  await expect(page.getByRole("button", { name: "Resume", exact: true })).toBeVisible();
  expect(captures).toBe(2);
});
test("unsupported startup protocol leaves a working Retry connection action", async ({ page }) => {
  let unsupported = true;
  await page.routeWebSocket("**/ws", (socket) => {
    const server = socket.connectToServer();
    server.onMessage((message) => {
      const frame = JSON.parse(String(message));
      if (unsupported && frame.type === "state.snapshot") frame.payload.schemaVersion = 999;
      socket.send(JSON.stringify(frame));
    });
  });
  await page.goto("/");
  await expect(page.getByText(/unsupported dashboard protocol/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry connection", exact: true })).toBeEnabled();
  unsupported = false;
  await page.getByRole("button", { name: "Retry connection", exact: true }).click();
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
});
test("an already-open tab renegotiates against the actual legacy backend after rollback", async ({
  page,
}) => {
  test.skip(external, "Rollback requires the test-owned stack");
  const observed = observe(page);
  await investigate(page);
  const before = observed.frames.length;
  await restart(true);
  await expect
    .poll(() =>
      observed.frames
        .slice(before)
        .some((frame) => frame.type === "state.snapshot" && !frame.payload.schemaVersion),
    )
    .toBe(true);
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
  await expect
    .poll(() =>
      observed.requests.some(
        (url) =>
          new URL(url).pathname.endsWith("/rounds") && !new URL(url).searchParams.has("view"),
      ),
    )
    .toBe(true);
  await expect(page.getByRole("heading", { name: "Observed rounds" })).toBeVisible();
  const exportData = await exportJSON(page);
  expect(exportData.investigation.rounds).toHaveLength(3);
});
