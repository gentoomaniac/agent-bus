import * as vscode from "vscode";
import { Api, ApiError } from "./api";
import { followEvents } from "./events";
import { Model } from "./model";
import { LegacyPrefs, Prefs } from "./prefs";
import { Message, ServerPrefs } from "./types";

const legacyKey = (part: keyof LegacyPrefs, url: string) => (part === "read" ? "lastRead:" : part + ":") + url;

// One connection to a monitor: its URL from the settings, the token from the secret storage, the
// model and the event stream. Everything restarts on login, logout and a changed URL.
export class Session implements vscode.Disposable {
  model: Model;
  prefs: Prefs;
  private readonly changeEmitter = new vscode.EventEmitter<void>();
  private readonly messageEmitter = new vscode.EventEmitter<Message>();
  private readonly refreshEmitter = new vscode.EventEmitter<void>();
  private modelSubscription: { dispose(): void } | undefined;
  private stream: AbortController | undefined;
  private generation = 0; // counts starts; work of an older start is dropped
  private hasToken = false;
  loginRequired = false; // the monitor refused our token (or we have none): no connection to show
  readonly log = vscode.window.createOutputChannel("Agent Messenger");

  readonly onDidChange = this.changeEmitter.event;
  readonly onDidReceiveMessage = this.messageEmitter.event;
  readonly onDidRefresh = this.refreshEmitter.event; // the state was read again, after every (re)connect

  constructor(private readonly context: vscode.ExtensionContext) {
    ({ model: this.model, prefs: this.prefs } = this.newModel(this.url, undefined));
  }

  get url(): string {
    const configured = vscode.workspace.getConfiguration("agentMessenger").get<string>("url", "http://localhost:8080");
    return configured.trim().replace(/\/+$/, "");
  }

  // The token belongs to one monitor: it is never sent to another URL.
  private tokenKey(url: string): string {
    return "agentMessenger.token:" + url;
  }

  private newModel(url: string, token: string | undefined): { model: Model; prefs: Prefs } {
    const api = new Api(url, token);
    const prefs = new Prefs(api, () => this.changeEmitter.fire(), (error) => this.report(error));
    const model = new Model(api, prefs);
    this.modelSubscription?.dispose();
    this.modelSubscription = model.onChange(() => this.changeEmitter.fire());
    return { model, prefs };
  }

  // What an older version kept in the global state for this monitor, before the prefs were shared.
  // Folded categories were then called directMessages where the monitor says dms.
  private legacyPrefs(url: string): LegacyPrefs {
    const state = this.context.globalState;
    return {
      read: state.get<Record<string, number>>(legacyKey("read", url), {}),
      favourites: state.get<string[]>(legacyKey("favourites", url), []),
      collapsed: state.get<string[]>(legacyKey("collapsed", url), []).map((id) => (id === "directMessages" ? "dms" : id)),
    };
  }

  private async dropLegacyPrefs(url: string, parts: (keyof LegacyPrefs)[]): Promise<void> {
    for (const part of parts) await this.context.globalState.update(legacyKey(part, url), undefined);
  }

  async start(): Promise<void> {
    const generation = ++this.generation;
    this.stop();
    const url = this.url;
    const token = await this.context.secrets.get(this.tokenKey(url));
    if (generation !== this.generation) return; // a newer start took over while the token was read
    this.hasToken = token !== undefined;
    const { model, prefs } = this.newModel(url, token);
    this.model = model;
    this.prefs = prefs;
    const stream = (this.stream = new AbortController());
    this.loginRequired = false;
    await vscode.commands.executeCommand("setContext", "agentMessenger.loginRequired", false);
    this.changeEmitter.fire();
    void followEvents(model.api, {
      onOpen: async () => {
        model.setLive(true);
        // The prefs come first: the first refresh counts a channel without a read marker as read.
        await this.dropLegacyPrefs(url, await prefs.load(this.legacyPrefs(url)));
        await model.refresh();
        this.refreshEmitter.fire();
      },
      onEvent: (event) => {
        if (event.event === "prefs") prefs.apply(JSON.parse(event.data) as ServerPrefs);
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
    this.loginRequired = true;
    this.changeEmitter.fire();
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
