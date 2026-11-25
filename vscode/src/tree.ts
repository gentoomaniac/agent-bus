import * as vscode from "vscode";
import { categorize, describeChannel } from "./channels";
import { byOwner, dmAgents, dmByOwner, dmOwners, groupBy, ownerOf, shortName } from "./names";
import { ago, describePresence, recentActivity } from "./presence";
import { Session } from "./session";
import { Agent, Channel, Presence } from "./types";

const RECENT_CHILDREN = 3; // recent activities listed under an agent
const RECENT_TOOLTIP = 10; // and in its tooltip

// A row of the tree. A section's id is the name the monitor's prefs use for it when it is folded.
export type Node =
  | { kind: "section"; id: string; label: string; children: Node[] }
  | { kind: "channel"; channel: Channel }
  | { kind: "agent"; agent: Agent; within?: string } // within: the channel it is listed under as a member
  | { kind: "agentDetail"; id: string; label: string; description: string; icon: string }; // a worker or a recent activity

// The icon and color of an agent, by presence state. Every error:<kind> looks the same.
function stateIcon(state: string): vscode.ThemeIcon {
  const color = (id: string) => new vscode.ThemeColor(id);
  if (state === "working") return new vscode.ThemeIcon("sync", color("charts.green"));
  if (state === "idle") return new vscode.ThemeIcon("circle-filled", color("charts.blue"));
  if (state === "waiting_for_owner") return new vscode.ThemeIcon("bell", color("charts.yellow"));
  if (state === "rate_limited") return new vscode.ThemeIcon("watch", color("charts.orange"));
  if (state === "unresponsive") return new vscode.ThemeIcon("warning", color("charts.red"));
  if (state.startsWith("error:")) return new vscode.ThemeIcon("error", color("charts.red"));
  return new vscode.ThemeIcon("circle-outline", color("disabledForeground"));
}

// Agents and DMs sit in a folding group per owner, by their short names; the tooltips keep the full
// IDs. A group's id is what the monitor's prefs keep when it is folded.
function agentGroups(agents: Agent[]): Node[] {
  const sorted = [...agents].sort((a, b) => byOwner(a.id, b.id));
  return groupBy(sorted, (agent) => ownerOf(agent.id)).map(({ key, items }) => ({
    kind: "section", id: "agents@" + key, label: key || "(no owner)", children: items.map((agent): Node => ({ kind: "agent", agent })),
  }));
}

function dmGroups(channels: Channel[]): Node[] {
  return groupBy([...channels].sort(dmByOwner), dmOwners).map(({ key, items }) => ({
    kind: "section", id: "dms@" + key, label: key || "(no owner)", children: items.map((channel): Node => ({ kind: "channel", channel })),
  }));
}

export class AgentMessengerTree implements vscode.TreeDataProvider<Node> {
  private readonly emitter = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.emitter.event;

  constructor(private readonly session: Session) {
    session.onDidChange(() => this.emitter.fire());
  }

  private get model() {
    return this.session.model;
  }

  getChildren(node?: Node): Node[] | Promise<Node[]> {
    if (node?.kind === "agent") return this.agentChildren(node.agent, node.within);
    if (node?.kind === "channel") return this.memberNodes(node.channel);
    if (node) return node.kind === "section" ? node.children : [];
    const { channels, agents } = this.model;
    const categories = categorize(channels, this.session.prefs.favourites);
    const channelNodes = (list: Channel[]): Node[] => list.map((channel) => ({ kind: "channel", channel }));
    const favourites: Node[] = categories.favourites.length > 0 ? [{ kind: "section", id: "favourites", label: "Favourites", children: channelNodes(categories.favourites) }] : [];
    return [
      ...favourites,
      { kind: "section", id: "channels", label: "Channels", children: channelNodes(categories.channels) },
      { kind: "section", id: "dms", label: "Direct messages", children: dmGroups(categories.directMessages) },
      { kind: "section", id: "agents", label: "Agents", children: agentGroups(agents.filter((agent) => agent.id !== this.model.self)) },
    ];
  }

  getTreeItem(node: Node): vscode.TreeItem {
    if (node.kind === "channel") return this.channelItem(node.channel);
    if (node.kind === "agent") return this.agentItem(node.agent, node.within);
    if (node.kind === "agentDetail") {
      const item = new vscode.TreeItem(node.label);
      item.id = node.id;
      item.description = node.description;
      item.tooltip = new vscode.MarkdownString().appendText(node.label);
      item.iconPath = new vscode.ThemeIcon(node.icon);
      return item;
    }
    const collapsed = this.session.prefs.collapsed.has(node.id);
    const item = new vscode.TreeItem(node.label, collapsed ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.Expanded);
    item.id = "category:" + node.id;
    return item;
  }

  // Remembers a category the user folded or unfolded, so it stays so after a refresh or restart.
  setCollapsed(node: Node, collapsed: boolean): void {
    if (node.kind === "section") this.session.prefs.setCollapsed(node.id, collapsed);
  }

