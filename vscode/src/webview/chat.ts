// The chat panel's page. Messages are untrusted: everything goes in with textContent, never as HTML.
import { matchTargets, mentionBefore, mentionText, resolveTarget } from "../completion";
import { continuesGroup, insertMessage } from "../messages";
import { parsePresenceMarks } from "../protocol";
import { Chip, chipTerm, snippetParts, withChip } from "../search";
import type { ChatState, FromWebview, PresenceMark, SearchHitView, ToWebview } from "../protocol";
import { presenceClass } from "../presence";
import { byOwner, groupBy, ownerOf, shortName } from "../names";
import { shortTime } from "../time";
import { isSeen } from "../viewport";
import { ThreadRow, ThreadSummary, summarizeThreads, threadRows } from "../threads";
import { shownMessages, toggleType, typeCounts } from "../typeFilter";
import { ALL, DEFAULT_TYPE, MESSAGE_TYPES, Message } from "../types";

declare function acquireVsCodeApi(): { postMessage(message: FromWebview): void; getState(): unknown; setState(state: unknown): void };
const host = acquireVsCodeApi();

const STATUS_LABEL: Record<string, string> = { unknown_sender: "unknown sender", rejected: "rejected" };
const ID_SUFFIX = 8; // characters of a message ID shown in "reply to": the end of a ULID tells IDs apart

let state: ChatState | undefined;
let presence = new Map<string, PresenceMark>(); // the page's checked copy of the state's presence
const messages: Message[] = [];

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className = "", text = ""): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  node.textContent = text;
  return node;
}

const list = document.getElementById("messages") as HTMLElement;
const composer = document.getElementById("composer") as HTMLFormElement;
const note = document.getElementById("note") as HTMLElement;
const typeSelect = document.getElementById("type") as HTMLSelectElement;
const targetBox = document.getElementById("targets") as HTMLElement;
const targetInput = document.getElementById("targetInput") as HTMLInputElement;
const threadsToggle = document.getElementById("threads") as HTMLButtonElement;
const bellToggle = document.getElementById("bell") as HTMLButtonElement;
const completions = document.getElementById("completions") as HTMLElement;
const titleInput = document.getElementById("title") as HTMLInputElement;
const textInput = document.getElementById("text") as HTMLTextAreaElement;
const sendButton = document.getElementById("send") as HTMLButtonElement;
const problem = document.getElementById("problem") as HTMLElement;
const searchToggle = document.getElementById("searchToggle") as HTMLButtonElement;
const searchbar = document.getElementById("searchbar") as HTMLElement;
const searchInput = document.getElementById("searchInput") as HTMLInputElement;
const searchField = document.getElementById("searchField") as HTMLElement;
const scopeToggle = document.getElementById("scope") as HTMLButtonElement;
const results = document.getElementById("results") as HTMLElement;
const typeBar = document.getElementById("types") as HTMLElement;
const memberPane = document.getElementById("members") as HTMLElement;
const membersToggle = document.getElementById("membersToggle") as HTMLButtonElement;
const memberSplit = document.getElementById("memberSplit") as HTMLElement;

for (const type of MESSAGE_TYPES) typeSelect.append(new Option(type, type));

const TIME_TICK_MS = 30_000; // relative times age: they are redrawn this often

const formatTime = (time: string): string => shortTime(time, new Date(), state?.timeFormat === "relative");

// The short time; the tooltip has the full date and the sequence.
function renderTime(message: Message, className: string): HTMLElement {
  const time = el("span", className + " muted", message.time ? formatTime(message.time) : "");
  if (message.time) {
    time.dataset.time = message.time;
    time.title = `${new Date(message.time).toLocaleString([], { dateStyle: "full", timeStyle: "medium" })} · #${message.sequence}`;
  }
  return time;
}

function showSubscribed(subscribed: boolean): void {
  bellToggle.textContent = subscribed ? "🔔" : "🔕";
  bellToggle.title = subscribed ? "Subscribed: every new message notifies. Click to unsubscribe" : "Subscribe: notify for every new message here";
  bellToggle.setAttribute("aria-pressed", String(subscribed));
}

