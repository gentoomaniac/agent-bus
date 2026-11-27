// The monitor API's shapes (docs/api.md); only the fields the extension uses.

export type ChannelKind = "general" | "group" | "dm";

export interface Channel {
  id: string; // "general", "group:<name>" or "dm:<agent>:<agent>"
  kind: ChannelKind;
  name: string; // a DM's name is "<agent> + <agent>"
  description: string;
  owner?: string; // the agent that owns a group channel; missing: the bus owner's
  agents?: string[]; // DM only
  messages: number;
  last_sequence: number;
  unread: number; // messages after the read marker that the monitor's agent did not write, as the monitor counts them
  targeted_unread: number; // of those, the ones aimed at the agent
}

export interface Agent {
  id: string;
  fingerprint: string;
  removed: boolean;
}

export interface Presence {
  agent: string;
  state: string; // working, idle, waiting_for_owner, rate_limited, error:<kind>, unresponsive, offline
  since?: string;
  activity?: string;
  activity_since?: string;
  workers?: { id?: string; description: string; since: string; context?: Context }[];
  recent?: { at: string; text: string }[]; // oldest first
  pending?: number; // unread mail targeted at the agent
  context?: Context; // of the session's latest model reply
}

// How many tokens the latest model reply was given; the window's size is not known.
export interface Context {
  tokens: number;
  model?: string;
  at?: string;
}

export interface Status {
  bus: "live" | "reconnecting";
  agent: string;
  can_send: boolean;
  note: string;
  password_set: boolean;
  pending_enrollments?: number; // agents waiting for approval (agent-busctl ca review); missing from an older monitor
}

export type MessageStatus = "verified" | "unknown_sender" | "rejected";

// Everything in a message is untrusted text from agents.
export interface Message {
  sequence: number;
  channel: string;
  status: MessageStatus;
  reason?: string;
  id?: string;
  from?: string;
  to?: string;
  thread?: string; // the root message of the thread
  reply_to?: string; // the parent message
  type?: string;
  targets?: string[];
  time?: string;
  title?: string;
  text?: string;
}

// A message as stored (GET /api/v1/messages/raw): the signed envelope, or its bytes when they are not JSON.
export interface RawMessage {
  channel: string;
  sequence: number;
  status: MessageStatus;
  reason?: string;
  size: number;
  raw?: unknown;
  raw_text?: string;
}

// One search result (docs/api.md, "Search"). `matches` are [start, end) offsets in code points of `snippet.text`.
export interface SearchHit {
  channel: string;
  sequence: number;
  from?: string;
  type?: string;
  time?: string;
  title?: string;
  snippet: { field: string; text: string; matches?: [number, number][] };
}

export interface SearchPage {
  results: SearchHit[];
  next_cursor?: string;
}

export const MESSAGE_TYPES = ["message", "request", "question", "answer", "feedback", "bug_report", "feature_request", "status", "ack"];
export const ALL = "@all";
export const DEFAULT_TYPE = "message";

// A change to a group channel as the bus owner (docs/api.md, channel changes).
export type ChannelAction = "create" | "invite" | "remove_member" | "update";

export interface ChannelChange {
  channel: string; // the name, without "group:"
  description?: string; // create, update
  agent?: string; // invite, remove_member
}

export interface ChangedChannel {
  name: string;
  description: string;
  owner?: string;
  members: string[];
  warning?: string; // why an invited agent was not told
}

export interface SendRequest {
  to: { kind: "agent" | "group"; name: string };
  type: string;
  targets: string[];
  title?: string;
  text: string;
}

// The shared preferences (docs/api.md): read markers by channel, favourite and subscribed channels and folded sections.
export interface ServerPrefs {
  read: Record<string, number>;
  favourites: string[];
  subscribed: string[]; // missing from a monitor older than subscriptions
  collapsed: string[]; // favourites, channels, dms, agents
}
