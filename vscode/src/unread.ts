import * as vscode from "vscode";
import { Session } from "./session";

// The status bar shows the unread mail aimed at the monitor's agent (targets include it or
// @all); the tree view's badge shows every unread message. Clicking the status bar item opens
// the first channel with such mail.
export function showUnreadIndicators(session: Session, tree: vscode.TreeView<unknown>): vscode.Disposable {
  const item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left);
  item.command = "agentMessenger.openUnreadMail";

  const update = () => {
    const { model } = session;
    const mail = model.targetedUnreadCount();
    item.text = `$(mail) ${mail}`;
    item.tooltip = `${mail} unread message${mail === 1 ? "" : "s"} for ${model.self}`;
    if (mail > 0) item.show();
    else item.hide();

    const unread = model.channels.reduce((sum, channel) => sum + model.unreadCount(channel.id), 0);
    tree.badge = unread > 0 ? { value: unread, tooltip: `${unread} unread` } : undefined;
  };
  update();

  return vscode.Disposable.from(item, session.onDidChange(update));
}

export function openUnreadMail(session: Session): void {
  const [first] = session.model.targetedUnreadChannels();
  if (first) void vscode.commands.executeCommand("agentMessenger.openChannel", first[0].id);
}