// Asks the host to open the record as stored, in an editor tab.
function renderRawButton(message: Message): HTMLElement {
  const button = el("button", "raw", "{ }");
  button.type = "button";
  button.title = "Raw message";
  button.setAttribute("aria-label", "Raw message");
  button.addEventListener("click", () => host.postMessage({ type: "raw", sequence: message.sequence }));
  return button;
}

// The dot shows the sender's presence. The monitor's own messages and agents without presence get a
// hollow one.
function paintDot(dot: HTMLElement, from: string): void {
  const mark = from === state?.self ? undefined : presence.get(from);
  dot.className = "dot " + presenceClass(mark?.state);
  if (!mark) dot.title = from === state?.self ? "you" : "no presence";
  else dot.title = mark.state + (mark.since ? " since " + shortTime(mark.since, new Date()) : "");
}

function renderDot(from: string): HTMLElement {
  const dot = el("span");
  dot.dataset.from = from;
  paintDot(dot, from);
  return dot;
}

// A presence event changes the dots only: no new drawing, so nothing moves.
function paintDots(): void {
  for (const dot of list.querySelectorAll<HTMLElement>(".dot")) paintDot(dot, dot.dataset.from ?? "");
  renderMembers();
}

// The agents that read the channel, on the right, by owner and with their state. A direct message
// has none: its two agents are in the title.
let showMembers = true;
function renderMembers(): void {
  const members = state && !isDirect() ? [...state.members].sort(byOwner) : [];
  membersToggle.hidden = members.length === 0;
  membersToggle.setAttribute("aria-pressed", String(showMembers));
  memberPane.hidden = members.length === 0 || !showMembers;
  memberSplit.hidden = memberPane.hidden;
  if (!memberPane.hidden) memberSplit.setAttribute("aria-valuenow", String(Math.round(memberPane.getBoundingClientRect().width)));
  memberPane.replaceChildren();
  for (const { key, items } of groupBy(members, ownerOf)) {
    memberPane.append(el("h2", "", key || "no owner"));
    for (const id of items) {
      const row = el("div", "member");
      const self = id === state?.self;
      const mark = self ? undefined : presence.get(id);
      row.title = id + (mark ? ": " + mark.state : "");
      row.append(renderDot(id), el("span", "name", shortName(id)), el("span", "state", self ? "you" : (mark?.state ?? "offline").replace(/_/g, " ")));
      memberPane.append(row);
    }
  }
}

// The member list's width: dragged at the split or moved with the arrow keys, kept with the panel.
const MEMBER_WIDTH = { min: 120, max: 480, step: 16 };
// At most 60% of the panel, so the messages keep their room in a narrow one.
function setMemberWidth(px: number): void {
  const max = Math.min(MEMBER_WIDTH.max, memberPane.parentElement!.clientWidth * 0.6 || MEMBER_WIDTH.max);
  const width = Math.round(Math.min(max, Math.max(MEMBER_WIDTH.min, px)));
  memberPane.style.width = width + "px";
  memberSplit.setAttribute("aria-valuenow", String(width));
  host.setState({ ...((host.getState() as object | undefined) ?? {}), memberWidth: width });
}
const savedWidth = (host.getState() as { memberWidth?: unknown } | undefined)?.memberWidth;
if (typeof savedWidth === "number") setMemberWidth(savedWidth);

memberSplit.addEventListener("pointerdown", (event) => {
  event.preventDefault(); // no text selection while dragging
  memberSplit.focus(); // so the arrow keys work after a click too
  memberSplit.setPointerCapture(event.pointerId);
  memberSplit.classList.add("dragging");
  const startX = event.clientX;
  const startWidth = memberPane.getBoundingClientRect().width;
  const move = (moved: PointerEvent) => setMemberWidth(startWidth + startX - moved.clientX);
  const stop = () => {
    memberSplit.classList.remove("dragging");
    memberSplit.removeEventListener("pointermove", move);
    memberSplit.removeEventListener("pointerup", stop);
    memberSplit.removeEventListener("pointercancel", stop);
  };
  memberSplit.addEventListener("pointermove", move);
  memberSplit.addEventListener("pointerup", stop);
  memberSplit.addEventListener("pointercancel", stop);
});
memberSplit.addEventListener("keydown", (event) => {
  const delta = event.key === "ArrowLeft" ? MEMBER_WIDTH.step : event.key === "ArrowRight" ? -MEMBER_WIDTH.step : 0;
  if (delta === 0) return;
  event.preventDefault();
  setMemberWidth(memberPane.getBoundingClientRect().width + delta);
});

