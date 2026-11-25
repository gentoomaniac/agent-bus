import * as vscode from "vscode";
import { Session } from "./session";
import { AgentMessengerTree } from "./tree";

export function activate(context: vscode.ExtensionContext): void {
  const session = new Session(context);
  const tree = new AgentMessengerTree(session);

  context.subscriptions.push(
    session,
    vscode.window.createTreeView("agentMessenger.tree", { treeDataProvider: tree }),
    vscode.commands.registerCommand("agentMessenger.login", () => session.login()),
    vscode.commands.registerCommand("agentMessenger.logout", () => session.logout()),
    vscode.workspace.onDidChangeConfiguration((change) => {
      if (change.affectsConfiguration("agentMessenger.url")) void session.start();
    }),
  );
  void session.start();
}

export function deactivate(): void {}
