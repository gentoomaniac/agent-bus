import { Message } from "./types";

export const MAX_DEPTH = 4; // deeper replies are drawn at this level

export interface ThreadRow {
  message: Message;
  depth: number; // 0 for a root, at most MAX_DEPTH
  replyTo?: string; // a root that answers a message which is not loaded: that message's ID
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
    for (const child of children.get(row.message) ?? []) walk({ message: child, depth: Math.min(row.depth + 1, MAX_DEPTH) });
  };
  roots.forEach(walk);
  return rows;
}
