package directory

import "git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"

// SubjectHead is where busd announces the current version in BUS: right after a change and then
// every few seconds. Agents and the monitor subscribe; only busd may publish.
const SubjectHead = "directory.head"

const headDomain = "agent-bus/directory-head/v1\n"

// MaxHeadChanges bounds the hints of one head; more are cut and Truncated is set.
const MaxHeadChanges = 20

// Head is the signed announcement of the current version. Its changes are hints about the last
// revision, for logs and the monitor: an agent never takes a record from a head, it syncs.
type Head struct {
	Version
	Changes   []Change `json:"changes,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// SignHead is the head as busd publishes it, with at most MaxHeadChanges hints.
func SignHead(ca *tlsinit.CA, version Version, changes []Change) ([]byte, error) {
	head := Head{Version: version, Changes: changes}
	if len(changes) > MaxHeadChanges {
		head.Changes, head.Truncated = changes[:MaxHeadChanges], true
	}
	return sign(ca, headDomain, head)
}

// VerifyHead reads a head signed by the CA in caPEM.
func VerifyHead(caPEM, message []byte) (Head, error) {
	var head Head
	err := open(caPEM, headDomain, message, &head)
	return head, err
}
