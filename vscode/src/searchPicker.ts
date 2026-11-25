import * as vscode from "vscode";
import { ApiError } from "./api";
import { ChatPanels } from "./chat";
import { hitPlace, plainLabel } from "./search";
import { Session } from "./session";
import { shortTime } from "./time";
import { SearchHit } from "./types";

const SEARCH_DELAY_MS = 250;

interface HitItem extends vscode.QuickPickItem {
  hit?: SearchHit; // the "Load more" row has none
  more?: true;
}

// "Agent Messenger: Search Messages": a quick input that searches all channels. A pick opens the
// message in its channel's panel. The query is the API's (docs/api.md, "Search"), so filters such as
// from: and type: are typed into it.
export function searchMessages(session: Session, chats: ChatPanels): void {
  if (!session.model.status) {
    void vscode.window.showInformationMessage("Agent Messenger: log in to search.");
    return;
  }
  const pick = vscode.window.createQuickPick<HitItem>();
  pick.placeholder = 'Search all channels: words, "phrases", from: type: targets: after: before:';
  let timer: ReturnType<typeof setTimeout> | undefined;
  let run = 0; // an answer to an older query is dropped
  let hits: SearchHit[] = [];
  let next: string | undefined;

  const search = async (append: boolean): Promise<void> => {
    clearTimeout(timer);
    const query = pick.value.trim();
    const thisRun = ++run;
    if (!query) {
      pick.items = [];
      pick.busy = false;
      return;
    }
    pick.busy = true;
    try {
      const page = await session.model.api.search(query, append ? next : undefined);
      if (thisRun !== run) return;
      hits = append ? [...hits, ...page.results] : page.results;
      next = page.next_cursor;
      const rows: HitItem[] = hits.map((hit) => ({
        label: plainLabel(hit.snippet.text),
        description: plainLabel(`${hitPlace(session.model.channels, hit.channel)} · ${hit.from ?? ""} · ${hit.time ? shortTime(hit.time, new Date()) : ""}`),
        detail: plainLabel([hit.type, hit.title].filter(Boolean).join(" · ")) || undefined,
        alwaysShow: true, // the monitor decided what matches, not the picker's filter
        hit,
      }));
      if (next) rows.push({ label: "Load more…", alwaysShow: true, more: true });
      else if (rows.length === 0) rows.push({ label: "No messages found.", alwaysShow: true });
      pick.items = rows;
    } catch (error) {
      if (thisRun !== run) return;
      if (error instanceof ApiError && error.unauthenticated) session.report(error);
      pick.items = [{ label: plainLabel("Search failed: " + (error as Error).message), alwaysShow: true }];
    } finally {
      if (thisRun === run) pick.busy = false;
    }
  };

  pick.onDidChangeValue(() => {
    clearTimeout(timer);
    timer = setTimeout(() => void search(false), SEARCH_DELAY_MS);
  });
  pick.onDidAccept(() => {
    const [chosen] = pick.selectedItems;
    if (chosen?.hit) {
      pick.hide();
      void chats.jumpTo(chosen.hit.channel, chosen.hit.sequence).catch((error) => session.report(error));
    } else if (chosen?.more) {
      void search(true);
    }
  });
  pick.onDidHide(() => {
    clearTimeout(timer);
    run++;
    pick.dispose();
  });
  pick.show();
}
