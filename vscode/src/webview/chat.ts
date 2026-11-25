// The chat panel's page. Messages are untrusted: everything goes in with textContent, never as HTML.
import { matchTargets, mentionBefore } from "../completion";
import { continuesGroup, insertMessage } from "../messages";
import type { ChatState, FromWebview, ToWebview } from "../protocol";
import { ThreadRow, threadRows } from "../threads";
import { ALL, DEFAULT_TYPE, MESSAGE_TYPES, Message } from "../types";

declare function acquireVsCodeApi(): { postMessage(message: FromWebview): void };
const host = acquireVsCodeApi();

const STATUS_LABEL: Record<string, string> = { unknown_sender: "unknown sender", rejected: "rejected" };
const ID_PREFIX = 8; // characters of a message ID shown in "reply to"
const COMPOSER_MAX_HEIGHT = 12 * 16; // px; the CSS max-height says the same

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
const completions =document.getElementById("completions") as HTMLElement;
const titleInput = document.getElementById("title") as HTMLInputElement;
const titleToggle = document.getElementById("toggleTitle") as HTMLButtonElement;
const textInput = document.getElementById("text") as HTMLTextAreaElement;
const sendButton = document.getElementById("send") as HTMLButtonElement;
const problem = document.getElementById("problem") as HTMLElement;

for (const type of MESSAGE_TYPES) typeSelect.append(new Option(type, type));

// Today's messages show the time only.
function formatTime(time: string): string {
  const date = new Date(time);
  const today = date.toDateString() === new Date().toDateString();
  return today ? date.toLocaleTimeString([], { timeStyle: "short" }) : date.toLocaleString([], { dateStyle: "short", timeStyle: "short" });
}

function renderHeader(message: Message): HTMLElement {
  const header = el("div", "meta");
  header.append(el("span", "from", message.from || "unknown sender"));
  if (message.time) {
    const time = el("span", "time muted", formatTime(message.time));
    time.title = `${new Date(message.time).toLocaleString()} · #${message.sequence}`;
    header.append(time);
  }
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
function renderMessage(row: ThreadRow, continued: boolean, isNew: boolean): HTMLElement {
  const { message } = row;
  const item = el("div", (continued ? "message continued" : "message") + (row.depth > 0 ? " depth-" + row.depth : ""));
  if (row.replyTo) item.append(el("div", "reply-to muted", "↳ reply to " + row.replyTo.slice(0, ID_PREFIX)));
  if (!continued) item.append(renderHeader(message));
  if (isNew && !continued) item.firstElementChild?.append(el("span", "new", "New"));
  if (message.title) item.append(el("div", "title", message.title));
  if (message.text) item.append(el("div", "text", message.text));
  if (message.reason && message.status !== "verified") item.append(el("div", "warning", message.reason));
  return item;
}

// Draws the list again. It keeps the distance from the bottom, so new messages at the end and
// older ones at the top leave what the reader looks at in place; the first drawing goes to the
// first unseen message, or to the end. The flat list marks it with a "New" divider above it; the
// thread view, whose order is not the stream's, with a "New" tag in its header.
function render(first = false): void {
  if (!state) return;
  const { threaded, dividerAfter, self } = state;
  const fromBottom = list.scrollHeight - list.scrollTop;
  list.replaceChildren();
  if (state.hasOlder) {
    const older = el("button", "older", "Load older messages");
    older.addEventListener("click", () => host.postMessage({ type: "older" }));
    list.append(older);
  }
  const firstUnseen = messages.find((message) => message.sequence > dividerAfter && message.from !== self);
  const rows: ThreadRow[] = threaded ? threadRows(messages) : messages.map((message) => ({ message, depth: 0 }));
  let marked: HTMLElement | undefined;
  let previous: Message | undefined;
  for (const row of rows) {
    const isNew = row.message === firstUnseen;
    if (isNew && !threaded) {
      marked = el("div", "divider", "New");
      list.append(marked);
      previous = undefined;
    }
    const item = renderMessage(row, !threaded && continuesGroup(previous, row.message), isNew && threaded);
    if (isNew && threaded) marked = item;
    list.append(item);
    previous = row.message;
  }
  if (first) (marked ?? list.lastElementChild)?.scrollIntoView({ block: marked ? "start" : "end" });
  else list.scrollTop = list.scrollHeight - fromBottom;
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
    const chip = el("span", "chip", id);
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
  targetInput.placeholder = targets.length ? "" : "to: @name, or @mentions in text";
}

// The completion list, shared by the target input and the textarea. `accept` inserts the pick.
let completion: { items: string[]; index: number; accept: (id: string) => void } | undefined;

function closeCompletion(): void {
  completion = undefined;
  completions.hidden = true;
}

function showCompletion(items: string[], accept: (id: string) => void): void {
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
  if (!completion) return false;
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
  if (targetInput.value) showCompletion(matchTargets(candidates(), targetInput.value, targets), addTarget);
  else closeCompletion();
}

function completeMention(): void {
  const mention = mentionBefore(textInput.value, textInput.selectionStart);
  if (!mention) return closeCompletion();
  showCompletion(matchTargets(candidates(), mention.query), (id) => {
    textInput.setRangeText(id + " ", mention.start + 1, textInput.selectionStart, "end");
    closeCompletion();
    growComposer();
  });
}

function showState(next: ChatState): void {
  const first = state === undefined || state.title !== next.title;
  state = next;
  document.getElementById("heading")!.textContent = next.title;
  document.getElementById("description")!.textContent = next.description;
  threadsToggle.setAttribute("aria-pressed", String(next.threaded));
  composer.hidden = !next.canSend;
  note.hidden = next.canSend;
  note.textContent = next.note;
  if (first || isDirect()) targets = [next.defaultTarget];
  renderTargets();
  messages.length = 0;
  for (const message of next.messages) insertMessage(messages, message);
  render(first);
}

function growComposer(): void {
  textInput.style.height = "auto";
  textInput.style.height = Math.min(textInput.scrollHeight, COMPOSER_MAX_HEIGHT) + "px";
}

window.addEventListener("message", (event: MessageEvent<ToWebview>) => {
  const data = event.data;
  if (data.type === "state") showState(data.state);
  else if (data.type === "message") {
    if (insertMessage(messages, data.message)) render();
  } else {
    sendButton.disabled = false;
    problem.textContent = data.error ?? "";
    if (!data.error) {
      textInput.value = "";
      titleInput.value = "";
      growComposer();
    }
  }
});

composer.addEventListener("submit", (event) => {
  event.preventDefault();
  if (!textInput.value.trim()) return;
  sendButton.disabled = true;
  problem.textContent = "";
  host.postMessage({ type: "send", draft: { type: typeSelect.value, targets: [...targets], title: titleInput.value, text: textInput.value } });
});

textInput.addEventListener("input", () => {
  growComposer();
  completeMention();
});
textInput.addEventListener("click", completeMention);
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
  titleToggle.textContent = titleInput.hidden ? "+ title" : "− title";
  if (titleInput.hidden) titleInput.value = "";
  else titleInput.focus();
});

host.postMessage({ type: "ready" });
