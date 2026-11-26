import assert from "node:assert/strict";
import { test } from "node:test";
import { ago, describePresence, presenceClass, recentActivity } from "../presence";
import { matchTargets, mentionBefore, mentionText, resolveTarget } from "../completion";
import { categorize, channelNameProblem, describeChannel, destinationOf, dmChannelId, groupName, invitableAgents } from "../channels";
import { byOwner, dmAgents, dmByOwner, dmOwners, groupBy, ownerOf, shortName } from "../names";
import { parseFromWebview, parsePresenceMarks } from "../protocol";
import { nextBackoff } from "../events";
import { continuesGroup, insertMessage, isTargeted } from "../messages";
import { SseParser } from "../sse";
import { ThreadRow } from "../threads";
import { Channel, Message } from "../types";

const message = (sequence: number, extra: Partial<Message> = {}): Message => ({ sequence, channel: "general", status: "verified", ...extra });
const channel = (extra: Partial<Channel>): Channel => ({ id: "general", kind: "general", name: "general", description: "", messages: 0, last_sequence: 0, unread: 0, targeted_unread: 0, ...extra });

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

test("dmChannelId sorts the two agents", () => {
  assert.equal(dmChannelId("b@y", "a@x"), "dm:a@x:b@y");
  assert.equal(dmChannelId("a@x", "b@y"), "dm:a@x:b@y");
});

test("nextBackoff doubles up to the maximum", () => {
  assert.equal(nextBackoff(1000, 30000), 2000);
  assert.equal(nextBackoff(20000, 30000), 30000);
  assert.equal(nextBackoff(30000, 30000), 30000);
});

test("categorize lists favourites also in their own section and ignores unknown ones", () => {
  const general = channel({});
  const group = channel({ id: "group:ops", kind: "group", name: "ops" });
  const dm = channel({ id: "dm:a:b", kind: "dm", name: "a + b" });
  const ids = (list: Channel[]) => list.map((c) => c.id);

  const plain = categorize([general, group, dm], new Set());
  assert.deepEqual([ids(plain.favourites), ids(plain.channels), ids(plain.directMessages)], [[], ["general", "group:ops"], ["dm:a:b"]]);

  const starred = categorize([general, group, dm], new Set(["dm:a:b", "group:ops", "group:gone"]));
  assert.deepEqual([ids(starred.favourites), ids(starred.channels), ids(starred.directMessages)], [["group:ops", "dm:a:b"], ["general", "group:ops"], ["dm:a:b"]]);
});

test("byOwner sorts agents by owner, then by name", () => {
  assert.deepEqual(["b@zed", "watcher@alice", "api@zed", "lib@alice"].sort(byOwner), ["lib@alice", "watcher@alice", "api@zed", "b@zed"]);
});

test("shortName and ownerOf split an agent ID at its @", () => {
  assert.deepEqual([shortName("api@alice"), ownerOf("api@alice")], ["api", "alice"]);
  assert.deepEqual([shortName("plain"), ownerOf("plain")], ["plain", ""]);
});

test("groupBy keeps the order and splits it into runs", () => {
  const ids = ["a@alice", "b@alice", "a@bob"];
  assert.deepEqual(groupBy(ids, ownerOf), [{ key: "alice", items: ["a@alice", "b@alice"] }, { key: "bob", items: ["a@bob"] }]);
});

test("DMs list their agents in owner order and sort by them", () => {
  const dm = (a: string, b: string) => channel({ id: dmChannelId(a, b), kind: "dm", name: `${a} + ${b}`, agents: [a, b].sort() });
  assert.deepEqual(dmAgents(dm("zz@alice", "aa@bob")), ["zz@alice", "aa@bob"]);
  assert.deepEqual([dmOwners(dm("a@alice", "b@alice")), dmOwners(dm("zz@alice", "aa@bob"))], ["alice", "alice ↔ bob"]);
  // Same-owner and two-owner DMs of alice don't interleave, so each owner group is one run.
  const sorted = [dm("c@alice", "d@alice"), dm("x@bob", "y@bob"), dm("b@alice", "x@bob"), dm("a@alice", "b@alice")].sort(dmByOwner);
  assert.deepEqual(sorted.map(dmAgents), [["a@alice", "b@alice"], ["c@alice", "d@alice"], ["b@alice", "x@bob"], ["x@bob", "y@bob"]]);
  const dmWithoutAgents = channel({ id: "dm:a@x:b@y", kind: "dm", name: "b@y + a@x" });
  assert.deepEqual(dmAgents(dmWithoutAgents), ["a@x", "b@y"]);
});

