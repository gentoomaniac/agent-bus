import * as vscode from "vscode";
import { ChatPanels } from "./chat";
import { Session } from "./session";
import { searchMessages } from "./searchPicker";
import { AgentMessengerTree, Node } from "./tree";
import { UpdateChecker } from "./updates";
import { showNotifications } from "./notifications";
import { openUnreadMail, showUnreadIndicators } from "./unread";

export function activate(context: vscode.ExtensionContext): void {
  const session = new Session(context);
  const tree = new AgentMessengerTree(session);
  const chats = new ChatPanels(context, session);
  const updates = new UpdateChecker(context, session.log);
  const treeView = vscode.window.createTreeView("agentMessenger.tree", { treeDataProvider: tree });

  context.subscriptions.push(
    session,
    chats,
    updates,
    treeView,
    treeView.onDidCollapseElement((event) => tree.setCollapsed(event.element, true)),
    treeView.onDidExpandElement((event) => tree.setCollapsed(event.element, false)),
    showUnreadIndicators(session, treeView),
    showNotifications(session, chats),
    vscode.commands.registerCommand("agentMessenger.openUnreadMail", () => openUnreadMail(session)),
    vscode.commands.registerCommand("agentMessenger.login", () => session.login()),
    vscode.commands.registerCommand("agentMessenger.searchMessages", () => searchMessages(session, chats)),
    vscode.commands.registerCommand("agentMessenger.openChannel", (channelId: string) => chats.open(channelId)),
    vscode.commands.registerCommand("agentMessenger.addFavourite", (node: Node) => setFavourite(session, node, true)),
    vscode.commands.registerCommand("agentMessenger.removeFavourite", (node: Node) => setFavourite(session, node, false)),
    vscode.commands.registerCommand("agentMessenger.messageAgent", (node: Node) => node.kind === "agent" && chats.openDirect(node.agent.id)),
    vscode.commands.registerCommand("agentMessenger.copyAgentId", (node: Node) => node.kind === "agent" && vscode.env.clipboard.writeText(node.agent.id)),
    vscode.commands.registerCommand("agentMessenger.checkForUpdates", () => updates.check(true)),
    vscode.commands.registerCommand("agentMessenger.logout", () => session.logout()),
    vscode.workspace.onDidChangeConfiguration((change) => {
      if (change.affectsConfiguration("agentMessenger.url")) void session.start();
    }),
  );
  void session.start();
  updates.start();
}

function setFavourite(session: Session, node: Node, favourite: boolean): void {
  if (node.kind === "channel") session.prefs.setFavourite(node.channel.id, favourite);
}

export function deactivate(): void {}
