export interface Span {
  top: number;
  bottom: number;
}

// Whether a row counts as seen in a view: at least half of the row is visible, or the row covers at
// least half of the view, so that a message taller than the view counts too.
export function isSeen(row: Span, view: Span): boolean {
  const visible = Math.min(row.bottom, view.bottom) - Math.max(row.top, view.top);
  if (visible <= 0) return false;
  return visible >= (row.bottom - row.top) / 2 || visible >= (view.bottom - view.top) / 2;
}