membersToggle.addEventListener("click", () => {
  showMembers = !showMembers;
  renderMembers();
});

// A sender, type or target in a header is a button: it adds its filter to the search and opens it.
function renderTag(className: string, text: string, chip: Chip): HTMLElement {
  const button = el("button", className + " tag", text);
  button.type = "button";
  button.title = "Search " + chipTerm(chip);
  button.addEventListener("click", () => addSearchChip(chip));
  return button;
}

// `newTag` is the "New" or "new" marker of a thread view, shown before the time.
function renderHeader(message: Message, newTag: string | undefined): HTMLElement {
  const header = el("div", "meta");
  header.append(renderDot(message.from ?? ""));
  header.append(message.from ? renderTag("from", message.from, { key: "from", value: message.from }) : el("span", "from", "unknown sender"));
  if (message.type && message.type !== DEFAULT_TYPE) header.append(renderTag("badge", message.type, { key: "type", value: message.type }));
  for (const target of message.targets ?? []) header.append(renderTag(target === state?.self ? "badge me" : "badge", "→ " + target, { key: "targets", value: target }));
  if (message.status === "verified") {
    const verified = el("span", "verified", "✓");
    verified.title = "verified";
    verified.setAttribute("aria-label", "verified");
    header.append(verified);
  } else {
    const warning = el("span", message.status === "rejected" ? "warning rejected" : "warning", "⚠ " + (STATUS_LABEL[message.status] ?? "unknown status"));
    if (message.reason) warning.title = message.reason;
    header.append(warning);
  }
  if (newTag) header.append(el("span", "new", newTag));
  header.append(renderTime(message, "time"), renderRawButton(message));
  return header;
}

// A row of the box: a small label on the left, the content on the right.
function renderSection(label: string, content: HTMLElement): HTMLElement {
  const section = el("div", "section");
  section.append(el("span", "label", label), content);
  return section;
}

// With a title the box has a label column, TITLE and TEXT split by a divider; without one it holds
// the text alone. A follow-up of a group has no header, so its time and raw message button sit in the box's corner.
function renderBox(message: Message, continued: boolean): HTMLElement {
  const box = el("div", "box");
  if (continued) {
    const corner = el("span", "corner");
    corner.append(renderTime(message, "time"), renderRawButton(message));
    box.append(corner);
  }
  if (message.title) {
    box.classList.add("labelled");
    box.append(renderSection("Title", el("div", "title", message.title)));
    if (message.text) box.append(renderSection("Text", el("div", "text", message.text)));
  } else {
    box.append(el("div", "text", message.text ?? ""));
  }
  return box;
}

// `row` carries the thread view's depth and the "reply to" line of a reply whose parent is missing.
// A thread root whose subtree has unseen mail is `highlighted`.
function renderMessage(row: ThreadRow, continued: boolean, isNew: boolean, highlighted: boolean): HTMLElement {
  const { message } = row;
  const item = el("div", (continued ? "message continued" : "message") + (row.depth > 0 ? " depth-" + row.depth : "") + (highlighted ? " has-new" : ""));
  item.dataset.sequence = String(message.sequence);
  if (row.replyTo) {
    const replyTo = el("div", "reply-to muted", "↳ reply to " + row.replyTo.slice(-ID_SUFFIX));
    replyTo.title = row.replyTo;
    item.append(replyTo);
  }
  if (!continued) item.append(renderHeader(message, isNew ? "New" : highlighted ? "new" : undefined));
  if (continued || message.title || message.text) item.append(renderBox(message, continued));
  if (message.reason && message.status !== "verified") item.append(el("div", "warning", message.reason));
  return item;
}

