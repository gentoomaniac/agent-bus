import assert from "node:assert/strict";
import { test } from "node:test";
import { ago, describePresence, recentActivity } from "../presence";
import { matchTargets, mentionBefore } from "../completion";
import { categorize, describeChannel, destinationOf } from "../channels";
import { nextBackoff } from "../events";
import { continuesGroup, insertMessage, isTargeted, unreadMessages } from "../messages";
import { SseParser } from "../sse";
import { Channel, Message } from "../types";

const message = (sequence: number, extra: Partial<Message> = {}): Message => ({ sequence, channel: "general", status: "verified", ...extra });
const channel = (extra: Partial<Channel>): Channel => ({ id: "general", kind: "general", name: "general", description: "", messages: 0, last_sequence: 0, ...extra });

test("SseParser reads events split across chunks and drops comments", () => {
  const parser = new SseParser();
  assert.deepEqual(parser.push(": connected\n\nid: general#12\nevent: mess"), []);
  const events = parser.push('age\ndata: {"a":1}\n\n: keep-alive\n\nevent: presence\r\ndata: x\r\ndata: y\r\n\r\n');
  assert.deepEqual(events, [
    { id: "general#12", event: "message", data: '{"a":1}' },
    { id: undefined, event: "presence", data: "x\ny" },
  ]);
});

test("SseParser defaults the event name to message and ignores events without data", () => {
  const parser = new SseParser();
  assert.deepEqual(parser.push("event: empty\n\ndata\n\ndata: z\n\n"), [
    { id: undefined, event: "message", data: "" },
    { id: undefined, event: "message", data: "z" },
  ]);
});

test("insertMessage keeps sequence order and drops duplicates", () => {
  const list: Message[] = [];
  assert.equal(insertMessage(list, message(3)), true);
  assert.equal(insertMessage(list, message(1)), true);
  assert.equal(insertMessage(list, message(2)), true);
  assert.equal(insertMessage(list, message(2, { text: "again" })), false);
  assert.deepEqual(list.map((m) => m.sequence), [1, 2, 3]);
  assert.equal(list[1].text, undefined);
});

test("isTargeted counts @all and the agent, not its own messages", () => {
  const me = "monitor@bob";
  assert.equal(isTargeted(message(1, { targets: ["@all"] }), me), true);
  assert.equal(isTargeted(message(1, { targets: ["a@x", me] }), me), true);
  assert.equal(isTargeted(message(1, { targets: ["a@x"] }), me), false);
  assert.equal(isTargeted(message(1), me), false);
  assert.equal(isTargeted(message(1, { from: me, targets: ["@all"] }), me), false);
});

test("unreadMessages counts what follows the last-read sequence, without own messages", () => {
  const list = [message(1), message(2), message(3, { from: "me@x" }), message(4)];
  assert.deepEqual(unreadMessages(list, 1, "me@x").map((m) => m.sequence), [2, 4]);
  assert.deepEqual(unreadMessages(list, 4, "me@x"), []);
});

test("describeChannel names channels as the web page does", () => {
  assert.equal(describeChannel(channel({})), "# general");
  assert.equal(describeChannel(channel({ id: "group:ops", kind: "group", name: "ops" })), "# ops");
  assert.equal(describeChannel(channel({ id: "dm:a:b", kind: "dm", name: "a@x + b@y", agents: ["a@x", "b@y"] })), "a@x ↔ b@y");
});

test("destinationOf aims at the channel, or at the peer of a DM the agent is in", () => {
  assert.deepEqual(destinationOf(channel({}), "me"), { kind: "group", name: "general" });
  assert.deepEqual(destinationOf(channel({ kind: "group", name: "ops" }), "me"), { kind: "group", name: "ops" });
  const dm = channel({ kind: "dm", agents: ["a@x", "b@y"] });
  assert.deepEqual(destinationOf(dm, "b@y"), { kind: "agent", name: "a@x" });
  assert.equal(destinationOf(dm, "c@z"), undefined);
});

test("nextBackoff doubles up to the maximum", () => {
  assert.equal(nextBackoff(1000, 30000), 2000);
  assert.equal(nextBackoff(20000, 30000), 30000);
  assert.equal(nextBackoff(30000, 30000), 30000);
});

