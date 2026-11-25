import { isTargeted } from "./messages";
import { Message } from "./types";

export type NotifyMode = "targetedAndSubscribed" | "subscribedOnly" | "off";
export const DEFAULT_NOTIFY_MODE: NotifyMode = "targetedAndSubscribed";
export const COALESCE_MS = 5000;
const SUMMARY_LENGTH = 80;

export function parseNotifyMode(value: unknown): NotifyMode {
  return value === "subscribedOnly" || value === "off" ? value : DEFAULT_NOTIFY_MODE;
}

// Why a live message notifies, if it does. A subscription wins over being addressed: muting it is
// then an unsubscribe. Without an agent only subscriptions count, and own messages never do.
export function notifyReason(message: Message, self: string, subscribed: ReadonlySet<string>, mode: NotifyMode): "subscribed" | "targeted" | undefined {
  if (mode === "off" || (self !== "" && message.from === self)) return undefined;
  if (subscribed.has(message.channel)) return "subscribed";
  if (mode === "subscribedOnly" || self === "") return undefined;
  const directToSelf = message.channel.startsWith("dm:") && message.to === self;
  return directToSelf || isTargeted(message, self) ? "targeted" : undefined;
}

// "<sender> in <channel>: <title or first line of the text>", the part after the colon cut to 80 characters.
export function describeNotification(message: Message, channelLabel: string): string {
  const firstLine = (message.text ?? "").split("\n").find((line) => line.trim() !== "") ?? "";
  const summary = Array.from((message.title?.trim() || firstLine).trim());
  const cut = summary.length > SUMMARY_LENGTH ? summary.slice(0, SUMMARY_LENGTH - 1).join("") + "…" : summary.join("");
  return `${message.from ?? "unknown"} in ${channelLabel}: ${cut}`;
}

// Lets the first item through at once and holds the ones that follow within the window; they come
// out together when it ends, which opens the next window.
export class Coalescer<T> {
  private held: T[] = [];
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(private readonly emit: (items: T[]) => void, private readonly windowMs = COALESCE_MS) {}

  offer(item: T): void {
    if (this.timer !== undefined) {
      this.held.push(item);
      return;
    }
    this.emit([item]);
    this.openWindow();
  }

  private openWindow(): void {
    this.timer = setTimeout(() => {
      this.timer = undefined;
      const items = this.held;
      this.held = [];
      if (items.length === 0) return;
      this.emit(items);
      this.openWindow();
    }, this.windowMs);
  }

  dispose(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    this.held = [];
  }
}
