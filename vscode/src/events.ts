import { Api, ApiError } from "./api";
import { SseEvent, SseParser } from "./sse";

export interface EventHandlers {
  // The stream opened: re-read the state now, since events missed while disconnected are gone.
  onOpen(): void | Promise<void>;
  onEvent(event: SseEvent): void;
  onUnauthenticated(): void;
  onDisconnect(): void;
}

export interface EventOptions {
  minDelayMs: number;
  maxDelayMs: number;
  // The monitor sends a keep-alive every 15 seconds; a stream silent for much longer is dead.
  silenceMs: number;
}

export const DEFAULT_OPTIONS: EventOptions = { minDelayMs: 1000, maxDelayMs: 30000, silenceMs: 45000 };

export function nextBackoff(previousMs: number, maxMs: number): number {
  return Math.min(previousMs * 2, maxMs);
}

// Keeps one connection to /api/v1/events until the signal aborts, reconnecting with a growing
// delay. A login that is no longer valid ends the loop.
export async function followEvents(api: Api, handlers: EventHandlers, signal: AbortSignal, options = DEFAULT_OPTIONS): Promise<void> {
  let delay = options.minDelayMs;
  while (!signal.aborted) {
    try {
      await readStream(api, handlers, signal, options, () => (delay = options.minDelayMs));
    } catch (error) {
      if (signal.aborted) return;
      if (error instanceof ApiError && error.status === 401) {
        handlers.onUnauthenticated();
        return;
      }
    }
    if (signal.aborted) return;
    handlers.onDisconnect();
    await sleep(delay, signal);
    delay = nextBackoff(delay, options.maxDelayMs);
  }
}

async function readStream(api: Api, handlers: EventHandlers, signal: AbortSignal, options: EventOptions, opened: () => void): Promise<void> {
  const attempt = new AbortController();
  const stop = () => attempt.abort();
  signal.addEventListener("abort", stop);
  let watchdog: NodeJS.Timeout | undefined;
  const feed = () => {
    clearTimeout(watchdog);
    watchdog = setTimeout(stop, options.silenceMs);
  };
  try {
    feed();
    const response = await api.fetch("/api/v1/events", { headers: { Accept: "text/event-stream" }, signal: attempt.signal });
    if (!response.body) throw new Error("the event stream has no body");
    opened();
    await handlers.onOpen();
    const parser = new SseParser();
    const decoder = new TextDecoder();
    for await (const chunk of response.body) {
      feed();
      for (const event of parser.push(decoder.decode(chunk as Uint8Array, { stream: true }))) handlers.onEvent(event);
    }
  } finally {
    clearTimeout(watchdog);
    signal.removeEventListener("abort", stop);
  }
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(done, ms);
    signal.addEventListener("abort", done, { once: true });
    function done() {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    }
  });
}
