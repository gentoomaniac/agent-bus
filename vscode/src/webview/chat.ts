// The chat panel's page. Messages are untrusted: everything goes in with textContent, never as HTML.
import { continuesGroup, insertMessage } from "../messages";
import type { ChatState, FromWebview, ToWebview } from "../protocol";
import { ALL, DEFAULT_TYPE, MESSAGE_TYPES, Message } from "../types";

declare function acquireVsCodeApi(): { postMessage(message: FromWebview): void };
const host = acquireVsCodeApi();

const STATUS_LABEL: Record<string, string> = { unknown_sender: "unknown sender", rejected: "rejected" };
const MENTIONS = ""; // the target picker's value for "no target picked: the @mentions in the text count"
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
const targetSelect = document.getElementById("target") as HTMLSelectElement;
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

function renderMessage(message: Message, continued: boolean): HTMLElement {
  const item = el("div", continued ? "message continued" : "message");
  if (!continued) item.append(renderHeader(message));
  if (message.title) item.append(el("div", "title", message.title));
  if (message.text) item.append(el("div", "text", message.text));
  if (message.reason && message.status !== "verified") item.append(el("div", "warning", message.reason));
  return item;
}

// Draws the list again. It keeps the distance from the bottom, so new messages at the end and
// older ones at the top leave what the reader looks at in place; the first drawing goes to the
// "New" divider, or to the end.
function render(first = false): void {
  if (!state) return;
  const fromBottom = list.scrollHeight - list.scrollTop;
  list.replaceChildren();
  if (state.hasOlder) {
    const older = el("button", "older", "Load older messages");
    older.addEventListener("click", () => host.postMessage({ type: "older" }));
    list.append(older);
  }
  let divider: HTMLElement | undefined;
  let previous: Message | undefined;
  for (const message of messages) {
    if (!divider && message.sequence > state.dividerAfter && message.from !== state.self) {
      divider = el("div", "divider", "New");
      list.append(divider);
      previous = undefined;
    }
    list.append(renderMessage(message, continuesGroup(previous, message)));
    previous = message;
  }
  if (first) (divider ?? list.lastElementChild)?.scrollIntoView({ block: divider ? "start" : "end" });
  else list.scrollTop = list.scrollHeight - fromBottom;
}

// The picker offers the channel's members and @all. A pick survives a new state while it is still offered.
function fillTargets(next: ChatState, keepPick: boolean): void {
  const pick = keepPick ? targetSelect.value : next.defaultTarget;
  const offered = [ALL, ...next.members.filter((id) => id !== next.self && id !== ALL)];
  if (!offered.includes(next.defaultTarget)) offered.push(next.defaultTarget);
  targetSelect.replaceChildren(new Option("to: @mentions in text", MENTIONS), ...offered.map((id) => new Option("to: " + id, id)));
  targetSelect.value = pick === MENTIONS || offered.includes(pick) ? pick : next.defaultTarget;
}

function showState(next: ChatState): void {
  const first = state === undefined || state.title !== next.title;
  state = next;
  document.getElementById("heading")!.textContent = next.title;
  document.getElementById("description")!.textContent = next.description;
  composer.hidden = !next.canSend;
  note.hidden = next.canSend;
  note.textContent = next.note;
  fillTargets(next, !first);
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
  const targets = targetSelect.value === MENTIONS ? [] : [targetSelect.value];
  host.postMessage({ type: "send", draft: { type: typeSelect.value, targets, title: titleInput.value, text: textInput.value } });
});

textInput.addEventListener("input", growComposer);
textInput.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    composer.requestSubmit();
  }
});

titleToggle.addEventListener("click", () => {
  titleInput.hidden = !titleInput.hidden;
  titleToggle.textContent = titleInput.hidden ? "+ title" : "− title";
  if (titleInput.hidden) titleInput.value = "";
  else titleInput.focus();
});

host.postMessage({ type: "ready" });
