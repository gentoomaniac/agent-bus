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

// "a@x, @all  b@y" -> ["a@x", "@all", "b@y"]
export function parseTargets(input: string): string[] {
  return input.split(/[\s,]+/).filter((target) => target !== "");
}
