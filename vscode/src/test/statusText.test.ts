import assert from "node:assert/strict";
import { test } from "node:test";
import { composerNote, describeConnection, describePending, statusBarText } from "../statusText";

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
  assert.equal(statusBarText(live, 0, 0), "");
  assert.equal(statusBarText(live, 3, 0), "$(mail) 3");
  assert.equal(statusBarText(down, 0, 0), "$(debug-disconnect) Agent Bus: reconnecting");
  assert.equal(statusBarText(down, 2, 1), "$(debug-disconnect) Agent Bus: reconnecting  $(mail) 2  $(person-add) 1");
});

test("describePending counts the agents waiting for approval, and says nothing for none or an older monitor", () => {
  assert.equal(describePending({ pending_enrollments: 1 }), "1 pending enrollment");
  assert.equal(describePending({ pending_enrollments: 3 }), "3 pending enrollments");
  assert.equal(describePending({ pending_enrollments: 0 }), "");
  assert.equal(describePending({}), "");
  assert.equal(describePending(undefined), "");
});
