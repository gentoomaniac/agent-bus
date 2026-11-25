package hook

import "testing"

func TestHarnessPIDWalksPastWrappers(t *testing.T) {
	// A fake /proc: PID -> name and parent.
	type process struct {
		name   string
		parent int
	}
	tree := map[int]process{
		10: {"claude", 1},
		20: {"sh", 10},
		30: {"env", 20},
		40: {"mise", 30},
		50: {"node", 20},
		60: {"sh", 1},
		70: {"bash", 999}, // the parent cannot be read
		80: {"sh", 0},
	}
	read := func(pid int) (string, int) { return tree[pid].name, tree[pid].parent }

	for _, test := range []struct {
		name  string
		start int
		want  int
	}{
		{"no wrapper", 10, 10},
		{"one wrapper", 20, 10},
		{"a chain of wrappers", 40, 10},
		{"the first non-wrapper wins", 50, 50},
		{"stops below PID 1", 60, 60},
		{"an unreadable parent is the harness", 70, 999},
		{"an unknown process", 12345, 12345},
		{"parent 0", 80, 80},
	} {
		if got := harnessPID(test.start, read); got != test.want {
			t.Errorf("%s: harnessPID(%d) = %d, want %d", test.name, test.start, got, test.want)
		}
	}
}
