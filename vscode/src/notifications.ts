import * as vscode from "vscode";
import { ChatPanels } from "./chat";
import { describeChannel } from "./channels";
import { Coalescer, describeNotification, muteLabel, notifyReason, parseNotifyMode } from "./notify";
import { Session } from "./session";
import { Message } from "./types";

// Shows a notification for a live message that is addressed to the monitor's agent or comes from
// a subscribed channel (agentMessenger.notifications), unless the user is looking at the channel.
export function showNotifications(session: Session, chats: ChatPanels): vscode.Disposable {
  const coalescer = new Coalescer<Message>((batch) => void notify(session, chats, batch));
  const subscription = session.onDidReceiveMessage((message) => {
    const mode = parseNotifyMode(vscode.workspace.getConfiguration("agentMessenger").get("notifications"));
    if (!notifyReason(message, session.model.self, session.prefs.subscribed, mode)) return;
    if (chats.isLooking(message.channel)) return;
    coalescer.offer(message);
  });
  return vscode.Disposable.from(subscription, { dispose: () => coalescer.dispose() });
}

async function notify(session: Session, chats: ChatPanels, batch: Message[]): Promise<void> {
  const [first] = batch;
  const channel = session.model.channels.find((candidate) => candidate.id === first.channel);
  const label = channel ? describeChannel(channel) : first.channel;
  if (batch.length > 1) {
    const choice = await vscode.window.showInformationMessage(`Agent Bus: ${batch.length} new messages`, "Open");
    if (choice) await chats.jumpTo(first.channel, first.sequence);
    return;
  }
  const reason = session.prefs.subscribed.has(first.channel) ? "subscribed" : "targeted";
  const mute = muteLabel(reason, label);
  const choice = await vscode.window.showInformationMessage(describeNotification(first, label), "Open", mute);
  if (choice === "Open") await chats.jumpTo(first.channel, first.sequence);
  else if (choice === mute) await muteChannel(session, first, reason);
}

// A subscribed channel is unsubscribed; for mail that is only addressed to the agent, the only
// thing to mute is the setting.
async function muteChannel(session: Session, message: Message, reason: "subscribed" | "targeted"): Promise<void> {
  if (reason === "subscribed") {
    session.prefs.setSubscribed(message.channel, false);
    return;
  }
  const turnOff = "Turn Off";
  const choice = await vscode.window.showInformationMessage("Stop notifications for messages addressed to you? Subscribed channels still notify.", turnOff);
  if (choice === turnOff) {
    await vscode.workspace.getConfiguration("agentMessenger").update("notifications", "subscribedOnly", vscode.ConfigurationTarget.Global);
  }
}