// "N replies" under a root, counting its whole subtree; "new" when the subtree holds unseen mail.
function renderReplies(summary: ThreadSummary): HTMLElement {
  const line = el("div", "replies muted", summary.replies === 1 ? "1 reply" : summary.replies + " replies");
  if (summary.unseen) line.append(" ", el("span", "new", "new"));
  return line;
}

// The "New" divider and the thread highlights go by `dividerAfter`: the first drawing takes it from
// the host. Mail that came in unseen while the page was away moves it; clearing it (to infinity)
// takes the markers away once everything below it was seen.
let dividerAfter = 0;
let hostDividerAfter: number | undefined;
const seen = new Set<number>(); // sequences that were on screen while the page was focused and visible

const isUnseen = (message: Message): boolean => message.sequence > dividerAfter && message.from !== state?.self;
const isActive = (): boolean => document.hasFocus() && document.visibilityState === "visible";

// An observer only fires on changes, so scrolling is caught here, and the rows already in view
// when the page gets focus or is drawn are caught by markVisibleRows.
const seenObserver = new IntersectionObserver((entries) => {
  if (!isActive()) return;
  for (const entry of entries) {
    if (entry.rootBounds && isSeen(entry.boundingClientRect, entry.rootBounds)) seen.add(Number((entry.target as HTMLElement).dataset.sequence));
  }
}, { root: list, threshold: [0, 0.25, 0.5, 0.75, 1] });

function markVisibleRows(): void {
  if (!isActive()) return;
  const view = list.getBoundingClientRect();
  for (const row of list.querySelectorAll<HTMLElement>("[data-sequence]")) {
    if (isSeen(row.getBoundingClientRect(), view)) seen.add(Number(row.dataset.sequence));
  }
}

// The page got focus or became visible. What was seen before decides whether the markers go;
// only then do the rows in view now count as seen.
function activate(): void {
  if (!isActive()) return;
  const unseen = shown().filter(isUnseen); // a message the filter hides cannot have been seen
  if (unseen.length > 0 && unseen.every((message) => seen.has(message.sequence))) {
    dividerAfter = Number.POSITIVE_INFINITY;
    render();
  }
  markVisibleRows();
}

const shown = (): Message[] => shownMessages(messages, state?.shownTypes ?? []);

// One chip per message type with its count, and "all". No chip pressed means every type is shown.
function renderTypeFilter(): void {
  const chosen = state?.shownTypes ?? [];
  const chip = (label: string, pressed: boolean, count: number | undefined, types: string[]): HTMLElement => {
    const button = el("button", count === 0 ? "pill empty-type" : "pill", count === undefined ? label : `${label} ${count}`);
    button.type = "button";
    button.setAttribute("aria-pressed", String(pressed));
    button.addEventListener("click", () => host.postMessage({ type: "shownTypes", value: types }));
    return button;
  };
  typeBar.replaceChildren(
    chip("all", chosen.length === 0, undefined, []),
    ...[...typeCounts(messages)].map(([type, count]) => chip(type, chosen.includes(type), count, toggleType(chosen, type))),
  );
}

// A hidden list loses its scroll position: the distance from the bottom is kept for the next drawing.
let heldFromBottom: number | undefined;

