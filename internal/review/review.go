// Package review is the owner's screen for enrollment requests, `agent-busctl ca review`
// (decision 41): a Bubble Tea model over busd's approval API. It decides nothing itself; busd
// checks every approval again.
package review

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
)

const (
	refreshEvery   = 2 * time.Second
	commandTimeout = time.Minute // an approval includes a confirmed reload of the broker
)

// Approver is busd's approval API (busd.Client).
type Approver interface {
	List(ctx context.Context) ([]enroll.Pending, error)
	Sign(ctx context.Context, ref string, replace bool) (string, error)
	Reject(ctx context.Context, ref, reason string) (string, error)
}

type mode int

const (
	browsing mode = iota
	filtering
	askingReason
	confirmingReplace
)

// Model is the screen: the pending requests, the selection and what the keys do right now.
type Model struct {
	approver Approver
	now      func() time.Time

	pending  []enroll.Pending
	selected string // request ID, so a refresh keeps the selection on the same request
	filter   string
	mode     mode
	asked    string // request ID a prompt was opened for: its answer applies to that request only
	input    string // the reason being typed
	notice   string // the result of the last action, or the last error
	width    int
}

// New is the screen for the approver's queue.
func New(approver Approver) Model {
	return Model{approver: approver, now: time.Now}
}

type listedMsg struct {
	pending []enroll.Pending
	err     error
}

type refreshMsg struct{}

type actionDoneMsg struct {
	message string
	err     error
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.listCmd(), refreshAfter()) }

func refreshAfter() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return refreshMsg{} })
}

func (m Model) listCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		pending, err := m.approver.List(ctx)
		return listedMsg{pending, err}
	}
}

func (m Model) signCmd(ref string, replace bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		message, err := m.approver.Sign(ctx, ref, replace)
		return actionDoneMsg{message, err}
	}
}

func (m Model) rejectCmd(ref, reason string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		message, err := m.approver.Reject(ctx, ref, reason)
		return actionDoneMsg{message, err}
	}
}

// visible is the pending requests that match the filter, in the order busd lists them.
func (m Model) visible() []enroll.Pending {
	if m.filter == "" {
		return m.pending
	}
	needle := strings.ToLower(m.filter)
	var shown []enroll.Pending
	for _, request := range m.pending {
		haystack := strings.ToLower(strings.Join([]string{request.AgentID, request.Hostname, request.Code, request.SigningFingerprint, request.TLSFingerprint}, " "))
		if strings.Contains(haystack, needle) {
			shown = append(shown, request)
		}
	}
	return shown
}

// asking is the request a prompt is about. A refresh can drop a request while its prompt is open;
// the selection then falls back to another one, which the answer must not reach.
func (m Model) asking() (request enroll.Pending, ok bool) {
	for _, candidate := range m.pending {
		if candidate.RequestID == m.asked {
			return candidate, true
		}
	}
	return enroll.Pending{}, false
}

// current is the selected request among the visible ones; without a match it is the first.
func (m Model) current() (request enroll.Pending, index int, ok bool) {
	shown := m.visible()
	for i, candidate := range shown {
		if candidate.RequestID == m.selected {
			return candidate, i, true
		}
	}
	if len(shown) == 0 {
		return enroll.Pending{}, 0, false
	}
	return shown[0], 0, true
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
	case refreshMsg:
		return m, tea.Batch(m.listCmd(), refreshAfter())
	case listedMsg:
		if message.err != nil {
			m.notice = "cannot read the queue: " + message.err.Error()
			return m, nil
		}
		m.pending = message.pending
		request, _, ok := m.current()
		if ok {
			m.selected = request.RequestID
		}
		if _, stillThere := m.asking(); (m.mode == askingReason || m.mode == confirmingReplace) && !stillThere {
			m.mode, m.notice = browsing, "the request is gone (decided elsewhere, or expired): nothing was sent"
		}
	case actionDoneMsg:
		m.notice = message.message
		if message.err != nil {
			m.notice = "failed: " + message.err.Error()
		}
		return m, m.listCmd()
	case tea.KeyMsg:
		return m.handleKey(message)
	}
	return m, nil
}

