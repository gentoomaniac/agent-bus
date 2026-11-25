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
}

export type ToWebview =
  | { type: "state"; state: ChatState }
  | { type: "message"; message: Message }
  | { type: "sendResult"; error?: string };

export interface Draft {
  type: string;
  targets: string;
  title: string;
  text: string;
}

export type FromWebview =
  | { type: "ready" }
  | { type: "older" }
  | { type: "send"; draft: Draft };
