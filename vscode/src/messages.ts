import { ALL, DEFAULT_TYPE, Message } from "./types";

const GROUP_WINDOW_MS = 5 * 60 * 1000;

// Inserts the message into the channel's list, which stays ordered by sequence. A message that
// is already in it (same sequence) is a duplicate: nothing changes and the answer is false.
export function insertMessage(list: Message[], message: Message): boolean {
  let at = list.length;
  while (at > 0 && list[at - 1].sequence > message.sequence) at--;
  if (at > 0 && list[at - 1].sequence === message.sequence) return false;
  list.splice(at, 0, message);
  return true;
}

// Mail for the agent: it is a target, or the message goes to everyone. The agent's own messages
// are never mail for it.
export function isTargeted(message: Message, agent: string): boolean {
  if (message.from === agent) return false;
  return (message.targets ?? []).some((target) => target === ALL || target === agent);
}

// Messages after the last-read sequence that the agent did not write itself.
export function unreadMessages(list: Message[], lastRead: number, agent: string): Message[] {
  return list.filter((message) => message.sequence > lastRead && message.from !== agent);
}

// Whether the message continues the previous one's group: the same sender within five minutes,
// with the same type and targets, so that hiding its header loses nothing. A title starts a group.
export function continuesGroup(previous: Message | undefined, message: Message): boolean {
  if (!previous || !message.from || previous.from !== message.from || message.title) return false;
  if ((previous.type ?? DEFAULT_TYPE) !== (message.type ?? DEFAULT_TYPE)) return false;
  if ((previous.targets ?? []).join(" ") !== (message.targets ?? []).join(" ")) return false;
  const gap = Date.parse(message.time ?? "") - Date.parse(previous.time ?? "");
  return gap >= 0 && gap <= GROUP_WINDOW_MS;
}
