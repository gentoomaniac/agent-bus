import { DEFAULT_TYPE, MESSAGE_TYPES, Message } from "./types";

const typeOf = (message: Message): string => message.type ?? DEFAULT_TYPE;

// How many of the messages each type has, the known types first, also those with none.
export function typeCounts(messages: Message[]): Map<string, number> {
  const counts = new Map<string, number>(MESSAGE_TYPES.map((type) => [type, 0]));
  for (const message of messages) counts.set(typeOf(message), (counts.get(typeOf(message)) ?? 0) + 1);
  return counts;
}

// No chosen type means every type is shown.
export function isShown(message: Message, shown: readonly string[]): boolean {
  return shown.length === 0 || shown.includes(typeOf(message));
}

export function shownMessages(messages: Message[], shown: readonly string[]): Message[] {
  return shown.length === 0 ? messages : messages.filter((message) => isShown(message, shown));
}

export function toggleType(shown: readonly string[], type: string): string[] {
  return shown.includes(type) ? shown.filter((other) => other !== type) : [...shown, type];
}
