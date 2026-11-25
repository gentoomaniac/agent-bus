import * as vscode from "vscode";
import { ChatPanels } from "./chat";
import { Session } from "./session";
import { AgentMessengerTree } from "./tree";
import { openUnreadMail, showUnreadIndicators } from "./unread";

export function activate(context: vscode.ExtensionContext): void {
  const session = new Session(context);
  const tree = new AgentMessengerTree(session);
  const chats = new ChatPanels(context, session);
  const treeView = vscode.window.createTreeView("agentMessenger.tree", { treeDataProvider: tree });

  context.subscriptions.push(
    session,
    chats,
    treeView,
    showUnreadIndicators(session, treeView),
    vscode.commands.registerCommand("agentMessenger.openUnreadMail", () => openUnreadMail(session)),
    vscode.commands.registerCommand("agentMessenger.login", () => session.login()),
    vscode.commands.registerCommand("agentMessenger.openChannel", (channelId: string) => chats.open(channelId)),
    vscode.commands.registerCommand("agentMessenger.logout", () => session.logout()),
    vscode.workspace.onDidChangeConfiguration((change) => {
      if (change.affectsConfiguration("agentMessenger.url")) void session.start();
    }),
  );
  void session.start();
}

export function deactivate(): void {}
