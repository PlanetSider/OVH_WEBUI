import { createHmac, webcrypto } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

// Resolve through Vite so both npm and pnpm use its existing compiler dependency.
const { build } = createRequire(import.meta.resolve("vite"))("esbuild");
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const tempRoot = await mkdtemp(join(tmpdir(), "ovh-http-signing-"));
const bundlePath = join(tempRoot, "http.mjs");
const originalWindow = globalThis.window;
const fixturesOnly = process.argv.includes("--fixtures");
const encoder = new TextEncoder();
const apiKey = "TestKey123";
const fixtures = { apiKey, requests: [] };

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function expectedSignature(headers, method, path, body, key = apiKey) {
  return createHmac("sha256", key)
    .update(`${method.toUpperCase()}\n${path}\n${headers["X-Request-Time"]}\n${headers["X-Request-Nonce"]}\n`)
    .update(body)
    .digest("hex");
}

function recordRequest(name, method, path, body, headers) {
  assert(headers["X-API-Key"] === apiKey, `${name}: API key header missing`);
  assert(/^\d+$/.test(headers["X-Request-Time"]), `${name}: timestamp invalid`);
  assert(/^[0-9a-f]{32}$/.test(headers["X-Request-Nonce"]), `${name}: nonce invalid`);
  assert(/^[0-9a-f]{64}$/.test(headers["X-Request-Signature"]), `${name}: signature format invalid`);
  assert(headers["X-Request-Signature"] === expectedSignature(headers, method, path, body), `${name}: signature mismatch`);
  fixtures.requests.push({ name, method: method.toUpperCase(), path, body: Buffer.from(body).toString("base64"), headers });
}

function browserWindow(cryptoApi, storage = {}) {
  const values = new Map(Object.entries(storage));
  return {
    crypto: cryptoApi,
    location: { origin: "http://compatibility.invalid" },
    localStorage: {
      getItem: (key) => values.get(key) ?? null,
      setItem: (key, value) => values.set(key, value),
      removeItem: (key) => values.delete(key),
    },
  };
}

async function expectRejection(operation, expected, label) {
  let actual;
  try {
    await operation();
  } catch (error) {
    actual = error;
  }
  assert(actual && (typeof expected === "string" ? actual.message === expected : actual === expected), `${label}: expected rejection was not preserved`);
}