// Draws the list again. It keeps the distance from the bottom, so new messages at the end and
// older ones at the top leave what the reader looks at in place; the first drawing always goes to
// the end, the latest message. The flat list marks the first unseen message with a "New" divider
// above it; the thread view, whose order is not the stream's, with a "New" tag in its header.
function render(first = false): void {
  if (!state || list.hidden) return; // a search shows its results instead; the list is drawn again when it closes
  const { threaded } = state;
  const fromBottom = heldFromBottom ?? list.scrollHeight - list.scrollTop;
  heldFromBottom = undefined;
  seenObserver.disconnect();
  list.replaceChildren();
  if (state.hasOlder) {
    const older = el("button", "older", "Load older messages");
    older.addEventListener("click", () => host.postMessage({ type: "older" }));
    list.append(older);
  }
  // The divider, the thread tree and the groups go by the messages the type filter lets through.
  renderTypeFilter();
  const visible = shown();
  if (visible.length === 0) list.append(el("div", "empty", messages.length > 0 ? "No message of these types." : "No messages yet."));
  const firstUnseen = visible.find(isUnseen);
  const rows: ThreadRow[] = threaded ? threadRows(visible) : visible.map((message) => ({ message, depth: 0 }));
  const summaries = threaded ? summarizeThreads(rows, isUnseen) : undefined;
  let previous: ThreadRow | undefined;
  for (const row of rows) {
    const isNew = row.message === firstUnseen;
    if (isNew && !threaded) {
      list.append(el("div", "divider", "New"));
    }
    const summary = summaries?.get(row.message);
    // A divider, or a thread with unseen mail (its "new" badge sits in the header), is not grouped.
    const grouped = state.groupMessages && !isNew && !summary?.unseen && continuesGroup(previous, row);
    const item = renderMessage(row, grouped, isNew && threaded, summary?.unseen === true);
    list.append(item);
    seenObserver.observe(item);
    if (summary?.replies) list.append(renderReplies(summary));
    previous = row;
  }
  if (first) list.lastElementChild?.scrollIntoView({ block: "end" });
  else list.scrollTop = list.scrollHeight - fromBottom;
  markVisibleRows();
}

// The chosen targets, as chips before the input. With none, the @mentions in the text count.
let targets: string[] = [];

function candidates(): string[] {
  return state ? [ALL, ...state.members.filter((id) => id !== state!.self && id !== ALL)] : [];
}

// In a direct message the recipient is the only target and cannot change.
function isDirect(): boolean {
  return state !== undefined && state.defaultTarget !== ALL;
}

function renderTargets(): void {
  const chips = targets.map((id) => {
    const chip = el("span", "chip", isDirect() ? "→ " + id : id);
    if (!isDirect()) {
      const remove = el("button", "", "×");
      remove.type = "button";
      remove.title = "Remove " + id;
      remove.addEventListener("click", () => {
        targets = targets.filter((t) => t !== id);
        renderTargets();
        targetInput.focus();
      });
      chip.append(remove);
    }
    return chip;
  });
  targetBox.replaceChildren(...chips, targetInput);
  targetInput.hidden = isDirect();
  targetInput.placeholder = targets.length ? "" : "@ to target";
}

// The completion list, shared by the target input and the textarea. `accept` inserts the pick.
let completion: { items: string[]; index: number; accept: (id: string) => void } | undefined;

function closeCompletion(): void {
  completion = undefined;
  completions.hidden = true;
}

// The list opens above the box, at the left edge of the field that asked for it.
function showCompletion(anchor: HTMLElement, items: string[], accept: (id: string) => void): void {
  if (items.length === 0) return closeCompletion();
  completion = { items, index: 0, accept };
  completions.replaceChildren(...items.map((id, i) => {
    const row = el("li", i === 0 ? "active" : "", id);
    row.setAttribute("role", "option");
    row.addEventListener("mousedown", (event) => {
      event.preventDefault(); // keep the focus where it is
      accept(id);
    });
    return row;
  }));
  completions.hidden = false;
  completions.style.left = anchor.getBoundingClientRect().left - composer.getBoundingClientRect().left + "px";
}

function moveCompletion(step: number): void {
  if (!completion) return;
  const rows = completions.children;
  rows[completion.index].classList.remove("active");
  completion.index = (completion.index + step + rows.length) % rows.length;
  rows[completion.index].classList.add("active");
  rows[completion.index].scrollIntoView({ block: "nearest" });
}

