// The model against a fake API: snapshot, unread bookkeeping, history and event de-duplication.
import assert from "node:assert/strict";
import { test } from "node:test";
import { Api } from "../api";
import { isTargeted } from "../messages";
import { Model, ReadStore } from "../model";
import { Channel, Message, Presence } from "../types";

const SELF = "monitor@bob";

function message(channel: string, sequence: number, extra: Partial<Message> = {}): Message {
  return { sequence, channel, status: "verified", from: "a@x", ...extra };
}

// The monitor: it counts unread messages from the read markers it holds (here the store's), and
// counts none while it holds no marker at all.
class FakeApi {
  channelList: Channel[];
  constructor(private readonly store: Message[], private readonly reads: { values: Record<string, number> } = { values: {} }) {
    const ids = [...new Set(store.map((m) => m.channel))];
    this.channelList = ids.map((id) => ({
      id, kind: id === "general" ? "general" : "group", name: id.replace("group:", ""), description: "", messages: 0,
      last_sequence: Math.max(...store.filter((m) => m.channel === id).map((m) => m.sequence)), unread: 0, targeted_unread: 0,
    }));
  }
  status = async () => ({ bus: "live" as const, agent: SELF, can_send: true, note: "", password_set: true });
  channels = async () => this.channelList.map((channel) => {
    if (Object.keys(this.reads.values).length === 0) return { ...channel };
    const unread = this.store.filter((m) => m.channel === channel.id && m.sequence > (this.reads.values[channel.id] ?? 0) && m.from !== SELF);
    return { ...channel, unread: unread.length, targeted_unread: unread.filter((m) => isTargeted(m, SELF)).length };
  });
  agents = async () => [];
  presenceList: Presence[] = [];
  presence = async () => this.presenceList;
  requests: string[] = [];
  messages = async (channel: string, after: number, limit: number) => {
    this.requests.push(`after ${after}`);
    return this.store.filter((m) => m.channel === channel && m.sequence > after).slice(0, limit);
  };
  messagesBefore = async (channel: string, before: number, limit: number) => {
    this.requests.push(`before ${before}`);
    return this.store.filter((m) => m.channel === channel && m.sequence < before).slice(-limit);
  };
}

function memoryStore(initial: Record<string, number> = {}): ReadStore & { values: Record<string, number> } {
  const values = { ...initial };
  return { values, get: (id) => values[id], set: (id, sequence) => void (values[id] = sequence) };
}

function newModel(store: Message[], reads = memoryStore()) {
  return { model: new Model(new FakeApi(store, reads) as unknown as Api, reads), reads };
}

test("a channel seen for the first time counts as read", async () => {
  const { model, reads } = newModel([message("general", 1), message("general", 2)]);
  await model.refresh();
  assert.equal(reads.values["general"], 2);
  assert.equal(model.unreadCount("general"), 0);
});

test("messages after the last-read sequence are unread, targeted ones are counted for the status bar", async () => {
  const messages = [message("general", 1), message("general", 2, { targets: ["@all"] }), message("group:ops", 3, { targets: [SELF] }), message("group:ops", 4)];
  const { model } = newModel(messages, memoryStore({ general: 1, "group:ops": 2 }));
  await model.refresh();
  assert.equal(model.unreadCount("general"), 1);
  assert.equal(model.unreadCount("group:ops"), 2);
  assert.equal(model.targetedUnreadCount(), 2);
  assert.deepEqual(model.targetedUnreadChannels().map(([channel, count]) => [channel.id, count]), [["general", 1], ["group:ops", 1]]);
});

test("events keep the monitor's counts up, once, and without the agent's own messages", async () => {
  const messages = [message("general", 1), message("general", 2, { targets: [SELF] })];
  const { model } = newModel(messages, memoryStore({ general: 0 }));
  await model.refresh();
  assert.deepEqual([model.unreadCount("general"), model.targetedUnreadCount()], [2, 1]);

  const send = (m: Message) => model.apply({ event: "message", data: JSON.stringify(m) });
  await send(message("general", 2, { targets: [SELF] })); // in the monitor's count already
  await send(message("general", 3, { targets: ["@all"] }));
  await send(message("general", 4, { from: SELF }));
  assert.deepEqual([model.unreadCount("general"), model.targetedUnreadCount()], [3, 2]);
});

