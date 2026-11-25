import { Presence } from "./types";

// "12s", "5m" or "3h", as the web page says it.
export function ago(time: string | undefined, now: number): string {
  const seconds = Math.max(0, Math.round((now - Date.parse(time ?? "")) / 1000));
  if (Number.isNaN(seconds)) return "";
  if (seconds < 60) return seconds + "s";
  if (seconds < 3600) return Math.floor(seconds / 60) + "m";
  return Math.floor(seconds / 3600) + "h";
}

// "state · N workers · activity (since)" for an agent's row.
export function describePresence(view: Presence | undefined, now: number): string {
  const parts = [view?.state ?? "offline"];
  const workers = view?.workers?.length ?? 0;
  if (workers > 0) parts.push(workers + (workers === 1 ? " worker" : " workers"));
  if (view?.activity) {
    const since = ago(view.activity_since, now);
    parts.push(view.activity + (since ? ` (${since})` : ""));
  }
  return parts.join(" · ");
}

// The CSS class of an agent's dot, grouped like the tree's icons: every error:<kind> looks the
// same, and an unknown state, like no presence at all, is a hollow dot.
export function presenceClass(state: string | undefined): string {
  if (state === "working" || state === "idle" || state === "waiting_for_owner" || state === "rate_limited" || state === "unresponsive") return state;
  return state?.startsWith("error:") ? "error" : "offline";
}

// The recent activity, newest first, at most `limit` entries.
export function recentActivity(view: Presence | undefined, limit: number): { at: string; text: string }[] {
  return (view?.recent ?? []).slice(-limit).reverse();
}
