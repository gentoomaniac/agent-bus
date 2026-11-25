// The shared prefs against a fake monitor: merge, coalesced read posts, replaced lists, migration.
import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError } from "../api";
import { Prefs, PrefsApi } from "../prefs";
import { ServerPrefs } from "../types";

// A monitor that keeps what it is sent and can hold a request back.
class FakeMonitor implements PrefsApi {
  stored: ServerPrefs = { read: {}, favourites: [], collapsed: [] };
  readPosts: Record<string, number>[] = [];
  replaced: string[][] = [];
  failing: Error | undefined;
  private release: (() => void) | undefined;
  private held: Promise<void> | undefined;

  hold(): void {
    this.held = new Promise((resolve) => (this.release = resolve));
  }
  letGo(): void {
    this.release?.();
    this.held = undefined;
  }
  private async request(): Promise<void> {
    await this.held;
    if (this.failing) throw this.failing;
  }
  async prefs() {
    await this.request();
    return structuredClone(this.stored);
  }
  async postRead(updates: Record<string, number>) {
    this.readPosts.push(updates);
    await this.request();
    for (const [id, sequence] of Object.entries(updates)) this.stored.read[id] = Math.max(this.stored.read[id] ?? 0, sequence);
    return { ...this.stored.read };
  }
  async putFavourites(channels: string[]) {
    await this.request();
    this.replaced.push(channels);
    this.stored.favourites = channels;
    return this.stored;
  }
  async putCollapsed(sections: string[]) {
    await this.request();
    this.stored.collapsed = sections;
    return this.stored;
  }
}

function newPrefs(monitor = new FakeMonitor()) {
  const errors: unknown[] = [];
  let changes = 0;
  const prefs = new Prefs(monitor, () => changes++, (error) => errors.push(error));
  return { prefs, monitor, errors, changes: () => changes };
}

test("a marker only moves forward", async () => {
  const { prefs, monitor } = newPrefs();
  await prefs.load();
  prefs.set("general", 5);
  prefs.set("general", 3);
  await prefs.flush();
  assert.equal(prefs.get("general"), 5);
  assert.deepEqual(monitor.readPosts, [{ general: 5 }]);
});

test("markers made while a post is in flight go out together afterwards, the highest per channel", async () => {
  const { prefs, monitor } = newPrefs();
  await prefs.load();
  monitor.hold();
  prefs.set("general", 1);
  prefs.set("general", 2);
  prefs.set("group:a", 7);
  prefs.set("general", 4);
  monitor.letGo();
  await prefs.flush();
  assert.deepEqual(monitor.readPosts, [{ general: 1 }, { general: 4, "group:a": 7 }]);
});

test("a failed post is reported and its markers go out with the next one", async () => {
  const { prefs, monitor, errors } = newPrefs();
  await prefs.load();
  monitor.failing = new Error("down");
  prefs.set("general", 3);
  await prefs.flush();
  assert.equal(errors.length, 1);
  monitor.failing = undefined;
  prefs.set("group:a", 1);
  await prefs.flush();
  assert.deepEqual(monitor.readPosts.at(-1), { general: 3, "group:a": 1 });
  assert.deepEqual(monitor.stored.read, { general: 3, "group:a": 1 });
});

test("applying the monitor's prefs keeps a newer local marker and replaces the lists", () => {
  const { prefs, changes } = newPrefs();
  prefs.set("general", 9);
  prefs.apply({ read: { general: 4, "group:a": 2 }, favourites: ["group:a"], collapsed: ["dms"] });
  assert.equal(prefs.get("general"), 9);
  assert.equal(prefs.get("group:a"), 2);
  assert.deepEqual([...prefs.favourites], ["group:a"]);
  assert.deepEqual([...prefs.collapsed], ["dms"]);
  assert.equal(changes(), 1);
});

test("setFavourite sets, and does nothing when the state is the wanted one", async () => {
  const { prefs, monitor, changes } = newPrefs();
  await prefs.load();
  prefs.setFavourite("general", true);
  prefs.setFavourite("general", true);
  prefs.setFavourite("group:a", false);
  await new Promise((resolve) => setImmediate(resolve));
  prefs.setFavourite("general", false);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(changes(), 3); // the load and the two changes
  assert.deepEqual(monitor.replaced, [["general"], []]);
});

test("the echo of an own change does not undo a newer one that is still to be sent", async () => {
  const { prefs, monitor } = newPrefs();
  await prefs.load();
  prefs.setFavourite("a", true);
  prefs.setFavourite("b", true);
  prefs.apply({ read: {}, favourites: ["a"], collapsed: [] }); // the monitor's event for the first PUT
  assert.deepEqual([...prefs.favourites], ["a", "b"]);
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(monitor.stored.favourites, ["a", "b"]);
});

test("the legacy prefs move to an empty monitor once", async () => {
  const { prefs, monitor } = newPrefs();
  const moved = await prefs.load({ read: { general: 8 }, favourites: ["group:a"], collapsed: ["dms"] });
  assert.equal(moved, true);
  assert.deepEqual(monitor.stored, { read: { general: 8 }, favourites: ["group:a"], collapsed: ["dms"] });
  assert.equal(prefs.get("general"), 8);
  assert.deepEqual([...prefs.favourites], ["group:a"]);
});

test("the legacy prefs are dropped without being sent when the monitor has prefs", async () => {
  const monitor = new FakeMonitor();
  monitor.stored = { read: { general: 2 }, favourites: [], collapsed: [] };
  const { prefs } = newPrefs(monitor);
  const moved = await prefs.load({ read: { general: 8 }, favourites: ["group:a"], collapsed: [] });
  assert.equal(moved, true);
  assert.deepEqual(monitor.stored, { read: { general: 2 }, favourites: [], collapsed: [] });
  assert.equal(prefs.get("general"), 2);
});

test("without prefs on the monitor the state stays in memory and nothing is sent", async () => {
  const monitor = new FakeMonitor();
  monitor.failing = new ApiError(404, "not_found", "no such endpoint");
  const { prefs, errors } = newPrefs(monitor);
  assert.equal(await prefs.load(), false);
  assert.equal(errors.length, 1);
  prefs.set("general", 3);
  prefs.setFavourite("general", true);
  await prefs.flush();
  assert.equal(prefs.get("general"), 3);
  assert.equal(prefs.favourites.has("general"), true);
  assert.deepEqual(monitor.readPosts, []);
});

test("a login that ended is thrown by load", async () => {
  const monitor = new FakeMonitor();
  monitor.failing = new ApiError(401, "unauthenticated", "log in first");
  await assert.rejects(newPrefs(monitor).prefs.load(), ApiError);
});
