import * as vscode from "vscode";
import { createChannel, inviteAgent } from "./channelAdmin";
import { ChatPanels } from "./chat";
import { Session } from "./session";
import { searchMessages } from "./searchPicker";
import { AgentMessengerTree, Node, SECTIONS } from "./tree";
import { UpdateChecker } from "./updates";
import { showNotifications } from "./notifications";
import { openUnreadMail, showUnreadIndicators } from "./unread";

export function activate(context: vscode.ExtensionContext): void {
  const session = new Session(context);
  const chats = new ChatPanels(context, session);
  const updates = new UpdateChecker(context, session.log);
  // A view per section, so each folds and resizes like the Explorer's Outline and Timeline. Inside
  // Direct Messages and Agents the owner groups fold as tree rows.
  const views = SECTIONS.map((section) => {
    const tree = new AgentMessengerTree(session, section);
    const view = vscode.window.createTreeView(`agentMessenger.${section}`, { treeDataProvider: tree });
    view.onDidCollapseElement((event) => tree.setCollapsed(event.element, true));
    view.onDidExpandElement((event) => tree.setCollapsed(event.element, false));
    return view;
  });
  const channelsView = views[SECTIONS.indexOf("channels")];
  const favouritesShown = () => vscode.commands.executeCommand("setContext", "agentMessenger.hasFavourites", session.prefs.favourites.size > 0);
  void favouritesShown();

  context.subscriptions.push(
    session,
    chats,
    updates,
    ...views,
    session.onDidChange(() => void favouritesShown()),
    showUnreadIndicators(session, channelsView),
    showNotifications(session, chats),
    vscode.commands.registerCommand("agentMessenger.openUnreadMail", () => openUnreadMail(session)),
    vscode.commands.registerCommand("agentMessenger.login", () => session.login()),
    vscode.commands.registerCommand("agentMessenger.searchMessages", () => searchMessages(session, chats)),
    vscode.commands.registerCommand("agentMessenger.openChannel", (channelId: string) => chats.open(channelId)),
    vscode.commands.registerCommand("agentMessenger.addFavourite", (node: Node) => setFavourite(session, node, true)),
    vscode.commands.registerCommand("agentMessenger.removeFavourite", (node: Node) => setFavourite(session, node, false)),
    vscode.commands.registerCommand("agentMessenger.subscribe", (node: Node) => setSubscribed(session, node, true)),
    vscode.commands.registerCommand("agentMessenger.unsubscribe", (node: Node) => setSubscribed(session, node, false)),
    vscode.commands.registerCommand("agentMessenger.messageAgent", (node: Node) => node.kind === "agent" && chats.openDirect(node.agent.id)),
    vscode.commands.registerCommand("agentMessenger.copyAgentId", (node: Node) => node.kind === "agent" && vscode.env.clipboard.writeText(node.agent.id)),
    vscode.commands.registerCommand("agentMessenger.inviteAgent", (node: Node) => node.kind === "channel" && inviteAgent(session, chats, node.channel.id)),
    vscode.commands.registerCommand("agentMessenger.createChannel", () => createChannel(session, chats)),
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

function setSubscribed(session: Session, node: Node, subscribed: boolean): void {
  if (node.kind === "channel") session.prefs.setSubscribed(node.channel.id, subscribed);
}

export function deactivate(): void {}