func (m Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.mode {
	case filtering:
		switch key.Type {
		case tea.KeyEnter:
			m.mode = browsing
		case tea.KeyEsc:
			m.filter, m.mode = "", browsing
		default:
			m.filter = edit(m.filter, key)
		}
		return m, nil
	case askingReason:
		return m.handleReasonKey(key)
	case confirmingReplace:
		m.mode = browsing
		if key.String() == "y" {
			return m, m.signCmd(m.asked, true)
		}
		m.notice = "not replaced"
		return m, nil
	}

	request, index, ok := m.current()
	shown := m.visible()
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		if ok && index > 0 {
			m.selected = shown[index-1].RequestID
		}
	case "down", "j":
		if ok && index < len(shown)-1 {
			m.selected = shown[index+1].RequestID
		}
	case "/":
		m.mode = filtering
	case "a":
		switch {
		case !ok:
		case request.Identity == enroll.IdentityReplaces:
			m.mode, m.asked = confirmingReplace, request.RequestID
		default:
			return m, m.signCmd(request.RequestID, false)
		}
	case "r":
		if ok {
			m.mode, m.asked, m.input = askingReason, request.RequestID, ""
		}
	}
	return m, nil
}

// edit applies a typed key to a line of text.
func edit(text string, key tea.KeyMsg) string {
	switch key.Type {
	case tea.KeyRunes:
		return text + string(key.Runes)
	case tea.KeySpace:
		return text + " "
	case tea.KeyBackspace:
		runes := []rune(text)
		return string(runes[:max(len(runes)-1, 0)])
	}
	return text
}

func (m Model) handleReasonKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.mode = browsing
	case tea.KeyEnter:
		m.mode = browsing
		return m, m.rejectCmd(m.asked, m.input)
	default:
		m.input = edit(m.input, key)
	}
	return m, nil
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	warningStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	dimStyle      = lipgloss.NewStyle().Faint(true)
)

func (m Model) View() string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n\n", titleStyle.Render(fmt.Sprintf("Enrollment requests: %d pending", len(m.pending))))

	shown := m.visible()
	selected, _, hasSelection := m.current()
	asked, _ := m.asking()
	if len(shown) == 0 {
		out.WriteString(dimStyle.Render("no pending requests") + "\n")
	}
	for _, request := range shown {
		line := fmt.Sprintf("%-10s %-28s %-20s %6s  %s", request.Code, request.AgentID, request.Hostname, age(m.now().Sub(request.SubmittedAt)), request.Identity)
		if hasSelection && request.RequestID == selected.RequestID {
			line = selectedStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		out.WriteString(line + "\n")
	}
	if hasSelection {
		out.WriteString("\n" + m.detail(selected))
	}

	out.WriteString("\n")
	switch m.mode {
	case filtering:
		fmt.Fprintf(&out, "filter: %s_   (enter keeps it, esc clears it)\n", m.filter)
	case askingReason:
		fmt.Fprintf(&out, "reject %s (%s), reason (told to the agent): %s_   (enter rejects, esc cancels)\n", asked.AgentID, asked.Code, m.input)
	case confirmingReplace:
		out.WriteString(warningStyle.Render(fmt.Sprintf("%s (%s) is registered with another key. Replace it and revoke the old key? [y/N]", asked.AgentID, asked.Code)) + "\n")
	default:
		if m.filter != "" {
			fmt.Fprintf(&out, "filter: %s\n", m.filter)
		}
		out.WriteString(dimStyle.Render("up/down select   a approve   r reject   / filter   q quit") + "\n")
	}
	if m.notice != "" {
		out.WriteString(m.notice + "\n")
	}
	return out.String()
}

func (m Model) detail(request enroll.Pending) string {
	meaning := map[string]string{
		enroll.IdentityNew:      "a new agent: approving registers it",
		enroll.IdentitySameKey:  "registered with this signing key: approving only switches it to a certificate",
		enroll.IdentityReplaces: warningStyle.Render("registered with ANOTHER signing key: approving replaces it and revokes the old key"),
	}[request.Identity]
	if meaning == "" {
		meaning = request.Identity
	}
	return fmt.Sprintf("agent        %s\nhostname     %s\ncode         %s   (the agent machine printed the same)\nsigning key  %s\nTLS key      %s\nsubmitted    %s ago, expires in %s\nidentity     %s\n",
		request.AgentID, request.Hostname, request.Code, request.SigningFingerprint, request.TLSFingerprint,
		age(m.now().Sub(request.SubmittedAt)), age(request.ExpiresAt.Sub(m.now())), meaning)
}

func age(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	return duration.Round(time.Second).String()
}
