package review

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
)

type fakeApprover struct {
	signed   []string // "<ref> replace=<bool>"
	rejected []string // "<ref>: <reason>"
	err      error
}

func (fake *fakeApprover) List(context.Context) ([]enroll.Pending, error) { return nil, fake.err }

func (fake *fakeApprover) Sign(_ context.Context, ref string, replace bool) (string, error) {
	fake.signed = append(fake.signed, ref+" replace="+map[bool]string{true: "true", false: "false"}[replace])
	return "approved " + ref, fake.err
}

func (fake *fakeApprover) Reject(_ context.Context, ref, reason string) (string, error) {
	fake.rejected = append(fake.rejected, ref+": "+reason)
	return "rejected " + ref, fake.err
}

var now = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func request(id, agentID, hostname, identity string) enroll.Pending {
	return enroll.Pending{
		RequestID: id, Code: "CODE-" + strings.ToUpper(id), AgentID: agentID, Hostname: hostname,
		SigningFingerprint: "SHA256:sign-" + id, TLSFingerprint: "SHA256:tls-" + id,
		SubmittedAt: now.Add(-5 * time.Minute), ExpiresAt: now.Add(23 * time.Hour), Identity: identity,
	}
}

func newModel(approver Approver, pending ...enroll.Pending) Model {
	model := New(approver)
	model.now = func() time.Time { return now }
	return feed(model, listedMsg{pending: pending})
}

func feed(model Model, message tea.Msg) Model {
	updated, _ := model.Update(message)
	return updated.(Model)
}

func press(t *testing.T, model Model, keys ...tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	var command tea.Cmd
	for _, key := range keys {
		var updated tea.Model
		updated, command = model.Update(key)
		model = updated.(Model)
	}
	return model, command
}

