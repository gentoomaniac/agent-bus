import { Presence } from "./types";

// "12s", "5m" or "3h", as the web page says it.
export function ago(time: string | undefined, now: number): string {
  const seconds = Math.max(0, Math.round((now - Date.parse(time ?? "")) / 1000));
  if (Number.isNaN(seconds)) return "";
  if (seconds < 60) return seconds + "s";
  if (seconds < 3600) return Math.floor(seconds / 60) + "m";
  return Math.floor(seconds / 3600) + "h";
}

// "state · activity (since)" for an agent's row.
export function describePresence(view: Presence | undefined, now: number): string {
  const state = view?.state ?? "offline";
  if (!view?.activity) return state;
  const since = ago(view.activity_since, now);
  return `${state} · ${view.activity}` + (since ? ` (${since})` : "");
}

// The recent activity, newest first, at most `limit` entries.
export function recentActivity(view: Presence | undefined, limit: number): { at: string; text: string }[] {
  return (view?.recent ?? []).slice(-limit).reverse();
}
