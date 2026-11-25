import * as vscode from "vscode";
import { ChatPanels } from "./chat";
import { Session } from "./session";
import { AgentMessengerTree } from "./tree";

export function activate(context: vscode.ExtensionContext): void {
  const session = new Session(context);
  const tree = new AgentMessengerTree(session);
  const chats = new ChatPanels(context, session);

  context.subscriptions.push(
    session,
    chats,
    vscode.window.createTreeView("agentMessenger.tree", { treeDataProvider: tree }),
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
