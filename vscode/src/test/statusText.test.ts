import assert from "node:assert/strict";
import { test } from "node:test";
import { composerNote, describeConnection, statusBarText } from "../statusText";

test("describeConnection tells a closed event stream from a bus that is down", () => {
  assert.deepEqual(describeConnection(true, { bus: "live" }), { ok: true, text: "live" });
  assert.deepEqual(describeConnection(true, undefined), { ok: true, text: "live" });
  assert.deepEqual(describeConnection(true, { bus: "reconnecting" }), { ok: false, text: "bus disconnected, reconnecting" });
  assert.deepEqual(describeConnection(false, { bus: "live" }), { ok: false, text: "reconnecting" });
  assert.deepEqual(describeConnection(false, { bus: "reconnecting" }), { ok: false, text: "reconnecting" });
});

test("composerNote says who sends, or why nobody can", () => {
  const status = { agent: "monitor@bob", can_send: true, note: "" };
  assert.equal(composerNote(status, true), "Sending as monitor@bob");
  assert.equal(composerNote(status, false), "Read-only: this conversation is between other agents.");
  assert.equal(composerNote({ ...status, can_send: false, note: "started without --agent" }, true), "started without --agent");
  assert.equal(composerNote({ ...status, can_send: false }, true), "Read-only: the monitor has no identity.");
});

test("statusBarText is empty when all is well and there is no mail", () => {
  const live = { ok: true, text: "live" };
  const down = { ok: false, text: "reconnecting" };
  assert.equal(statusBarText(live, 0), "");
  assert.equal(statusBarText(live, 3), "$(mail) 3");
  assert.equal(statusBarText(down, 0), "$(debug-disconnect) Agent bus: reconnecting");
  assert.equal(statusBarText(down, 2), "$(debug-disconnect) Agent bus: reconnecting  $(mail) 2");
});
