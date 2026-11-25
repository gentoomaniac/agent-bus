import assert from "node:assert/strict";
import { test } from "node:test";
import { fetchFromGithub, isNewer, parseRelease, parseVersion } from "../release";

test("parseVersion reads major.minor.patch, with or without v, and ignores a suffix", () => {
  assert.deepEqual(parseVersion("1.2.3"), [1, 2, 3]);
  assert.deepEqual(parseVersion("v0.10.0"), [0, 10, 0]);
  assert.deepEqual(parseVersion("v0.2.0-rc1"), [0, 2, 0]);
  assert.equal(parseVersion("1.2"), undefined);
  assert.equal(parseVersion("latest"), undefined);
  assert.equal(parseVersion(""), undefined);
});

test("isNewer compares the numbers, not the text", () => {
  assert.equal(isNewer("0.2.0", "0.1.0"), true);
  assert.equal(isNewer("0.10.0", "0.9.0"), true);
  assert.equal(isNewer("1.0.0", "0.99.99"), true);
  assert.equal(isNewer("0.1.1", "0.1.0"), true);
  assert.equal(isNewer("0.1.0", "0.1.0"), false);
  assert.equal(isNewer("0.1.0", "0.2.0"), false);
  assert.equal(isNewer("garbage", "0.1.0"), false);
  assert.equal(isNewer("0.2.0", "garbage"), false);
});

const release = {
  tag_name: "v0.2.0",
  html_url: "https://github.com/gentoomaniac/agent-bus/releases/tag/v0.2.0",
  draft: false,
  prerelease: false,
  assets: [
    { name: "agent-messenger_0.2.0_linux_amd64.tar.gz", browser_download_url: "https://example/tar" },
    { name: "agent-messenger.vsix", browser_download_url: "https://github.com/gentoomaniac/agent-bus/releases/download/v0.2.0/agent-messenger.vsix" },
  ],
};

test("parseRelease takes the version, the page and the vsix asset", () => {
  assert.deepEqual(parseRelease(release), { version: "0.2.0", url: release.html_url, vsixUrl: release.assets[1].browser_download_url });
});

test("parseRelease of a release without the vsix has no vsixUrl", () => {
  assert.equal(parseRelease({ ...release, assets: [] })?.vsixUrl, undefined);
  assert.equal(parseRelease({ ...release, assets: undefined })?.vsixUrl, undefined);
});

test("parseRelease rejects drafts, prereleases and answers that are no release", () => {
  assert.equal(parseRelease({ ...release, draft: true }), undefined);
  assert.equal(parseRelease({ ...release, prerelease: true }), undefined);
  assert.equal(parseRelease({ ...release, tag_name: "nightly" }), undefined);
  assert.equal(parseRelease({ message: "Not Found" }), undefined);
  assert.equal(parseRelease(null), undefined);
  assert.equal(parseRelease("x"), undefined);
});

test("parseRelease drops an unusable release page", () => {
  assert.equal(parseRelease({ ...release, html_url: undefined })?.url, undefined);
  assert.equal(parseRelease({ ...release, html_url: "https://evil.example/release" })?.url, undefined);
});

test("parseRelease keeps the vsix URL only when it is https on github.com", () => {
  const vsixUrlOf = (url: string) =>
    parseRelease({ ...release, assets: [{ name: "agent-messenger.vsix", browser_download_url: url }] })?.vsixUrl;
  const good = "https://github.com/gentoomaniac/agent-bus/releases/download/v0.2.0/agent-messenger.vsix";
  assert.equal(vsixUrlOf(good), good);
  assert.equal(vsixUrlOf("http://github.com/x/agent-messenger.vsix"), undefined);
  assert.equal(vsixUrlOf("https://example.com/agent-messenger.vsix"), undefined);
  assert.equal(vsixUrlOf("https://github.com.evil.com/agent-messenger.vsix"), undefined);
  assert.equal(vsixUrlOf("https://evilgithub.com/agent-messenger.vsix"), undefined);
  assert.equal(vsixUrlOf("not a url"), undefined);
});

// A fake fetch answering from a table of URL to [status, location or body].
const fakeFetch = (answers: Record<string, [number, string]>, seen: string[] = []): typeof fetch =>
  (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    seen.push(url);
    assert.equal(init?.redirect, "manual");
    const [status, text] = answers[url] ?? [404, ""];
    return status >= 300 && status < 400 ? new Response(null, { status, headers: { location: text } }) : new Response(text, { status });
  }) as typeof fetch;

test("fetchFromGithub follows redirects to the download hosts", async () => {
  const response = await fetchFromGithub(
    "https://github.com/a/b/releases/download/v1/x.vsix",
    {},
    fakeFetch({
      "https://github.com/a/b/releases/download/v1/x.vsix": [302, "https://release-assets.githubusercontent.com/x"],
      "https://release-assets.githubusercontent.com/x": [200, "vsix"],
    }),
  );
  assert.equal(await response.text(), "vsix");
});

test("fetchFromGithub refuses a redirect to another host, and does not request it", async () => {
  const seen: string[] = [];
  const fetchUrl = fakeFetch({ "https://github.com/x": [302, "https://evil.example/x"] }, seen);
  await assert.rejects(fetchFromGithub("https://github.com/x", {}, fetchUrl), /refusing to fetch/);
  assert.deepEqual(seen, ["https://github.com/x"]);
});

test("fetchFromGithub refuses a redirect to http and a start URL that is not allowed", async () => {
  const fetchUrl = fakeFetch({ "https://github.com/x": [301, "http://github.com/y"] });
  await assert.rejects(fetchFromGithub("https://github.com/x", {}, fetchUrl), /refusing to fetch/);
  await assert.rejects(fetchFromGithub("https://github.com.evil.com/x", {}, fetchUrl), /refusing to fetch/);
});

test("fetchFromGithub gives up after five redirects", async () => {
  const fetchUrl = fakeFetch({ "https://github.com/loop": [302, "https://github.com/loop"] });
  await assert.rejects(fetchFromGithub("https://github.com/loop", {}, fetchUrl), /too many redirects/);
});
