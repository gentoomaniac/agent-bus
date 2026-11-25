import { ApiError } from "./api";
import { ReadStore } from "./model";
import { ServerPrefs } from "./types";

// The part of the monitor's API the prefs use (docs/api.md, "Shared preferences").
export interface PrefsApi {
  prefs(): Promise<ServerPrefs>;
  postRead(updates: Record<string, number>): Promise<Record<string, number>>; // answers the merged markers
  putFavourites(channels: string[]): Promise<unknown>;
  putCollapsed(sections: string[]): Promise<unknown>;
}

// What an older version of the extension kept in the editor's global state for this monitor.
export interface LegacyPrefs {
  read: Record<string, number>;
  favourites: string[];
  collapsed: string[];
}

// The owner's read markers, favourites and folded sections, shared with every client through the
// monitor. They are kept in memory too, so the extension still works against a monitor without
// prefs (an older one): nothing is sent then.
export class Prefs implements ReadStore {
  readonly favourites = new Set<string>();
  readonly collapsed = new Set<string>();
  private readonly read = new Map<string, number>();
  private readonly unsent = new Map<string, number>();
  private flushing: Promise<void> | undefined;
  private replacing: Promise<unknown> = Promise.resolve(); // favourites and collapsed go out in the order they were changed
  private replacements = 0; // changes not yet on the server: its echo must not undo them
  private online = false;

  constructor(private readonly api: PrefsApi, private readonly onChange: () => void, private readonly onError: (error: unknown) => void) {}

  get(channelId: string): number | undefined {
    return this.read.get(channelId);
  }

  // Raises the marker. Markers go out one request at a time; what is marked meanwhile is merged
  // (the highest sequence per channel) and sent when the request ends.
  set(channelId: string, sequence: number): void {
    if (sequence <= (this.read.get(channelId) ?? -1)) return;
    this.read.set(channelId, sequence);
    this.unsent.set(channelId, sequence);
    void this.flush();
  }

  // Resolves when everything marked so far has been sent (or the monitor has no prefs).
  flush(): Promise<void> {
    return (this.flushing ??= this.sendUnsent().finally(() => (this.flushing = undefined)));
  }

  private async sendUnsent(): Promise<void> {
    while (this.online && this.unsent.size > 0) {
      const sending = Object.fromEntries(this.unsent);
      this.unsent.clear();
      try {
        await this.api.postRead(sending);
      } catch (error) {
        for (const [channelId, sequence] of Object.entries(sending)) this.unsent.set(channelId, Math.max(sequence, this.unsent.get(channelId) ?? 0));
        this.onError(error);
        return; // the next marker tries again
      }
    }
  }

  setFavourite(channelId: string, favourite: boolean): void {
    if (this.favourites.has(channelId) === favourite) return;
    if (favourite) this.favourites.add(channelId);
    else this.favourites.delete(channelId);
    this.replace(() => this.api.putFavourites([...this.favourites]));
    this.onChange();
  }

  setCollapsed(section: string, collapsed: boolean): void {
    if (this.collapsed.has(section) === collapsed) return;
    if (collapsed) this.collapsed.add(section);
    else this.collapsed.delete(section);
    this.replace(() => this.api.putCollapsed([...this.collapsed]));
  }

  // The list is read when its turn comes, so the last change always wins.
  private replace(send: () => Promise<unknown>): void {
    this.replacements++;
    this.replacing = this.replacing
      .then(() => (this.online ? send() : undefined))
      .catch(this.onError)
      .finally(() => this.replacements--);
  }

  // Takes the monitor's prefs, from a fetch or a `prefs` event. Markers only move forward, as on
  // the monitor.
  apply(server: ServerPrefs): void {
    for (const [channelId, sequence] of Object.entries(server.read)) {
      if (sequence > (this.read.get(channelId) ?? -1)) this.read.set(channelId, sequence);
    }
    if (this.replacements === 0) {
      replaceAll(this.favourites, server.favourites);
      replaceAll(this.collapsed, server.collapsed);
    }
    this.onChange();
  }

  // Reads the prefs from the monitor, moving the legacy ones there once if it has none. Answers
  // whether the monitor holds the prefs now, so the legacy copy can go. A login that ended is
  // thrown for the caller to handle; any other failure leaves the prefs in memory.
  async load(legacy?: LegacyPrefs): Promise<boolean> {
    let server: ServerPrefs;
    try {
      server = await this.api.prefs();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) throw error;
      this.onError(error);
      return false;
    }
    this.online = true;
    if (legacy && isEmpty(server) && !isEmpty(legacy)) {
      try {
        if (Object.keys(legacy.read).length > 0) server.read = (await this.api.postRead(legacy.read));
        if (legacy.favourites.length > 0) await this.api.putFavourites(legacy.favourites);
        if (legacy.collapsed.length > 0) await this.api.putCollapsed(legacy.collapsed);
        server = { read: server.read, favourites: legacy.favourites, collapsed: legacy.collapsed };
      } catch (error) {
        this.onError(error);
        this.apply(server);
        return false;
      }
    }
    this.apply(server);
    void this.flush(); // markers made while offline or before the load
    return true;
  }
}

function replaceAll(set: Set<string>, members: string[]): void {
  set.clear();
  for (const member of members) set.add(member);
}

function isEmpty(prefs: LegacyPrefs | ServerPrefs): boolean {
  return Object.keys(prefs.read).length === 0 && prefs.favourites.length === 0 && prefs.collapsed.length === 0;
}
