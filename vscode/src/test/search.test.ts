import assert from "node:assert/strict";
import { test } from "node:test";
import { parseFromWebview } from "../protocol";
import { buildQuery, hitPlace, plainLabel, scopeTerms, snippetParts } from "../search";
import { Channel } from "../types";

const channel = (extra: Partial<Channel>): Channel => ({ id: "general", kind: "general", name: "general", description: "", messages: 0, last_sequence: 0, ...extra });

test("scopeTerms limits to a group by its name and to a DM by both agents", () => {
  assert.deepEqual(scopeTerms(channel({})), ["to:#general"]);
  assert.deepEqual(scopeTerms(channel({ id: "group:ops", kind: "group", name: "ops" })), ["to:#ops"]);
  assert.deepEqual(scopeTerms(channel({ id: "group:a b", kind: "group", name: "a b" })), ['to:"#a b"']);
  assert.deepEqual(scopeTerms(channel({ id: "dm:a@x:b@y", kind: "dm", name: "a@x + b@y", agents: ["a@x", "b@y"] })), ["to:a@x", "to:b@y"]);
});

test("buildQuery puts the scope before the typed text and leaves out what is empty", () => {
  assert.equal(buildQuery(["to:#ops"], "  deploy  from:bob "), "to:#ops deploy  from:bob");
  assert.equal(buildQuery([], "deploy"), "deploy");
  assert.equal(buildQuery(["to:#ops"], " "), "to:#ops");
});

test("snippetParts cuts at the matches, counted in code points", () => {
  assert.deepEqual(snippetParts("a deploy b"), [{ text: "a deploy b", match: false }]);
  assert.deepEqual(snippetParts("a deploy b", [[2, 8]]), [
    { text: "a ", match: false },
    { text: "deploy", match: true },
    { text: " b", match: false },
  ]);
  // "😀" is one code point but two UTF-16 units: a UTF-16 offset would cut it in half.
  assert.deepEqual(snippetParts("😀 ok 😀", [[2, 4]]), [
    { text: "😀 ", match: false },
    { text: "ok", match: true },
    { text: " 😀", match: false },
  ]);
  assert.deepEqual(snippetParts("ab", [[0, 1], [1, 2]]), [{ text: "a", match: true }, { text: "b", match: true }]);
});

test("snippetParts clamps offsets that are out of range and skips overlaps and empty ranges", () => {
  assert.deepEqual(snippetParts("abc", [[1, 99]]), [{ text: "a", match: false }, { text: "bc", match: true }]);
  assert.deepEqual(snippetParts("abc", [[0, 2], [1, 3]]), [{ text: "ab", match: true }, { text: "c", match: true }]);
  assert.deepEqual(snippetParts("abc", [[2, 2], [5, 6], [-3, -1]]), [{ text: "abc", match: false }]);
  assert.deepEqual(snippetParts("", [[0, 1]]), []);
});

test("plainLabel makes one line and defuses icon syntax", () => {
  assert.equal(plainLabel("  a\n\n b\t c "), "a b c");
  assert.equal(plainLabel("deploy $(rocket) now"), "deploy \\$(rocket) now");
});

test("hitPlace names a listed channel and falls back to the ID", () => {
  const channels = [channel({ id: "group:ops", kind: "group", name: "ops" })];
  assert.equal(hitPlace(channels, "group:ops"), "# ops");
  assert.equal(hitPlace(channels, "group:new"), "group:new");
});

test("parseFromWebview checks search and jump messages", () => {
  assert.deepEqual(parseFromWebview({ type: "search", run: 3, text: "deploy", allChannels: false }), { type: "search", run: 3, text: "deploy", allChannels: false });
  assert.deepEqual(parseFromWebview({ type: "search", run: 3, text: "", allChannels: true, cursor: "c1", extra: 1 }), { type: "search", run: 3, text: "", allChannels: true, cursor: "c1" });
  assert.deepEqual(parseFromWebview({ type: "jump", channel: "group:ops", sequence: 42 }), { type: "jump", channel: "group:ops", sequence: 42 });
  const search = { type: "search", run: 1, text: "x", allChannels: false };
  for (const bad of [{ ...search, run: "1" }, { ...search, run: 1.5 }, { ...search, text: 1 }, { ...search, allChannels: "yes" }, { ...search, cursor: 5 },
    { type: "jump", channel: "", sequence: 1 }, { type: "jump", channel: 7, sequence: 1 }, { type: "jump", channel: "general", sequence: -1 }, { type: "jump", channel: "general" }]) {
    assert.equal(parseFromWebview(bad), undefined);
  }
});
