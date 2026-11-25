import { Agent, Channel, SendRequest } from "./types";

// How the web page names a channel.
export function describeChannel(channel: Channel): string {
  if (channel.kind === "dm") return channel.name.replace(" + ", " ↔ ");
  return "# " + (channel.kind === "general" ? "general" : channel.name);
}

// "dm:<a>:<b>", the two agents sorted (docs/api.md).
export function dmChannelId(a: string, b: string): string {
  return "dm:" + [a, b].sort().join(":");
}

// Where a message composed in the channel goes. A DM can be written only by one of its two agents.
export function destinationOf(channel: Channel, self: string): SendRequest["to"] | undefined {
  if (channel.kind === "general") return { kind: "group", name: "general" };
  if (channel.kind === "group") return { kind: "group", name: channel.name };
  const agents = channel.agents ?? [];
  if (!agents.includes(self)) return undefined;
  return { kind: "agent", name: agents.find((id) => id !== self) ?? self };
}

// The name of a group channel's ID ("group:<name>"); general and direct messages have no members
// to manage.
export function groupName(channelId: string): string | undefined {
  return channelId.startsWith("group:") ? channelId.slice("group:".length) : undefined;
}

// The agents an invite may name: not removed, not the monitor's own, not reading the channel yet.
export function invitableAgents(agents: Agent[], members: string[], self: string): string[] {
  return agents.filter((agent) => !agent.removed && agent.id !== self && !members.includes(agent.id)).map((agent) => agent.id);
}

// What busd takes for a channel name (bus.ValidateGroupName): an error to show, or undefined.
export function channelNameProblem(name: string): string | undefined {
  if (!/^[a-z0-9-]{1,64}$/.test(name)) return "lowercase letters, digits and -, at most 64";
  if (name === "general") return "general is the default channel";
  return undefined;
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
