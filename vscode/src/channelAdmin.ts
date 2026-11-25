import * as vscode from "vscode";
import { ApiError } from "./api";
import { channelNameProblem, groupName, invitableAgents } from "./channels";
import type { ChatPanels } from "./chat";
import { shortName } from "./names";
import { Session } from "./session";

// The bus owner's changes to group channels, made by the monitor as its agent through busd
// (docs/api.md, channel changes). busd decides whether that agent may; its refusal is shown as is.

// How long a new channel may take to be listed by the monitor: 10 × 500 ms.
const LISTED_ATTEMPTS = 10;
const LISTED_PAUSE_MS = 500;

function failed(session: Session, error: unknown): void {
  if (error instanceof ApiError && error.unauthenticated) session.report(error);
  else void vscode.window.showErrorMessage("Agent Messenger: " + (error as Error).message);
}

// Asks for an agent that does not read the channel yet and invites it; the agent is told by a DM.
export async function inviteAgent(session: Session, chats: ChatPanels, channelId: string): Promise<void> {
  const name = groupName(channelId);
  if (!name) return;
  const { model } = session;
  try {
    const candidates = invitableAgents(model.agents, await model.api.members(channelId), model.self);
    if (candidates.length === 0) {
      void vscode.window.showInformationMessage(`Every agent reads # ${name} already.`);
      return;
    }
    const picked = await vscode.window.showQuickPick(
      candidates.map((id) => ({ label: shortName(id), description: id, id })),
      { title: `Invite to # ${name}`, placeHolder: "The agent reads the channel from now on and is told so", matchOnDescription: true },
    );
    if (!picked) return;
    const changed = await model.api.changeChannel("invite", { channel: name, agent: picked.id });
    chats.showMembers(channelId, changed.members);
    if (changed.warning) void vscode.window.showWarningMessage("Agent Messenger: " + changed.warning);
  } catch (error) {
    failed(session, error);
  }
}

export async function removeMember(session: Session, chats: ChatPanels, channelId: string, agentId: string): Promise<void> {
  const name = groupName(channelId);
  if (!name) return;
  const remove = "Remove";
  const answer = await vscode.window.showWarningMessage(`Remove ${agentId} from # ${name}?`, { modal: true, detail: "It no longer reads the channel." }, remove);
  if (answer !== remove) return;
  try {
    const changed = await session.model.api.changeChannel("remove_member", { channel: name, agent: agentId });
    chats.showMembers(channelId, changed.members);
  } catch (error) {
    failed(session, error);
  }
}

// Asks for a name and a description, creates the channel and opens it. The monitor's agent owns it.
export async function createChannel(session: Session, chats: ChatPanels): Promise<void> {
  const name = await vscode.window.showInputBox({ title: "Create Channel", prompt: "Name", placeHolder: "agent-bus", validateInput: (value) => channelNameProblem(value) });
  if (!name) return;
  const description = await vscode.window.showInputBox({
    title: `Create # ${name}`, prompt: "What the channel is for", validateInput: (value) => (value.trim() ? undefined : "a channel needs a description"),
  });
  if (!description) return;
  try {
    await session.model.api.changeChannel("create", { channel: name, description });
    const id = "group:" + name;
    // The monitor lists the channel once it has read the catalogue again, a moment after busd's answer.
    for (let attempt = 0; attempt < LISTED_ATTEMPTS && !session.model.channels.some((channel) => channel.id === id); attempt++) {
      await new Promise((resolve) => setTimeout(resolve, LISTED_PAUSE_MS));
      await session.model.refreshState();
    }
    chats.open(id);
  } catch (error) {
    failed(session, error);
  }
}
