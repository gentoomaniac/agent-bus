import { CHIP_KEYS, Chip } from "./search";
import { Message, SearchHit } from "./types";

// An agent's presence as the page draws it: a dot, with the state and its start as the tooltip.
export interface PresenceMark {
  state: string;
  since?: string;
}

// What the chat panel's page knows. The page never talks to the monitor: the extension does.
export interface ChatState {
  title: string;
  description: string;
  self: string; // the monitor's agent
  canSend: boolean;
  note: string; // who the composer sends as, or why it is missing
  messages: Message[];
  dividerAfter: number; // the "New" divider goes before the first message after this sequence
  hasOlder: boolean;
  olderError?: string; // why the last read of older messages failed; until the next success or reconnect
  members: string[]; // agents the target picker offers, besides @all
  canManageMembers: boolean; // the pane offers to invite and remove: a group channel, and the monitor can send
  defaultTarget: string; // @all in a channel, the recipient in a direct message
  subscribed: boolean; // every message of the channel notifies (shared through the monitor)
  groupMessages: boolean; // follow-ups of the same sender share one header (the setting agentMessenger.groupMessages)
  timeFormat: "absolute" | "relative"; // the setting agentMessenger.timeFormat
  threaded: boolean; // the thread view is on (a UI choice, kept per editor)
  shownTypes: string[]; // the list is limited to these message types; none means all (kept per editor, for every panel)
  presence: Record<string, PresenceMark>; // by agent; a plain object, as a Map does not survive postMessage
}

// A search result with the channel's name, so the page does not need the channel list.
export interface SearchHitView extends SearchHit {
  where: string;
}

// The message a reply answers, looked up by ID because the page has not loaded it: with `where`
// (the channel's name as the page shows it) when it is in another channel.
export interface ParentView extends Message {
  where?: string;
}

export type ToWebview =
  | { type: "state"; state: ChatState }
  | { type: "message"; message: Message }
  | { type: "presence"; presence: Record<string, PresenceMark> } // a presence event: only the dots change
  | { type: "subscribed"; value: boolean } // the subscription changed, here or in another client
  | { type: "sendResult"; error?: string }
  | { type: "olderDone" } // the host has finished the older-messages request, whether or not a state came with it
  | { type: "focusComposer" }
  | { type: "searchResults"; run: number; hits: SearchHitView[]; next?: string; error?: string } // the answer to the search with the same run
  | { type: "parent"; id: string; message?: ParentView; gone?: boolean } // the answer to the lookup of `id`; neither field: it failed
  | { type: "reveal"; sequence: number } // scroll to the message and flash it
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
  | { type: "threaded"; value: boolean }
  | { type: "shownTypes"; value: string[] }
  | { type: "subscribe"; value: boolean } // subscribe the channel or unsubscribe it
  | { type: "parent"; id: string } // look up a message by ID: the parent of a reply that is not loaded
  | { type: "raw"; sequence: number } // open the record as stored
  | { type: "search"; run: number; chips: Chip[]; text: string; allChannels: boolean; cursor?: string }
  | { type: "jump"; channel: string; sequence: number } // show a search result: load history and open its channel if needed
  | { type: "invite" } // pick an agent to invite to the channel
  | { type: "removeMember"; agent: string };

function isChip(raw: unknown): raw is Chip {
  if (typeof raw !== "object" || raw === null) return false;
  const { key, value } = raw as Record<string, unknown>;
  return typeof key === "string" && CHIP_KEYS.includes(key) && typeof value === "string" && value.trim() !== "";
}

// What the page posted, if it is a well-formed message. The page is our own code, but it renders
// untrusted text, so the host checks what it acts on.
export function parseFromWebview(raw: unknown): FromWebview | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const message = raw as Record<string, unknown>;
  if (message.type === "ready" || message.type === "older" || message.type === "invite") return { type: message.type };
  if (message.type === "parent") return typeof message.id === "string" && message.id !== "" ? { type: "parent", id: message.id } : undefined;
  if (message.type === "removeMember") return typeof message.agent === "string" && message.agent !== "" ? { type: "removeMember", agent: message.agent } : undefined;
  if (message.type === "raw") return Number.isSafeInteger(message.sequence) && (message.sequence as number) >= 0 ? { type: "raw", sequence: message.sequence as number } : undefined;
  if (message.type === "search") {
    const { run, chips, text, allChannels, cursor } = message;
    if (!Number.isSafeInteger(run) || typeof text !== "string" || typeof allChannels !== "boolean") return undefined;
    if (cursor !== undefined && typeof cursor !== "string") return undefined;
    if (!Array.isArray(chips) || !chips.every(isChip)) return undefined;
    return { type: "search", run: run as number, chips: chips.map(({ key, value }) => ({ key, value })), text, allChannels, ...(cursor === undefined ? {} : { cursor }) };
  }
  if (message.type === "jump") {
    const { channel, sequence } = message;
    return typeof channel === "string" && channel !== "" && Number.isSafeInteger(sequence) && (sequence as number) >= 0 ? { type: "jump", channel, sequence: sequence as number } : undefined;
  }
  if (message.type === "threaded" || message.type === "subscribe") return typeof message.value === "boolean" ? { type: message.type, value: message.value } : undefined;
  if (message.type === "shownTypes") return Array.isArray(message.value) && message.value.every((type) => typeof type === "string") ? { type: "shownTypes", value: message.value } : undefined;
  if (message.type !== "send" || typeof message.draft !== "object" || message.draft === null) return undefined;
  const { type, targets, title, text } = message.draft as Record<string, unknown>;
  if (typeof type !== "string" || typeof title !== "string" || typeof text !== "string") return undefined;
  if (!Array.isArray(targets) || !targets.every((target) => typeof target === "string")) return undefined;
  return { type: "send", draft: { type, targets, title, text } };
}

// The presence marks the host sent, without the entries that are not well-formed. The page draws
// them next to untrusted names, so it takes only what it can check.
export function parsePresenceMarks(raw: unknown): Map<string, PresenceMark> {
  const marks = new Map<string, PresenceMark>();
  if (typeof raw !== "object" || raw === null) return marks;
  for (const [agent, mark] of Object.entries(raw)) {
    if (typeof mark !== "object" || mark === null) continue;
    const { state, since } = mark as Record<string, unknown>;
    if (typeof state !== "string") continue;
    marks.set(agent, typeof since === "string" ? { state, since } : { state });
  }
  return marks;
}