test("a marker at the newest message means nothing unread, also one the monitor has not counted yet", async () => {
  const { model, reads } = newModel([message("general", 1), message("general", 2, { targets: [SELF] })], memoryStore({ general: 0 }));
  await model.refresh();
  reads.values["general"] = 2; // read on another device
  assert.deepEqual([model.unreadCount("general"), model.targetedUnreadCount()], [0, 0]);
  // The next message starts a new count; the old one must not come back.
  await model.apply({ event: "message", data: JSON.stringify(message("general", 3)) });
  assert.deepEqual([model.unreadCount("general"), model.targetedUnreadCount()], [1, 0]);
});

test("markRead moves the last-read sequence to the newest loaded message", async () => {
  const { model, reads } = newModel([message("general", 1), message("general", 2), message("general", 3)], memoryStore({ general: 1 }));
  await model.refresh();
  await model.loadHistory("general", 10);
  model.markRead("general");
  assert.equal(reads.values["general"], 3);
  assert.equal(model.unreadCount("general"), 0);
});

test("an event for a message that is loaded already is a duplicate", async () => {
  const { model } = newModel([message("general", 1), message("general", 2)], memoryStore({ general: 0 }));
  await model.refresh();
  await model.loadHistory("general", 10);
  const duplicate = await model.apply({ event: "message", data: JSON.stringify(message("general", 2)) });
  assert.equal(duplicate, undefined);
  const added = await model.apply({ event: "message", data: JSON.stringify(message("general", 3)) });
  assert.equal(added?.sequence, 3);
  assert.equal(model.unreadCount("general"), 3);
});

test("the first message of a channel the model did not know is new, though the refresh it causes loads it", async () => {
  const store = [message("general", 1)];
  const { model } = newModel(store, memoryStore({ general: 1 }));
  await model.refresh();
  store.push(message("group:new", 1));
  const added = await model.apply({ event: "message", data: JSON.stringify(message("group:new", 1)) });
  assert.equal(added?.channel, "group:new");
  assert.equal(await model.apply({ event: "message", data: JSON.stringify(message("group:new", 1)) }), undefined);
});

test("loadHistory walks back page by page through older messages until there are enough", async () => {
  const sequences = Array.from({ length: 250 }, (_, i) => 10 * (i + 1)); // a sparse channel
  const api = new FakeApi(sequences.map((sequence) => message("general", sequence)));
  const model = new Model(api as unknown as Api, memoryStore());
  await model.refresh();
  assert.deepEqual(api.requests, [], "a refresh loads no history of a channel nobody opened");

  await model.loadHistory("general", 150);
  assert.deepEqual(api.requests, [`before ${Number.MAX_SAFE_INTEGER}`, "before 1510"]);
  assert.deepEqual(model.messagesOf("general").map((m) => m.sequence), sequences.slice(-200));
  assert.equal(model.hasOlder("general"), true);

  await model.loadOlder("general"); // 50 are left: a short page ends the history
  assert.equal(model.messagesOf("general").length, 250);
  assert.equal(model.hasOlder("general"), false);
});

test("asking for older messages while a page is on its way makes one request", async () => {
  const api = new FakeApi([1, 2, 3].map((sequence) => message("general", sequence)));
  const model = new Model(api as unknown as Api, memoryStore());
  await model.refresh();
  await Promise.all([model.loadOlder("general"), model.loadOlder("general")]);
  assert.deepEqual(api.requests, [`before ${Number.MAX_SAFE_INTEGER}`]);
});

