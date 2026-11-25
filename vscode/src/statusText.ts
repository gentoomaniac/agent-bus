import { Status } from "./types";

export interface Connection {
  ok: boolean;
  text: string;
}

// The web page's "live" indicator: this extension's event stream and the monitor's own connection
// to the bus. A status that is not read yet counts as a bus that is up.
export function describeConnection(live: boolean, status: Pick<Status, "bus"> | undefined): Connection {
  if (!live) return { ok: false, text: "reconnecting" };
  if (status && status.bus !== "live") return { ok: false, text: "bus disconnected, reconnecting" };
  return { ok: true, text: "live" };
}

// What the composer area says: who the panel sends as, or why it cannot send.
export function composerNote(status: Pick<Status, "agent" | "can_send" | "note">, canWrite: boolean): string {
  if (!status.can_send) return status.note || "Read-only: the monitor has no identity.";
  if (!canWrite) return "Read-only: this conversation is between other agents.";
  return "Sending as " + status.agent;
}

// "2 pending enrollments", empty for none. The monitor never approves: that is done in the terminal.
export function describePending(status: Pick<Status, "pending_enrollments"> | undefined): string {
  const count = status?.pending_enrollments ?? 0;
  return count > 0 ? `${count} pending enrollment${count === 1 ? "" : "s"}` : "";
}

export const APPROVE_HINT = "Approve with agent-busctl ca review";

// The status bar item's text: what is wrong with the connection, the unread mail for the monitor's
// agent and the agents waiting for approval. Empty when there is nothing to say.
export function statusBarText(connection: Connection, mail: number, pending: number): string {
  return [
    connection.ok ? "" : `$(debug-disconnect) Agent bus: ${connection.text}`,
    mail > 0 ? `$(mail) ${mail}` : "",
    pending > 0 ? `$(person-add) ${pending}` : "",
  ].filter(Boolean).join("  ");
}
