import assert from "node:assert/strict";
import { test } from "node:test";
import { isShown, shownMessages, toggleType, typeCounts } from "../typeFilter";
import { MESSAGE_TYPES, Message } from "../types";

const message = (sequence: number, type?: string): Message => ({ sequence, channel: "general", status: "verified", ...(type ? { type } : {}) });

test("typeCounts counts a message without a type as the default type and lists types with none", () => {
  const counts = typeCounts([message(1), message(2, "message"), message(3, "question"), message(4, "custom")]);
  assert.equal(counts.get("message"), 2);
  assert.equal(counts.get("question"), 1);
  assert.equal(counts.get("ack"), 0);
  assert.equal(counts.get("custom"), 1);
  assert.deepEqual([...counts.keys()].slice(0, MESSAGE_TYPES.length), MESSAGE_TYPES);
});

test("no chosen type shows every message, chosen types show only theirs", () => {
  const list = [message(1), message(2, "question"), message(3, "ack")];
  assert.deepEqual(shownMessages(list, []), list);
  assert.deepEqual(shownMessages(list, ["question", "message"]).map((m) => m.sequence), [1, 2]);
  assert.equal(isShown(message(4), ["question"]), false);
  assert.equal(isShown(message(4), []), true);
});

test("toggleType adds a type and removes it again, without changing its input", () => {
  const shown = ["question"];
  assert.deepEqual(toggleType(shown, "ack"), ["question", "ack"]);
  assert.deepEqual(toggleType(shown, "question"), []);
  assert.deepEqual(shown, ["question"]);
});
