import { randomBytes } from "node:crypto";
import * as vscode from "vscode";
import { describeChannel, destinationOf, parseTargets } from "./channels";
import { ChatState, FromWebview, ToWebview } from "./protocol";
import { Session } from "./session";

const HISTORY = 100; // messages loaded when a channel is opened

interface Chat {
  panel: vscode.WebviewPanel;
  dividerAfter: number; // last-read sequence when the panel was opened or shown again
}

// One chat panel per channel. The page only draws; the monitor is read and written from here.
export class ChatPanels implements vscode.Disposable {
  private readonly chats = new Map<string, Chat>();
  private readonly subscriptions: vscode.Disposable[];

  constructor(private readonly context: vscode.ExtensionContext, private readonly session: Session) {
    this.subscriptions = [
      session.onDidReceiveMessage((message) => {
        const chat = this.chats.get(message.channel);
        if (!chat) return;
        void chat.panel.webview.postMessage({ type: "message", message } satisfies ToWebview);
        if (chat.panel.visible) session.model.markRead(message.channel);
      }),
      session.onDidRefresh(() => this.chats.forEach((chat, channelId) => void this.show(channelId, chat))),
    ];
  }

  open(channelId: string): void {
    const existing = this.chats.get(channelId);
    if (existing) {
      existing.panel.reveal();
      return;
    }
    const channel = this.session.model.channels.find((c) => c.id === channelId);
    if (!channel) return;
    const panel = vscode.window.createWebviewPanel("agentMessenger.chat", describeChannel(channel), vscode.ViewColumn.Active, {
      enableScripts: true,
      retainContextWhenHidden: true,
      localResourceRoots: [vscode.Uri.joinPath(this.context.extensionUri, "dist")],
    });
    const chat: Chat = { panel, dividerAfter: 0 };
    this.chats.set(channelId, chat);
    panel.webview.html = this.html(panel.webview);
    panel.onDidDispose(() => this.chats.delete(channelId));
    panel.onDidChangeViewState(() => panel.visible && void this.show(channelId, chat));
    panel.webview.onDidReceiveMessage((message: FromWebview) => void this.handle(channelId, chat, message));
  }

  dispose(): void {
    this.subscriptions.forEach((subscription) => subscription.dispose());
    this.chats.forEach((chat) => chat.panel.dispose());
  }

  private async handle(channelId: string, chat: Chat, message: FromWebview): Promise<void> {
    const model = this.session.model;
    if (message.type === "ready") {
      await this.show(channelId, chat);
    } else if (message.type === "older") {
      await this.guarded(() => model.loadOlder(channelId));
      this.post(channelId, chat);
    } else {
      const destination = destinationOf(model.channels.find((c) => c.id === channelId)!, model.self);
      const { type, targets, title, text } = message.draft;
      let error: string | undefined;
      try {
        if (!destination) throw new Error("this conversation is read-only");
        await model.api.send({ to: destination, type, targets: parseTargets(targets), title: title.trim() || undefined, text });
      } catch (caught) {
        error = (caught as Error).message;
      }
      void chat.panel.webview.postMessage({ type: "sendResult", error } satisfies ToWebview);
    }
  }

  private async guarded(action: () => Promise<void>): Promise<void> {
    try {
      await action();
    } catch (error) {
      void vscode.window.showErrorMessage("Agent Messenger: " + (error as Error).message);
    }
  }

  // Loads the history, remembers where the "New" divider goes, shows the channel and marks it read.
  private async show(channelId: string, chat: Chat): Promise<void> {
    const model = this.session.model;
    await this.guarded(() => model.loadHistory(channelId, HISTORY));
    chat.dividerAfter = model.lastRead(channelId);
    this.post(channelId, chat);
    if (chat.panel.visible) model.markRead(channelId);
  }

