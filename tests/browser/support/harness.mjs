import { spawn } from "node:child_process";
import { createServer as createHTTPServer } from "node:http";
import { createServer as createTCPServer } from "node:net";
import { mkdtemp, mkdir, readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const supportDirectory = dirname(fileURLToPath(import.meta.url));
export const repositoryRoot = resolve(supportDirectory, "../../..");
const fixturePath = join(repositoryRoot, "fixtures/provider/browser_acceptance_v1.json");
const defaultDaemonBinary = join(repositoryRoot, "bin/workcairn-daemon");
const fakeCredential = "browser-acceptance-fixture-key";

async function listen(server) {
  await new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolveListen();
    });
  });
  return server.address().port;
}

async function reservePort() {
  const server = createTCPServer();
  const port = await listen(server);
  await new Promise((resolveClose) => server.close(resolveClose));
  return port;
}

async function loadProviderFixture(scenario) {
  const fixture = JSON.parse(await readFile(fixturePath, "utf8"));
  if (fixture.schema_version !== "workcairn-browser-provider-fixture.v1") {
    throw new Error(`unsupported provider fixture version: ${fixture.schema_version}`);
  }
  const responses = fixture.scenarios?.[scenario];
  if (!Array.isArray(responses) || responses.length === 0) {
    throw new Error(`provider fixture scenario is missing: ${scenario}`);
  }
  return structuredClone(responses);
}

async function startProviderMock(scenario) {
  const responses = await loadProviderFixture(scenario);
  const calls = [];
  const server = createHTTPServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    let payload = null;
    try {
      payload = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    } catch {
      response.writeHead(400, { "content-type": "application/json" });
      response.end(JSON.stringify({ type: "error", error: { type: "invalid_request_error", message: "fixture JSON required" } }));
      return;
    }

    const current = responses[calls.length];
    calls.push({
      path: request.url,
      method: request.method,
      model: payload?.model || "",
      structured: payload?.output_config?.format?.type === "json_schema",
      fixture: current?.name || "unexpected"
    });
    if (request.method !== "POST" || request.url !== "/v1/messages" || request.headers["x-api-key"] !== fakeCredential || !current) {
      response.writeHead(500, { "content-type": "application/json" });
      response.end(JSON.stringify({ type: "error", error: { type: "api_error", message: "unexpected browser fixture request" } }));
      return;
    }
    if (current.delay_ms) await new Promise((resolveDelay) => setTimeout(resolveDelay, current.delay_ms));
    response.writeHead(current.status, current.headers);
    response.end(JSON.stringify(current.body));
  });
  const port = await listen(server);
  return {
    url: `http://127.0.0.1:${port}`,
    calls,
    expectedCount: responses.length,
    close: () => new Promise((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose()))
  };
}

async function waitForReady(baseURL, processHandle, output) {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    if (processHandle.exitCode != null) {
      throw new Error(`workcairn-daemon exited before readiness (${processHandle.exitCode}): ${output.stderr.join("")}`);
    }
    try {
      const response = await fetch(`${baseURL}/readyz`);
      if (response.ok) return;
    } catch {}
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error(`workcairn-daemon readiness timed out: ${output.stderr.join("")}`);
}

async function startDaemon({ vaultRoot, providerURL }) {
  const binary = process.env.WORKCAIRN_DAEMON_BINARY || defaultDaemonBinary;
  await stat(binary);
  const port = await reservePort();
  const address = `127.0.0.1:${port}`;
  const output = { stdout: [], stderr: [] };
  let pairingResolve;
  let pairingReject;
  const pairing = new Promise((resolvePairing, rejectPairing) => {
    pairingResolve = resolvePairing;
    pairingReject = rejectPairing;
  });
  const child = spawn(binary, [
    "-vault", vaultRoot,
    "-listen", address,
    "-mobile",
    "-provider-timeout", "10s",
    "-shutdown-timeout", "5s",
    "-scheduler-interval", "1h"
  ], {
    cwd: repositoryRoot,
    env: {
      PATH: process.env.PATH,
      HOME: process.env.HOME,
      LANG: process.env.LANG || "en_US.UTF-8",
      TMPDIR: process.env.TMPDIR || tmpdir(),
      ANTHROPIC_API_KEY: fakeCredential,
      WORKCAIRN_CLAUDE_BASE_URL: providerURL
    },
    stdio: ["ignore", "pipe", "pipe"]
  });
  let stdoutBuffer = "";
  child.stdout.on("data", (chunk) => {
    const text = chunk.toString("utf8");
    output.stdout.push(text);
    stdoutBuffer += text;
    const match = stdoutBuffer.match(/Pairing code: ([A-Za-z0-9_-]+)/);
    if (match) pairingResolve(match[1]);
  });
  child.stderr.on("data", (chunk) => output.stderr.push(chunk.toString("utf8")));
  child.once("error", pairingReject);
  child.once("exit", (code) => {
    if (!stdoutBuffer.includes("Pairing code:")) pairingReject(new Error(`daemon exited before pairing code (${code})`));
  });
  const baseURL = `http://${address}`;
  await waitForReady(baseURL, child, output);
  const pairingCode = await Promise.race([
    pairing,
    new Promise((_, reject) => setTimeout(() => reject(new Error("pairing code timed out")), 5_000))
  ]);
  return { child, baseURL, pairingCode, output };
}

async function stopDaemon(daemon) {
  if (!daemon || daemon.child.exitCode != null) return;
  const exited = new Promise((resolveExit) => daemon.child.once("exit", resolveExit));
  daemon.child.kill("SIGTERM");
  const graceful = await Promise.race([
    exited.then(() => true),
    new Promise((resolveTimeout) => setTimeout(() => resolveTimeout(false), 7_000))
  ]);
  if (!graceful) {
    daemon.child.kill("SIGKILL");
    await exited;
  }
}

export async function startBrowserEnvironment(scenario) {
  const temporaryRoot = await mkdtemp(join(tmpdir(), "workcairn-browser-"));
  const vaultRoot = join(temporaryRoot, "vault");
  await mkdir(vaultRoot, { recursive: true });
  const provider = await startProviderMock(scenario);
  let daemon = await startDaemon({ vaultRoot, providerURL: provider.url });
  return {
    scenario,
    temporaryRoot,
    vaultRoot,
    provider,
    get daemon() { return daemon; },
    async restartDaemon() {
      await stopDaemon(daemon);
      daemon = await startDaemon({ vaultRoot, providerURL: provider.url });
      return daemon;
    },
    async stop() {
      await stopDaemon(daemon);
      await provider.close();
      await rm(temporaryRoot, { recursive: true, force: true });
    }
  };
}

export async function pathExists(path) {
  try {
    await stat(path);
    return true;
  } catch {
    return false;
  }
}
