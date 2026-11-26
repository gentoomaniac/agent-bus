import { Message } from "./types";

export const MAX_DEPTH = 4; // deeper replies are drawn at this level

export interface ThreadRow {
  message: Message;
  depth: number; // 0 for a root, at most MAX_DEPTH
  replyTo?: string; // a root that answers a message which is not loaded: that message's ID
  parent?: Message; // the message it answers, for a reply
}

export interface ThreadSummary {
  replies: number; // the whole subtree below the root
  unseen: boolean; // the root or any reply is unseen
}

// The summary of every thread (a root with its subtree) in rows from threadRows, keyed by the root message.
export function summarizeThreads(rows: ThreadRow[], isUnseen: (message: Message) => boolean): Map<Message, ThreadSummary> {
  const summaries = new Map<Message, ThreadSummary>();
  let current: ThreadSummary | undefined;
  for (const { message, depth } of rows) {
    if (depth === 0) summaries.set(message, (current = { replies: 0, unseen: isUnseen(message) }));
    else if (current) {
      current.replies++;
      current.unseen ||= isUnseen(message);
    }
  }
  return summaries;
}

// The parent of a message among the loaded ones: `reply_to`, else the `thread` root. A parent
// must come earlier in the stream, which also rules out cycles in bad data.
function parentOf(message: Message, byId: Map<string, Message>): Message | undefined {
  for (const id of [message.reply_to, message.thread]) {
    const parent = id ? byId.get(id) : undefined;
    if (parent && parent.sequence < message.sequence) return parent;
  }
  return undefined;
}

// Orders the messages of a channel as threads: roots by sequence, each followed by its replies
// (depth first, siblings by sequence). A message whose parent is not loaded is a root of its own,
// and says which message it answers.
export function threadRows(messages: Message[]): ThreadRow[] {
  const byId = new Map<string, Message>();
  for (const message of messages) if (message.id) byId.set(message.id, message);

  const children = new Map<Message, Message[]>();
  const roots: ThreadRow[] = [];
  for (const message of [...messages].sort((a, b) => a.sequence - b.sequence)) {
    const parent = parentOf(message, byId);
    if (!parent) roots.push({ message, depth: 0, replyTo: message.reply_to || message.thread || undefined });
    else children.set(parent, [...(children.get(parent) ?? []), message]);
  }

  const rows: ThreadRow[] = [];
  const walk = (row: ThreadRow): void => {
    rows.push(row);
    for (const child of children.get(row.message) ?? []) walk({ message: child, depth: Math.min(row.depth + 1, MAX_DEPTH), parent: row.message });
  };
  roots.forEach(walk);
  return rows;
}

// The flat list's rows, in stream order: each reply knows its parent among `all` (every loaded
// message of the channel, also those the type filter hides), or the ID of the one it answers.
export function flatRows(messages: Message[], all: Message[]): ThreadRow[] {
  const byId = new Map<string, Message>();
  for (const message of all) if (message.id) byId.set(message.id, message);
  return messages.map((message) => {
    const parent = parentOf(message, byId);
    return parent ? { message, depth: 0, parent } : { message, depth: 0, replyTo: message.reply_to || undefined };
  });
}

// The thread of each loaded message: the top of its chain of loaded parents, or the message that
// top answers when that one is not loaded. Messages with the same key belong to one conversation.
export function threadKeys(messages: Message[]): Map<Message, string> {
  const byId = new Map<string, Message>();
  for (const message of messages) if (message.id) byId.set(message.id, message);
  const keys = new Map<Message, string>();
  const keyOf = (message: Message): string => {
    const known = keys.get(message);
    if (known !== undefined) return known;
    const parent = parentOf(message, byId);
    const thread = message.thread && message.thread !== message.id ? message.thread : "";
    const key = parent ? keyOf(parent) : message.reply_to || thread || message.id || "#" + message.sequence;
    keys.set(message, key);
    return key;
  };
  for (const message of messages) keyOf(message);
  return keys;
}

// A hue (0–359) for a thread key, so every message of a thread carries the same color.
export function threadHue(key: string): number {
  let hash = 0;
  for (let i = 0; i < key.length; i++) hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
  return hash % 360;
}

// The first line of a message, cut to `max` characters: what a reply line quotes of its parent.
export function quoteOf(message: Message, max = 80): string {
  const line = (message.title || message.text || "").split("\n").find((part) => part.trim()) ?? "";
  return line.length > max ? line.slice(0, max - 1).trimEnd() + "…" : line.trim();
}
