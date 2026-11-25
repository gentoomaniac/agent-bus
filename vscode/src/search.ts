import { describeChannel } from "./channels";
import { Channel } from "./types";

// A filter of a query is one token: a value with white space is quoted, as the API reads it (from:"a b").
function term(key: string, value: string): string {
  const plain = value.replaceAll('"', "");
  return /\s/.test(plain) ? `${key}:"${plain}"` : `${key}:${plain}`;
}

// The `to:` terms that limit a search to one conversation. A group is `to:#name`. `to:` names the
// recipient of a direct message, so a DM needs both of its agents; the results then hold the
// agents' other DMs too, and the caller keeps only the results of the channel.
export function scopeTerms(channel: Channel): string[] {
  if (channel.kind === "general") return ["to:#general"];
  if (channel.kind === "group") return [term("to", "#" + channel.name)];
  return (channel.agents ?? []).map((agent) => term("to", agent));
}

// A filter a click on a tag adds to the search, shown as a removable chip: `from:`, `type:` or `targets:`.
export interface Chip {
  key: "from" | "type" | "targets";
  value: string;
}

export const CHIP_KEYS: readonly string[] = ["from", "type", "targets"];

export function chipTerm({ key, value }: Chip): string {
  return term(key, value);
}

// The chips with one more, unless it is there already.
export function withChip(chips: readonly Chip[], chip: Chip): Chip[] {
  return chips.some(({ key, value }) => key === chip.key && value === chip.value) ? [...chips] : [...chips, chip];
}

// The query the monitor gets: the scope, the chips, then what the user typed. The API ANDs the
// terms; only several `type:` filters are alternatives.
export function buildQuery(scope: string[], chips: readonly Chip[], text: string): string {
  return [...scope, ...chips.map(chipTerm), text.trim()].filter(Boolean).join(" ");
}

// The channel of a result as the page names it; the ID of one the monitor does not list (yet).
export function hitPlace(channels: readonly Channel[], channelId: string): string {
  const channel = channels.find((candidate) => candidate.id === channelId);
  return channel ? describeChannel(channel) : channelId;
}

// Text from an agent as one line of a quick pick, which would turn "$(name)" into an icon.
export function plainLabel(text: string): string {
  return text.replace(/\s+/g, " ").trim().replaceAll("$(", "\\$(");
}

// A piece of a snippet, as the page draws it: plain text or a match.
export interface SnippetPart {
  text: string;
  match: boolean;
}

// Cuts a snippet at the matches. The API counts code points, not UTF-16 units, so the text is
// split into code points first. Offsets that are out of range or overlap are clamped or skipped.
export function snippetParts(text: string, matches: readonly (readonly [number, number])[] = []): SnippetPart[] {
  const characters = Array.from(text);
  const parts: SnippetPart[] = [];
  let at = 0;
  for (const [start, end] of matches) {
    const from = Math.max(start, at);
    const to = Math.min(end, characters.length);
    if (!(from < to)) continue;
    if (from > at) parts.push({ text: characters.slice(at, from).join(""), match: false });
    parts.push({ text: characters.slice(from, to).join(""), match: true });
    at = to;
  }
  if (at < characters.length) parts.push({ text: characters.slice(at).join(""), match: false });
  return parts;
}
