import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";

function loadTs(path, modules) {
  const source = readFileSync(new URL(path, import.meta.url), "utf8");
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
  });
  const module = { exports: {} };
  const run = vm.runInNewContext(`(function (require, module, exports) { ${outputText}\n})`, { Error });
  run((name) => {
    if (!(name in modules)) throw new Error(`Unexpected import: ${name}`);
    return modules[name];
  }, module, module.exports);
  return module.exports;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function equalDeps(a, b) {
  return a.length === b.length && a.every((item, index) => Object.is(item, b[index]));
}

// Exercise the real hook source without a DOM test dependency.
class HookHarness {
  constructor(queryFn, deps = []) {
    this.queryFn = queryFn;
    this.deps = deps;
    this.slots = [];
    this.unmounted = false;
    this.updatesAfterUnmount = 0;
    const react = {
      useState: (initial) => {
        const index = this.cursor++;
        if (!(index in this.slots)) this.slots[index] = initial;
        return [this.slots[index], (value) => {
          if (this.unmounted) this.updatesAfterUnmount++;
          this.slots[index] = typeof value === "function" ? value(this.slots[index]) : value;
          this.scheduleRender();
        }];
      },
      useRef: (initial) => {
        const index = this.cursor++;
        if (!(index in this.slots)) this.slots[index] = { current: initial };
        return this.slots[index];
      },
      useCallback: (callback, depsForCallback) => {
        const index = this.cursor++;
        const previous = this.slots[index];
        if (previous && equalDeps(previous.deps, depsForCallback)) return previous.callback;
        this.slots[index] = { deps: [...depsForCallback], callback };
        return callback;
      },
      useEffect: (setup, depsForEffect) => {
        const index = this.cursor++;
        const previous = this.slots[index];
        if (!previous || !equalDeps(previous.deps, depsForEffect)) {
          this.pendingEffects.push(() => {
            previous?.cleanup?.();
            this.slots[index] = { deps: [...depsForEffect], cleanup: setup() };
          });
        }
      },
    };
    const { useApiQuery } = loadTs("../src/hooks/useApi.ts", {
      react,
      "@/lib/api": { default: {} },
    });
    this.useApiQuery = useApiQuery;
    this.render();
  }

  render() {
    if (this.unmounted) return;
    this.cursor = 0;
    this.pendingEffects = [];
    this.result = this.useApiQuery(this.queryFn, this.deps);
    for (const effect of this.pendingEffects) effect();
  }

  scheduleRender() {
    if (this.unmounted || this.queued) return;
    this.queued = true;
    queueMicrotask(() => {
      this.queued = false;
      this.render();
    });
  }

  changeQuery(queryFn, deps) {
    this.queryFn = queryFn;
    this.deps = deps;
    this.render();
  }

  unmount() {
    this.unmounted = true;
    for (const slot of this.slots) slot?.cleanup?.();
  }
}

function flush() {
  return new Promise((resolve) => setImmediate(resolve));
}

test("old result cannot overwrite a newer refresh or end its loading state", async () => {
  const old = deferred();
  const latest = deferred();
  let calls = 0;
  const hook = new HookHarness(() => ++calls === 1 ? old.promise : latest.promise);
  const refresh = hook.result.refetch();
  assert.equal(calls, 2);
  old.resolve("old");
  await flush();
  assert.equal(hook.result.data, null);
  assert.equal(hook.result.isLoading, true);
  latest.resolve("new");
  assert.equal(await refresh, true);
  await flush();
  assert.equal(hook.result.data, "new");
  assert.equal(hook.result.isLoading, false);
  hook.unmount();
});

test("late old result cannot replace completed refresh", async () => {
  const old = deferred();
  const latest = deferred();
  let calls = 0;
  const hook = new HookHarness(() => ++calls === 1 ? old.promise : latest.promise);
  const refresh = hook.result.refetch();
  latest.resolve("new");
  assert.equal(await refresh, true);
  await flush();
  old.resolve("old");
  await flush();
  assert.equal(hook.result.data, "new");
  hook.unmount();
});

