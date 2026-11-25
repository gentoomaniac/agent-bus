package review

import (
	"errors"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
)

func TestTextFromTheNetworkNeverReachesTheTerminalAsControlCharacters(t *testing.T) {
	hostile := request("a1", "lib-x@bob", "evil\x1b[2J\x1b[Hhost", enroll.IdentityNew)
	hostile.Identity = "unknown: registry\nsaid \x1b]0;pwned\a"
	hostile.Code = "CODE\r-A1"
	model := newModel(&fakeApprover{}, hostile)
	model = feed(model, actionDoneMsg{message: "approved\x1b[31m", err: nil})
	for _, view := range []string{
		model.View(),
		feed(model, actionDoneMsg{err: errors.New("busd said \x1b[2Jno")}).View(),
		feed(model, listedMsg{err: errors.New("registry: \x1b[2Jdown")}).View(),
	} {
		if strings.ContainsAny(view, "\x1b\x07\r\x00") { // lipgloss adds no styling without a terminal
			t.Errorf("the view carries a control sequence from the network: %q", view)
		}
	}
	// What the owner needs is still there, and the request is still named by its ID.
	if view := model.View(); !strings.Contains(view, "evil[2J[Hhost") || !strings.Contains(view, "lib-x@bob") {
		t.Errorf("the request is not shown:\n%s", view)
	}
	approver := &fakeApprover{}
	model = newModel(approver, hostile)
	_, command := press(t, model, key("a"))
	finish(t, model, command)
	if len(approver.signed) != 1 || approver.signed[0] != "a1 replace=false" {
		t.Errorf("signed %v", approver.signed)
	}
}
