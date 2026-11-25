// The model against a fake API: snapshot, unread bookkeeping, history and event de-duplication.
import assert from "node:assert/strict";
import { test } from "node:test";
import { Api } from "../api";
import { Model, ReadStore } from "../model";
import { Channel, Message, Presence } from "../types";

const SELF = "monitor@bob";

function message(channel: string, sequence: number, extra: Partial<Message> = {}): Message {
  return { sequence, channel, status: "verified", from: "a@x", ...extra };
}

class FakeApi {
  channelList: Channel[];
  constructor(private readonly store: Message[]) {
    const ids = [...new Set(store.map((m) => m.channel))];
    this.channelList = ids.map((id) => ({
      id, kind: id === "general" ? "general" : "group", name: id.replace("group:", ""), description: "", messages: 0,
      last_sequence: Math.max(...store.filter((m) => m.channel === id).map((m) => m.sequence)),
    }));
  }
  status = async () => ({ bus: "live" as const, agent: SELF, can_send: true, note: "", password_set: true });
  channels = async () => this.channelList;
  agents = async () => [];
  presenceList: Presence[] = [];
  presence = async () => this.presenceList;
  messages = async (channel: string, after: number, limit: number) =>
    this.store.filter((m) => m.channel === channel && m.sequence > after).slice(0, limit);
}

function memoryStore(initial: Record<string, number> = {}): ReadStore & { values: Record<string, number> } {
  const values = { ...initial };
  return { values, get: (id) => values[id], set: (id, sequence) => void (values[id] = sequence) };
}

function newModel(store: Message[], reads = memoryStore()) {
  return { model: new Model(new FakeApi(store) as unknown as Api, reads), reads };
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

test("markRead moves the last-read sequence to the newest loaded message", async () => {
  const { model, reads } = newModel([message("general", 1), message("general", 2), message("general", 3)], memoryStore({ general: 1 }));
  await model.refresh();
  model.markRead("general");
  assert.equal(reads.values["general"], 3);
  assert.equal(model.unreadCount("general"), 0);
});

test("an event for a message that a refresh already loaded is a duplicate", async () => {
  const { model } = newModel([message("general", 1), message("general", 2)], memoryStore({ general: 0 }));
  await model.refresh();
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

test("loadHistory walks back through older messages until there are enough", async () => {
  const all = [1, 2, 3, 900, 1400].map((sequence) => message("general", sequence));
  const { model } = newModel(all);
  await model.refresh();
  await model.loadHistory("general", 5);
  assert.deepEqual(model.messagesOf("general").map((m) => m.sequence), [1, 2, 3, 900, 1400].slice(0, 5));
  assert.equal(model.hasOlder("general"), false);
});

test("a channel that appears after the first refresh starts unread, its message targeted", async () => {
  const store = [message("general", 1)];
  const api = new FakeApi(store);
  const model = new Model(api as unknown as Api, memoryStore());
  await model.refresh();
  store.push(message("group:new", 2, { targets: [SELF] }));
  api.channelList = new FakeApi(store).channelList;
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
  api.channelList = [...api.channelList, { id: "group:empty", kind: "group", name: "empty", description: "", messages: 0, last_sequence: 0 }];
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
