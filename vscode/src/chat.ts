import { randomBytes } from "node:crypto";
import * as vscode from "vscode";
import { ApiError } from "./api";
import { inviteAgent, removeMember } from "./channelAdmin";
import { describeChannel, destinationOf, dmChannelId } from "./channels";
import { ChatState, FromWebview, PresenceMark, ToWebview, parseFromWebview } from "./protocol";
import { buildQuery, hitPlace, scopeTerms } from "./search";
import { Session } from "./session";
import { composerNote } from "./statusText";
import { isShown } from "./typeFilter";
import { ALL, Channel, SearchHit } from "./types";

const EMPTY_PAGES = 3; // search pages read in a row while the channel filter leaves none of their results
const HISTORY = 100; // messages loaded when a channel is opened
const THREADED_KEY = "agentMessenger.threaded"; // UI only: not one of the preferences shared through the monitor
const SHOWN_TYPES_KEY = "agentMessenger.shownTypes"; // likewise

interface Chat {
  panel: vscode.WebviewPanel;
  dividerAfter: number; // last-read sequence when the panel was opened or shown again
  members: string[];
  focusComposer: boolean; // focus it once the page has drawn
  ready: boolean; // the page has asked for its state
  subscribed: boolean; // what the page shows on its bell
  reveal?: number; // a message to scroll to once the page has it
  olderError?: string; // why the last read of older messages failed; the page shows it on its button
}

// One chat panel per channel. The page only draws; the monitor is read and written from here.
export class ChatPanels implements vscode.Disposable {
  private readonly chats = new Map<string, Chat>();
  private readonly emptyDirects = new Map<string, Channel>(); // direct messages without a message yet
  private sentPresence = ""; // the last presence posted to the panels, as JSON: only changes are sent again
  private readonly subscriptions: vscode.Disposable[];

  constructor(private readonly context: vscode.ExtensionContext, private readonly session: Session) {
    this.subscriptions = [
      session.onDidReceiveMessage((message) => {
        const chat = this.chats.get(message.channel);
        if (!chat) return;
        void chat.panel.webview.postMessage({ type: "message", message } satisfies ToWebview);
        if (chat.panel.visible) session.model.markRead(message.channel);
      }),
      session.onDidChange(() => {
        this.chats.forEach((chat, channelId) => this.postSubscribed(channelId, chat));
        const presence = this.presenceMarks();
        const json = JSON.stringify(presence);
        if (json === this.sentPresence) return;
        this.sentPresence = json;
        this.chats.forEach((chat) => void chat.panel.webview.postMessage({ type: "presence", presence } satisfies ToWebview));
      }),
      session.onDidRefresh(() => this.chats.forEach((chat, channelId) => void this.show(channelId, chat, chat.panel.visible))),
      vscode.workspace.onDidChangeConfiguration((change) => {
        if (change.affectsConfiguration("agentMessenger.groupMessages") || change.affectsConfiguration("agentMessenger.timeFormat")) this.chats.forEach((chat, channelId) => this.post(channelId, chat));
      }),
    ];
  }

  // Opens the conversation with an agent, as an empty panel while the two have no messages yet.
  openDirect(agentId: string): void {
    const { self, channels } = this.session.model;
    const id = dmChannelId(self, agentId);
    if (!channels.some((channel) => channel.id === id) && !this.emptyDirects.has(id)) {
      const agents = [self, agentId].sort();
      this.emptyDirects.set(id, { id, kind: "dm", name: agents.join(" + "), description: "", agents, messages: 0, last_sequence: 0, unread: 0, targeted_unread: 0 });
    }
    this.open(id, true);
  }

  // Whether the user is looking at the channel's panel: it is shown and focused, in a focused window.
  isLooking(channelId: string): boolean {
    const panel = this.chats.get(channelId)?.panel;
    return !!panel && panel.visible && panel.active && vscode.window.state.focused;
  }

