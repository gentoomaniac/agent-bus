// The @name completion of the composer. The web page implements the same rules separately.

// The candidates (@all and agent IDs) that the query completes, in their order. The query is what
// was typed after the "@", in any case. It matches by prefix, either the whole ID or the name
// before the "@" of an agent ID ("lib" and "lib-x@al" both match "lib-x@alice"; "all" matches "@all").
export function matchTargets(candidates: string[], query: string, exclude: string[] = []): string[] {
  const wanted = query.replace(/^@/, "").toLowerCase();
  return candidates.filter((id) => {
    if (exclude.includes(id)) return false;
    const full = id.toLowerCase();
    const name = full.startsWith("@") ? full.slice(1) : full.split("@")[0];
    return full.startsWith(wanted) || name.startsWith(wanted);
  });
}

// The candidate that exactly the typed text names, ignoring case and a leading "@", as on the web page.
export function resolveTarget(candidates: string[], typed: string): string | undefined {
  const wanted = typed.trim().replace(/^@?/, "@").toLowerCase();
  return candidates.find((id) => id.replace(/^@?/, "@").toLowerCase() === wanted);
}

// The "@word" that ends right before the caret, if the "@" starts the text or follows whitespace.
// `start` is where the "@" is; `query` is the word after it.
export function mentionBefore(text: string, caret: number): { start: number; query: string } | undefined {
  const found = /(?:^|\s)@(\S*)$/.exec(text.slice(0, caret));
  if (!found) return undefined;
  const query = found[1];
  return { start: caret - query.length - 1, query };
}

// What replaces the "@word" when a candidate is picked: "@all" already has its "@", an agent ID has none.
export function mentionText(id: string): string {
  return (id.startsWith("@") ? id : "@" + id) + " ";
}
