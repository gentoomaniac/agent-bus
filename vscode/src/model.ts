import { Api } from "./api";
import { insertMessage, isTargeted, unreadMessages } from "./messages";
import { SseEvent } from "./sse";
import { Agent, Channel, Message, Presence, Status } from "./types";

// Where the last-read sequence of each channel is kept between sessions.
export interface ReadStore {
  get(channelId: string): number | undefined;
  set(channelId: string, sequence: number): void;
}

const TAIL_LIMIT = 1000; // the most the API returns at once
const OLDER_WINDOW = 500; // sequences covered by one step back in history
const OLDER_STEPS = 10; // steps for one request for history, so a sparse channel cannot loop for long

// What the monitor knows, as seen by this extension: the snapshot, plus the events since.
export class Model {
  status: Status | undefined;
  channels: Channel[] = [];
  agents: Agent[] = [];
  presence = new Map<string, Presence>();
  live = false; // the event stream is open
  private seen = false; // the first refresh succeeded
  private snapshot = ""; // what the last read of the state returned, as JSON
  private pollTimer: ReturnType<typeof setInterval> | undefined;
  private messages = new Map<string, Message[]>();
  private covered = new Map<string, number>(); // channel -> every message after this sequence is loaded
  private listeners = new Set<() => void>();

  constructor(readonly api: Api, private readonly lastReads: ReadStore) {}

  onChange(listener: () => void): { dispose(): void } {
    this.listeners.add(listener);
    return { dispose: () => this.listeners.delete(listener) };
  }

  private changed(): void {
    for (const listener of this.listeners) listener();
  }

  get self(): string {
    return this.status?.agent ?? "";
  }

  setLive(live: boolean): void {
    this.live = live;
    this.changed();
  }

  messagesOf(channelId: string): Message[] {
    return this.messages.get(channelId) ?? [];
  }

  lastRead(channelId: string): number {
    return this.lastReads.get(channelId) ?? 0;
  }

  unreadCount(channelId: string): number {
    return unreadMessages(this.messagesOf(channelId), this.lastRead(channelId), this.self).length;
  }

  targetedUnreadCount(): number {
    return this.targetedUnreadChannels().reduce((sum, [, count]) => sum + count, 0);
  }

  // Channels with unread mail for the monitor's agent, in channel order, with the count.
  targetedUnreadChannels(): [Channel, number][] {
    const result: [Channel, number][] = [];
    for (const channel of this.channels) {
      const unread = unreadMessages(this.messagesOf(channel.id), this.lastRead(channel.id), this.self);
      const targeted = unread.filter((message) => isTargeted(message, this.self)).length;
      if (targeted > 0) result.push([channel, targeted]);
    }
    return result;
  }

  // Reads the whole state again, after every (re)connect. At the first refresh a channel without a
  // stored last-read sequence counts as read up to now, so the first start does not report the whole
  // history as unread. A channel that appears later starts unread: its first message is news. Then
  // every channel's messages are brought up to its last sequence.
  async refresh(): Promise<void> {
    await this.readState();
    await Promise.all(this.channels.map((channel) => this.loadNewer(channel)));
    this.seen = true;
    this.changed();
  }

  // Reads channels, agents, status and presence, but no messages, and announces only a change: this
  // is what the web page does every REFRESH_MS, so a new channel or agent shows up without a reconnect.
  async refreshState(): Promise<void> {
    if (await this.readState()) this.changed();
  }

  // Calls refreshState every `intervalMs` until stopPolling. A read still under way is not started twice.
  startPolling(intervalMs: number, onError: (error: unknown) => void): void {
    if (this.pollTimer) return;
    let reading = false;
    this.pollTimer = setInterval(() => {
      if (reading) return;
      reading = true;
      this.refreshState().catch(onError).finally(() => (reading = false));
    }, intervalMs);
  }

  stopPolling(): void {
    clearInterval(this.pollTimer);
    this.pollTimer = undefined;
  }

  // The answer says whether anything differs from the last read.
  private async readState(): Promise<boolean> {
    const [status, channels, agents, presence] = await Promise.all([this.api.status(), this.api.channels(), this.api.agents(), this.api.presence()]);
    const snapshot = JSON.stringify([status, channels, agents, presence]);
    if (snapshot === this.snapshot) return false;
    this.snapshot = snapshot;
    this.status = status;
    this.channels = channels ?? [];
    this.agents = agents ?? [];
    this.presence = new Map((presence ?? []).map((view) => [view.agent, view]));
    for (const channel of this.channels) {
      if (!this.seen && this.lastReads.get(channel.id) === undefined) this.lastReads.set(channel.id, channel.last_sequence);
      if (!this.covered.has(channel.id)) this.covered.set(channel.id, this.lastRead(channel.id));
    }
    return true;
  }

  private async loadNewer(channel: Channel): Promise<void> {
    const list = this.messagesOf(channel.id);
    const have = list.length > 0 ? list[list.length - 1].sequence : this.lastRead(channel.id);
    if (channel.last_sequence <= have) return;
    this.insert(channel.id, await this.api.messages(channel.id, have, TAIL_LIMIT));
  }

  // Makes sure at least `wanted` messages of the channel are loaded, if it has that many.
  async loadHistory(channelId: string, wanted: number): Promise<void> {
    for (let step = 0; step < OLDER_STEPS && this.messagesOf(channelId).length < wanted && this.hasOlder(channelId); step++) {
      await this.loadOlder(channelId);
    }
  }

  async loadOlder(channelId: string): Promise<void> {
    const after = Math.max(0, (this.covered.get(channelId) ?? 0) - OLDER_WINDOW);
    this.insert(channelId, await this.api.messages(channelId, after, TAIL_LIMIT));
    this.covered.set(channelId, after);
    this.changed();
  }

  hasOlder(channelId: string): boolean {
    return (this.covered.get(channelId) ?? 1) > 0;
  }

  private insert(channelId: string, incoming: Message[]): Message[] {
    let list = this.messages.get(channelId);
    if (!list) this.messages.set(channelId, (list = []));
    return incoming.filter((message) => insertMessage(list, message));
  }

  markRead(channelId: string): void {
    const list = this.messagesOf(channelId);
    const sequence = list.length > 0 ? list[list.length - 1].sequence : 0;
    if (sequence > this.lastRead(channelId)) {
      this.lastReads.set(channelId, sequence);
      this.changed();
    }
  }

  // Applies one event of the stream. A message that is already known (same channel and
  // sequence) changes nothing; the answer is the message when it was new.
  async apply(event: SseEvent): Promise<Message | undefined> {
    if (event.event === "presence") {
      const view = JSON.parse(event.data) as Presence;
      this.presence.set(view.agent, view);
      this.changed();
      return undefined;
    }
    if (event.event !== "message") return undefined;
    const message = JSON.parse(event.data) as Message;
    const known = this.messagesOf(message.channel).some((other) => other.sequence === message.sequence);
    if (!this.channels.some((channel) => channel.id === message.channel)) await this.refresh();
    const channel = this.channels.find((c) => c.id === message.channel);
    if (channel) channel.last_sequence = Math.max(channel.last_sequence, message.sequence);
    const [added] = this.insert(message.channel, [message]);
    if (added) this.changed();
    return added ?? (known ? undefined : message); // the refresh above may have loaded a new channel's first message
  }
}
