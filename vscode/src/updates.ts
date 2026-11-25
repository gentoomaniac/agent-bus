import * as fs from "node:fs/promises";
import * as vscode from "vscode";
import { fetchFromGithub, isNewer, parseRelease, Release } from "./release";

const LATEST_RELEASE_URL = "https://api.github.com/repos/gentoomaniac/agent-bus/releases/latest";
const CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000;
const API_TIMEOUT_MS = 10_000;
const DOWNLOAD_TIMEOUT_MS = 120_000;
const SKIPPED_KEY = "agentMessenger.skippedVersion";

const INSTALL = "Install";
const RELEASE_NOTES = "Release Notes";
const SKIP = "Skip This Version";

// Tells about a newer release on GitHub, and installs its .vsix on request. The only request
// besides the download goes to api.github.com, and carries no user data and no token.
export class UpdateChecker implements vscode.Disposable {
  private readonly offered = new Set<string>(); // versions already shown in this session
  private timer: NodeJS.Timeout | undefined;

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly log: vscode.OutputChannel,
  ) {}

  private get currentVersion(): string {
    return this.context.extension.packageJSON.version;
  }

  // Checks now and then every 24 hours. The setting is read at each time, so it can change while
  // the editor runs.
  start(): void {
    const automaticCheck = () => {
      if (vscode.workspace.getConfiguration("agentMessenger").get<boolean>("checkForUpdates", true)) void this.check(false);
    };
    automaticCheck();
    this.timer = setInterval(automaticCheck, CHECK_INTERVAL_MS);
  }

  dispose(): void {
    clearInterval(this.timer);
  }

  // The automatic check fails quietly into the log and tries again at the next interval. The
  // manual one says what happened, and offers the newest version even if it was skipped or shown.
  async check(manual: boolean): Promise<void> {
    let release: Release | undefined;
    try {
      release = await this.fetchLatestRelease();
    } catch (error) {
      this.log.appendLine(`update check: ${(error as Error).message}`);
      if (manual) void vscode.window.showErrorMessage(`Agent Bus: the update check failed (${(error as Error).message}).`);
      return;
    }
    if (!release) {
      this.log.appendLine("update check: the answer is no usable release");
      if (manual) void vscode.window.showErrorMessage("Agent Bus: couldn't read the latest release.");
      return;
    }
    if (!isNewer(release.version, this.currentVersion)) {
      if (manual) void vscode.window.showInformationMessage(`Agent Bus: you have the latest version (${this.currentVersion}).`);
      return;
    }
    if (!manual && (this.offered.has(release.version) || this.context.globalState.get<string>(SKIPPED_KEY) === release.version)) return;
    this.offered.add(release.version);
    await this.offer(release);
  }

  private async fetchLatestRelease(): Promise<Release | undefined> {
    const response = await fetch(LATEST_RELEASE_URL, {
      signal: AbortSignal.timeout(API_TIMEOUT_MS), // covers the body too
      headers: { Accept: "application/vnd.github+json", "User-Agent": `agent-messenger-vscode/${this.currentVersion}` },
      redirect: "manual", // the API has no reason to send us to another host
    });
    if (!response.ok) throw new Error(`GitHub answered HTTP ${response.status}${response.status < 400 ? " (the API does not redirect)" : ""}`);
    return parseRelease(await response.json());
  }

  private async offer(release: Release): Promise<void> {
    const buttons = [...(release.vsixUrl ? [INSTALL] : []), ...(release.url ? [RELEASE_NOTES] : []), SKIP];
    const choice = await vscode.window.showInformationMessage(
      `Agent Bus ${release.version} is available (you have ${this.currentVersion}).`,
      ...buttons,
    );
    if (choice === INSTALL && release.vsixUrl) await this.install(release.version, release.vsixUrl);
    else if (choice === RELEASE_NOTES && release.url) await vscode.env.openExternal(vscode.Uri.parse(release.url));
    else if (choice === SKIP) await this.context.globalState.update(SKIPPED_KEY, release.version);
  }

  private async install(version: string, vsixUrl: string): Promise<void> {
    const file = vscode.Uri.joinPath(this.context.globalStorageUri, `agent-messenger-${version}.vsix`);
    try {
      await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Notification, title: `Installing Agent Bus ${version}` },
        async () => {
          const response = await fetchFromGithub(vsixUrl, { signal: AbortSignal.timeout(DOWNLOAD_TIMEOUT_MS) });
          if (!response.ok) throw new Error(`the download answered HTTP ${response.status}`);
          await fs.mkdir(this.context.globalStorageUri.fsPath, { recursive: true });
          await fs.writeFile(file.fsPath, Buffer.from(await response.arrayBuffer()));
          await vscode.commands.executeCommand("workbench.extensions.installExtension", file);
        },
      );
    } catch (error) {
      this.log.appendLine(`install ${version}: ${(error as Error).message}`);
      void vscode.window.showErrorMessage("Agent Bus: the update could not be installed. See the Agent Bus output for details.");
      return;
    } finally {
      await fs.rm(file.fsPath, { force: true });
    }
    const reload = "Reload Window";
    if ((await vscode.window.showInformationMessage(`Agent Bus ${version} is installed.`, reload)) === reload) {
      await vscode.commands.executeCommand("workbench.action.reloadWindow");
    }
  }
}
