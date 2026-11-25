// Which live messages notify, how the notification reads, and how a burst is coalesced.
import assert from "node:assert/strict";
import { test } from "node:test";
import { Coalescer, describeNotification, muteLabel, notifyReason, parseNotifyMode } from "../notify";
import { Message } from "../types";

const SELF = "monitor@bob";
const subscribed = new Set(["group:ops"]);

function message(extra: Partial<Message>): Message {
  return { sequence: 1, channel: "general", status: "verified", from: "a@x", targets: [], ...extra };
}
const reason = (extra: Partial<Message>, mode = parseNotifyMode(undefined), self = SELF) => notifyReason(message(extra), self, subscribed, mode);

test("a message to the agent or to @all notifies", () => {
  assert.equal(reason({ targets: [SELF] }), "targeted");
  assert.equal(reason({ targets: ["other@x", "@all"] }), "targeted");
  assert.equal(reason({ targets: ["other@x"] }), undefined);
  assert.equal(reason({}), undefined);
});

test("a direct message to the agent notifies without targets", () => {
  assert.equal(reason({ channel: "dm:a@x:monitor@bob", to: SELF }), "targeted");
  assert.equal(reason({ channel: "dm:a@x:other@x", to: "other@x" }), undefined);
});

test("a message in a subscribed channel notifies, and that wins over being addressed", () => {
  assert.equal(reason({ channel: "group:ops" }), "subscribed");
  assert.equal(reason({ channel: "group:ops", targets: [SELF] }), "subscribed");
});

test("the own messages never notify, subscribed or not", () => {
  assert.equal(reason({ from: SELF, targets: ["@all"] }), undefined);
  assert.equal(reason({ from: SELF, channel: "group:ops" }), undefined);
});

test("the mode narrows or turns off", () => {
  assert.equal(reason({ targets: ["@all"] }, "subscribedOnly"), undefined);
  assert.equal(reason({ channel: "group:ops" }, "subscribedOnly"), "subscribed");
  assert.equal(reason({ channel: "group:ops", targets: ["@all"] }, "off"), undefined);
  assert.equal(parseNotifyMode("nonsense"), "targetedAndSubscribed");
});

test("without an agent only subscriptions notify", () => {
  assert.equal(reason({ targets: ["@all"] }, "targetedAndSubscribed", ""), undefined);
  assert.equal(reason({ channel: "group:ops", from: undefined }, "targetedAndSubscribed", ""), "subscribed");
});

test("the notification names sender and channel and shows the title, else the first line", () => {
  assert.equal(describeNotification(message({ title: "Deploy", text: "body" }), "# ops"), "a@x in # ops: Deploy");
  assert.equal(describeNotification(message({ text: "\n first\nsecond" }), "# ops"), "a@x in # ops: first");
});

test("the part after the colon is at most 80 characters", () => {
  const text = describeNotification(message({ text: "x".repeat(200) }), "# ops");
  assert.equal(text, "a@x in # ops: " + "x".repeat(79) + "…");
  assert.equal(describeNotification(message({ text: "y".repeat(80) }), "# ops").length, "a@x in # ops: ".length + 80);
});

test("the mute button names the channel only for a subscription", () => {
  assert.equal(muteLabel("subscribed", "# ops"), "Mute # ops");
  assert.equal(muteLabel("targeted", "# ops"), "Turn off @-notifications");
});

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

test("the first message goes out at once, a burst after it as one batch when the window ends", async () => {
  const batches: number[][] = [];
  const coalescer = new Coalescer<number>((items) => batches.push(items), 30);
  coalescer.offer(1);
  coalescer.offer(2);
  coalescer.offer(3);
  assert.deepEqual(batches, [[1]]);
  await sleep(50);
  assert.deepEqual(batches, [[1], [2, 3]]);
  coalescer.dispose();
});

test("a quiet window ends without a notification, and the next message goes out at once", async () => {
  const batches: number[][] = [];
  const coalescer = new Coalescer<number>((items) => batches.push(items), 20);
  coalescer.offer(1);
  await sleep(40);
  coalescer.offer(2);
  assert.deepEqual(batches, [[1], [2]]);
  coalescer.dispose();
});

test("a batch opens its own window: the next message waits for it", async () => {
  const batches: number[][] = [];
  const coalescer = new Coalescer<number>((items) => batches.push(items), 40);
  coalescer.offer(1);
  coalescer.offer(2);
  await sleep(55);
  coalescer.offer(3);
  assert.deepEqual(batches, [[1], [2]]);
  await sleep(50);
  assert.deepEqual(batches, [[1], [2], [3]]);
  coalescer.dispose();
});
