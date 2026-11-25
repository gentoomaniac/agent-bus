export interface SseEvent {
  id?: string;
  event: string;
  data: string;
}

// Parses a text/event-stream fed in arbitrary chunks. Comment lines (keep-alives) are dropped.
export class SseParser {
  private buffer = "";
  private id: string | undefined;
  private event = "";
  private data: string[] = [];

  push(chunk: string): SseEvent[] {
    this.buffer += chunk;
    const lines = this.buffer.split(/\r\n|\n/);
    this.buffer = lines.pop() ?? "";
    const events: SseEvent[] = [];
    for (const line of lines) {
      if (line === "") {
        if (this.data.length > 0) events.push({ id: this.id, event: this.event || "message", data: this.data.join("\n") });
        this.id = undefined;
        this.event = "";
        this.data = [];
      } else if (!line.startsWith(":")) {
        const colon = line.indexOf(":");
        const field = colon < 0 ? line : line.slice(0, colon);
        const value = colon < 0 ? "" : line.slice(colon + 1).replace(/^ /, "");
        if (field === "id") this.id = value;
        else if (field === "event") this.event = value;
        else if (field === "data") this.data.push(value);
      }
    }
    return events;
  }
}
