import assert from "node:assert/strict";
import { test } from "node:test";
import { flatRows, quoteOf, summarizeThreads, threadKeys, threadRows } from "../threads";
import { Message } from "../types";

const message = (sequence: number, id: string, extra: Partial<Message> = {}): Message => ({ sequence, id, channel: "general", status: "verified", ...extra });
const shape = (messages: Message[]) => threadRows(messages).map((row) => `${row.message.id}:${row.depth}${row.replyTo ? ":" + row.replyTo : ""}`);

test("replies follow their parent, siblings and roots go by sequence", () => {
  const messages = [
    message(1, "a"),
    message(2, "b"),
    message(3, "a1", { reply_to: "a", thread: "a" }),
    message(4, "b1", { reply_to: "b", thread: "b" }),
    message(5, "a1x", { reply_to: "a1", thread: "a" }),
    message(6, "a2", { reply_to: "a", thread: "a" }),
  ];
  assert.deepEqual(shape(messages), ["a:0", "a1:1", "a1x:2", "a2:1", "b:0", "b1:1"]);
});

test("an unloaded reply_to falls back to the loaded thread root", () => {
  const messages = [message(1, "a"), message(5, "a9", { reply_to: "gone", thread: "a" })];
  assert.deepEqual(shape(messages), ["a:0", "a9:1"]);
});

test("a reply whose parent and root are not loaded is a root that says what it answers", () => {
  const messages = [message(7, "x", { reply_to: "01JABCDEFG", thread: "01JROOT" }), message(8, "y", { thread: "01JROOT" })];
  assert.deepEqual(shape(messages), ["x:0:01JABCDEFG", "y:0:01JROOT"]);
});

test("the indent stops at four levels", () => {
  const chain = [message(1, "m0")];
  for (let i = 1; i <= 6; i++) chain.push(message(i + 1, "m" + i, { reply_to: "m" + (i - 1), thread: "m0" }));
  assert.deepEqual(shape(chain), ["m0:0", "m1:1", "m2:2", "m3:3", "m4:4", "m5:4", "m6:4"]);
});

test("summarizeThreads counts the whole subtree and finds unseen mail in it", () => {
  const messages = [
    message(1, "a"),
    message(2, "a1", { reply_to: "a" }),
    message(3, "a1x", { reply_to: "a1" }),
    message(4, "b"),
    message(5, "c"),
    message(6, "c1", { reply_to: "c" }),
  ];
  const summaries = summarizeThreads(threadRows(messages), (m) => m.sequence > 5);
  const byId = (id: string) => summaries.get(messages.find((m) => m.id === id)!);
  assert.deepEqual(byId("a"), { replies: 2, unseen: false });
  assert.deepEqual(byId("b"), { replies: 0, unseen: false });
  assert.deepEqual(byId("c"), { replies: 1, unseen: true });
  assert.equal(summaries.size, 3);
});

test("a reference to a later or the same message does not make a cycle", () => {
  const messages = [message(1, "a", { reply_to: "b" }), message(2, "b", { reply_to: "b" })];
  assert.deepEqual(shape(messages), ["a:0:b", "b:0:b"]);
});

test("flatRows keeps the stream order and finds a parent the filter hides", () => {
  const all = [message(1, "a", { type: "request" }), message(2, "b"), message(3, "a1", { reply_to: "a", thread: "a" }), message(4, "x1", { reply_to: "x", thread: "x" })];
  const rows = flatRows(all.slice(1), all);
  assert.deepEqual(rows.map((row) => [row.message.id, row.parent?.id, row.replyTo]), [["b", undefined, undefined], ["a1", "a", undefined], ["x1", undefined, "x"]]);
});

test("threadKeys puts a thread under its root, and replies to one missing message together", () => {
  const messages = [
    message(1, "a", { thread: "a" }),
    message(2, "a1", { reply_to: "a", thread: "a" }),
    message(3, "a2", { reply_to: "a1", thread: "a" }),
    message(4, "b"),
    message(5, "x1", { reply_to: "x", thread: "x" }),
    message(6, "x2", { reply_to: "x", thread: "x" }),
  ];
  const keys = threadKeys(messages);
  assert.deepEqual(messages.map((m) => keys.get(m)), ["a", "a", "a", "b", "x", "x"]);
});

test("quoteOf takes the title, else the first text line, and cuts it", () => {
  assert.equal(quoteOf(message(1, "a", { title: "Release", text: "body" })), "Release");
  assert.equal(quoteOf(message(1, "a", { text: "\n  first line \nsecond" })), "first line");
  assert.equal(quoteOf(message(1, "a", { text: "x".repeat(100) }), 10), "x".repeat(9) + "…");
});