// Arrow keys move, Tab and Enter complete, Esc closes. The answer says whether the key was used.
function completionKey(event: KeyboardEvent): boolean {
  if (!completion || event.isComposing) return false; // an input method owns the keys
  if (event.key === "ArrowDown" || event.key === "ArrowUp") moveCompletion(event.key === "ArrowDown" ? 1 : -1);
  else if (event.key === "Tab" || event.key === "Enter") completion.accept(completion.items[completion.index]);
  else if (event.key === "Escape") closeCompletion();
  else return false;
  event.preventDefault();
  return true;
}

function addTarget(id: string): void {
  if (!targets.includes(id)) targets.push(id);
  targetInput.value = "";
  closeCompletion();
  renderTargets();
}

// Text left in the target input counts when it names a candidate exactly; any other text is an
// unknown target and refuses the send, as on the web page. In a direct message the input is hidden.
function resolveTargetInput(): void {
  const typed = targetInput.value.trim();
  if (!typed || isDirect()) return;
  const id = resolveTarget(candidates(), typed);
  if (!id) throw new Error("Unknown target: " + typed);
  addTarget(id);
}

function completeTarget(): void {
  if (targetInput.value) showCompletion(targetInput, matchTargets(candidates(), targetInput.value, targets), addTarget);
  else closeCompletion();
}

function completeMention(): void {
  const mention = mentionBefore(textInput.value, textInput.selectionStart);
  if (!mention) return closeCompletion();
  showCompletion(textInput, matchTargets(candidates(), mention.query), (id) => {
    textInput.setRangeText(mentionText(id), mention.start, textInput.selectionStart, "end");
    closeCompletion();
    growComposer();
  });
}

function showState(next: ChatState): void {
  const first = state === undefined || state.title !== next.title;
  state = next;
  presence = parsePresenceMarks(next.presence);
  if (first || next.dividerAfter !== hostDividerAfter) {
    dividerAfter = next.dividerAfter;
    seen.clear();
  }
  hostDividerAfter = next.dividerAfter;
  document.getElementById("heading")!.textContent = next.title;
  document.getElementById("description")!.textContent = next.description;
  threadsToggle.setAttribute("aria-pressed", String(next.threaded));
  showSubscribed(next.subscribed);
  composer.hidden = !next.canSend;
  showNote();
  textInput.placeholder = isDirect() ? "Message " + next.defaultTarget : `Message ${next.title.replace(/^# /, "#")} (Enter sends, Shift+Enter for a new line)`;
  if (first || isDirect()) targets = [next.defaultTarget];
  renderTargets();
  renderMembers();
  messages.length = 0;
  for (const message of next.messages) insertMessage(messages, message);
  render(first);
}

// The CSS max-height stops the growth at about ten lines; the text scrolls from there.
function growComposer(): void {
  textInput.style.height = "auto";
  textInput.style.height = textInput.scrollHeight + "px";
}

let sending = false;
let sentNote: ReturnType<typeof setTimeout> | undefined;
const SENT_NOTE_MS = 3000;

// The note under the messages: who the page sends as, "Sent." for a moment after a send.
function showNote(sent = false): void {
  clearTimeout(sentNote);
  note.textContent = sent ? "Sent." : state?.note ?? "";
  if (sent) sentNote = setTimeout(showNote, SENT_NOTE_MS);
}

function updateSend(): void {
  sendButton.disabled = sending || !textInput.value.trim();
}

// ---- search ----
// While the search has text or chips, its results take the place of the message list.

const SEARCH_DELAY_MS = 250;
let searching = false;
let allChannels = false; // the search is not limited to the open channel
let chips: Chip[] = []; // the filters of clicked tags, ANDed with each other and the text
let searchRun = 0; // the host's answers to an older search are dropped
let appending = false; // the search under way asks for the next page
let searchTimer: ReturnType<typeof setTimeout> | undefined;

function hideList(): void {
  if (!list.hidden) heldFromBottom = list.scrollHeight - list.scrollTop;
  list.hidden = typeBar.hidden = true;
  results.hidden = false;
}

function showList(): void {
  results.hidden = true;
  list.hidden = typeBar.hidden = false;
  render();
}

function openSearch(): void {
  searching = true;
  searchbar.hidden = false;
  searchToggle.setAttribute("aria-pressed", "true");
  searchInput.focus();
  runSearch();
}