  // Shows a message, for example a search result: opens its channel's panel, which loads older
  // history until it holds the message.
  async jumpTo(channelId: string, sequence: number): Promise<void> {
    this.open(channelId);
    const chat = this.chats.get(channelId);
    if (!chat) return;
    chat.reveal = sequence;
    if (chat.ready) await this.reveal(channelId, chat);
  }

  private async reveal(channelId: string, chat: Chat): Promise<void> {
    const { reveal: sequence } = chat;
    if (sequence === undefined) return;
    chat.reveal = undefined;
    const model = this.session.model;
    await this.guarded(async () => {
      while (!model.messagesOf(channelId).some((message) => message.sequence === sequence) && model.hasOlder(channelId)) await model.loadOlder(channelId);
    });
    // A message the type filter hides cannot be shown: the filter goes, as on the web page.
    const target = model.messagesOf(channelId).find((message) => message.sequence === sequence);
    if (target && !isShown(target, this.shownTypes)) await this.setShownTypes([]);
    this.post(channelId, chat);
    void chat.panel.webview.postMessage({ type: "reveal", sequence } satisfies ToWebview);
  }

  // Answers the page's lookup of the message a reply answers. Not finding it (404) is an answer;
  // any other failure is told as none, and the page keeps saying the message is not shown.
  private async lookUpParent(channelId: string, chat: Chat, id: string): Promise<void> {
    const { model } = this.session;
    let answer: Extract<ToWebview, { type: "parent" }> = { type: "parent", id };
    try {
      const message = await model.api.messageById(id);
      answer = { ...answer, message: message.channel === channelId ? message : { ...message, where: hitPlace(model.channels, message.channel) } };
    } catch (error) {
      // Only unknown_message means gone: a monitor older than the lookup answers not_found.
      if (error instanceof ApiError && error.code === "unknown_message") answer = { ...answer, gone: true };
      else if (error instanceof ApiError && error.unauthenticated) this.session.report(error);
    }
    void chat.panel.webview.postMessage(answer satisfies ToWebview);
  }

  // Answers a search of the page. A failure goes to the page, which shows it in place of the results.
  private async search(channelId: string, chat: Chat, { run, chips, text, allChannels, cursor }: Extract<FromWebview, { type: "search" }>): Promise<void> {
    const { model } = this.session;
    const channel = this.channelOf(channelId);
    const scope = allChannels || !channel ? [] : scopeTerms(channel);
    let answer: Extract<ToWebview, { type: "searchResults" }>;
    try {
      // The channel filter can empty a page that has a next one: look at a few more before giving up.
      const query = buildQuery(scope, chips, text);
      const inScope = (hit: SearchHit) => allChannels || hit.channel === channelId;
      let page = await model.api.search(query, cursor);
      let hits = page.results.filter(inScope);
      for (let more = 1; more < EMPTY_PAGES && hits.length === 0 && page.next_cursor; more++) {
        page = await model.api.search(query, page.next_cursor);
        hits = page.results.filter(inScope);
      }
      answer = { type: "searchResults", run, hits: hits.map((hit) => ({ ...hit, where: hitPlace(model.channels, hit.channel) })), next: page.next_cursor };
    } catch (error) {
      if (error instanceof ApiError && error.unauthenticated) this.session.report(error);
      answer = { type: "searchResults", run, hits: [], error: (error as Error).message };
    }
    void chat.panel.webview.postMessage(answer satisfies ToWebview);
  }

  // Shows a channel's new members in its open panel, after an invite or a removal.
  showMembers(channelId: string, members: string[]): void {
    const chat = this.chats.get(channelId);
    if (!chat) return;
    chat.members = members;
    this.post(channelId, chat);
  }

  // A conversation the monitor lists, or an empty direct message opened here.
  private channelOf(channelId: string): Channel | undefined {
    return this.session.model.channels.find((channel) => channel.id === channelId) ?? this.emptyDirects.get(channelId);
  }