  private post(channelId: string, chat: Chat): void {
    const model = this.session.model;
    const channel = model.channels.find((c) => c.id === channelId);
    if (!channel || !model.status) return;
    const canWrite = destinationOf(channel, model.self) !== undefined;
    const state: ChatState = {
      title: describeChannel(channel),
      description: channel.description,
      self: model.self,
      canSend: model.status.can_send && canWrite,
      note: !model.status.can_send ? model.status.note || "Read-only: the monitor has no identity." : "You are not part of this conversation; it is read-only.",
      messages: model.messagesOf(channelId),
      dividerAfter: chat.dividerAfter,
      hasOlder: model.hasOlder(channelId),
    };
    void chat.panel.webview.postMessage({ type: "state", state } satisfies ToWebview);
  }

  private html(webview: vscode.Webview): string {
    const nonce = randomBytes(16).toString("base64");
    const script = webview.asWebviewUri(vscode.Uri.joinPath(this.context.extensionUri, "dist", "chat.js"));
    return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'nonce-${nonce}'; script-src 'nonce-${nonce}'">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style nonce="${nonce}">
  [hidden] { display: none !important; }
  html, body { height: 100%; margin: 0; }
  body { display: flex; flex-direction: column; font-family: var(--vscode-font-family); font-size: var(--vscode-font-size); color: var(--vscode-foreground); }
  header { padding: 8px 12px; border-bottom: 1px solid var(--vscode-panel-border); }
  h1 { font-size: 1.2em; margin: 0; }
  #description, .muted { color: var(--vscode-descriptionForeground); }
  #messages { flex: 1; overflow-y: auto; padding: 8px 12px; }
  .message { padding: 6px 0; }
  .meta, .targets { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  .from { font-weight: bold; }
  .pill { padding: 0 6px; border-radius: 8px; background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); font-size: 0.85em; }
  .pill.all { background: var(--vscode-statusBarItem-warningBackground); color: var(--vscode-statusBarItem-warningForeground); }
  .pill.me { background: var(--vscode-button-background); color: var(--vscode-button-foreground); }
  .badge { font-size: 0.85em; }
  .badge.verified { color: var(--vscode-testing-iconPassed); }
  .badge.unknown_sender { color: var(--vscode-editorWarning-foreground); }
  .badge.rejected, .reason { color: var(--vscode-errorForeground); }
  .title { font-weight: bold; margin-top: 4px; }
  .text { white-space: pre-wrap; overflow-wrap: anywhere; margin-top: 2px; }
  .divider { display: flex; align-items: center; gap: 8px; margin: 8px 0; color: var(--vscode-errorForeground); font-weight: bold; }
  .divider::before, .divider::after { content: ""; flex: 1; border-top: 1px solid var(--vscode-errorForeground); }
  .older { display: block; margin: 0 auto 8px; }
  #composer { display: flex; flex-direction: column; gap: 6px; padding: 8px 12px; border-top: 1px solid var(--vscode-panel-border); }
  #composer .row { display: flex; gap: 6px; }
  #targets, #title { flex: 1; }
  input, select, textarea, button { font: inherit; color: var(--vscode-input-foreground); background: var(--vscode-input-background); border: 1px solid var(--vscode-input-border, transparent); padding: 3px 6px; }
  button { color: var(--vscode-button-foreground); background: var(--vscode-button-background); cursor: pointer; }
  button:disabled { opacity: 0.5; cursor: default; }
  textarea { min-height: 4em; resize: vertical; }
  #note { padding: 8px 12px; border-top: 1px solid var(--vscode-panel-border); color: var(--vscode-descriptionForeground); }
  #problem { color: var(--vscode-errorForeground); }
</style>
</head>
<body>
<header><h1 id="heading"></h1><div id="description"></div></header>
<main id="messages"></main>
<div id="note" hidden></div>
<form id="composer" hidden>
  <div class="row">
    <select id="type" aria-label="Type"></select>
    <input id="targets" placeholder="Targets: agent IDs or @all" aria-label="Targets">
    <input id="title" placeholder="Title (optional)" aria-label="Title">
  </div>
  <textarea id="text" placeholder="Message (Ctrl+Enter sends)" aria-label="Message"></textarea>
  <div class="row"><button id="send" type="submit">Send</button><span id="problem"></span></div>
</form>
<script nonce="${nonce}" src="${script}"></script>
</body>
</html>`;
  }
}