test("continuesGroup joins as the web page does", () => {
  const row = (extra: Partial<Message>, parent?: Message): ThreadRow => ({ message: message(2, { from: "a@x", to: "general", time: "2026-11-24T12:01:00Z", ...extra }), depth: 0, parent });
  const first = row({ time: "2026-11-24T12:00:00Z" });
  assert.equal(continuesGroup(undefined, first), false);
  assert.equal(continuesGroup(first, row({})), true);
  assert.equal(continuesGroup(first, row({ time: "2026-11-24T12:04:59Z" })), true);
  assert.equal(continuesGroup(first, row({ time: "2026-11-24T12:05:00Z" })), false);
  assert.equal(continuesGroup(first, row({ from: "b@y" })), false);
  assert.equal(continuesGroup(first, row({ from: undefined })), false);
  assert.equal(continuesGroup(first, row({ to: "ops" })), false);
  assert.equal(continuesGroup(first, row({ type: "question" })), false);
  assert.equal(continuesGroup(first, row({ status: "unknown_sender" })), false);
  assert.equal(continuesGroup(first, row({ time: "not a time" })), false);
  assert.equal(continuesGroup(first, row({ title: "A title", targets: ["b@y"] })), true); // neither counts, as on the web page
  const parent = message(1);
  assert.equal(continuesGroup(first, row({}, parent)), false);
  assert.equal(continuesGroup(row({}, parent), row({}, parent)), true);
  // A reply to a message that is not loaded keeps its own header and "not shown here" line.
  const answering = (id: string): ThreadRow => ({ ...row({}), replyTo: id });
  assert.equal(continuesGroup(first, answering("x")), false);
  assert.equal(continuesGroup(answering("x"), answering("y")), false);
  assert.equal(continuesGroup(answering("x"), answering("x")), true);
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
  const workers = [{ description: "tests", since: "2026-11-24T12:00:00Z" }];
  assert.equal(describePresence({ agent: "a", state: "idle", workers }, now), "idle · 1 worker");
  assert.equal(describePresence({ agent: "a", state: "working", activity: "Bash npm", workers: [...workers, ...workers] }, now), "working · 2 workers · Bash npm");
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

test("resolveTarget finds the one candidate that is typed in full", () => {
  const all = ["@all", "lib-x@alice", "Lib-Y@bob"];
  assert.equal(resolveTarget(all, "all"), "@all");
  assert.equal(resolveTarget(all, " @ALL "), "@all");
  assert.equal(resolveTarget(all, "lib-y@bob"), "Lib-Y@bob");
  assert.equal(resolveTarget(all, "@lib-x@alice"), "lib-x@alice");
  assert.equal(resolveTarget(all, "lib"), undefined); // a prefix is not a name
  assert.equal(resolveTarget(all, ""), undefined);
});

test("parseFromWebview accepts well-formed messages only", () => {
  const draft = { type: "message", targets: ["@all"], title: "", text: "hi" };
  assert.deepEqual(parseFromWebview({ type: "ready", extra: 1 }), { type: "ready" });
  assert.deepEqual(parseFromWebview({ type: "send", draft }), { type: "send", draft });
  assert.deepEqual(parseFromWebview({ type: "threaded", value: true }), { type: "threaded", value: true });
  assert.deepEqual(parseFromWebview({ type: "subscribe", value: false }), { type: "subscribe", value: false });
  assert.equal(parseFromWebview({ type: "subscribe", value: 1 }), undefined);
  assert.deepEqual(parseFromWebview({ type: "shownTypes", value: ["ack"] }), { type: "shownTypes", value: ["ack"] });
  assert.equal(parseFromWebview({ type: "shownTypes", value: [1] }), undefined);
  assert.equal(parseFromWebview({ type: "shownTypes", value: "ack" }), undefined);
  assert.deepEqual(parseFromWebview({ type: "raw", sequence: 7, extra: 1 }), { type: "raw", sequence: 7 });
  for (const bad of [null, "ready", {}, { type: "nope" }, { type: "threaded", value: "yes" }, { type: "raw" }, { type: "raw", sequence: "7" }, { type: "raw", sequence: -1 }, { type: "raw", sequence: 1.5 }, { type: "send" }, { type: "send", draft: { ...draft, targets: "@all" } },
    { type: "send", draft: { ...draft, targets: [1] } }, { type: "send", draft: { ...draft, text: undefined } }]) {
    assert.equal(parseFromWebview(bad), undefined);
  }
});

test("mentionText gives every candidate exactly one @", () => {
  assert.equal(mentionText("@all"), "@all ");
  assert.equal(mentionText("lib-x@alice"), "@lib-x@alice ");
});

test("mentionBefore finds an @word ending at the caret", () => {
  assert.deepEqual(mentionBefore("hi @lib-x@al", 12), { start: 3, query: "lib-x@al" });
  assert.deepEqual(mentionBefore("@", 1), { start: 0, query: "" });
  assert.deepEqual(mentionBefore("hi @lib there", 7), { start: 3, query: "lib" });
  assert.equal(mentionBefore("mail a@b", 8), undefined);
  assert.equal(mentionBefore("hi @lib there", 13), undefined);
});

test("parsePresenceMarks keeps only well-formed marks", () => {
  const sent = { a: { state: "idle", since: "t" }, b: { state: "working", since: undefined }, c: { state: 1 }, d: null, e: { state: "x", since: 3 } };
  const raw: unknown = JSON.parse(JSON.stringify(sent)); // as postMessage delivers it
  assert.deepEqual([...parsePresenceMarks(raw)], [["a", { state: "idle", since: "t" }], ["b", { state: "working" }], ["e", { state: "x" }]]);
  assert.equal(parsePresenceMarks(undefined).size, 0);
});

test("presenceClass groups the states like the tree", () => {
  assert.equal(presenceClass("working"), "working");
  assert.equal(presenceClass("error:auth"), "error");
  assert.equal(presenceClass("offline"), "offline");
  assert.equal(presenceClass(undefined), "offline");
});

test("invitableAgents leaves out members, removed agents and the monitor's own", () => {
  const agents = [
    { id: "a@x", fingerprint: "", removed: false },
    { id: "b@x", fingerprint: "", removed: false },
    { id: "gone@x", fingerprint: "", removed: true },
    { id: "monitor@x", fingerprint: "", removed: false },
  ];
  assert.deepEqual(invitableAgents(agents, ["a@x"], "monitor@x"), ["b@x"]);
});

test("groupName is the name of a group channel only", () => {
  assert.equal(groupName("group:agent-bus"), "agent-bus");
  assert.equal(groupName("general"), undefined);
  assert.equal(groupName("dm:a@x:b@x"), undefined);
});

test("channelNameProblem takes the names busd takes", () => {
  assert.equal(channelNameProblem("agent-bus"), undefined);
  for (const name of ["", "Agent", "a b", "general", "x".repeat(65)]) assert.notEqual(channelNameProblem(name), undefined, name);
});

test("parseFromWebview reads the member actions", () => {
  assert.deepEqual(parseFromWebview({ type: "invite" }), { type: "invite" });
  assert.deepEqual(parseFromWebview({ type: "removeMember", agent: "a@x" }), { type: "removeMember", agent: "a@x" });
  assert.equal(parseFromWebview({ type: "removeMember", agent: "" }), undefined);
  assert.equal(parseFromWebview({ type: "removeMember" }), undefined);
});
