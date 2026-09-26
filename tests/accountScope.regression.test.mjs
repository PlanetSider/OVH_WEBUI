import test from "node:test";
import assert from "node:assert/strict";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { accountQueryKey, accountRequestConfig } from "../src/hooks/ovh/account-scope-utils.ts";

test("account request overrides an earlier account without mutating input", () => {
  const input = { params: { account: "stale", limit: 10 } };
  const scoped = accountRequestConfig("current", input);
  assert.deepEqual(scoped.params, { account: "current", limit: 10 });
  assert.equal(scoped.injectAccount, false);
  assert.deepEqual(input.params, { account: "stale", limit: 10 });
  assert.throws(() => accountRequestConfig(""), /选择 OVH 账户/);
});

test("late previous-account response never becomes current-account query data", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const keys = {
    A: accountQueryKey(["server-control", "list"], "A"),
    B: accountQueryKey(["server-control", "list"], "B"),
  };
  assert.notDeepEqual(keys.A, keys.B);

  const resolvers = {};
  const fetchAccount = ({ queryKey }) => new Promise((resolve) => {
    resolvers[queryKey.at(-1)] = resolve;
  });
  const observer = new QueryObserver(client, { queryKey: keys.A, queryFn: fetchAccount });
  const unsubscribe = observer.subscribe(() => {});
  try {
    observer.setOptions({ queryKey: keys.B, queryFn: fetchAccount });
    resolvers.A(["server-A"]);
    await Promise.resolve();
    assert.notDeepEqual(observer.getCurrentResult().data, ["server-A"]);
    resolvers.B(["server-B"]);
    await Promise.resolve();
    assert.deepEqual(await client.fetchQuery({ queryKey: keys.B, queryFn: fetchAccount, staleTime: Infinity }), ["server-B"]);
    assert.deepEqual(observer.getCurrentResult().data, ["server-B"]);
  } finally {
    unsubscribe();
    client.clear();
  }
});
