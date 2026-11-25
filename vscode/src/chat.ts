import { randomBytes } from "node:crypto";
import * as vscode from "vscode";
import { ApiError } from "./api";
import { describeChannel, destinationOf } from "./channels";
import { ChatState, FromWebview, ToWebview } from "./protocol";
import { Session } from "./session";
import { ALL } from "./types";

const HISTORY = 100; // messages loaded when a channel is opened

interface Chat {
  panel: vscode.WebviewPanel;
  dividerAfter: number; // last-read sequence when the panel was opened or shown again
  members: string[];
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
      session.onDidRefresh(() => this.chats.forEach((chat, channelId) => void this.show(channelId, chat, chat.panel.visible))),
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
    const chat: Chat = { panel, dividerAfter: 0, members: [] };
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
      const { type, targets, title, text } = message.draft;
      let error: string | undefined;
      try {
        const channel = model.channels.find((c) => c.id === channelId);
        const destination = channel && destinationOf(channel, model.self);
        if (!destination) throw new Error("this conversation is read-only");
        await model.api.send({ to: destination, type, targets, title: title.trim() || undefined, text });
      } catch (caught) {
        error = (caught as Error).message;
        this.session.report(caught);
      }
      void chat.panel.webview.postMessage({ type: "sendResult", error } satisfies ToWebview);
    }
  }

  private async guarded(action: () => Promise<void>): Promise<void> {
    try {
      await action();
    } catch (error) {
      if (error instanceof ApiError && error.unauthenticated) this.session.report(error);
      else void vscode.window.showErrorMessage("Agent Messenger: " + (error as Error).message);
    }
  }

  // Loads the history, remembers where the "New" divider goes, shows the channel and marks it read.
  // A visible panel keeps its divider across a reconnect: its messages were read already.
  private async show(channelId: string, chat: Chat, keepDivider = false): Promise<void> {
    const model = this.session.model;
    await this.guarded(async () => {
      await model.loadHistory(channelId, HISTORY);
      chat.members = await model.api.members(channelId);
    });
    if (!keepDivider) chat.dividerAfter = model.lastRead(channelId);
    this.post(channelId, chat);
    if (chat.panel.visible) model.markRead(channelId);
  }

  private post(channelId: string, chat: Chat): void {
    const model = this.session.model;
    const channel = model.channels.find((c) => c.id === channelId);
    if (!channel || !model.status) return;
    const destination = destinationOf(channel, model.self);
    const canWrite = destination !== undefined;
    const state: ChatState = {
      title: describeChannel(channel),
      description: channel.description,
      self: model.self,
      canSend: model.status.can_send && canWrite,
      note: !model.status.can_send ? model.status.note || "Read-only: the monitor has no identity." : "You are not part of this conversation; it is read-only.",
      messages: model.messagesOf(channelId),
      dividerAfter: chat.dividerAfter,
      hasOlder: model.hasOlder(channelId),
      members: chat.members,
      defaultTarget: channel.kind === "dm" && destination ? destination.name : ALL,
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
  .muted, #description { color: var(--vscode-descriptionForeground); }
  header { display: flex; align-items: baseline; gap: 8px; padding: 6px 12px; border-bottom: 1px solid var(--vscode-panel-border); }
  h1 { font-size: 1.1em; margin: 0; white-space: nowrap; }
  #description { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  #messages { flex: 1; overflow-y: auto; padding: 4px 0; }
  .message { padding: 4px 12px; font-family: var(--vscode-editor-font-family, var(--vscode-font-family)); font-size: var(--vscode-editor-font-size, inherit); }
  .message:hover { background: var(--vscode-list-hoverBackground); }
  .message.continued { padding-top: 0; }
  .meta { display: flex; flex-wrap: wrap; gap: 6px; align-items: baseline; font-family: var(--vscode-font-family); font-size: var(--vscode-font-size); }
  .from { font-weight: bold; }
  .time { font-size: 0.85em; }
  .badge { padding: 0 5px; border-radius: 3px; font-size: 0.8em; color: var(--vscode-descriptionForeground); border: 1px solid var(--vscode-widget-border, var(--vscode-panel-border)); }
  .badge.me { color: var(--vscode-foreground); border-color: var(--vscode-focusBorder); }
  .verified { color: var(--vscode-descriptionForeground); font-size: 0.85em; cursor: default; }
  .warning { color: var(--vscode-editorWarning-foreground); font-size: 0.85em; }
  .warning.rejected { color: var(--vscode-errorForeground); }
  .title { font-weight: bold; }
  .text { white-space: pre-wrap; overflow-wrap: anywhere; }
  .divider { display: flex; align-items: center; gap: 8px; margin: 6px 12px; color: var(--vscode-errorForeground); font-size: 0.85em; }
  .divider::after { content: ""; flex: 1; border-top: 1px solid var(--vscode-errorForeground); }
  .older { display: block; margin: 4px auto 8px; }
  input, select, textarea, button { font: inherit; }
  button { color: var(--vscode-button-foreground); background: var(--vscode-button-background); border: 0; padding: 3px 10px; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--vscode-button-hoverBackground); }
  button:disabled { opacity: 0.5; cursor: default; }
  #note { padding: 8px 12px; border-top: 1px solid var(--vscode-panel-border); color: var(--vscode-descriptionForeground); }
  #composer { margin: 8px 12px 10px; border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); background: var(--vscode-input-background); border-radius: 4px; }
  #composer:focus-within { border-color: var(--vscode-focusBorder); }
  #composer textarea, #composer input { display: block; box-sizing: border-box; width: 100%; color: var(--vscode-input-foreground); background: transparent; border: 0; outline: 0; padding: 6px 8px; }
  #composer textarea { resize: none; max-height: 12em; }
  #title { font-weight: bold; border-bottom: 1px solid var(--vscode-panel-border); }
  #toolbar { display: flex; align-items: center; gap: 6px; padding: 2px 6px 4px; }
  #toolbar select { max-width: 14em; color: var(--vscode-foreground); background: var(--vscode-dropdown-background); border: 1px solid var(--vscode-dropdown-border); font-size: 0.9em; padding: 1px 2px; }
  #toolbar .link { color: var(--vscode-textLink-foreground); background: transparent; padding: 0 4px; font-size: 0.9em; }
  #toolbar .spacer { flex: 1; }
  #hint { font-size: 0.85em; }
  #problem { color: var(--vscode-errorForeground); font-size: 0.9em; }
</style>
</head>
<body>
<header><h1 id="heading"></h1><div id="description"></div></header>
<main id="messages"></main>
<div id="note" hidden></div>
<form id="composer" hidden>
  <input id="title" placeholder="Title" aria-label="Title" hidden>
  <textarea id="text" rows="1" placeholder="Message" aria-label="Message"></textarea>
  <div id="toolbar">
    <select id="type" aria-label="Type"></select>
    <select id="target" aria-label="To"></select>
    <button id="toggleTitle" class="link" type="button">+ title</button>
    <span class="spacer"></span>
    <span id="problem"></span>
    <span id="hint" class="muted">Enter sends, Shift+Enter adds a line</span>
    <button id="send" type="submit">Send</button>
  </div>
</form>
<script nonce="${nonce}" src="${script}"></script>
</body>
</html>`;
  }
}
