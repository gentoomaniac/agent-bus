import { Message } from "./types";

// What the chat panel's page knows. The page never talks to the monitor: the extension does.
export interface ChatState {
  title: string;
  description: string;
  self: string; // the monitor's agent
  canSend: boolean;
  note: string; // why the composer is missing, when it is
  messages: Message[];
  dividerAfter: number; // the "New" divider goes before the first message after this sequence
  hasOlder: boolean;
  members: string[]; // agents the target picker offers, besides @all
  defaultTarget: string; // @all in a channel, the recipient in a direct message
  threaded: boolean; // the thread view is on (a UI choice, kept per editor)
}

export type ToWebview =
  | { type: "state"; state: ChatState }
  | { type: "message"; message: Message }
  | { type: "sendResult"; error?: string }
  | { type: "focusComposer" }
  | { type: "activated" }; // the panel became visible and active

export interface Draft {
  type: string;
  targets: string[]; // empty: the @mentions in the text count
  title: string;
  text: string;
}

export type FromWebview =
  | { type: "ready" }
  | { type: "older" }
  | { type: "send"; draft: Draft }
  | { type: "threaded"; value: boolean };

// What the page posted, if it is a well-formed message. The page is our own code, but it renders
// untrusted text, so the host checks what it acts on.
export function parseFromWebview(raw: unknown): FromWebview | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const message = raw as Record<string, unknown>;
  if (message.type === "ready" || message.type === "older") return { type: message.type };
  if (message.type === "threaded") return typeof message.value === "boolean" ? { type: "threaded", value: message.value } : undefined;
  if (message.type !== "send" || typeof message.draft !== "object" || message.draft === null) return undefined;
  const { type, targets, title, text } = message.draft as Record<string, unknown>;
  if (typeof type !== "string" || typeof title !== "string" || typeof text !== "string") return undefined;
  if (!Array.isArray(targets) || !targets.every((target) => typeof target === "string")) return undefined;
  return { type: "send", draft: { type, targets, title, text } };
}
