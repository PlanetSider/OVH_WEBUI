import { strict as assert } from "node:assert";
import { test } from "node:test";
import { isValidQueueBatch, MAX_QUEUE_BATCH_TASKS, mergeQueueOptions, resolveImportedQueueOptions, subsidiaryForQueueAccount } from "./purchase-guards.ts";

test("batch quantity requires positive safe integers and at most 200 total tasks", () => {
  assert.equal(MAX_QUEUE_BATCH_TASKS, 200);
  for (const invalid of [Number(""), 0, -1, 1.5, NaN, Infinity, 201, 100000, Number.MAX_SAFE_INTEGER + 1]) {
    assert.equal(isValidQueueBatch(invalid, 1), false, `quantity ${invalid}`);
  }
  for (const dcCount of [0, -1, 1.5, NaN, Infinity, 201]) {
    assert.equal(isValidQueueBatch(1, dcCount), false, `datacenters ${dcCount}`);
  }
  assert.equal(isValidQueueBatch(1, 200), true);
  assert.equal(isValidQueueBatch(2, 100), true);
  assert.equal(isValidQueueBatch(3, 67), false);
  assert.equal(isValidQueueBatch(20, 10), true);
  assert.equal(isValidQueueBatch(21, 10), false);
});

test("URL addons survive catalog arrival and merge with grouped selections", () => {
  const imported = "ram-64, custom-addon, ram-64";
  const beforeCatalog = resolveImportedQueueOptions(imported, null);
  assert.deepEqual(mergeQueueOptions(beforeCatalog.picked, beforeCatalog.extras.join(", ")), ["ram-64", "custom-addon"]);

  const afterCatalog = resolveImportedQueueOptions(imported, {
    memory: [{ value: "ram-64" }, { value: "ram-128" }],
    storage: [{ value: "storage-nvme" }],
  });
  assert.deepEqual(afterCatalog.picked, { memory: "ram-64" });
  assert.deepEqual(afterCatalog.extras, ["custom-addon"]);
  assert.deepEqual(mergeQueueOptions(afterCatalog.picked, "custom-addon, storage-nvme, ram-64"),
    ["ram-64", "custom-addon", "storage-nvme"]);
  assert.deepEqual(resolveImportedQueueOptions("legacy-option", {
    memory: [{ value: "ram-64" }], storage: [],
  }).extras, ["legacy-option"]);
});

test("account subsidiary follows backend zone-first and endpoint fallback", () => {
  assert.equal(subsidiaryForQueueAccount(null), null);
  assert.equal(subsidiaryForQueueAccount({ zone: " GB ", endpoint: "ovh-us" }), "GB");
  assert.equal(subsidiaryForQueueAccount({ zone: "", endpoint: "OVH-US" }), "US");
  assert.equal(subsidiaryForQueueAccount({ zone: " ", endpoint: "ovh-ca" }), "CA");
  assert.equal(subsidiaryForQueueAccount({ zone: "", endpoint: "kimsufi-ca" }), "CA");
  assert.equal(subsidiaryForQueueAccount({ zone: "", endpoint: "soyoustart-ca" }), "CA");
  assert.equal(subsidiaryForQueueAccount({ zone: "", endpoint: "ovh-eu" }), "IE");
});
