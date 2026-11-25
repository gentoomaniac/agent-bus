import assert from "node:assert/strict";
import { test } from "node:test";
import { categorize, describeChannel, destinationOf, parseTargets } from "../channels";
import { nextBackoff } from "../events";
import { insertMessage, isTargeted, unreadMessages } from "../messages";
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

test("parseTargets splits on commas and spaces", () => {
  assert.deepEqual(parseTargets(" a@x, @all  b@y,"), ["a@x", "@all", "b@y"]);
  assert.deepEqual(parseTargets(""), []);
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