function closeSearch(): void {
  searching = false;
  searchRun++;
  clearTimeout(searchTimer);
  searchbar.hidden = true;
  searchToggle.setAttribute("aria-pressed", "false");
  showList();
}

// The chips sit in front of the input, each with a button that removes it.
function renderChips(): void {
  const nodes = chips.map((chip) => {
    const node = el("span", "chip", chipTerm(chip));
    const remove = el("button", "", "×");
    remove.type = "button";
    remove.title = "Remove " + chipTerm(chip);
    remove.setAttribute("aria-label", remove.title);
    remove.addEventListener("click", () => {
      chips = chips.filter((other) => other !== chip);
      renderChips();
      searchInput.focus();
      runSearch();
    });
    node.append(remove);
    return node;
  });
  searchField.replaceChildren(...nodes, searchInput);
}

function addSearchChip(chip: Chip): void {
  chips = withChip(chips, chip);
  renderChips();
  openSearch();
}

function renderHit(hit: SearchHitView): HTMLElement {
  const button = el("button", "hit");
  button.type = "button";
  const meta = el("span", "hit-meta");
  meta.append(el("span", "", hit.where), el("span", "from", hit.from ?? ""), el("span", "muted", hit.time ? shortTime(hit.time, new Date()) : ""));
  if (hit.type) meta.append(el("span", "badge", hit.type));
  button.append(meta);
  if (hit.title && hit.snippet.field !== "title") button.append(el("span", "hit-text hit-title", hit.title));
  const snippet = el("span", "hit-text");
  snippet.append(...snippetParts(hit.snippet.text, hit.snippet.matches).map((part) => (part.match ? el("mark", "", part.text) : document.createTextNode(part.text))));
  button.append(snippet);
  button.addEventListener("click", () => {
    closeSearch();
    host.postMessage({ type: "jump", channel: hit.channel, sequence: hit.sequence });
  });
  return button;
}

function runSearch(cursor?: string): void {
  clearTimeout(searchTimer);
  searchRun++;
  if (!searchInput.value.trim() && chips.length === 0) return showList();
  hideList();
  appending = cursor !== undefined;
  if (!appending) results.replaceChildren(el("div", "empty", "Searching…"));
  host.postMessage({ type: "search", run: searchRun, chips, text: searchInput.value, allChannels, cursor });
}

function showResults(answer: Extract<ToWebview, { type: "searchResults" }>): void {
  if (!searching || answer.run !== searchRun) return;
  if (!appending) results.replaceChildren();
  results.querySelector(".more")?.remove();
  if (answer.error) results.append(el("div", "empty", "Search failed: " + answer.error));
  else if (!appending && answer.hits.length === 0) results.append(el("div", "empty", "No messages found."));
  results.append(...answer.hits.map(renderHit));
  const { next } = answer;
  if (next) {
    const more = el("button", "older more", "Load more");
    more.addEventListener("click", () => {
      more.disabled = true;
      runSearch(next);
    });
    results.append(more);
  }
}

// Scrolls to the message and marks it for a moment.
function reveal(sequence: number): void {
  if (searching) closeSearch();
  const row = list.querySelector<HTMLElement>(`[data-sequence="${sequence}"]`);
  if (!row) return;
  row.scrollIntoView({ block: "center" });
  row.classList.add("flash");
  row.addEventListener("animationend", () => row.classList.remove("flash"), { once: true });
}