func text(s string) []tea.KeyMsg {
	var keys []tea.KeyMsg
	for _, r := range s {
		if r == ' ' {
			keys = append(keys, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		keys = append(keys, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return keys
}

var (
	keyUp    = tea.KeyMsg{Type: tea.KeyUp}
	keyDown  = tea.KeyMsg{Type: tea.KeyDown}
	keyEnter = tea.KeyMsg{Type: tea.KeyEnter}
	keyEsc   = tea.KeyMsg{Type: tea.KeyEsc}
)

func key(s string) tea.KeyMsg { return text(s)[0] }

// finish runs the command a key press returned and feeds its result back, as the runtime does.
func finish(t *testing.T, model Model, command tea.Cmd) Model {
	t.Helper()
	if command == nil {
		t.Fatal("the key did nothing")
	}
	return feed(model, command())
}

func threeRequests(approver Approver) Model {
	return newModel(approver,
		request("a1", "lib-x@bob", "laptop", enroll.IdentityNew),
		request("b2", "api@bob", "server", enroll.IdentitySameKey),
		request("c3", "web@bob", "desktop", enroll.IdentityReplaces),
	)
}

func TestArrowsMoveTheSelectionWithinTheList(t *testing.T) {
	model := threeRequests(&fakeApprover{})
	if request, _, _ := model.current(); request.RequestID != "a1" {
		t.Fatalf("first selection %q", request.RequestID)
	}
	model, _ = press(t, model, keyUp)
	if request, _, _ := model.current(); request.RequestID != "a1" {
		t.Errorf("up from the first moved to %q", request.RequestID)
	}
	model, _ = press(t, model, keyDown, keyDown, keyDown)
	if request, _, _ := model.current(); request.RequestID != "c3" {
		t.Errorf("down past the last moved to %q", request.RequestID)
	}
	model, _ = press(t, model, key("k"))
	if request, _, _ := model.current(); request.RequestID != "b2" {
		t.Errorf("k moved to %q", request.RequestID)
	}
}

func TestApprovingSignsTheSelectedRequestAndRefreshes(t *testing.T) {
	approver := &fakeApprover{}
	model := threeRequests(approver)
	model, _ = press(t, model, keyDown)
	model, command := press(t, model, key("a"))
	model = finish(t, model, command)
	if len(approver.signed) != 1 || approver.signed[0] != "b2 replace=false" {
		t.Fatalf("signed %v", approver.signed)
	}
	if !strings.Contains(model.View(), "approved b2") {
		t.Errorf("the result is not shown:\n%s", model.View())
	}
}

func TestReplacingAnExistingIdentityNeedsAYes(t *testing.T) {
	approver := &fakeApprover{}
	model := threeRequests(approver)
	model, _ = press(t, model, keyDown, keyDown)

	model, command := press(t, model, key("a"))
	if command != nil || len(approver.signed) != 0 {
		t.Fatal("approving an identity that would be replaced went through without asking")
	}
	if !strings.Contains(model.View(), "Replace it and revoke the old key?") {
		t.Errorf("no confirmation asked:\n%s", model.View())
	}
	for _, no := range []tea.KeyMsg{key("n"), keyEnter, keyEsc, key("A")} {
		asked, command := press(t, model, no)
		if command != nil || len(approver.signed) != 0 {
			t.Fatalf("%v confirmed the replacement", no)
		}
		if asked.mode != browsing {
			t.Errorf("%v left the confirmation open", no)
		}
	}

	model, command = press(t, model, key("y"))
	model = finish(t, model, command)
	if len(approver.signed) != 1 || approver.signed[0] != "c3 replace=true" {
		t.Errorf("signed %v", approver.signed)
	}
}

func TestRejectingAsksForAReasonAndEscapeCancels(t *testing.T) {
	approver := &fakeApprover{}
	model := threeRequests(approver)
	model, _ = press(t, model, key("r"))
	if model.mode != askingReason {
		t.Fatal("r did not ask for a reason")
	}
	cancelled, command := press(t, model, append(text("oops"), keyEsc)...)
	if command != nil || cancelled.mode != browsing || len(approver.rejected) != 0 {
		t.Fatal("escape did not cancel the rejection")
	}

	model, command = press(t, model, append(text("not my host"), tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace}, keyEnter)...)
	model = finish(t, model, command)
	if len(approver.rejected) != 1 || approver.rejected[0] != "a1: not my ho" {
		t.Errorf("rejected %v", approver.rejected)
	}
}

func TestFilteringNarrowsTheListAndKeepsASelectableRequest(t *testing.T) {
	model := threeRequests(&fakeApprover{})
	model, _ = press(t, model, key("/"))
	model, _ = press(t, model, text("server")...)
	if shown := model.visible(); len(shown) != 1 || shown[0].RequestID != "b2" {
		t.Fatalf("visible %v", shown)
	}
	if request, _, ok := model.current(); !ok || request.RequestID != "b2" {
		t.Errorf("the selection is not on the visible request")
	}
	model, _ = press(t, model, keyEnter)
	if model.mode != browsing || model.filter != "server" {
		t.Errorf("enter did not keep the filter: %q", model.filter)
	}
	model, _ = press(t, model, key("/"))
	model, _ = press(t, model, keyEsc)
	if model.filter != "" || len(model.visible()) != 3 {
		t.Errorf("escape did not clear the filter")
	}
	model, _ = press(t, model, key("/"))
	model, _ = press(t, model, text("sha256:tls-c3")...)
	if shown := model.visible(); len(shown) != 1 || shown[0].RequestID != "c3" {
		t.Errorf("filtering by a fingerprint: %v", shown)
	}
	model, _ = press(t, model, key("a")) // typed into the filter, not an approval
	if len(model.visible()) != 0 {
		t.Error("a typed letter in the filter did something else")
	}
}

func TestARefreshKeepsTheSelectionOnTheSameRequest(t *testing.T) {
	model := threeRequests(&fakeApprover{})
	model, _ = press(t, model, keyDown)
	model = feed(model, listedMsg{pending: []enroll.Pending{
		request("z9", "new@bob", "x", enroll.IdentityNew),
		request("a1", "lib-x@bob", "laptop", enroll.IdentityNew),
		request("b2", "api@bob", "server", enroll.IdentitySameKey),
	}})
	if request, _, _ := model.current(); request.RequestID != "b2" {
		t.Errorf("the selection jumped to %q", request.RequestID)
	}
	model = feed(model, listedMsg{pending: []enroll.Pending{request("z9", "new@bob", "x", enroll.IdentityNew)}})
	if request, _, ok := model.current(); !ok || request.RequestID != "z9" {
		t.Errorf("after the selected request left: %v", request)
	}
	model = feed(model, listedMsg{})
	if _, _, ok := model.current(); ok || !strings.Contains(model.View(), "no pending requests") {
		t.Errorf("an empty queue:\n%s", model.View())
	}
	if _, command := press(t, model, key("a"), key("r")); command != nil {
		t.Error("a key acted on a request that is not there")
	}
}

func TestErrorsAreShownNotFatal(t *testing.T) {
	approver := &fakeApprover{err: errors.New("busd is not running")}
	model := threeRequests(approver)
	model, command := press(t, model, key("a"))
	model = finish(t, model, command)
	if !strings.Contains(model.View(), "failed: busd is not running") {
		t.Errorf("the failure is not shown:\n%s", model.View())
	}
	model = feed(model, listedMsg{err: errors.New("timeout")})
	if !strings.Contains(model.View(), "cannot read the queue: timeout") || len(model.pending) != 3 {
		t.Errorf("a failed refresh:\n%s", model.View())
	}
}

func TestQuitKeys(t *testing.T) {
	model := threeRequests(&fakeApprover{})
	for _, quit := range []tea.KeyMsg{key("q"), {Type: tea.KeyCtrlC}} {
		_, command := press(t, model, quit)
		if command == nil || command() != tea.Quit() {
			t.Errorf("%v does not quit", quit)
		}
	}
	// Typing a q into the reason is not a quit.
	model, _ = press(t, model, key("r"))
	if _, command := press(t, model, key("q")); command != nil {
		t.Error("q quit while a reason was being typed")
	}
}

func TestViewShowsWhatTheOwnerComparesAndWarnsAboutReplacement(t *testing.T) {
	model := threeRequests(&fakeApprover{})
	view := model.View()
	for _, want := range []string{"3 pending", "CODE-A1", "lib-x@bob", "laptop", "SHA256:sign-a1", "SHA256:tls-a1", "5m0s", "a new agent"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	model, _ = press(t, model, keyDown, keyDown)
	if view := model.View(); !strings.Contains(view, "ANOTHER signing key") || !strings.Contains(view, "SHA256:sign-c3") {
		t.Errorf("a replacing request is not flagged:\n%s", view)
	}
}
