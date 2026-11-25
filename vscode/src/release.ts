export const VSIX_ASSET = "agent-messenger.vsix";

export interface Release {
  version: string; // the tag without its "v"
  url: string; // the release page
  vsixUrl: string | undefined; // absent when the release has no extension asset
}

// "1.2.3" or "v1.2.3" as [1, 2, 3]. A suffix after the patch ("-rc1", "+build") is ignored, as
// the release workflow packs v0.2.0-rc1 as 0.2.0. Undefined for anything else.
export function parseVersion(text: string): [number, number, number] | undefined {
  const match = /^v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/.exec(text.trim());
  return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : undefined;
}

export function isNewer(candidate: string, current: string): boolean {
  const a = parseVersion(candidate);
  const b = parseVersion(current);
  if (!a || !b) return false;
  for (let i = 0; i < 3; i++) if (a[i] !== b[i]) return a[i] > b[i];
  return false;
}

// The release from GitHub's "latest release" response. Undefined for a draft, a prerelease or an
// answer that is no release with a version tag and a page.
export function parseRelease(body: unknown): Release | undefined {
  if (typeof body !== "object" || body === null) return undefined;
  const release = body as { tag_name?: unknown; html_url?: unknown; draft?: unknown; prerelease?: unknown; assets?: unknown };
  if (release.draft === true || release.prerelease === true) return undefined;
  if (typeof release.tag_name !== "string" || typeof release.html_url !== "string") return undefined;
  if (!parseVersion(release.tag_name)) return undefined;
  const assets = Array.isArray(release.assets) ? (release.assets as { name?: unknown; browser_download_url?: unknown }[]) : [];
  const vsix = assets.find((asset) => asset?.name === VSIX_ASSET && typeof asset.browser_download_url === "string");
  return {
    version: release.tag_name.trim().replace(/^v/, ""),
    url: release.html_url,
    vsixUrl: vsix?.browser_download_url as string | undefined,
  };
}