searchToggle.addEventListener("click", () => (searching ? closeSearch() : openSearch()));
document.getElementById("searchClose")!.addEventListener("click", closeSearch);
scopeToggle.addEventListener("click", () => {
  allChannels = !allChannels;
  scopeToggle.setAttribute("aria-pressed", String(allChannels));
  runSearch();
});
searchInput.addEventListener("input", () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(runSearch, SEARCH_DELAY_MS);
});
searchbar.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && event.target === searchInput) runSearch();
  else if (event.key === "Backspace" && event.target === searchInput && searchInput.value === "" && chips.length > 0) {
    chips.pop();
    renderChips();
    runSearch();
  } else return;
  event.preventDefault();
});
// On the document, so Esc closes the search from the results too; a key a completion list used is left alone.
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape" || !searching || event.defaultPrevented) return;
  event.preventDefault();
  closeSearch();
});
// Capturing, so the editor's own find does not open: the page owns Ctrl+F (Cmd+F on macOS).
window.addEventListener("keydown", (event) => {
  if (!(event.ctrlKey || event.metaKey) || event.altKey || event.shiftKey || event.key.toLowerCase() !== "f") return;
  event.preventDefault();
  openSearch();
  searchInput.select();
}, true);

window.addEventListener("message", (event: MessageEvent<ToWebview>) => {
  const data = event.data;
  if (data.type === "state") showState(data.state);
  else if (data.type === "searchResults") showResults(data);
  else if (data.type === "reveal") reveal(data.sequence);
  else if (data.type === "focusComposer") textInput.focus();
  else if (data.type === "activated") activate();
  else if (data.type === "subscribed") showSubscribed(data.value);
  else if (data.type === "presence") {
    presence = parsePresenceMarks(data.presence);
    paintDots();
  } else if (data.type === "message") {
    // Mail that arrives while nobody looks, with nothing unseen before it, starts a new unseen block.
    const lastBefore = messages.length > 0 ? messages[messages.length - 1].sequence : 0;
    const startsBlock = !isActive() && !messages.some(isUnseen);
    if (insertMessage(messages, data.message)) {
      if (startsBlock && data.message.sequence > lastBefore) {
        dividerAfter = lastBefore;
        seen.clear();
      }
      render();
    }
  } else {
    sending = false;
    problem.textContent = data.error ?? "";
    if (!data.error) {
      textInput.value = "";
      titleInput.value = "";
      growComposer();
      showNote(true);
    }
    updateSend();
  }
});

window.addEventListener("focus", activate);
document.addEventListener("visibilitychange", activate);

composer.addEventListener("submit", (event) => {
  event.preventDefault();
  if (sending) return; // Enter again while a send is under way posts nothing
  try {
    resolveTargetInput();
  } catch (error) {
    problem.textContent = (error as Error).message;
    return;
  }
  if (!textInput.value.trim()) return;
  sending = true;
  updateSend();
  problem.textContent = "";
  host.postMessage({ type: "send", draft: { type: typeSelect.value, targets: [...targets], title: titleInput.value, text: textInput.value } });
});

textInput.addEventListener("input", () => {
  growComposer();
  updateSend();
  completeMention();
});
textInput.addEventListener("click", completeMention);
textInput.addEventListener("keyup", (event) => {
  if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) completeMention(); // the caret moved
});
textInput.addEventListener("blur", closeCompletion);
textInput.addEventListener("keydown", (event) => {
  if (completionKey(event)) return;
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    composer.requestSubmit();
  }
});

targetInput.addEventListener("input", completeTarget);
targetInput.addEventListener("blur", closeCompletion);
targetInput.addEventListener("keydown", (event) => {
  if (completionKey(event)) return;
  if (event.key === "Backspace" && targetInput.value === "" && targets.length > 0) {
    targets.pop();
    renderTargets();
    targetInput.focus();
  } else if (event.key === "Enter") {
    event.preventDefault(); // not a send: the message is written in the text box
    textInput.focus();
  }
});

bellToggle.addEventListener("click", () => {
  if (state) host.postMessage({ type: "subscribe", value: bellToggle.getAttribute("aria-pressed") !== "true" });
});

threadsToggle.addEventListener("click", () => {
  if (state) host.postMessage({ type: "threaded", value: !state.threaded });
});

// Only the times change on the tick, not the whole list.
const timeTick = setInterval(() => {
  for (const time of list.querySelectorAll<HTMLElement>("[data-time]")) time.textContent = formatTime(time.dataset.time!);
}, TIME_TICK_MS);
window.addEventListener("pagehide", () => clearInterval(timeTick));

host.postMessage({ type: "ready" });
