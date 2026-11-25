import * as vscode from "vscode";
import { Api, ApiError } from "./api";
import { followEvents } from "./events";
import { Model } from "./model";
import { Message } from "./types";

// One connection to a monitor: its URL from the settings, the token from the secret storage, the
// model and the event stream. Everything restarts on login, logout and a changed URL.
export class Session implements vscode.Disposable {
  model: Model;
  private readonly changeEmitter = new vscode.EventEmitter<void>();
  private readonly messageEmitter = new vscode.EventEmitter<Message>();
  private readonly refreshEmitter = new vscode.EventEmitter<void>();
  private modelSubscription: { dispose(): void } | undefined;
  private stream: AbortController | undefined;
  private generation = 0; // counts starts; work of an older start is dropped
  private hasToken = false;
  private readonly log = vscode.window.createOutputChannel("Agent Messenger");

  readonly onDidChange = this.changeEmitter.event;
  readonly onDidReceiveMessage = this.messageEmitter.event;
  readonly onDidRefresh = this.refreshEmitter.event; // the state was read again, after every (re)connect

  constructor(private readonly context: vscode.ExtensionContext) {
    this.model = this.newModel(this.url, undefined);
  }

  get url(): string {
    const configured = vscode.workspace.getConfiguration("agentMessenger").get<string>("url", "http://localhost:8080");
    return configured.trim().replace(/\/+$/, "");
  }

  // The token belongs to one monitor: it is never sent to another URL.
  private tokenKey(url: string): string {
    return "agentMessenger.token:" + url;
  }

  private newModel(url: string, token: string | undefined): Model {
    const key = "lastRead:" + url;
    const memento = this.context.globalState;
    const model = new Model(new Api(url, token), {
      get: (channelId) => memento.get<Record<string, number>>(key, {})[channelId],
      set: (channelId, sequence) => void memento.update(key, { ...memento.get<Record<string, number>>(key, {}), [channelId]: sequence }),
    });
    this.modelSubscription?.dispose();
    this.modelSubscription = model.onChange(() => this.changeEmitter.fire());
    return model;
  }

  // Favourite channels and folded categories, per monitor URL like the last-read sequences.
  get favourites(): Set<string> {
    return this.readSet("favourites:");
  }

  get collapsedCategories(): Set<string> {
    return this.readSet("collapsed:");
  }

  toggleFavourite(channelId: string): void {
    this.updateSet("favourites:", channelId, !this.favourites.has(channelId));
    this.changeEmitter.fire();
  }

  setCategoryCollapsed(categoryId: string, collapsed: boolean): void {
    this.updateSet("collapsed:", categoryId, collapsed);
  }

  private readSet(prefix: string): Set<string> {
    return new Set(this.context.globalState.get<string[]>(prefix + this.url, []));
  }

  private updateSet(prefix: string, member: string, present: boolean): void {
    const members = this.readSet(prefix);
    if (present) members.add(member);
    else members.delete(member);
    void this.context.globalState.update(prefix + this.url, [...members]);
  }

  async start(): Promise<void> {
    const generation = ++this.generation;
    this.stop();
    const url = this.url;
    const token = await this.context.secrets.get(this.tokenKey(url));
    if (generation !== this.generation) return; // a newer start took over while the token was read
    this.hasToken = token !== undefined;
    const model = (this.model = this.newModel(url, token));
    const stream = (this.stream = new AbortController());
    await vscode.commands.executeCommand("setContext", "agentMessenger.loginRequired", false);
    this.changeEmitter.fire();
    void followEvents(model.api, {
      onOpen: async () => {
        model.setLive(true);
        await model.refresh();
        this.refreshEmitter.fire();
      },
      onEvent: (event) => {
        model.apply(event).then(
          (message) => message && generation === this.generation && this.messageEmitter.fire(message),
          (error) => this.report(error),
        );
      },
      onUnauthenticated: () => void this.requireLogin(),
      onDisconnect: () => model.setLive(false),
      onError: (error) => this.log.appendLine(`event stream: ${(error as Error).message}`),
    }, stream.signal);
  }

  stop(): void {
    this.stream?.abort();
  }

  dispose(): void {
    this.generation++;
    this.stop();
    this.modelSubscription?.dispose();
    this.log.dispose();
  }

  // Handles a failed API call: a login that is no longer valid asks for a new one, anything else
  // goes to the log.
  report(error: unknown): void {
    if (error instanceof ApiError && error.unauthenticated) void this.requireLogin();
    else this.log.appendLine((error as Error).message);
  }

  private async requireLogin(): Promise<void> {
    await vscode.commands.executeCommand("setContext", "agentMessenger.loginRequired", true);
    if (!this.hasToken) return;
    const choice = await vscode.window.showWarningMessage("Agent Messenger: the session ended. Log in again.", "Log In");
    if (choice) await vscode.commands.executeCommand("agentMessenger.login");
  }

  // Asks for the password, trades it for a session token and keeps only the token.
  async login(): Promise<void> {
    const url = this.url;
    const password = await vscode.window.showInputBox({ prompt: `Password of the bus monitor at ${url}`, password: true, ignoreFocusOut: true });
    if (password === undefined) return;
    try {
      const { token } = await new Api(url, undefined).login(password);
      await this.context.secrets.store(this.tokenKey(url), token);
    } catch (error) {
      if (error instanceof ApiError && error.code === "no_password_set") {
        void vscode.window.showInformationMessage("This monitor has no password; no login is needed.");
      } else {
        void vscode.window.showErrorMessage("Agent Messenger: login failed: " + (error as Error).message);
        return;
      }
    }
    await this.start();
  }

  async logout(): Promise<void> {
    const url = this.url;
    const token = await this.context.secrets.get(this.tokenKey(url));
    if (token) {
      await new Api(url, token).logout().catch(() => undefined); // the session may be gone already
      await this.context.secrets.delete(this.tokenKey(url));
    }
    await this.start();
  }
}
