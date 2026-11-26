import assert from "node:assert/strict";
import { test } from "node:test";
import { parseFromWebview } from "../protocol";
import { Chip, buildQuery, chipTerm, hitPlace, plainLabel, scopeTerms, snippetParts, withChip } from "../search";
import { Channel } from "../types";

const channel = (extra: Partial<Channel>): Channel => ({ id: "general", kind: "general", name: "general", description: "", messages: 0, last_sequence: 0, unread: 0, targeted_unread: 0, ...extra });

test("scopeTerms limits to a group by its name and to a DM by both agents", () => {
  assert.deepEqual(scopeTerms(channel({})), ["to:#general"]);
  assert.deepEqual(scopeTerms(channel({ id: "group:ops", kind: "group", name: "ops" })), ["to:#ops"]);
  assert.deepEqual(scopeTerms(channel({ id: "group:a b", kind: "group", name: "a b" })), ['to:"#a b"']);
  assert.deepEqual(scopeTerms(channel({ id: "dm:a@x:b@y", kind: "dm", name: "a@x + b@y", agents: ["a@x", "b@y"] })), ["from:a@x", "from:b@y", "to:a@x", "to:b@y"]);
});

test("buildQuery puts the scope, then the chips, then the typed text, and leaves out what is empty", () => {
  assert.equal(buildQuery(["to:#ops"], [], "  deploy  from:bob "), "to:#ops deploy  from:bob");
  assert.equal(buildQuery([], [], "deploy"), "deploy");
  assert.equal(buildQuery(["to:#ops"], [], " "), "to:#ops");
  const chips: Chip[] = [{ key: "type", value: "question" }, { key: "targets", value: "@all" }, { key: "from", value: "a b" }];
  assert.equal(buildQuery(["to:#ops"], chips, "deploy"), 'to:#ops type:question targets:@all from:"a b" deploy');
  assert.equal(buildQuery([], chips.slice(0, 1), ""), "type:question");
});

test("chipTerm drops quotes from a value, and withChip adds a chip once", () => {
  assert.equal(chipTerm({ key: "from", value: 'x"y' }), "from:xy");
  const type: Chip = { key: "type", value: "ack" };
  const once = withChip([], type);
  assert.deepEqual(withChip(once, { key: "type", value: "ack" }), [type]);
  assert.deepEqual(withChip(once, { key: "from", value: "ack" }), [type, { key: "from", value: "ack" }]);
  assert.deepEqual(once, [type]); // the argument is not changed
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

test("parseFromWebview checks the lookup of a parent", () => {
  assert.deepEqual(parseFromWebview({ type: "parent", id: "01J", extra: 1 }), { type: "parent", id: "01J" });
  for (const bad of [{ type: "parent" }, { type: "parent", id: "" }, { type: "parent", id: 7 }]) assert.equal(parseFromWebview(bad), undefined);
});

test("parseFromWebview checks search and jump messages", () => {
  assert.deepEqual(parseFromWebview({ type: "search", run: 3, chips: [], text: "deploy", allChannels: false }), { type: "search", run: 3, chips: [], text: "deploy", allChannels: false });
  assert.deepEqual(parseFromWebview({ type: "search", run: 3, chips: [{ key: "from", value: "a@x", extra: 1 }], text: "", allChannels: true, cursor: "c1", extra: 1 }),
    { type: "search", run: 3, chips: [{ key: "from", value: "a@x" }], text: "", allChannels: true, cursor: "c1" });
  assert.deepEqual(parseFromWebview({ type: "jump", channel: "group:ops", sequence: 42 }), { type: "jump", channel: "group:ops", sequence: 42 });
  const search = { type: "search", run: 1, chips: [], text: "x", allChannels: false };
  for (const bad of [{ ...search, chips: undefined }, { ...search, chips: "from:a" }, { ...search, chips: [null] }, { ...search, chips: [{ key: "to", value: "a" }] },
    { ...search, chips: [{ key: "from", value: 1 }] }, { ...search, chips: [{ key: "from", value: " " }] },{ ...search, run: "1" }, { ...search, run: 1.5 }, { ...search, text: 1 }, { ...search, allChannels: "yes" }, { ...search, cursor: 5 },
    { type: "jump", channel: "", sequence: 1 }, { type: "jump", channel: 7, sequence: 1 }, { type: "jump", channel: "general", sequence: -1 }, { type: "jump", channel: "general" }]) {
    assert.equal(parseFromWebview(bad), undefined);
  }
});