  open(channelId: string, focusComposer = false): void {
    const existing = this.chats.get(channelId);
    if (existing) {
      existing.panel.reveal();
      if (focusComposer) void existing.panel.webview.postMessage({ type: "focusComposer" } satisfies ToWebview);
      return;
    }
    const channel = this.channelOf(channelId);
    if (!channel) return;
    const panel = vscode.window.createWebviewPanel("agentMessenger.chat", describeChannel(channel), vscode.ViewColumn.Active, {
      enableScripts: true,
      retainContextWhenHidden: true,
      localResourceRoots: [vscode.Uri.joinPath(this.context.extensionUri, "dist")],
    });
    const chat: Chat = { panel, dividerAfter: 0, members: [], focusComposer, ready: false, subscribed: this.session.prefs.subscribed.has(channelId) };
    this.chats.set(channelId, chat);
    panel.webview.html = this.html(panel.webview);
    panel.onDidDispose(() => {
      this.chats.delete(channelId);
      this.emptyDirects.delete(channelId);
    });
    panel.onDidChangeViewState(() => {
      if (!panel.visible) return;
      // The page decides, by what it showed, whether the divider goes: it is kept here.
      void this.show(channelId, chat, true);
      if (panel.active) void panel.webview.postMessage({ type: "activated" } satisfies ToWebview);
    });
    panel.webview.onDidReceiveMessage((raw: unknown) => {
      const message = parseFromWebview(raw);
      if (!message) this.session.report(new Error("chat panel: ignored a malformed message from the page"));
      else this.handle(channelId, chat, message).catch((error) => this.session.report(error));
    });
  }

  dispose(): void {
    this.subscriptions.forEach((subscription) => subscription.dispose());
    this.chats.forEach((chat) => chat.panel.dispose());
  }