  private channelItem(channel: Channel): vscode.TreeItem {
    const dm = channel.kind === "dm";
    const item = new vscode.TreeItem(dm ? dmAgents(channel).map(shortName).join(" ↔ ") : describeChannel(channel));
    const unread = this.model.unreadCount(channel.id);
    item.id = channel.id;
    const favourite = this.session.prefs.favourites.has(channel.id);
    const subscribed = this.session.prefs.subscribed.has(channel.id);
    // The context value says which of the star and bell actions the menu offers.
    item.contextValue = subscribed ? (favourite ? "subscribedFavouriteChannel" : "subscribedChannel") : favourite ? "favouriteChannel" : "channel";
    item.description = [unread > 0 ? String(unread) : "", subscribed ? "🔔" : ""].filter(Boolean).join(" ") || undefined;
    item.tooltip = dm ? dmAgents(channel).join(" ↔ ") : channel.description || describeChannel(channel);
    item.iconPath = new vscode.ThemeIcon(channel.kind === "dm" ? "mail" : "comment-discussion");
    item.command = { command: "agentMessenger.openChannel", title: "Open", arguments: [channel.id] };
    // A channel unfolds into its members; a DM's two agents are in its name.
    if (!dm) item.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
    return item;
  }

  // The agents that read a channel, by owner, as rows like those under Agents.
  private async memberNodes(channel: Channel): Promise<Node[]> {
    let members: string[];
    try {
      members = await this.model.api.members(channel.id);
    } catch (error) {
      return [{ kind: "agentDetail", id: `members:${channel.id}`, label: "couldn't load the members", description: (error as Error).message, icon: "warning" }];
    }
    // Fetched again at every refresh while unfolded: simple, and cheap at this size.
    const known = new Map(this.model.agents.map((agent) => [agent.id, agent]));
    const unregistered = (id: string): Agent => ({ id, fingerprint: "", removed: false });
    return [...members].sort(byOwner).map((id): Node => ({ kind: "agent", agent: known.get(id) ?? unregistered(id), within: channel.id }));
  }

  // An agent's workers and its latest activities.
  // The ids carry the row's place, as the same agent can be listed under Agents and channels.
  private agentChildren(agent: Agent, within?: string): Node[] {
    const view = this.model.presence.get(agent.id);
    if (agent.removed || !view) return [];
    const now = Date.now();
    const place = within ? `member:${within}:` : "";
    const workers = (view.workers ?? []).map((worker, index): Node => ({
      kind: "agentDetail", id: `${place}worker:${agent.id}:${worker.id ?? index}`, label: worker.description, description: ago(worker.since, now), icon: "person",
    }));
    const recent = recentActivity(view, RECENT_CHILDREN).map((entry, index): Node => ({
      kind: "agentDetail", id: `${place}recent:${agent.id}:${index}:${entry.at}`, label: entry.text, description: ago(entry.at, now) + " ago", icon: "history",
    }));
    return [...workers, ...recent];
  }

  // Under Agents the owner is the group; a channel's members show it before the state.
  private agentItem(agent: Agent, within?: string): vscode.TreeItem {
    const item = new vscode.TreeItem(shortName(agent.id));
    item.id = (within ? `member:${within}:` : "agent:") + agent.id;
    // The menu offers "Message Agent" only for an agent the monitor can write to.
    const messageable = !agent.removed && this.model.status?.can_send === true;
    item.contextValue = messageable ? "agent" : "agentNoMessage";
    // A click opens the conversation, as on the web page. Where the agent cannot be messaged, a
    // click only expands the row. The monitor's own agent is not listed.
    if (messageable) item.command = { command: "agentMessenger.messageAgent", title: "Message Agent", arguments: [{ kind: "agent", agent }] };
    if (agent.removed) {
      item.description = "removed";
      item.tooltip = new vscode.MarkdownString().appendText(`${agent.id} (removed)\n${agent.fingerprint}`);
      item.iconPath = new vscode.ThemeIcon("circle-slash");
      return item;
    }
    const view = this.model.presence.get(agent.id);
    item.description = [within ? ownerOf(agent.id) : "", describePresence(view, Date.now())].filter(Boolean).join(" · ");
    item.tooltip = this.agentTooltip(agent, view);
    item.iconPath = stateIcon(view?.state ?? "offline");
    const expandable = (view?.workers?.length ?? 0) + (view?.recent?.length ?? 0) > 0;
    item.collapsibleState = expandable ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None;
    return item;
  }

  // What the web page shows for an agent: state and since, activity, workers, pending mail and
  // recent activity. Everything from an agent is text: appendText escapes it.
  private agentTooltip(agent: Agent, view: Presence | undefined): vscode.MarkdownString {
    const now = Date.now();
    const tip = new vscode.MarkdownString();
    const since = ago(view?.since, now);
    tip.appendMarkdown("**").appendText(agent.id).appendMarkdown("**\n\n").appendText((view?.state ?? "offline") + (since ? ` for ${since}` : ""));
    if (view?.activity) tip.appendMarkdown("\n\n").appendText(`${view.activity} (${ago(view.activity_since, now)})`);
    if (view?.pending) tip.appendMarkdown("\n\n").appendText(`${view.pending} unread targeted`);
    if (view?.workers?.length) {
      tip.appendMarkdown("\n\n**Workers**");
      for (const worker of view.workers) tip.appendMarkdown("\n\n").appendText(`${worker.description} (${ago(worker.since, now)})`);
    }
    const recent = recentActivity(view, RECENT_TOOLTIP);
    if (recent.length) {
      tip.appendMarkdown("\n\n**Recent activity**");
      for (const entry of recent) tip.appendMarkdown("\n\n").appendText(`${ago(entry.at, now)} ago ${entry.text}`);
    }
    return tip;
  }
}
