// The API client and the event loop against a small fake monitor.
import assert from "node:assert/strict";
import http from "node:http";
import { AddressInfo } from "node:net";
import { after, before, test } from "node:test";
import { Api, ApiError } from "../api";
import { followEvents } from "../events";
import { SseEvent } from "../sse";

let server: http.Server;
let baseUrl: string;
let streams = 0;
const requests: { method?: string; url?: string; authorization?: string; body: string }[] = [];

before(async () => {
  server = http.createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    requests.push({ method: req.method, url: req.url, authorization: req.headers.authorization, body });
    if (req.headers.authorization !== "Bearer good") {
      res.writeHead(401, { "Content-Type": "application/json" }).end(JSON.stringify({ error: "log in first", code: "unauthenticated" }));
    } else if (req.url === "/api/v1/events") {
      streams++;
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(": connected\n\n");
      res.end(`id: general#${streams}\nevent: message\ndata: {"sequence":${streams}}\n\n`);
    } else if (req.url === "/api/v1/logout") {
      res.writeHead(204).end();
    } else {
      res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ id: "m1" }));
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  baseUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

after(() => server.close());

test("requests carry the bearer token, and a send is a JSON POST", async () => {
  requests.length = 0;
  const api = new Api(baseUrl, "good");
  assert.deepEqual(await api.send({ to: { kind: "group", name: "general" }, type: "message", targets: ["@all"], text: "hi" }), { id: "m1" });
  await api.messages("dm:a:b", 7, 50);
  await api.rawMessage("group:ops", 9);
  await api.search("to:#ops deploy", "c 1");
  await api.logout();
  assert.deepEqual(requests.map((r) => [r.method, r.url, r.authorization]), [
    ["POST", "/api/v1/send", "Bearer good"],
    ["GET", "/api/v1/messages?channel=dm%3Aa%3Ab&after=7&limit=50", "Bearer good"],
    ["GET", "/api/v1/messages/raw?channel=group%3Aops&sequence=9", "Bearer good"],
    ["GET", "/api/v1/search?q=to%3A%23ops%20deploy&cursor=c%201", "Bearer good"],
    ["POST", "/api/v1/logout", "Bearer good"],
  ]);
  assert.deepEqual(JSON.parse(requests[0].body).to, { kind: "group", name: "general" });
});

test("a 401 becomes an ApiError that says the login is missing", async () => {
  await assert.rejects(new Api(baseUrl, "stale").channels(), (error: unknown) => error instanceof ApiError && error.unauthenticated);
});

test("followEvents reconnects, calls onOpen each time and delivers the events", async () => {
  streams = 0;
  const controller = new AbortController();
  const events: SseEvent[] = [];
  let opens = 0;
  await followEvents(new Api(baseUrl, "good"), {
    onOpen: () => { opens++; },
    onEvent: (event) => {
      events.push(event);
      if (events.length === 2) controller.abort();
    },
    onUnauthenticated: () => assert.fail("logged in"),
    onDisconnect: () => {},
    onError: (error) => assert.fail(String(error)),
  }, controller.signal, { minDelayMs: 5, maxDelayMs: 20, silenceMs: 1000 });
  assert.equal(opens, 2);
  assert.deepEqual(events.map((e) => e.id), ["general#1", "general#2"]);
});

test("followEvents stops and reports a login that is no longer valid", async () => {
  let unauthenticated = 0;
  await followEvents(new Api(baseUrl, "stale"), {
    onOpen: () => assert.fail("not open"),
    onEvent: () => assert.fail("no events"),
    onUnauthenticated: () => { unauthenticated++; },
    onDisconnect: () => {},
    onError: (error) => assert.fail(String(error)),
  }, new AbortController().signal, { minDelayMs: 5, maxDelayMs: 20, silenceMs: 1000 });
  assert.equal(unauthenticated, 1);
});