  private async handle(channelId: string, chat: Chat, message: FromWebview): Promise<void> {
    const model = this.session.model;
    if (message.type === "ready") {
      chat.ready = true;
      await this.show(channelId, chat);
      if (chat.focusComposer) void chat.panel.webview.postMessage({ type: "focusComposer" } satisfies ToWebview);
      chat.focusComposer = false;
      await this.reveal(channelId, chat);
    } else if (message.type === "search") {
      await this.search(channelId, chat, message);
    } else if (message.type === "parent") {
      await this.lookUpParent(channelId, chat, message.id);
    } else if (message.type === "jump") {
      await this.jumpTo(message.channel, message.sequence);
    } else if (message.type === "older") {
      // No toast: the page asks again whenever the reader scrolls near the top, which would stack
      // them while the monitor answers with errors. Its button shows the error instead.
      try {
        await model.loadOlder(channelId);
        chat.olderError = undefined;
      } catch (error) {
        if (error instanceof ApiError && error.unauthenticated) this.session.report(error);
        chat.olderError = (error as Error).message;
      }
      this.post(channelId, chat);
    } else if (message.type === "raw") {
      await this.guarded(async () => {
        const { raw, raw_text } = await model.api.rawMessage(channelId, message.sequence);
        const content = raw_text ?? JSON.stringify(raw, null, 2);
        const document = await vscode.workspace.openTextDocument({ language: "json", content });
        await vscode.window.showTextDocument(document, { preview: true });
      });
    } else if (message.type === "subscribe") {
      this.session.prefs.setSubscribed(channelId, message.value); // its change event posts the new state
    } else if (message.type === "threaded") {
      await this.context.globalState.update(THREADED_KEY, message.value);
      this.chats.forEach((other, otherId) => this.post(otherId, other));
    } else if (message.type === "shownTypes") {
      await this.setShownTypes(message.value);
    } else if (message.type === "invite") {
      await inviteAgent(this.session, this, channelId);
    } else if (message.type === "removeMember") {
      await removeMember(this.session, this, channelId, message.agent);
    } else {
      const { type, targets, title, text } = message.draft;
      let error: string | undefined;
      try {
        const channel = this.channelOf(channelId);
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

  private get shownTypes(): string[] {
    return this.context.globalState.get<string[]>(SHOWN_TYPES_KEY, []);
  }

  // The type filter is one for all panels, as the web page's is for all channels.
  private async setShownTypes(types: string[]): Promise<void> {
    await this.context.globalState.update(SHOWN_TYPES_KEY, types);
    this.chats.forEach((chat, channelId) => this.post(channelId, chat));
  }

  private async guarded(action: () => Promise<void>): Promise<void> {
    try {
      await action();
    } catch (error) {
      if (error instanceof ApiError && error.unauthenticated) this.session.report(error);
      else void vscode.window.showErrorMessage("Agent Bus: " + (error as Error).message);
    }
  }

  // Loads the history, remembers where the "New" divider goes, shows the channel and marks it read.
  // A visible panel keeps its divider across a reconnect: its messages were read already. Showing
  // clears the older-messages error, so the page tries again.
  private async show(channelId: string, chat: Chat, keepDivider = false): Promise<void> {
    const model = this.session.model;
    chat.olderError = undefined;
    if (model.channels.some((channel) => channel.id === channelId)) {
      await this.guarded(async () => {
        await model.loadHistory(channelId, HISTORY);
        await model.loadUnread(channelId);
        chat.members = await model.api.members(channelId);
      });
    } else {
      chat.members = this.emptyDirects.get(channelId)?.agents ?? []; // the monitor knows no such channel yet
    }
    if (!keepDivider) chat.dividerAfter = model.lastRead(channelId);
    this.post(channelId, chat);
    if (chat.panel.visible) model.markRead(channelId);
  }

  private postSubscribed(channelId: string, chat: Chat): void {
    const value = this.session.prefs.subscribed.has(channelId);
    if (value === chat.subscribed) return;
    chat.subscribed = value;
    void chat.panel.webview.postMessage({ type: "subscribed", value } satisfies ToWebview);
  }

  private presenceMarks(): Record<string, PresenceMark> {
    return Object.fromEntries([...this.session.model.presence].map(([agent, { state, since }]) => [agent, { state, since }]));
  }

  private post(channelId: string, chat: Chat): void {
    const model = this.session.model;
    const channel = this.channelOf(channelId);
    if (!channel || !model.status) return;
    const destination = destinationOf(channel, model.self);
    const canWrite = destination !== undefined;
    chat.subscribed = this.session.prefs.subscribed.has(channelId);
    const state: ChatState = {
      title: describeChannel(channel),
      description: channel.description,
      self: model.self,
      canSend: model.status.can_send && canWrite,
      note: composerNote(model.status, canWrite),
      messages: model.messagesOf(channelId),
      dividerAfter: chat.dividerAfter,
      hasOlder: model.hasOlder(channelId),
      olderError: chat.olderError,
      members: chat.members,
      canManageMembers: model.status.can_send && channel.kind === "group",
      subscribed: chat.subscribed,
      groupMessages: vscode.workspace.getConfiguration("agentMessenger").get<boolean>("groupMessages", true),
      timeFormat: vscode.workspace.getConfiguration("agentMessenger").get<string>("timeFormat") === "relative" ? "relative" : "absolute",
      threaded: this.context.globalState.get<boolean>(THREADED_KEY, false),
      shownTypes: this.shownTypes,
      presence: this.presenceMarks(),
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
  body { --name-indent: 16px; display: flex; flex-direction: column; font-family: var(--vscode-font-family); font-size: var(--vscode-font-size); color: var(--vscode-foreground); }
  .muted, #description { color: var(--vscode-descriptionForeground); }
  header { display: flex; align-items: baseline; gap: 8px; padding: 6px 12px; border-bottom: 1px solid var(--vscode-panel-border); }
  h1 { font-size: 1.1em; margin: 0; white-space: nowrap; }
  header .link { color: var(--vscode-textLink-foreground); background: transparent; padding: 0 4px; font-size: 0.9em; }
  header .link[aria-pressed="true"] { color: var(--vscode-foreground); border-bottom: 1px solid var(--vscode-focusBorder); }
  .reply-to { display: block; max-width: calc(100% - var(--name-indent)); margin-left: var(--name-indent); padding: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; font-size: 0.85em; font-family: var(--vscode-font-family); color: var(--vscode-descriptionForeground); background: transparent; }
  .reply-to .where { opacity: 0.8; }
  button.reply-to:hover:not(:disabled) { background: transparent; color: var(--vscode-textLink-foreground); }
  .reply-to .who { font-weight: 600; }
  .reply-to.missing { font-style: italic; }
  .in-thread { box-shadow: inset 3px 0 0 hsl(var(--thread-hue) 60% 55%); }
  .thread { margin: 6px 8px; border: 1px solid var(--vscode-panel-border); border-radius: 4px; box-shadow: inset 3px 0 0 hsl(var(--thread-hue) 60% 55%); }
  .thread .depth-1, .thread .depth-2, .thread .depth-3, .thread .depth-4 { border-left-color: hsl(var(--thread-hue) 60% 55% / 0.5); }
  .flash { animation: flash 1.5s ease-out; }
  @keyframes flash { from { background: var(--vscode-editor-findMatchHighlightBackground); } to { background: transparent; } }
  .new { padding: 0 5px; border-radius: 3px; font-size: 0.8em; color: var(--vscode-errorForeground); border: 1px solid var(--vscode-errorForeground); }
  .depth-1, .depth-2, .depth-3, .depth-4 { margin-left: 12px; border-left: 2px solid var(--vscode-panel-border); }
  .depth-2 { margin-left: 24px; }
  .depth-3 { margin-left: 36px; }
  .depth-4 { margin-left: 48px; }
  .has-new { border-left: 3px solid var(--vscode-focusBorder); }
  #description { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  #content { flex: 1; display: flex; min-height: 0; }
  #messages, #results { flex: 1; min-width: 0; overflow-y: auto; padding: 4px 0; }
  #members { flex: none; width: 13em; overflow-y: auto; padding: 4px 0; }
  #memberSplit { flex: none; width: 4px; cursor: col-resize; user-select: none; border-left: 1px solid var(--vscode-panel-border); }
  #memberSplit:hover, #memberSplit:focus-visible, #memberSplit.dragging { background: var(--vscode-sash-hoverBorder, var(--vscode-focusBorder)); outline: 0; }
  #members h2 { margin: 6px 10px 2px; font-size: 0.8em; font-weight: normal; text-transform: uppercase; color: var(--vscode-descriptionForeground); }
  .member { display: flex; align-items: center; gap: 4px; padding: 1px 10px; white-space: nowrap; }
  .member .name { overflow: hidden; text-overflow: ellipsis; }
  .member .state { margin-left: auto; padding-left: 6px; font-size: 0.85em; color: var(--vscode-descriptionForeground); }
  #members .invite { margin: 4px 10px; }
  .member .remove { visibility: hidden; padding: 0 2px; }
  .member:hover .remove, .member .remove:focus-visible { visibility: visible; }
  @media (max-width: 520px) { #members, #memberSplit, #membersToggle { display: none; } }
  #searchbar { display: flex; align-items: center; gap: 8px; padding: 6px 12px; border-bottom: 1px solid var(--vscode-panel-border); }
  #searchField { flex: 1; min-width: 0; display: flex; flex-wrap: wrap; gap: 4px; align-items: center; background: var(--vscode-input-background); border: 1px solid var(--vscode-input-border, transparent); border-radius: 4px; padding: 2px 6px; }
  #searchField:focus-within { border-color: var(--vscode-focusBorder); }
  #searchInput { flex: 1; min-width: 8em; color: var(--vscode-input-foreground); background: transparent; border: 0; padding: 2px 0; outline: 0; }
  .tag { background: transparent; }
  .tag:not(.badge) { color: inherit; padding: 0; }
  .tag:hover:not(:disabled) { background: var(--vscode-toolbar-hoverBackground); text-decoration: underline; }
  .tag:focus-visible { outline: 1px solid var(--vscode-focusBorder); }
  #types { display: flex; flex-wrap: wrap; gap: 4px; padding: 4px 12px; border-bottom: 1px solid var(--vscode-panel-border); }
  #types:empty { display: none; }
  .empty-type { opacity: 0.6; }
  #types .pill, #searchbar .pill { color: var(--vscode-descriptionForeground); background: transparent; border: 1px solid var(--vscode-widget-border, var(--vscode-panel-border)); border-radius: 10px; font-size: 0.85em; padding: 1px 8px; white-space: nowrap; }
  #types .pill:hover:not(:disabled), #searchbar .pill:hover:not(:disabled) { color: var(--vscode-foreground); background: var(--vscode-toolbar-hoverBackground); }
  #types .pill[aria-pressed="true"], #searchbar .pill[aria-pressed="true"] { color: var(--vscode-foreground); border-color: var(--vscode-focusBorder); }
  #searchbar .link { color: var(--vscode-foreground); background: transparent; padding: 0 6px; font-size: 1.1em; }
  #types button:focus-visible, #searchbar button:focus-visible, header .link:focus-visible { outline: 1px solid var(--vscode-focusBorder); }
  .hit { display: block; width: 100%; box-sizing: border-box; text-align: left; padding: 6px 12px; color: var(--vscode-foreground); background: transparent; border-bottom: 1px solid var(--vscode-panel-border); }
  .hit:hover:not(:disabled), .hit:focus-visible { background: var(--vscode-list-hoverBackground); }
  .hit-meta { display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline; font-size: 0.9em; }
  .hit-text { display: block; margin-top: 2px; white-space: pre-wrap; overflow-wrap: anywhere; }
  .hit-title { font-weight: bold; }
  mark { color: inherit; background: var(--vscode-editor-findMatchHighlightBackground); }
  .empty { padding: 12px; text-align: center; color: var(--vscode-descriptionForeground); }
  .flash { animation: flash 1.5s ease-out; }
  @keyframes flash { from { background: var(--vscode-editor-findMatchHighlightBackground); } to { background: transparent; } }
  .message { padding: 6px 12px; font-family: var(--vscode-editor-font-family, var(--vscode-font-family)); font-size: var(--vscode-editor-font-size, inherit); }
  .message.continued { padding-top: 0; }
  .meta { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; font-family: var(--vscode-font-family); font-size: var(--vscode-font-size); }
  .meta .time { margin-left: auto; }
  .dot { flex: none; box-sizing: border-box; width: 8px; height: 8px; margin-right: 2px; border-radius: 50%; background: var(--vscode-disabledForeground); }
  .dot.working { background: var(--vscode-charts-green); }
  .dot.idle { background: var(--vscode-charts-blue); }
  .dot.waiting_for_owner { background: var(--vscode-charts-yellow); }
  .dot.rate_limited { background: var(--vscode-charts-orange); }
  .dot.unresponsive, .dot.error { background: var(--vscode-charts-red); }
  .dot.offline { background: transparent; border: 1px solid var(--vscode-disabledForeground); }
  .from { font-weight: bold; }
  .time { flex: none; font-size: 0.85em; font-family: var(--vscode-font-family); }
  .box { position: relative; margin: 4px 0 0 var(--name-indent); border: 1px solid var(--vscode-widget-border, var(--vscode-panel-border)); border-radius: 6px; line-height: 1.45; }
  .box > .text { padding: 6px 10px; }
  /* Hidden by opacity, not visibility, so the keyboard still reaches the raw message button. */
  .box .corner { position: absolute; top: 2px; right: 8px; display: flex; gap: 6px; align-items: center; opacity: 0; }
  .box:hover .corner, .box:focus-within .corner { opacity: 1; }
  .raw { padding: 0 4px; font-family: var(--vscode-editor-font-family, monospace); font-size: 0.85em; color: var(--vscode-descriptionForeground); background: transparent; opacity: 0; }
  .message:hover .raw, .message:focus-within .raw, .box .raw { opacity: 1; }
  .raw:hover:not(:disabled) { color: var(--vscode-foreground); background: var(--vscode-toolbar-hoverBackground); }
  .raw:focus-visible { outline: 1px solid var(--vscode-focusBorder); }
  .section { display: grid; grid-template-columns: 3.5em 1fr; gap: 8px; padding: 6px 10px; }
  .section + .section { border-top: 1px solid var(--vscode-widget-border, var(--vscode-panel-border)); }
  .label { padding-top: 0.15em; font-family: var(--vscode-font-family); font-size: 0.7em; letter-spacing: 0.05em; text-transform: uppercase; color: var(--vscode-descriptionForeground); }
  .badge { padding: 0 5px; border-radius: 3px; font-size: 0.8em; color: var(--vscode-descriptionForeground); border: 1px solid var(--vscode-widget-border, var(--vscode-panel-border)); }
  .badge.me { color: var(--vscode-foreground); border-color: var(--vscode-focusBorder); }
  .verified { color: var(--vscode-descriptionForeground); font-size: 0.85em; cursor: default; }
  .warning { color: var(--vscode-editorWarning-foreground); font-size: 0.85em; }
  .warning.rejected { color: var(--vscode-errorForeground); }
  .message > .warning { margin-left: var(--name-indent); }
  .title { font-weight: bold; overflow-wrap: anywhere; }
  .replies { padding: 0 12px 4px calc(12px + var(--name-indent)); font-size: 0.85em; }
  .text { white-space: pre-wrap; overflow-wrap: anywhere; }
  .divider { display: flex; align-items: center; gap: 8px; margin: 6px 12px; color: var(--vscode-errorForeground); font-size: 0.85em; }
  .divider::after { content: ""; flex: 1; border-top: 1px solid var(--vscode-errorForeground); }
  .older { display: block; margin: 4px auto 8px; }
  input, select, textarea, button { font: inherit; }
  button { color: var(--vscode-button-foreground); background: var(--vscode-button-background); border: 0; padding: 3px 10px; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--vscode-button-hoverBackground); }
  button:disabled { opacity: 0.5; cursor: default; }
  #note:empty { display: none; }
  #note { padding: 8px 12px; border-top: 1px solid var(--vscode-panel-border); color: var(--vscode-descriptionForeground); }
  #composer { position: relative; margin: 8px 12px 10px; border: 1px solid var(--vscode-input-border, var(--vscode-widget-border, var(--vscode-panel-border))); background: var(--vscode-input-background); border-radius: 8px; }
  #composer:focus-within { border-color: var(--vscode-focusBorder); }
  #composer textarea, #composer input { display: block; box-sizing: border-box; width: 100%; color: var(--vscode-input-foreground); background: transparent; border: 0; outline: 0; padding: 0; }
  #composer ::placeholder { color: var(--vscode-input-placeholderForeground); }
  #composer textarea { resize: none; max-height: 10lh; padding: 8px 10px 4px; line-height: 1.4; }
  #composer #title { font-weight: bold; padding: 6px 10px; border-bottom: 1px solid var(--vscode-widget-border, var(--vscode-panel-border)); border-radius: 8px 8px 0 0; }
  #composer #title:hover { background: var(--vscode-list-hoverBackground); }
  #composer #title:focus { border-bottom-color: var(--vscode-focusBorder); }
  #composer textarea:hover { background: var(--vscode-list-hoverBackground); }
  .field-label { flex: none; font-size: 0.8em; color: var(--vscode-descriptionForeground); }
  #targets { padding: 1px 4px; border: 1px solid transparent; border-radius: 6px; }
  #targets:hover { background: var(--vscode-list-hoverBackground); }
  #targets:focus-within { border-color: var(--vscode-focusBorder); }
  #toolbar select.pill:hover, #toolbar select.pill:focus-visible { border-color: var(--vscode-focusBorder); }
  #toolbar { display: flex; align-items: center; gap: 6px; padding: 2px 6px 6px 8px; }
  #toolbar button:focus-visible, #toolbar select:focus-visible { outline: 1px solid var(--vscode-focusBorder); outline-offset: 1px; }
  #completions { position: absolute; bottom: 100%; left: 0; margin: 0 0 4px; padding: 3px; list-style: none; max-height: 12em; overflow-y: auto; min-width: 10em; color: var(--vscode-editorSuggestWidget-foreground); background: var(--vscode-editorSuggestWidget-background); border: 1px solid var(--vscode-editorSuggestWidget-border, var(--vscode-widget-border, var(--vscode-focusBorder))); border-radius: 6px; box-shadow: 0 2px 8px var(--vscode-widget-shadow); z-index: 1; }
  #completions li { padding: 2px 8px; border-radius: 4px; cursor: pointer; }
  #completions li.active { color: var(--vscode-editorSuggestWidget-selectedForeground); background: var(--vscode-editorSuggestWidget-selectedBackground); }
  #targets { flex: 1; min-width: 0; display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
  #targets input { flex: 1; min-width: 6em; width: auto; font-size: 0.9em; }
  .chip { display: inline-flex; align-items: center; padding: 1px 8px; border-radius: 10px; font-size: 0.85em; color: var(--vscode-badge-foreground); background: var(--vscode-badge-background); }
  .chip button { color: inherit; background: transparent; padding: 0 0 0 4px; line-height: 1; }
  .chip button:hover:not(:disabled) { background: transparent; opacity: 0.7; }
  #toolbar .pill { color: var(--vscode-descriptionForeground); background: transparent; border: 1px solid var(--vscode-widget-border, var(--vscode-panel-border)); border-radius: 10px; font-size: 0.85em; padding: 1px 8px; }
  #toolbar select.pill { max-width: 10em; cursor: pointer; } /* the native arrow stays: it shows the pill is a choice */
  #toolbar select.pill option { color: var(--vscode-dropdown-foreground); background: var(--vscode-dropdown-background); }
  #toolbar .pill:hover:not(:disabled) { color: var(--vscode-foreground); background: var(--vscode-toolbar-hoverBackground); }
  #toolbar .pill[aria-pressed="true"] { color: var(--vscode-foreground); border-color: var(--vscode-focusBorder); }
  #send { flex: none; width: 28px; height: 28px; padding: 0; border-radius: 50%; font-size: 1.1em; line-height: 1; }
  #problem { color: var(--vscode-errorForeground); font-size: 0.9em; }
</style>
</head>
<body>
<header><h1 id="heading"></h1><div id="description"></div><button id="searchToggle" class="link" type="button" title="Search messages (Ctrl+F)" aria-pressed="false">Search</button><button id="bell" class="link" type="button" aria-pressed="false"></button><button id="threads" class="link" type="button" title="Show replies under their parent" aria-pressed="false">Threads</button><button id="membersToggle" class="link" type="button" title="Show the agents that read this channel" aria-pressed="true" hidden>Members</button></header>
<div id="searchbar" role="search" hidden>
  <div id="searchField"><input id="searchInput" type="text" placeholder="Search: words, &quot;phrases&quot;, from: type: targets: after: before:" aria-label="Search messages" autocomplete="off" spellcheck="false"></div>
  <button id="scope" class="pill" type="button" title="Search all channels, not only this one" aria-pressed="false">All channels</button>
  <button id="searchClose" class="link" type="button" title="Close search (Esc)" aria-label="Close search">×</button>
</div>
<div id="types" role="group" aria-label="Message types"></div>
<div id="content">
<main id="results" hidden></main>
<main id="messages"></main>
<div id="memberSplit" role="separator" aria-orientation="vertical" aria-label="Resize the member list" aria-valuemin="120" aria-valuemax="480" tabindex="0" hidden></div>
<aside id="members" aria-label="Members" hidden></aside>
</div>
<div id="note"></div>
<form id="composer" hidden>
  <ul id="completions" role="listbox" hidden></ul>
  <input id="title" placeholder="Title (optional)" aria-label="Title">
  <textarea id="text" rows="1" aria-label="Message"></textarea>
  <div id="toolbar">
    <span class="field-label">Type</span>
    <select id="type" class="pill" aria-label="Type"></select>
    <span class="field-label">To</span>
    <div id="targets"><input id="targetInput" placeholder="@ to target" aria-label="To"></div>
    <span id="problem"></span>
    <button id="send" type="submit" title="Send" aria-label="Send" disabled>↑</button>
  </div>
</form>
<script nonce="${nonce}" src="${script}"></script>
</body>
</html>`;
  }
}
