import { Channel } from "./types";

// An agent ID is "<name>@<owner>" (docs/design/identity-and-trust.md).
function split(id: string): { name: string; owner: string } {
  const at = id.lastIndexOf("@");
  return at < 0 ? { name: id, owner: "" } : { name: id.slice(0, at), owner: id.slice(at + 1) };
}

// The part before the "@", shown in the side panel under its owner's group.
export function shortName(id: string): string {
  return split(id).name;
}

export function ownerOf(id: string): string {
  return split(id).owner;
}

// Orders agent IDs by owner, then by name.
export function byOwner(a: string, b: string): number {
  const x = split(a);
  const y = split(b);
  return x.owner.localeCompare(y.owner) || x.name.localeCompare(y.name);
}

// A DM's two agents in owner order. The monitor sees every DM, so neither has to be its own agent.
export function dmAgents(channel: Channel): string[] {
  return [...(channel.agents ?? channel.name.split(" + "))].sort(byOwner);
}

// The group a DM is listed in: its owner, or both owners when the agents belong to two.
export function dmOwners(channel: Channel): string {
  return [...new Set(dmAgents(channel).map(ownerOf))].join(" ↔ ");
}

// Orders DMs by their owner group, then by their agents.
export function dmByOwner(a: Channel, b: Channel): number {
  const group = dmOwners(a).localeCompare(dmOwners(b));
  if (group !== 0) return group;
  const x = dmAgents(a);
  const y = dmAgents(b);
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const order = byOwner(x[i], y[i]);
    if (order !== 0) return order;
  }
  return x.length - y.length;
}

// Splits an ordered list into runs with the same key, keeping the order.
export function groupBy<T>(items: T[], key: (item: T) => string): { key: string; items: T[] }[] {
  const groups: { key: string; items: T[] }[] = [];
  for (const item of items) {
    const k = key(item);
    if (groups.length > 0 && groups[groups.length - 1].key === k) groups[groups.length - 1].items.push(item);
    else groups.push({ key: k, items: [item] });
  }
  return groups;
}
