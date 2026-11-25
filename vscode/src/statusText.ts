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

// The status bar item's text: what is wrong with the connection, then the unread mail for the
// monitor's agent. Empty when there is nothing to say.
export function statusBarText(connection: Connection, mail: number): string {
  return [connection.ok ? "" : `$(debug-disconnect) Agent bus: ${connection.text}`, mail > 0 ? `$(mail) ${mail}` : ""].filter(Boolean).join("  ");
}
