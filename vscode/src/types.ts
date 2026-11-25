// The monitor API's shapes (docs/api.md); only the fields the extension uses.

export type ChannelKind = "general" | "group" | "dm";

export interface Channel {
  id: string; // "general", "group:<name>" or "dm:<agent>:<agent>"
  kind: ChannelKind;
  name: string; // a DM's name is "<agent> + <agent>"
  description: string;
  agents?: string[]; // DM only
  messages: number;
  last_sequence: number;
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
  workers?: { id?: string; description: string; since: string }[];
  recent?: { at: string; text: string }[]; // oldest first
  pending?: number; // unread mail targeted at the agent
}

export interface Status {
  bus: "live" | "reconnecting";
  agent: string;
  can_send: boolean;
  note: string;
  password_set: boolean;
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

export const MESSAGE_TYPES = ["message", "request", "question", "answer", "feedback", "bug_report", "feature_request", "status", "ack"];
export const ALL = "@all";
export const DEFAULT_TYPE = "message";

export interface SendRequest {
  to: { kind: "agent" | "group"; name: string };
  type: string;
  targets: string[];
  title?: string;
  text: string;
}

// The shared preferences (docs/api.md): read markers by channel, favourite channels and folded sections.
export interface ServerPrefs {
  read: Record<string, number>;
  favourites: string[];
  collapsed: string[]; // favourites, channels, dms, agents
}
