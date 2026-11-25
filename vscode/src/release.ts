export const VSIX_ASSET = "agent-messenger.vsix";

// Where a download may come from: the release page and asset URL on github.com, and the hosts
// GitHub redirects asset downloads to.
const RELEASE_HOSTS = ["github.com"];
const DOWNLOAD_HOSTS = [...RELEASE_HOSTS, "objects.githubusercontent.com", "release-assets.githubusercontent.com"];
const MAX_REDIRECTS = 5;

export interface Release {
  version: string; // the tag without its "v"
  url: string | undefined; // the release page; absent when it is not on github.com
  vsixUrl: string | undefined; // absent when the release has no extension asset on github.com
}

// Whether the URL is https and its host is exactly one of the hosts.
export function isHttpsUrlOn(text: string, hosts: string[]): boolean {
  try {
    const url = new URL(text);
    return url.protocol === "https:" && hosts.includes(url.hostname);
  } catch {
    return false;
  }
}

// Fetches without letting the server send the request anywhere: every redirect hop is followed by
// hand, at most five, and only to the download hosts.
export async function fetchFromGithub(url: string, init: RequestInit = {}, fetchUrl: typeof fetch = fetch): Promise<Response> {
  let target = url;
  for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
    if (!isHttpsUrlOn(target, DOWNLOAD_HOSTS)) throw new Error(`refusing to fetch ${target}`);
    const response = await fetchUrl(target, { ...init, redirect: "manual" });
    const location = response.headers.get("location");
    if (response.status < 300 || response.status >= 400 || !location) return response;
    target = new URL(location, target).href;
  }
  throw new Error("too many redirects");
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
// answer that is no release with a version tag. URLs that are not https on github.com are dropped.
export function parseRelease(body: unknown): Release | undefined {
  if (typeof body !== "object" || body === null) return undefined;
  const release = body as { tag_name?: unknown; html_url?: unknown; draft?: unknown; prerelease?: unknown; assets?: unknown };
  if (release.draft === true || release.prerelease === true) return undefined;
  if (typeof release.tag_name !== "string") return undefined;
  if (!parseVersion(release.tag_name)) return undefined;
  const assets = Array.isArray(release.assets) ? (release.assets as { name?: unknown; browser_download_url?: unknown }[]) : [];
  const vsixUrl = assets.find((asset) => asset?.name === VSIX_ASSET)?.browser_download_url;
  return {
    version: release.tag_name.trim().replace(/^v/, ""),
    url: typeof release.html_url === "string" && isHttpsUrlOn(release.html_url, RELEASE_HOSTS) ? release.html_url : undefined,
    vsixUrl: typeof vsixUrl === "string" && isHttpsUrlOn(vsixUrl, RELEASE_HOSTS) ? vsixUrl : undefined,
  };
}
