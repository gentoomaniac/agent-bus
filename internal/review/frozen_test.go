package review

import (
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
)

// without returns the queue minus one request, as a refresh brings it when that one was decided
// elsewhere or expired.
func without(model Model, id string) listedMsg {
	var rest []enroll.Pending
	for _, request := range model.pending {
		if request.RequestID != id {
			rest = append(rest, request)
		}
	}
	return listedMsg{pending: rest}
}

func TestAReplaceConfirmationActsOnlyOnTheRequestItWasOpenedFor(t *testing.T) {
	approver := &fakeApprover{}
	model := threeRequests(approver)
	model, _ = press(t, model, keyDown, keyDown, key("a")) // c3 replaces another key
	if model.mode != confirmingReplace {
		t.Fatal("no confirmation")
	}

	// c3 disappears; the selection falls back to a1. A yes must not sign a1 with replace.
	model = feed(model, without(model, "c3"))
	if model.mode != browsing || !strings.Contains(model.notice, "gone") {
		t.Errorf("the prompt stayed open for a request that is gone: mode %v, notice %q", model.mode, model.notice)
	}
	model, command := press(t, model, key("y"))
	if command != nil || len(approver.signed) != 0 {
		t.Errorf("a yes after the request vanished signed %v", approver.signed)
	}
}

func TestAReplaceConfirmationKeepsItsRequestWhileTheListChangesAround(t *testing.T) {
	approver := &fakeApprover{}
	model := threeRequests(approver)
	model, _ = press(t, model, keyDown, keyDown, key("a"))
	model = feed(model, without(model, "a1")) // another request goes; c3 stays
	if model.mode != confirmingReplace {
		t.Fatalf("the prompt was cancelled although its request is still there: %q", model.notice)
	}
	model, command := press(t, model, key("y"))
	finish(t, model, command)
	if len(approver.signed) != 1 || approver.signed[0] != "c3 replace=true" {
		t.Errorf("signed %v", approver.signed)
	}
}

func TestARejectionActsOnlyOnTheRequestItWasOpenedFor(t *testing.T) {
	approver := &fakeApprover{}
	model := threeRequests(approver)
	model, _ = press(t, model, key("r")) // a1
	model, _ = press(t, model, text("no")...)

	model = feed(model, without(model, "a1"))
	if model.mode != browsing {
		t.Fatal("the reason prompt stayed open for a request that is gone")
	}
	if _, command := press(t, model, keyEnter); command != nil || len(approver.rejected) != 0 {
		t.Errorf("an enter after the request vanished rejected %v", approver.rejected)
	}

	// The same when the selection moves while the reason is open: the answer is for a1 still.
	model = threeRequests(approver)
	model, _ = press(t, model, key("r"))
	model = feed(model, listedMsg{pending: append([]enroll.Pending{request("z0", "new@bob", "x", enroll.IdentityNew)}, model.pending...)})
	model, command := press(t, model, append(text("no"), keyEnter)...)
	finish(t, model, command)
	if len(approver.rejected) != 1 || approver.rejected[0] != "a1: no" {
		t.Errorf("rejected %v", approver.rejected)
	}
}
