// The chat panel's page. Messages are untrusted: everything goes in with textContent, never as HTML.
import { insertMessage } from "../messages";
import type { ChatState, FromWebview, ToWebview } from "../protocol";
import { ALL, MESSAGE_TYPES, Message } from "../types";

declare function acquireVsCodeApi(): { postMessage(message: FromWebview): void };
const host = acquireVsCodeApi();

const STATUS_LABEL: Record<string, string> = { verified: "verified", unknown_sender: "unknown sender", rejected: "rejected" };

// Only these become class names; the status comes from the wire.
const KNOWN_STATUSES: string[] = Object.keys(STATUS_LABEL);

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
const targetsInput = document.getElementById("targets") as HTMLInputElement;
const titleInput = document.getElementById("title") as HTMLInputElement;
const textInput = document.getElementById("text") as HTMLTextAreaElement;
const sendButton = document.getElementById("send") as HTMLButtonElement;
const problem = document.getElementById("problem") as HTMLElement;

for (const type of MESSAGE_TYPES) typeSelect.append(new Option(type, type));

function renderMessage(message: Message): HTMLElement {
  const isDirect = message.channel.startsWith("dm:");
  const header = el("div", "meta");
  header.append(el("span", "from", message.from || "unknown sender"));
  if (message.to) header.append(el("span", "muted", "→ " + (isDirect ? message.to : "#" + message.to)));
  if (message.time) header.append(el("span", "muted", new Date(message.time).toLocaleString()));
  if (message.type) header.append(el("span", "pill", message.type));
  header.append(el("span", "badge " + (KNOWN_STATUSES.includes(message.status) ? message.status : "unknown"), STATUS_LABEL[message.status] ?? "unknown status"));
  header.append(el("span", "muted", "#" + message.sequence));

  const item = el("div", "message");
  item.append(header);
  if (message.targets?.length) {
    const targets = el("div", "targets");
    for (const target of message.targets) targets.append(el("span", "pill " + (target === ALL ? "all" : target === state?.self ? "me" : ""), target));
    item.append(targets);
  }
  if (message.title) item.append(el("div", "title", message.title));
  if (message.text) item.append(el("div", "text", message.text));
  if (message.reason) item.append(el("div", "reason", message.reason));
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
  for (const message of messages) {
    if (!divider && message.sequence > state.dividerAfter && message.from !== state.self) {
      divider = el("div", "divider", "New");
      list.append(divider);
    }
    list.append(renderMessage(message));
  }
  if (first) (divider ?? list.lastElementChild)?.scrollIntoView({ block: divider ? "start" : "end" });
  else list.scrollTop = list.scrollHeight - fromBottom;
}

function showState(next: ChatState): void {
  const first = state === undefined || state.title !== next.title;
  state = next;
  document.getElementById("heading")!.textContent = next.title;
  document.getElementById("description")!.textContent = next.description;
  composer.hidden = !next.canSend;
  note.hidden = next.canSend;
  note.textContent = next.note;
  messages.length = 0;
  for (const message of next.messages) insertMessage(messages, message);
  render(first);
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
    }
  }
});

composer.addEventListener("submit", (event) => {
  event.preventDefault();
  if (!textInput.value.trim()) return;
  sendButton.disabled = true;
  problem.textContent = "";
  host.postMessage({ type: "send", draft: { type: typeSelect.value, targets: targetsInput.value, title: titleInput.value, text: textInput.value } });
});

textInput.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) composer.requestSubmit();
});

host.postMessage({ type: "ready" });
