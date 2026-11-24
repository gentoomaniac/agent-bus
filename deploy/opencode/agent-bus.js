// OpenCode plugin for the agent bus (docs/opencode.md). It tells the session's `agent-bus mcp`
// what OpenCode is doing, so the owner's monitor shows whether the model works, how many workers
// it runs and whether something is wrong with it, and it delivers "you have mail" when the
// session goes idle and while it stays idle. It costs the model nothing and never throws into
// OpenCode.
//
// It speaks to the same commands as the Claude Code hooks: `agent-bus event --kind=<hook>` with a
// small JSON on stdin, and `agent-bus pending --format=hook`.
import { spawn } from "node:child_process";

const AGENT_BUS = process.env.AGENT_BUS_BIN || "agent-bus";
const COMMAND_TIMEOUT_MS = 3000;
// How often an idle session is checked for mail aimed at it. A check is one local socket call.
const IDLE_POLL_MS = 15000;

// run starts agent-bus, feeds it stdin and resolves with its stdout. It never rejects.
function run(args, stdin = "") {
  return new Promise((resolve) => {
    let output = "";
    let child;
    try {
      child = spawn(AGENT_BUS, args, { stdio: ["pipe", "pipe", "ignore"] });
    } catch {
      return resolve("");
    }
    const timer = setTimeout(() => child.kill(), COMMAND_TIMEOUT_MS);
    child.stdout.on("data", (chunk) => (output += chunk));
    child.on("error", () => resolve(""));
    child.on("close", () => {
      clearTimeout(timer);
      resolve(output);
    });
    child.stdin.on("error", () => {});
    child.stdin.end(stdin);
  });
}

function report(kind, input = {}) {
  return run(["event", "--kind=" + kind], JSON.stringify(input));
}

// toolInput keeps what presence shows (a command's first word, a file's name), nothing else.
function toolInput(args = {}) {
  return {
    command: args.command,
    file_path: args.filePath ?? args.file_path ?? args.path,
    description: args.description,
  };
}

// errorKind maps an OpenCode session error to the kinds Claude Code's StopFailure reports. null
// means the user stopped the turn: not an error.
function errorKind(error) {
  switch (error?.name) {
    case "MessageAbortedError":
      return null;
    case "ProviderAuthError":
      return "authentication_failed";
    case "MessageOutputLengthError":
      return "max_output_tokens";
    case "APIError": {
      const status = error.data?.statusCode;
      if (status === 429) return "rate_limit";
      if (status === 401 || status === 403) return "authentication_failed";
      if (status === 402) return "billing_error";
      if (status === 404) return "model_not_found";
      if (status >= 500) return "server_error";
      if (status >= 400) return "invalid_request";
      return "unknown";
    }
    default:
      return "unknown";
  }
}

async function server({ client }) {
  // Subagent sessions run inside a task of the main one: their idle and errors are not the agent's.
  const childSessions = new Set();
  // OpenCode goes idle after an error too; that idle must not hide the error or prompt a model
  // that just failed.
  const failedSessions = new Set();

  // deliverMail prompts the session about new mail. The mail is marked told only after the prompt
  // was accepted, so a failed prompt is retried at the next idle.
  async function deliverMail(sessionID) {
    const output = (await run(["pending", "--format=plugin"])).trim();
    if (!output) return;
    try {
      const { reason, ids } = JSON.parse(output);
      const result = await client.session.promptAsync({ path: { id: sessionID }, body: { parts: [{ type: "text", text: reason }] } });
      if (result?.error) return;
      await run(["pending", "--mark=" + ids.join(",")]);
    } catch {
      // the next idle tries again; the mail is still in the inbox
    }
  }

  // An idle session is checked for mail every IDLE_POLL_MS until it is busy again, so mail that
  // arrives after the idle event wakes it. The adapter reports only mail aimed at the agent, so
  // chatter never prompts the model.
  const idleWatchers = new Map();

  function stopWatchingIdle(sessionID) {
    const watcher = idleWatchers.get(sessionID);
    if (!watcher) return;
    watcher.stopped = true;
    clearTimeout(watcher.timer);
    idleWatchers.delete(sessionID);
  }

  function watchWhileIdle(sessionID) {
    stopWatchingIdle(sessionID);
    const watcher = { stopped: false, timer: null };
    // One check at a time: the next is scheduled when the last one is done.
    const check = async () => {
      await deliverMail(sessionID);
      if (!watcher.stopped) schedule();
    };
    const schedule = () => {
      watcher.timer = setTimeout(check, IDLE_POLL_MS);
      watcher.timer.unref?.();
    };
    idleWatchers.set(sessionID, watcher);
    schedule();
  }

  return {
    event: async ({ event }) => {
      const properties = event.properties ?? {};
      if (event.type === "session.created" || event.type === "session.updated") {
        if (properties.info?.parentID) childSessions.add(properties.info.id);
        return;
      }
      if (childSessions.has(properties.sessionID)) return;
      switch (event.type) {
        case "session.status":
          if (properties.status?.type === "busy") {
            stopWatchingIdle(properties.sessionID);
            failedSessions.delete(properties.sessionID);
            await report("user-prompt");
          }
          if (properties.status?.type === "retry") await report("stop-failure", { error: "retrying" });
          break;
        case "session.idle":
          if (failedSessions.delete(properties.sessionID)) break;
          await report("stop");
          await deliverMail(properties.sessionID);
          watchWhileIdle(properties.sessionID);
          break;
        case "session.error": {
          stopWatchingIdle(properties.sessionID);
          const kind = errorKind(properties.error);
          if (kind) failedSessions.add(properties.sessionID);
          await (kind ? report("stop-failure", { error: kind }) : report("stop"));
          break;
        }
        case "permission.updated":
          await report("notification", { notification_type: "permission_prompt" });
          break;
        case "permission.replied":
          await report("post-tool");
          break;
        case "session.deleted":
        case "server.instance.disposed":
          for (const sessionID of [...idleWatchers.keys()]) stopWatchingIdle(sessionID);
          await report("session-end");
          break;
      }
    },
    // Tool hooks do not wait for agent-bus: they run on every tool call, like Claude Code's async
    // hooks. A task call is a worker, told apart by its call ID; its events are chained so the
    // launch comes before the start, and the start before the stop.
    "tool.execute.before": async (input, output) => {
      const isTask = input.tool === "task";
      const launched = report("pre-tool", { tool_name: isTask ? "Agent" : input.tool, tool_input: toolInput(output.args) });
      if (isTask) launched.then(() => report("subagent-start", { agent_id: input.callID, agent_type: "task" }));
    },
    "tool.execute.after": async (input) => {
      const done = report("post-tool", { tool_name: input.tool });
      if (input.tool === "task") done.then(() => report("subagent-stop", { agent_id: input.callID }));
    },
  };
}

// The V1 plugin module form, which OpenCode 2 still loads: one default export, so the plugin
// is registered once.
export default { id: "agent-bus", server };