test("loadUnread loads pages until the last-read sequence is held, and stops at the cap", async () => {
  const sequences = Array.from({ length: 700 }, (_, i) => 10 * (i + 1));
  const make = (read: number) => {
    const reads = memoryStore({ general: read });
    const model = new Model(new FakeApi(sequences.map((sequence) => message("general", sequence)), reads) as unknown as Api, reads);
    return model;
  };
  const fresh = make(6500); // 50 unseen: the newest page holds them
  await fresh.refresh();
  await fresh.loadUnread("general");
  assert.equal(fresh.messagesOf("general").length, 100);

  const backlog = make(5500); // 150 unseen
  await backlog.refresh();
  await backlog.loadUnread("general");
  assert.equal(backlog.messagesOf("general").length, 200);
  assert.ok(backlog.messagesOf("general")[0].sequence <= 5500);

  const never = make(0); // never read: all 700 are unseen, so the preload stops at its cap
  await never.refresh();
  await never.loadUnread("general");
  assert.equal(never.messagesOf("general").length, 500);
});

test("a refresh brings the channels that have messages loaded up to date, and only those", async () => {
  const store = [message("general", 1), message("group:ops", 2)];
  const api = new FakeApi(store);
  const model = new Model(api as unknown as Api, memoryStore());
  await model.refresh();
  await model.loadHistory("general", 10);
  api.requests.length = 0;
  store.push(message("general", 3), message("group:ops", 4));
  api.channelList = new FakeApi(store).channelList;
  await model.refresh();
  assert.deepEqual(api.requests, ["after 1"]);
  assert.deepEqual(model.messagesOf("general").map((m) => m.sequence), [1, 3]);
  assert.deepEqual(model.messagesOf("group:ops"), []);
});

test("a channel that appears after the first refresh starts unread, its message targeted", async () => {
  const store = [message("general", 1)];
  const reads = memoryStore();
  const api = new FakeApi(store, reads);
  const model = new Model(api as unknown as Api, reads);
  await model.refresh();
  store.push(message("group:new", 2, { targets: [SELF] }));
  api.channelList = new FakeApi(store, reads).channelList;
  await model.refresh();
  assert.equal(model.unreadCount("group:new"), 1);
  assert.equal(model.targetedUnreadCount(), 1);
  assert.equal(model.unreadCount("general"), 0);
});

test("presence comes from the snapshot and from presence events, and a change is announced", async () => {
  const api = new FakeApi([message("general", 1)]);
  api.presenceList = [{ agent: "a@x", state: "idle" }];
  const model = new Model(api as unknown as Api, memoryStore());
  await model.refresh();
  assert.equal(model.presence.get("a@x")?.state, "idle");
  let changes = 0;
  model.onChange(() => changes++);
  const view: Presence = { agent: "a@x", state: "working", activity: "Bash npm", workers: [{ description: "tests", since: "2026-11-24T12:00:00Z" }] };
  await model.apply({ event: "presence", data: JSON.stringify(view), id: undefined });
  assert.deepEqual(model.presence.get("a@x"), view);
  assert.equal(changes, 1);
});

test("refreshState announces a new channel or agent, and nothing when the state is the same", async () => {
  const api = new FakeApi([message("general", 1)]);
  const model = new Model(api as unknown as Api, memoryStore());
  await model.refresh();
  let changes = 0;
  model.onChange(() => changes++);
  await model.refreshState();
  assert.equal(changes, 0);
  api.channelList = [...api.channelList, { id: "group:empty", kind: "group", name: "empty", description: "", messages: 0, last_sequence: 0, unread: 0, targeted_unread: 0 }];
  await model.refreshState();
  assert.deepEqual(model.channels.map((channel) => channel.id), ["general", "group:empty"]);
  assert.equal(changes, 1);
  await model.refreshState();
  assert.equal(changes, 1);
});

test("polling refreshes the state until it is stopped, and starting twice makes one timer", async () => {
  const api = new FakeApi([message("general", 1)]);
  const model = new Model(api as unknown as Api, memoryStore());
  await model.refresh();
  let changes = 0;
  model.onChange(() => changes++);
  model.startPolling(1, assert.ifError);
  model.startPolling(1, assert.ifError);
  api.presenceList = [{ agent: "a@x", state: "idle" }];
  while (changes === 0) await new Promise((resolve) => setTimeout(resolve, 2));
  model.stopPolling();
  assert.equal(model.presence.get("a@x")?.state, "idle");
  api.presenceList = [];
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(model.presence.size, 1); // stopped: the change is not read
});
