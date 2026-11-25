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
