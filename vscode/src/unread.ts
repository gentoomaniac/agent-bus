import * as vscode from "vscode";
import { Session } from "./session";
import { APPROVE_HINT, Connection, describeConnection, describePending, statusBarText } from "./statusText";

// The status bar shows the bus connection when it is down, the unread mail aimed at the
// monitor's agent (targets include it or @all) and the pending enrollments; it hides when there
// is none of them. The tree view's badge shows every unread message, its description the
// connection and its message the pending enrollments. Clicking the status bar
// item opens the first channel with such mail.
export function showUnreadIndicators(session: Session, tree: vscode.TreeView<unknown>): vscode.Disposable {
  const item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left);
  item.command = "agentMessenger.openUnreadMail";

  const update = () => {
    const { model } = session;
    // Before a login there is no connection to speak of.
    const connection: Connection = session.loginRequired ? { ok: true, text: "" } : describeConnection(model.live, model.status);
    const mail = model.targetedUnreadCount();
    const pending = session.loginRequired ? "" : describePending(model.status);
    const text = statusBarText(connection, mail, pending ? model.status?.pending_enrollments ?? 0 : 0);
    item.text = text;
    item.tooltip = [
      session.loginRequired ? "" : `Agent Bus: ${connection.text}`,
      mail > 0 ? `${mail} unread message${mail === 1 ? "" : "s"} for ${model.self}` : "",
      pending ? `${pending}. ${APPROVE_HINT}` : "",
    ].filter(Boolean).join("\n");
    if (text) item.show();
    else item.hide();

    tree.description = connection.text || undefined;
    tree.message = pending ? `${pending}. ${APPROVE_HINT}` : undefined;
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
