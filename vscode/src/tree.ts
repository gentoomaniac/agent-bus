import * as vscode from "vscode";
import { describeChannel } from "./channels";
import { Session } from "./session";
import { Agent, Channel } from "./types";

type Node =
  | { kind: "section"; label: string; children: () => Node[] }
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
    if (node) return node.kind === "section" ? node.children() : [];
    const { channels, agents } = this.model;
    return [
      { kind: "section", label: "Channels", children: () => channels.filter((c) => c.kind !== "dm").map((channel) => ({ kind: "channel", channel })) },
      { kind: "section", label: "Direct messages", children: () => channels.filter((c) => c.kind === "dm").map((channel) => ({ kind: "channel", channel })) },
      { kind: "section", label: "Agents", children: () => agents.map((agent) => ({ kind: "agent", agent })) },
    ];
  }

  getTreeItem(node: Node): vscode.TreeItem {
    if (node.kind === "section") return new vscode.TreeItem(node.label, vscode.TreeItemCollapsibleState.Expanded);
    if (node.kind === "channel") return this.channelItem(node.channel);
    return this.agentItem(node.agent);
  }

  private channelItem(channel: Channel): vscode.TreeItem {
    const item = new vscode.TreeItem(describeChannel(channel));
    const unread = this.model.unreadCount(channel.id);
    item.id = channel.id;
    item.description = unread > 0 ? String(unread) : undefined;
    item.tooltip = channel.description || describeChannel(channel);
    item.iconPath = new vscode.ThemeIcon(channel.kind === "dm" ? "mail" : "comment-discussion");
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
