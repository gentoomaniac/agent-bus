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
  | { type: "sendResult"; error?: string };

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
