import { Channel } from "./types";

// An agent ID is "<name>@<owner>" (docs/design/identity-and-trust.md).
function split(id: string): { name: string; owner: string } {
  const at = id.lastIndexOf("@");
  return at < 0 ? { name: id, owner: "" } : { name: id.slice(0, at), owner: id.slice(at + 1) };
}

// Orders agent IDs by owner, then by name.
export function byOwner(a: string, b: string): number {
  const x = split(a);
  const y = split(b);
  return x.owner.localeCompare(y.owner) || x.name.localeCompare(y.name);
}

// Short names for the side panel: the part before the "@", unless another agent in the list has
// the same one, in which case both keep the full ID.
export function shortNames(ids: Iterable<string>): (id: string) => string {
  const owners = new Map<string, Set<string>>();
  for (const id of ids) {
    const { name, owner } = split(id);
    owners.set(name, (owners.get(name) ?? new Set()).add(owner));
  }
  return (id) => {
    const { name } = split(id);
    return (owners.get(name)?.size ?? 0) > 1 ? id : name;
  };
}

// A DM's two agents in owner order. The monitor sees every DM, so neither has to be its own agent.
export function dmAgents(channel: Channel): string[] {
  return [...(channel.agents ?? channel.name.split(" + "))].sort(byOwner);
}

// Orders DMs by their agents, owner first.
export function dmByOwner(a: Channel, b: Channel): number {
  const x = dmAgents(a);
  const y = dmAgents(b);
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const order = byOwner(x[i], y[i]);
    if (order !== 0) return order;
  }
  return x.length - y.length;
}
