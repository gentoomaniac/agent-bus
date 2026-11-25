import * as vscode from "vscode";
import { categorize, describeChannel } from "./channels";
import { Session } from "./session";
import { Agent, Channel } from "./types";

// A row of the tree. A section's id is the name the monitor's prefs use for it when it is folded.
export type Node =
  | { kind: "section"; id: string; label: string; children: Node[] }
  | { kind: "channel"; channel: Channel }
  | { kind: "agent"; agent: Agent };

// The color of an agent's dot, by presence state.
function stateColor(state: string): vscode.ThemeColor {
  if (state === "working") return new vscode.ThemeColor("charts.green");
  if (state === "idle") return new vscode.ThemeColor("charts.blue");
  if (state === "waiting_for_owner") return new vscode.ThemeColor("charts.yellow");
  if (state === "rate_limited") return new vscode.ThemeColor("charts.orange");
  if (state === "unresponsive" || state.startsWith("error:")) return new vscode.ThemeColor("charts.red");
  return new vscode.ThemeColor("disabledForeground");
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

  private agentItem(agent: Agent): vscode.TreeItem {
    const item = new vscode.TreeItem(agent.id);
    item.tooltip = agent.fingerprint;
    if (agent.removed) {
      item.description = "removed";
      item.iconPath = new vscode.ThemeIcon("circle-slash");
      return item;
    }
    const view = this.model.presence.get(agent.id);
    const state = view?.state ?? "offline";
    item.description = view?.activity ? `${state}: ${view.activity}` : state;
    item.iconPath = new vscode.ThemeIcon("circle-filled", stateColor(state));
    return item;
  }
}
