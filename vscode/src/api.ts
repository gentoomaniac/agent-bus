import { Agent, Channel, Message, Presence, SendRequest, ServerPrefs, Status } from "./types";

export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) {
    super(message);
  }
  get unauthenticated(): boolean {
    return this.status === 401 && this.code === "unauthenticated";
  }
}

// The monitor's JSON API. The token is the session token of a login, sent as a Bearer header:
// that is what lets a client without cookies change state (docs/api.md, "Changing state").
export class Api {
  constructor(readonly baseUrl: string, private readonly token: string | undefined) {}

  // Opens a request for callers that read the body themselves (the event stream).
  async fetch(path: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers);
    if (this.token) headers.set("Authorization", "Bearer " + this.token);
    const response = await fetch(this.baseUrl + path, { ...init, headers });
    if (!response.ok) {
      const body = (await response.json().catch(() => ({}))) as { error?: string; code?: string };
      throw new ApiError(response.status, body.code ?? "", body.error ?? response.statusText);
    }
    return response;
  }

  private async json<T>(path: string, body?: unknown, method = body === undefined ? "GET" : "POST"): Promise<T> {
    const headers = body === undefined ? undefined : { "Content-Type": "application/json" };
    const response = await this.fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    return (response.status === 204 ? undefined : await response.json()) as T;
  }

  login(password: string): Promise<{ token: string }> {
    return this.json("/api/v1/login", { password });
  }
  async logout(): Promise<void> {
    await this.json("/api/v1/logout", {});
  }
  status(): Promise<Status> {
    return this.json("/api/v1/status");
  }
  channels(): Promise<Channel[]> {
    return this.json("/api/v1/channels");
  }
  agents(): Promise<Agent[]> {
    return this.json("/api/v1/agents");
  }
  presence(): Promise<Presence[]> {
    return this.json("/api/v1/presence");
  }
  messages(channel: string, after: number, limit: number): Promise<Message[]> {
    return this.json(`/api/v1/messages?channel=${encodeURIComponent(channel)}&after=${after}&limit=${limit}`);
  }
  send(request: SendRequest): Promise<{ id: string }> {
    return this.json("/api/v1/send", request);
  }
  prefs(): Promise<ServerPrefs> {
    return this.json("/api/v1/prefs");
  }
  postRead(updates: Record<string, number>): Promise<Record<string, number>> {
    return this.json("/api/v1/prefs/read", updates);
  }
  putFavourites(channels: string[]): Promise<ServerPrefs> {
    return this.json("/api/v1/prefs/favourites", channels, "PUT");
  }
  putCollapsed(sections: string[]): Promise<ServerPrefs> {
    return this.json("/api/v1/prefs/collapsed", sections, "PUT");
  }
}
