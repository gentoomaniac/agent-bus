// The chat panel's page. Messages are untrusted: everything goes in with textContent, never as HTML.
import { matchTargets, mentionBefore, mentionText } from "../completion";
import { continuesGroup, insertMessage } from "../messages";
import { parsePresenceMarks } from "../protocol";
import type { ChatState, FromWebview, ToWebview } from "../protocol";
import { shortTime } from "../time";
import { isSeen } from "../viewport";
import { ThreadRow, ThreadSummary, summarizeThreads, threadRows } from "../threads";
import { ALL, DEFAULT_TYPE, MESSAGE_TYPES, Message } from "../types";

declare function acquireVsCodeApi(): { postMessage(message: FromWebview): void };
const host = acquireVsCodeApi();

const STATUS_LABEL: Record<string, string> = { unknown_sender: "unknown sender", rejected: "rejected" };
const ID_SUFFIX = 8; // characters of a message ID shown in "reply to": the end of a ULID tells IDs apart

let state: ChatState | undefined;
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
const completions = document.getElementById("completions") as HTMLElement;
const titleInput = document.getElementById("title") as HTMLInputElement;
const titleToggle = document.getElementById("toggleTitle") as HTMLButtonElement;
const textInput = document.getElementById("text") as HTMLTextAreaElement;
const sendButton = document.getElementById("send") as HTMLButtonElement;
const problem = document.getElementById("problem") as HTMLElement;

for (const type of MESSAGE_TYPES) typeSelect.append(new Option(type, type));

// The short time in the narrow column left of the text; the tooltip has the full date and the sequence.
function renderTime(message: Message): HTMLElement {
  const time = el("span", "time muted", message.time ? shortTime(message.time, new Date()) : "");
  if (message.time) time.title = `${new Date(message.time).toLocaleString([], { dateStyle: "full", timeStyle: "medium" })} · #${message.sequence}`;
  return time;
}

function renderHeader(message: Message): HTMLElement {
  const header = el("div", "meta");
  header.append(renderTime(message), el("span", "from", message.from || "unknown sender"));
  if (message.type && message.type !== DEFAULT_TYPE) header.append(el("span", "badge", message.type));
  for (const target of message.targets ?? []) header.append(el("span", target === state?.self ? "badge me" : "badge", "→ " + target));
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
  return header;
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
  item.append(continued ? renderTime(message) : renderHeader(message));
  if (!continued && (isNew || highlighted)) item.lastElementChild?.append(el("span", "new", isNew ? "New" : "new"));
  if (message.title) item.append(el("div", "title", message.title));
  if (message.text) item.append(el("div", "text", message.text));
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
  const unseen = messages.filter(isUnseen);
  if (unseen.length > 0 && unseen.every((message) => seen.has(message.sequence))) {
    dividerAfter = Number.POSITIVE_INFINITY;
    render();
  }
  markVisibleRows();
}

// Draws the list again. It keeps the distance from the bottom, so new messages at the end and
// older ones at the top leave what the reader looks at in place; the first drawing goes to the
// first unseen message, or to the end. The flat list marks it with a "New" divider above it; the
// thread view, whose order is not the stream's, with a "New" tag in its header.
function render(first = false): void {
  if (!state) return;
  const { threaded } = state;
  const fromBottom = list.scrollHeight - list.scrollTop;
  seenObserver.disconnect();
  list.replaceChildren();
  if (state.hasOlder) {
    const older = el("button", "older", "Load older messages");
    older.addEventListener("click", () => host.postMessage({ type: "older" }));
    list.append(older);
  }
  const firstUnseen = messages.find(isUnseen);
  const rows: ThreadRow[] = threaded ? threadRows(messages) : messages.map((message) => ({ message, depth: 0 }));
  const summaries = threaded ? summarizeThreads(rows, isUnseen) : undefined;
  let marked: HTMLElement | undefined;
  let previous: Message | undefined;
  for (const row of rows) {
    const isNew = row.message === firstUnseen;
    if (isNew && !threaded) {
      marked = el("div", "divider", "New");
      list.append(marked);
      previous = undefined;
    }
    const summary = summaries?.get(row.message);
    const item = renderMessage(row, !threaded && continuesGroup(previous, row.message), isNew && threaded, summary?.unseen === true);
    if (isNew && threaded) marked = item;
    list.append(item);
    seenObserver.observe(item);
    if (summary?.replies) list.append(renderReplies(summary));
    previous = row.message;
  }
  if (first) (marked ?? list.lastElementChild)?.scrollIntoView({ block: marked ? "start" : "end" });
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
  targetInput.focus();
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
  state = { ...next, presence: parsePresenceMarks(next.presence) };
  if (first || next.dividerAfter !== hostDividerAfter) {
    dividerAfter = next.dividerAfter;
    seen.clear();
  }
  hostDividerAfter = next.dividerAfter;
  document.getElementById("heading")!.textContent = next.title;
  document.getElementById("description")!.textContent = next.description;
  threadsToggle.setAttribute("aria-pressed", String(next.threaded));
  composer.hidden = !next.canSend;
  note.hidden = next.canSend;
  note.textContent = next.note;
  textInput.placeholder = isDirect() ? "Message " + next.defaultTarget : `Message ${next.title.replace(/^# /, "#")} (Enter sends, Shift+Enter for a new line)`;
  if (first || isDirect()) targets = [next.defaultTarget];
  renderTargets();
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

function updateSend(): void {
  sendButton.disabled = sending || !textInput.value.trim();
}

window.addEventListener("message", (event: MessageEvent<ToWebview>) => {
  const data = event.data;
  if (data.type === "state") showState(data.state);
  else if (data.type === "focusComposer") textInput.focus();
  else if (data.type === "activated") activate();
  else if (data.type === "presence") {
    if (state) state.presence = parsePresenceMarks(data.presence);
  }
  else if (data.type === "message") {
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
    }
    updateSend();
  }
});

window.addEventListener("focus", activate);
document.addEventListener("visibilitychange", activate);

composer.addEventListener("submit", (event) => {
  event.preventDefault();
  if (sending || !textInput.value.trim()) return; // Enter again while a send is under way posts nothing
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

threadsToggle.addEventListener("click", () => {
  if (state) host.postMessage({ type: "threaded", value: !state.threaded });
});

titleToggle.addEventListener("click", () => {
  titleInput.hidden = !titleInput.hidden;
  titleToggle.setAttribute("aria-pressed", String(!titleInput.hidden));
  if (titleInput.hidden) titleInput.value = "";
  else titleInput.focus();
});

host.postMessage({ type: "ready" });
