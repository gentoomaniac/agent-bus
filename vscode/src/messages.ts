import { ThreadRow } from "./threads";
import { ALL, Message } from "./types";

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

// Whether the row continues the previous one's group, as on the web page: the same sender wrote
// it to the same place, with the same type and verification status, within five minutes, and
// answering the same message (loaded or not).
export function continuesGroup(previous: ThreadRow | undefined, row: ThreadRow): boolean {
  if (!previous || !row.message.from || previous.parent !== row.parent || previous.replyTo !== row.replyTo) return false;
  const [before, message] = [previous.message, row.message];
  return before.from === message.from && before.to === message.to && before.type === message.type && before.status === message.status
    && Date.parse(message.time ?? "") - Date.parse(before.time ?? "") < GROUP_WINDOW_MS;
}

// The text of the button that loads older messages; after a failed load it says why, and the page
// stops loading by itself until the button is pressed.
export function olderButtonText(error: string | undefined): string {
  return error ? `Load older messages (the last try failed: ${error})` : "Load older messages";
}
