import * as vscode from "vscode";
import { categorize, describeChannel } from "./channels";
import { ago, describePresence, recentActivity } from "./presence";
import { Session } from "./session";
import { Agent, Channel, Presence } from "./types";

const RECENT_CHILDREN = 3; // recent activities listed under an agent
const RECENT_TOOLTIP = 10; // and in its tooltip

// A row of the tree. A section's id is the name the monitor's prefs use for it when it is folded.
export type Node =
  | { kind: "section"; id: string; label: string; children: Node[] }
  | { kind: "channel"; channel: Channel }
  | { kind: "agent"; agent: Agent }
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

export class AgentMessengerTree implements vscode.TreeDataProvider<Node> {
  private readonly emitter = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.emitter.event;

  constructor(private readonly session: Session) {
    session.onDidChange(() => this.emitter.fire());
  }

  private get model() {
    return this.session.model;
  }

  getChildren(node?: Node): Node[] {
    if (node?.kind === "agent") return this.agentChildren(node.agent);
    if (node) return node.kind === "section" ? node.children : [];
    const { channels, agents } = this.model;
    const categories = categorize(channels, this.session.prefs.favourites);
    const channelNodes = (list: Channel[]): Node[] => list.map((channel) => ({ kind: "channel", channel }));
    const favourites: Node[] = categories.favourites.length > 0 ? [{ kind: "section", id: "favourites", label: "Favourites", children: channelNodes(categories.favourites) }] : [];
    return [
      ...favourites,
      { kind: "section", id: "channels", label: "Channels", children: channelNodes(categories.channels) },
      { kind: "section", id: "dms", label: "Direct messages", children: channelNodes(categories.directMessages) },
      { kind: "section", id: "agents", label: "Agents", children: agents.map((agent) => ({ kind: "agent", agent })) },
    ];
  }

  getTreeItem(node: Node): vscode.TreeItem {
    if (node.kind === "channel") return this.channelItem(node.channel);
    if (node.kind === "agent") return this.agentItem(node.agent);
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
    const item = new vscode.TreeItem(describeChannel(channel));
    const unread = this.model.unreadCount(channel.id);
    item.id = channel.id;
    item.contextValue = this.session.prefs.favourites.has(channel.id) ? "favouriteChannel" : "channel";
    item.description = unread > 0 ? String(unread) : undefined;
    item.tooltip = channel.description || describeChannel(channel);
    item.iconPath = new vscode.ThemeIcon(channel.kind === "dm" ? "mail" : "comment-discussion");
    item.command = { command: "agentMessenger.openChannel", title: "Open", arguments: [channel.id] };
    return item;
  }

  // An agent's workers and its latest activities.
  private agentChildren(agent: Agent): Node[] {
    const view = this.model.presence.get(agent.id);
    if (agent.removed || !view) return [];
    const now = Date.now();
    const workers = (view.workers ?? []).map((worker, index): Node => ({
      kind: "agentDetail", id: `worker:${agent.id}:${worker.id ?? index}`, label: worker.description, description: ago(worker.since, now), icon: "person",
    }));
    const recent = recentActivity(view, RECENT_CHILDREN).map((entry): Node => ({
      kind: "agentDetail", id: `recent:${agent.id}:${entry.at}`, label: entry.text, description: ago(entry.at, now) + " ago", icon: "history",
    }));
    return [...workers, ...recent];
  }

  private agentItem(agent: Agent): vscode.TreeItem {
    const item = new vscode.TreeItem(agent.id);
    item.id = "agent:" + agent.id;
    // The menu offers "Message Agent" only for an agent the monitor can write to.
    const messageable = !agent.removed && agent.id !== this.model.self && this.model.status?.can_send === true;
    item.contextValue = messageable ? "agent" : "agentNoMessage";
    if (agent.removed) {
      item.description = "removed";
      item.tooltip = new vscode.MarkdownString().appendText(`${agent.id} (removed)\n${agent.fingerprint}`);
      item.iconPath = new vscode.ThemeIcon("circle-slash");
      return item;
    }
    const view = this.model.presence.get(agent.id);
    item.description = describePresence(view, Date.now());
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
