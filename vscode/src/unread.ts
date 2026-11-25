import * as vscode from "vscode";
import { Session } from "./session";
import { Connection, describeConnection, statusBarText } from "./statusText";

// The status bar shows the bus connection when it is down and the unread mail aimed at the
// monitor's agent (targets include it or @all); it hides when there is neither. The tree view's
// badge shows every unread message and its description the connection. Clicking the status bar
// item opens the first channel with such mail.
export function showUnreadIndicators(session: Session, tree: vscode.TreeView<unknown>): vscode.Disposable {
  const item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left);
  item.command = "agentMessenger.openUnreadMail";

  const update = () => {
    const { model } = session;
    // Before a login there is no connection to speak of.
    const connection: Connection = session.loginRequired ? { ok: true, text: "" } : describeConnection(model.live, model.status);
    const mail = model.targetedUnreadCount();
    const text = statusBarText(connection, mail);
    item.text = text;
    item.tooltip = [
      session.loginRequired ? "" : `Agent bus: ${connection.text}`,
      mail > 0 ? `${mail} unread message${mail === 1 ? "" : "s"} for ${model.self}` : "",
    ].filter(Boolean).join("\n");
    if (text) item.show();
    else item.hide();

    tree.description = connection.text || undefined;
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
