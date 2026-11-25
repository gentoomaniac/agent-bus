// OpenCode plugin for the agent bus (docs/opencode.md). It tells the session's `agent-bus mcp`
// what OpenCode is doing, so the owner's monitor shows whether the model works, how many workers
// it runs and whether something is wrong with it, and it delivers "you have mail" when the
// session goes idle and while it stays idle. It costs the model nothing and never throws into
// OpenCode.
//
// It speaks to the same commands as the Claude Code hooks: `agent-bus event --kind=<hook>` with a
// small JSON on stdin, and `agent-bus pending --format=plugin`.
//
// It is an OpenCode 2 plugin (`Plugin.define`); OpenCode 1's plugin API is not supported.
import { spawn } from "node:child_process";
import { Plugin } from "@opencode/plugin";

const AGENT_BUS = process.env.AGENT_BUS_BIN || "agent-bus";
const COMMAND_TIMEOUT_MS = 3000;
// How often an idle session is checked for mail aimed at it. A check is one local socket call.
const IDLE_POLL_MS = 15000;

// The tool that starts a subagent; presence knows it under Claude Code's name for it, "Agent".
const SUBAGENT_TOOL = "subagent";

const EVENT_TYPES = [
  "session.created",
  "session.deleted",
  "session.execution.started",
  "session.execution.succeeded",
  "session.execution.failed",
  "session.execution.interrupted",
  "session.retry.scheduled",
  "permission.asked",
  "permission.replied",
];

// run starts agent-bus, feeds it stdin and resolves with its stdout and whether it exited 0 in
// time. It never rejects.
function run(args, stdin = "") {
  return new Promise((resolve) => {
    let output = "";
    let child;
    try {
      child = spawn(AGENT_BUS, args, { stdio: ["pipe", "pipe", "ignore"] });
    } catch {
      return resolve({ output: "", ok: false });
    }
    const timer = setTimeout(() => child.kill(), COMMAND_TIMEOUT_MS);
    child.stdout.on("data", (chunk) => (output += chunk));
    child.on("error", () => resolve({ output: "", ok: false }));
    child.on("close", (code) => {
      clearTimeout(timer);
      resolve({ output, ok: code === 0 });
    });
    child.stdin.on("error", () => {});
    child.stdin.end(stdin);
  });
}

function report(kind, input = {}) {
  return run(["event", "--kind=" + kind], JSON.stringify(input));
}

// toolInput keeps what presence shows (a command's first word, a file's name), nothing else.
function toolInput(input) {
  const args = input && typeof input === "object" ? input : {};
  return {
    command: args.command,
    file_path: args.filePath ?? args.file_path ?? args.path,
    description: args.description,
  };
}

// errorKind maps a failed execution's error to the kinds Claude Code's StopFailure reports.
function errorKind(error) {
  if (error?.type === "provider.no-route") return "model_not_found";
  const status = error?.status;
  if (status === 429) return "rate_limit";
  if (status === 401 || status === 403) return "authentication_failed";
  if (status === 402) return "billing_error";
  if (status === 404) return "model_not_found";
  if (status >= 500) return "server_error";
  if (status >= 400) return "invalid_request";
  return "unknown";
}

export default Plugin.define({
  id: "agent-bus",
  async setup(ctx) {
    // Subagent sessions run inside a task of the main one: they are workers, and their idle and
    // errors are not the agent's.
    const childSessions = new Set();
    // The session that went idle last is checked for mail every IDLE_POLL_MS until it is busy
    // again, so mail that arrives after the idle wakes it. The adapter reports only mail aimed at
    // the agent, so chatter never prompts the model. One session is enough: all of them share the
    // agent's one inbox.
    let watcher = null;

    // Mail the session was prompted about but that could not be marked told. Only the mark is
    // retried for it: prompting again would wake the model for mail it already has.
    let unmarkedIds = [];

    async function markTold(ids) {
      const { ok } = await run(["pending", "--mark=" + ids.join(",")]);
      return ok;
    }

    // deliverMail prompts the session about new mail. The mail is marked told only after the prompt
    // was accepted, so a failed prompt is retried at the next check. It runs outside the event
    // loop, so one call at a time.
    let delivering = false;
    async function deliverMail(sessionID) {
      if (delivering) return;
      delivering = true;
      try {
        if (unmarkedIds.length > 0) {
          if (!(await markTold(unmarkedIds))) return;
          unmarkedIds = [];
        }
        const { output } = await run(["pending", "--format=plugin"]);
        if (!output.trim()) return;
        const { reason, ids } = JSON.parse(output);
        await ctx.session.prompt({ sessionID, text: reason });
        if (!(await markTold(ids))) unmarkedIds = ids;
      } catch {
        // the next check tries again; the mail is still in the inbox
      } finally {
        delivering = false;
      }
    }

    function stopWatchingIdle() {
      if (!watcher) return;
      watcher.stopped = true;
      clearTimeout(watcher.timer);
      watcher = null;
    }

    function watchWhileIdle(sessionID) {
      stopWatchingIdle();
      const own = { sessionID, stopped: false, timer: null };
      watcher = own;
      // One check at a time: the next is scheduled when the last one is done.
      const check = async () => {
        await deliverMail(sessionID);
        if (!own.stopped) schedule();
      };
      const schedule = () => {
        own.timer = setTimeout(check, IDLE_POLL_MS);
        own.timer.unref?.();
      };
      schedule();
    }

    // handle maps one OpenCode event to agent-bus calls.
    async function handle({ type, data }) {
      const sessionID = data?.sessionID;
      if (type === "session.created") {
        if (data.parentID) {
          childSessions.add(sessionID);
          await report("subagent-start", { agent_id: sessionID, agent_type: "task" });
        }
        return;
      }
      if (childSessions.has(sessionID)) {
        if (type === "session.deleted") childSessions.delete(sessionID);
        if (type.startsWith("session.execution.") && type !== "session.execution.started") {
          await report("subagent-stop", { agent_id: sessionID });
        }
        return;
      }
      switch (type) {
        case "session.execution.started":
          stopWatchingIdle();
          await report("user-prompt");
          break;
        case "session.execution.succeeded":
        case "session.execution.interrupted":
          await report("stop");
          deliverMail(sessionID);
          watchWhileIdle(sessionID);
          break;
        case "session.execution.failed":
          await report("stop-failure", { error: errorKind(data.error) });
          break;
        case "session.retry.scheduled":
          await report("stop-failure", { error: "retrying" });
          break;
        case "permission.asked":
          await report("notification", { notification_type: "permission_prompt" });
          break;
        case "permission.replied":
          await report("post-tool");
          break;
        case "session.deleted":
          if (watcher?.sessionID === sessionID) stopWatchingIdle();
          await report("session-end");
          break;
      }
    }

    const abort = new AbortController();
    (async () => {
      for await (const event of ctx.event.subscribe({ types: EVENT_TYPES, signal: abort.signal })) {
        await handle(event).catch(() => {});
      }
    })().catch(() => {});

    // Tool hooks do not wait for agent-bus: they run on every tool call, like Claude Code's async
    // hooks.
    await ctx.tool.hook("execute.before", (call) => {
      const tool = call.tool === SUBAGENT_TOOL ? "Agent" : call.tool;
      report("pre-tool", { tool_name: tool, tool_input: toolInput(call.input) });
    });
    await ctx.tool.hook("execute.after", (call) => {
      report("post-tool", { tool_name: call.tool });
    });

    return async () => {
      abort.abort();
      stopWatchingIdle();
      await report("session-end");
    };
  },
});