test("stale failure cannot replace a newer successful refresh", async () => {
  const old = deferred();
  const latest = deferred();
  let calls = 0;
  const hook = new HookHarness(() => ++calls === 1 ? old.promise : latest.promise);
  const refresh = hook.result.refetch();
  latest.resolve("new");
  assert.equal(await refresh, true);
  await flush();
  old.reject(new Error("obsolete error"));
  await flush();
  assert.equal(hook.result.data, "new");
  assert.equal(hook.result.error, null);
  hook.unmount();
});

test("stale success cannot replace a newer failed refresh", async () => {
  const old = deferred();
  const latest = deferred();
  let calls = 0;
  const hook = new HookHarness(() => ++calls === 1 ? old.promise : latest.promise);
  const refresh = hook.result.refetch();
  latest.reject(new Error("current error"));
  assert.equal(await refresh, false);
  await flush();
  old.resolve("obsolete success");
  await flush();
  assert.equal(hook.result.data, null);
  assert.equal(hook.result.error.message, "current error");
  assert.equal(hook.result.isLoading, false);
  hook.unmount();
});

test("refresh failure resolves false and keeps the last successful data", async () => {
  let request = async () => "cached";
  const hook = new HookHarness(() => request());
  await flush();
  request = async () => { throw new Error("offline"); };
  assert.equal(await hook.result.refetch(), false);
  await flush();
  assert.equal(hook.result.data, "cached");
  assert.equal(hook.result.error.message, "offline");
  assert.equal(hook.result.isLoading, false);
  hook.unmount();
});

test("dependency change and unmount invalidate in-flight results", async () => {
  const old = deferred();
  const latest = deferred();
  const hook = new HookHarness(() => old.promise, ["A"]);
  hook.changeQuery(() => latest.promise, ["B"]);
  latest.resolve("B");
  await flush();
  old.resolve("A");
  await flush();
  assert.equal(hook.result.data, "B");
  const later = deferred();
  hook.changeQuery(() => later.promise, ["C"]);
  hook.unmount();
  later.resolve("ignored");
  await flush();
  assert.equal(hook.updatesAfterUnmount, 0);
  assert.equal(hook.result.data, "B");
});

test("unmounted manual refresh resolves false without state updates", async () => {
  const pending = deferred();
  const hook = new HookHarness(() => pending.promise);
  const refresh = hook.result.refetch();
  hook.unmount();
  pending.resolve("too late");
  assert.equal(await refresh, false);
  await flush();
  assert.equal(hook.result.data, null);
  assert.equal(hook.updatesAfterUnmount, 0);
});

test("saved refetch after unmount does not start another request", async () => {
  let calls = 0;
  const hook = new HookHarness(async () => ++calls);
  await flush();
  const savedRefetch = hook.result.refetch;
  hook.unmount();
  assert.equal(await savedRefetch(), false);
  assert.equal(calls, 1);
  assert.equal(hook.updatesAfterUnmount, 0);
});

test("contact requests propagate HTTP errors and retain success parsing", async () => {
  let request = async () => { throw new Error("HTTP 500"); };
  const { api } = loadTs("../src/lib/api.ts", {
    "./http": { apiRequest: (...args) => request(...args) },
  });
  for (const message of ["HTTP 401", "HTTP 500", "network offline"]) {
    request = async () => { throw new Error(message); };
    await assert.rejects(api.getContactChangeRequests(), new RegExp(message));
  }
  request = async (url) => {
    assert.equal(url, "/api/ovh/contact-change-requests");
    return { status: "success", data: [{ id: 7 }] };
  };
  const response = await api.getContactChangeRequests();
  assert.equal(response.success, true);
  assert.equal(response.requests[0].id, 7);
  assert.equal(response.data[0].id, 7);
  request = async () => ({ status: "success", data: [] });
  const empty = await api.getContactChangeRequests();
  assert.equal(empty.success, true);
  assert.equal(empty.requests.length, 0);
});
