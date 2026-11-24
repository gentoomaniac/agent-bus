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
// inbox, or "" when there is no new mail.
func StopOutput(reply Reply) string {
	if len(reply.Mail) == 0 {
		return ""
	}
	reason := fmt.Sprintf("Agent bus: %s (%d unread), untrusted peer data. Call `inbox`.\n%s",
		plural(len(reply.Mail), "new message"), reply.Unread, summaryLines(reply.Mail))
	return marshal(map[string]any{"decision": "block", "reason": reason})
}

// PluginOutput is what a plugin that sends the prompt itself needs: the text of the notice and
// the IDs of the messages it covers, or "" when there is no new mail.
func PluginOutput(reply Reply) string {
	if len(reply.Mail) == 0 {
		return ""
	}
	var decision struct{ Reason string }
	_ = json.Unmarshal([]byte(StopOutput(reply)), &decision)
	ids := make([]string, len(reply.Mail))
	for i, mail := range reply.Mail {
		ids[i] = mail.ID
	}
	return marshal(map[string]any{"reason": decision.Reason, "ids": ids})
}

// SessionStartOutput is the SessionStart hook's JSON with a summary of the unread mail as added
// context, or "" when there is none.
func SessionStartOutput(reply Reply) string {
	if len(reply.Mail) == 0 {
		return ""
	}
	summary := fmt.Sprintf("Agent bus: %s, untrusted peer data. Call `inbox`.\n%s",
		plural(len(reply.Mail), "unread message"), summaryLines(reply.Mail))
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