test("categorize moves favourites out of their category and ignores unknown ones", () => {
  const general = channel({});
  const group = channel({ id: "group:ops", kind: "group", name: "ops" });
  const dm = channel({ id: "dm:a:b", kind: "dm", name: "a + b" });
  const ids = (list: Channel[]) => list.map((c) => c.id);

  const plain = categorize([general, group, dm], new Set());
  assert.deepEqual([ids(plain.favourites), ids(plain.channels), ids(plain.directMessages)], [[], ["general", "group:ops"], ["dm:a:b"]]);

  const starred = categorize([general, group, dm], new Set(["dm:a:b", "group:ops", "group:gone"]));
  assert.deepEqual([ids(starred.favourites), ids(starred.channels), ids(starred.directMessages)], [["group:ops", "dm:a:b"], ["general"], []]);
});

test("continuesGroup joins the same sender within five minutes only", () => {
  const first = message(1, { from: "a@x", time: "2026-11-24T12:00:00Z", targets: ["@all"] });
  const at = (time: string, extra: Partial<Message> = {}) => message(2, { from: "a@x", targets: ["@all"], time, ...extra });
  assert.equal(continuesGroup(undefined, first), false);
  assert.equal(continuesGroup(first, at("2026-11-24T12:05:00Z")), true);
  assert.equal(continuesGroup(first, at("2026-11-24T12:05:01Z")), false);
  assert.equal(continuesGroup(first, at("2026-11-24T12:01:00Z", { from: "b@y" })), false);
  assert.equal(continuesGroup(first, at("2026-11-24T12:01:00Z", { type: "question" })), false);
  assert.equal(continuesGroup(first, at("2026-11-24T12:01:00Z", { targets: ["b@y"] })), false);
  assert.equal(continuesGroup(first, at("2026-11-24T12:01:00Z", { title: "New topic" })), false);
  assert.equal(continuesGroup(first, at("not a time")), false);
});

test("describePresence says state, activity and how long; ago counts s, m and h", () => {
  const now = Date.parse("2026-11-24T12:10:00Z");
  assert.equal(ago("2026-11-24T12:09:48Z", now), "12s");
  assert.equal(ago("2026-11-24T12:05:00Z", now), "5m");
  assert.equal(ago("2026-11-24T09:10:00Z", now), "3h");
  assert.equal(ago(undefined, now), "");
  assert.equal(describePresence(undefined, now), "offline");
  assert.equal(describePresence({ agent: "a", state: "idle" }, now), "idle");
  assert.equal(describePresence({ agent: "a", state: "working", activity: "Bash npm", activity_since: "2026-11-24T12:09:57Z" }, now), "working · Bash npm (3s)");
});

test("recentActivity is newest first and limited", () => {
  const recent = [1, 2, 3, 4].map((n) => ({ at: "t" + n, text: "e" + n }));
  assert.deepEqual(recentActivity({ agent: "a", state: "idle", recent }, 2).map((e) => e.text), ["e4", "e3"]);
  assert.deepEqual(recentActivity(undefined, 2), []);
});

test("matchTargets matches by prefix on the whole ID or the name, ignoring case and chosen targets", () => {
  const all = ["@all", "lib-x@alice", "Lib-Y@bob", "web@alice"];
  assert.deepEqual(matchTargets(all, ""), all);
  assert.deepEqual(matchTargets(all, "a"), ["@all"]);
  assert.deepEqual(matchTargets(all, "@lib"), ["lib-x@alice", "Lib-Y@bob"]);
  assert.deepEqual(matchTargets(all, "LIB-x@al"), ["lib-x@alice"]);
  assert.deepEqual(matchTargets(all, "alice"), []);
  assert.deepEqual(matchTargets(all, "lib", ["lib-x@alice"]), ["Lib-Y@bob"]);
});

test("mentionBefore finds an @word ending at the caret", () => {
  assert.deepEqual(mentionBefore("hi @lib-x@al", 12), { start: 3, query: "lib-x@al" });
  assert.deepEqual(mentionBefore("@", 1), { start: 0, query: "" });
  assert.deepEqual(mentionBefore("hi @lib there", 7), { start: 3, query: "lib" });
  assert.equal(mentionBefore("mail a@b", 8), undefined);
  assert.equal(mentionBefore("hi @lib there", 13), undefined);
});
