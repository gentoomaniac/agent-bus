package hook

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
)

// Hooks that `agent-bus event --kind` understands, named like the Claude Code hooks they report.
var Kinds = []string{
	"user-prompt", "stop", "pre-tool", "post-tool", "subagent-start", "subagent-stop", "stop-failure",
	"notification", "session-start", "session-end",
}

// subagentTool is the tool that launches a subagent.
const subagentTool = "Agent"

// hookInput is the part of a hook's JSON that presence reads. It never keeps file contents or
// whole commands.
type hookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Command      string `json:"command"`
		Description  string `json:"description"`
	} `json:"tool_input"`
	AgentID          string `json:"agent_id"`
	AgentType        string `json:"agent_type"`
	Error            string `json:"error"`
	NotificationType string `json:"notification_type"`
}

// EventFor turns the JSON a hook of the given kind received into a presence event. It says false
// for a hook that tells presence nothing. A hook input that cannot be read still gives the event
// its kind alone says.
func EventFor(kind string, input []byte) (presence.Event, bool) {
	var parsed hookInput
	_ = json.Unmarshal(input, &parsed) // a malformed input leaves the fields empty
	switch kind {
	case "user-prompt":
		return presence.Event{Kind: presence.EventPrompt}, true
	case "stop":
		return presence.Event{Kind: presence.EventStop}, true
	case "pre-tool":
		if parsed.ToolName == subagentTool {
			return presence.Event{Kind: presence.EventAgentCall, Detail: parsed.ToolInput.Description}, true
		}
		return presence.Event{Kind: presence.EventTool, Tool: parsed.ToolName, Detail: toolDetail(parsed)}, true
	case "post-tool":
		return presence.Event{Kind: presence.EventToolDone}, true
	case "subagent-start":
		return presence.Event{Kind: presence.EventWorkerStart, AgentID: parsed.AgentID, Detail: parsed.AgentType}, true
	case "subagent-stop":
		return presence.Event{Kind: presence.EventWorkerEnd, AgentID: parsed.AgentID}, true
	case "stop-failure":
		return presence.Event{Kind: presence.EventFailure, Detail: parsed.Error}, true
	case "notification":
		return notificationEvent(parsed.NotificationType)
	case "session-start":
		return presence.Event{Kind: presence.EventSessionStart}, true
	case "session-end":
		return presence.Event{Kind: presence.EventSessionEnd}, true
	}
	return presence.Event{}, false
}

// notificationEvent maps the notifications that say the model waits for the human or that a usage
// limit stopped or resumed it. The others (idle prompts, auth messages) say nothing new.
func notificationEvent(notificationType string) (presence.Event, bool) {
	switch notificationType {
	case "permission_prompt", "agent_needs_input", "elicitation_dialog", "elicitation_url_dialog":
		return presence.Event{Kind: presence.EventNeedsOwner}, true
	case "quota_auto_resume_fired":
		return presence.Event{Kind: presence.EventPrompt}, true
	case "quota_auto_resume_stale", "quota_auto_resume_disabled":
		return presence.Event{Kind: presence.EventFailure, Detail: "rate_limit"}, true
	}
	return presence.Event{}, false
}

// commandWrappers are words that only run the next command; the next command is what to show.
var commandWrappers = []string{"env", "sudo", "nohup", "time", "nice", "command", "exec"}

// toolDetail is what the monitor shows next to the tool name: the base name of the file it works
// on or the command word of its command. Leading VAR=value words and wrappers are skipped because
// they may carry secrets, and only plain name characters are kept.
func toolDetail(input hookInput) string {
	for _, word := range strings.Fields(input.ToolInput.Command) {
		if strings.Contains(word, "=") || slices.Contains(commandWrappers, filepath.Base(word)) {
			continue
		}
		return plainName(filepath.Base(word))
	}
	for _, path := range []string{input.ToolInput.FilePath, input.ToolInput.NotebookPath, input.ToolInput.Path} {
		if path != "" {
			return plainName(filepath.Base(path))
		}
	}
	return ""
}

// plainName keeps letters, digits, dot, underscore and hyphen.
func plainName(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return -1
	}, name)
}
