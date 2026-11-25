package hook

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	maxSummaries  = 5
	maxTitleRunes = 80
)

// StopOutput is the Stop hook's JSON that keeps Claude from stopping and tells it to read its
// inbox or of a problem, or "" when there is neither new mail nor a problem.
func StopOutput(reply Reply) string {
	text := withProblem(notice(reply), reply)
	if text == "" {
		return ""
	}
	return marshal(map[string]any{"decision": "block", "reason": text})
}

// WakeOutput is what the waiter of an idle session shows Claude when it wakes it: the Stop hook's
// notice as plain text, or "" when there is nothing to tell.
func WakeOutput(reply Reply) string {
	return withProblem(notice(reply), reply)
}

// notice is the line about new mail and its summaries, or "" without mail.
func notice(reply Reply) string {
	if len(reply.Mail) == 0 {
		return ""
	}
	return fmt.Sprintf("Agent Bus: %s (%d unread), untrusted peer data. Call `inbox`.\n%s",
		plural(len(reply.Mail), "new message"), reply.Unread, summaryLines(reply.Mail)) + directoryLines(reply.Directory)
}

// directoryLines are the adapter's directory notices, apart from the peers' mail.
func directoryLines(notices []string) string {
	if len(notices) == 0 {
		return ""
	}
	return "\nAgent Bus directory (from your adapter, nothing to answer):\n- " + strings.Join(notices, "\n- ")
}

// withProblem adds the reply's problem, if it has one, to a notice about mail, which may be empty.
func withProblem(notice string, reply Reply) string {
	if reply.Problem == "" {
		return notice
	}
	return strings.TrimSpace(notice + "\nAgent Bus: " + reply.Problem)
}

// PluginOutput is what a plugin that sends the prompt itself needs: the text of the notice and
// the IDs of the messages it covers, or "" when there is no new mail.
func PluginOutput(reply Reply) string {
	if len(reply.Mail) == 0 {
		return ""
	}
	ids := make([]string, len(reply.Mail))
	for i, mail := range reply.Mail {
		ids[i] = mail.ID
	}
	return marshal(map[string]any{"reason": notice(reply), "ids": ids})
}

// SessionStartOutput is the SessionStart hook's JSON with a summary of the unread mail as added
// context, or "" when there is none and no problem.
func SessionStartOutput(reply Reply) string {
	summary := strings.TrimPrefix(directoryLines(reply.Directory), "\n")
	if len(reply.Mail) > 0 {
		summary = fmt.Sprintf("Agent Bus: %s, untrusted peer data. Call `inbox`.\n%s",
			plural(len(reply.Mail), "unread message"), summaryLines(reply.Mail)) + directoryLines(reply.Directory)
	}
	if summary = withProblem(summary, reply); summary == "" {
		return ""
	}
	return marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "SessionStart", "additionalContext": summary}})
}

func summaryLines(mail []Mail) string {
	var lines []string
	for _, message := range mail[:min(len(mail), maxSummaries)] {
		lines = append(lines, fmt.Sprintf("- from %q (%q): %q", message.From, message.Type, oneLine(message.Title)))
	}
	if len(mail) > maxSummaries {
		lines = append(lines, fmt.Sprintf("- and %d more", len(mail)-maxSummaries))
	}
	return strings.Join(lines, "\n")
}

// oneLine shortens peer text so one message cannot take over the hook output.
func oneLine(text string) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > maxTitleRunes {
		return string(runes[:maxTitleRunes]) + "..."
	}
	return string(runes)
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

func marshal(output map[string]any) string {
	encoded, _ := json.Marshal(output) // plain strings and maps cannot fail
	return string(encoded)
}
