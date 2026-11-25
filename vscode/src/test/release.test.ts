import assert from "node:assert/strict";
import { test } from "node:test";
import { isNewer, parseRelease, parseVersion } from "../release";

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
    { name: "agent-messenger.vsix", browser_download_url: "https://example/vsix" },
  ],
};

test("parseRelease takes the version, the page and the vsix asset", () => {
  assert.deepEqual(parseRelease(release), { version: "0.2.0", url: release.html_url, vsixUrl: "https://example/vsix" });
});

test("parseRelease of a release without the vsix has no vsixUrl", () => {
  assert.equal(parseRelease({ ...release, assets: [] })?.vsixUrl, undefined);
  assert.equal(parseRelease({ ...release, assets: undefined })?.vsixUrl, undefined);
});

test("parseRelease rejects drafts, prereleases and answers that are no release", () => {
  assert.equal(parseRelease({ ...release, draft: true }), undefined);
  assert.equal(parseRelease({ ...release, prerelease: true }), undefined);
  assert.equal(parseRelease({ ...release, tag_name: "nightly" }), undefined);
  assert.equal(parseRelease({ ...release, html_url: undefined }), undefined);
  assert.equal(parseRelease({ message: "Not Found" }), undefined);
  assert.equal(parseRelease(null), undefined);
  assert.equal(parseRelease("x"), undefined);
});
