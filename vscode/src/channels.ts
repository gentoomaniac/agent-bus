import { Channel, SendRequest } from "./types";

// How the web page names a channel.
export function describeChannel(channel: Channel): string {
  if (channel.kind === "dm") return channel.name.replace(" + ", " ↔ ");
  return "# " + (channel.kind === "general" ? "general" : channel.name);
}

// Where a message composed in the channel goes. A DM can be written only by one of its two agents.
export function destinationOf(channel: Channel, self: string): SendRequest["to"] | undefined {
  if (channel.kind === "general") return { kind: "group", name: "general" };
  if (channel.kind === "group") return { kind: "group", name: channel.name };
  const agents = channel.agents ?? [];
  if (!agents.includes(self)) return undefined;
  return { kind: "agent", name: agents.find((id) => id !== self) ?? self };
}

export interface ChannelCategories {
  favourites: Channel[];
  channels: Channel[];
  directMessages: Channel[];
}

// Sorts channels into the tree's categories. A favourite moves out of its own category into
// Favourites; a favourite id that matches no channel is ignored.
export function categorize(channels: Channel[], favourites: ReadonlySet<string>): ChannelCategories {
  const result: ChannelCategories = { favourites: [], channels: [], directMessages: [] };
  for (const channel of channels) {
    if (favourites.has(channel.id)) result.favourites.push(channel);
    else if (channel.kind === "dm") result.directMessages.push(channel);
    else result.channels.push(channel);
  }
  return result;
}
