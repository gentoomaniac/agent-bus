// The model against a fake API: snapshot, unread bookkeeping, history and event de-duplication.
import assert from "node:assert/strict";
import { test } from "node:test";
import { Api } from "../api";
import { Model, ReadStore } from "../model";
import { Channel, Message } from "../types";

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
  presence = async () => [];
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

test("loadHistory walks back through older messages until there are enough", async () => {
  const all = [1, 2, 3, 900, 1400].map((sequence) => message("general", sequence));
  const { model } = newModel(all);
  await model.refresh();
  await model.loadHistory("general", 5);
  assert.deepEqual(model.messagesOf("general").map((m) => m.sequence), [1, 2, 3, 900, 1400].slice(0, 5));
  assert.equal(model.hasOlder("general"), false);
});
