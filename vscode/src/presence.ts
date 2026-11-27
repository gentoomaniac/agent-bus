import { Context, Presence } from "./types";

// "12s", "5m" or "3h", as the web page says it.
export function ago(time: string | undefined, now: number): string {
  const seconds = Math.max(0, Math.round((now - Date.parse(time ?? "")) / 1000));
  if (Number.isNaN(seconds)) return "";
  if (seconds < 60) return seconds + "s";
  if (seconds < 3600) return Math.floor(seconds / 60) + "m";
  return Math.floor(seconds / 3600) + "h";
}

// A token count at a glance, as the web page says it: "850", "97k", "1.2M".
export function shortTokens(tokens: number): string {
  if (tokens < 1000) return String(tokens);
  if (tokens < 999_500) return Math.round(tokens / 1000) + "k"; // not "1000k"
  return (Math.round(tokens / 100_000) / 10) + "M";
}

// "context 97k" for a row; empty without a context.
export function contextShort(context: Context | undefined): string {
  return context ? "context " + shortTokens(context.tokens) : "";
}

// "96,761 tokens · claude-opus-5-5 · 20s ago" for a tooltip.
export function contextLong(context: Context, now: number): string {
  const since = ago(context.at, now);
  return [context.tokens.toLocaleString("en-US") + " tokens", context.model, since && since + " ago"].filter(Boolean).join(" · ");
}

// "state · context · N workers · activity (since)" for an agent's row.
export function describePresence(view: Presence | undefined, now: number): string {
  const parts = [view?.state ?? "offline"];
  if (view?.context) parts.push(contextShort(view.context));
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