async function run() {
  await build({
    entryPoints: [join(projectRoot, "src/lib/http.ts")],
    bundle: true,
    platform: "browser",
    format: "esm",
    outfile: bundlePath,
    logLevel: "silent",
  });

  const { api, buildRequestAuthHeaders } = await import(pathToFileURL(bundlePath).href);
  const nonceSource = {
    getRandomValues: (bytes) => {
      assert(bytes instanceof Uint8Array && bytes.length === 16, "nonce must contain 128 random bits");
      return webcrypto.getRandomValues(bytes);
    },
  };
  const modes = [
    ["full-webcrypto", webcrypto],
    ["no-randomUUID", { ...nonceSource, subtle: webcrypto.subtle }],
    ["no-subtle", nonceSource],
  ];
  const binaryView = new Uint8Array([99, 0, 255, 10, 34, 229, 99]).subarray(1, 6);
  const builderRequests = [
    ["login", "GET", "/api/stats", ""],
    ["utf8", "post", "/api/test/%E6%B5%8B%E8%AF%95?account=acct-1&filter=a%2Bb", JSON.stringify({ label: "\u6d4b\u8bd5", ok: true })],
    ["binary", "POST", "/api/test?account=acct-1", binaryView],
  ];

  for (const [mode, cryptoApi] of modes) {
    globalThis.window = browserWindow(cryptoApi);
    for (const [name, method, path, body] of builderRequests) {
      const headers = await buildRequestAuthHeaders(apiKey, method, path, body);
      recordRequest(`${mode}/${name}`, method, path, typeof body === "string" ? encoder.encode(body) : body, headers);
    }

    const longKey = "Abc123".repeat(20);
    const longBody = new Uint8Array(257).fill(255);
    const longHeaders = await buildRequestAuthHeaders(longKey, "POST", "/api/test", longBody);
    assert(longHeaders["X-Request-Signature"] === expectedSignature(longHeaders, "POST", "/api/test", longBody, longKey), `${mode}: long key/body mismatch`);

    globalThis.window = browserWindow(cryptoApi, {
      ovh_sniper_api_key: apiKey,
      backendUrl: "https://fixture.invalid",
      ovh_active_server_control_account_id: "acct-1",
    });
    for (const [name, request] of [
      ["axios-login", { url: "/stats", method: "GET" }],
      ["axios-account", { url: "/server-control/test", method: "POST", params: { filter: "a b" }, data: { label: "\u6d4b\u8bd5" } }],
    ]) {
      let capturedConfig;
      await api.request({
        ...request,
        adapter: async (config) => {
          capturedConfig = config;
          return { data: { ok: true }, status: 200, statusText: "OK", headers: {}, config };
        },
      });
      assert(capturedConfig, `${mode}/${name}: adapter was not called`);
      const target = name === "axios-login" ? "/api/stats" : "/api/server-control/test?filter=a+b&account=acct-1";
      assert(api.getUri(capturedConfig) === `https://fixture.invalid${target}`, `${mode}/${name}: final URL mismatch`);
      const headers = Object.fromEntries(
        ["X-API-Key", "X-Request-Time", "X-Request-Nonce", "X-Request-Signature"].map((header) => [header, capturedConfig.headers.get(header)]),
      );
      if (name === "axios-account") {
        assert(capturedConfig.data === JSON.stringify(request.data), `${mode}/${name}: body bytes changed`);
      }
      recordRequest(`${mode}/${name}`, request.method, target, encoder.encode(capturedConfig.data ?? ""), headers);
    }
  }
  assert(new Set(fixtures.requests.map((request) => request.headers["X-Request-Nonce"])).size === fixtures.requests.length, "nonce was reused");

  for (const [cryptoApi, message] of [
    [undefined, "Secure browser crypto is unavailable"],
    [{}, "Secure random number generation is unavailable"],
  ]) {
    globalThis.window = browserWindow(cryptoApi, { ovh_sniper_api_key: apiKey });
    await expectRejection(() => buildRequestAuthHeaders(apiKey, "GET", "/api/stats"), message, "builder fail-closed");
    let dispatched = false;
    await expectRejection(() => api.get("/stats", {
      adapter: async (config) => {
        dispatched = true;
        return { data: {}, status: 200, statusText: "OK", headers: {}, config };
      },
    }), message, "interceptor fail-closed");
    assert(!dispatched, "unsigned request reached the adapter");
  }

  for (const failingMethod of ["importKey", "sign"]) {
    const failure = new Error(`native ${failingMethod} rejected`);
    const subtle = {
      importKey: webcrypto.subtle.importKey.bind(webcrypto.subtle),
      sign: webcrypto.subtle.sign.bind(webcrypto.subtle),
      [failingMethod]: async () => { throw failure; },
    };
    globalThis.window = browserWindow({ ...nonceSource, subtle });
    await expectRejection(() => buildRequestAuthHeaders(apiKey, "GET", "/api/stats"), failure, "native crypto failure");
  }

  if (fixturesOnly) {
    process.stdout.write(`${JSON.stringify(fixtures)}\n`);
  } else {
    console.log(`http-crypto-regression-ok requests=${fixtures.requests.length} modes=full,no-randomUUID,no-subtle builder=verified interceptor=verified utf8-binary=verified account-query=verified fail-closed=verified native-errors=verified`);
  }
}

try {
  await run();
} finally {
  if (originalWindow === undefined) delete globalThis.window;
  else globalThis.window = originalWindow;
  await rm(tempRoot, { recursive: true, force: true });
}
