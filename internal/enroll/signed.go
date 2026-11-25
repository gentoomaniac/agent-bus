package enroll

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// directoryDomain is prefixed to what the CA key signs, so that no other signature of the CA
// (it signs certificates) can pass for a directory.
const directoryDomain = "agent-bus/directory/v1\n"

// DirectoryReply is busd's answer to SubjectDirectory. The shared enrollment login lets anyone
// who has it answer on an agent's inbox, so the directory is signed by the CA key, which only
// busd holds: Payload is the JSON of a directoryPayload, exactly the bytes Signature covers.
type DirectoryReply struct {
	Payload   []byte `json:"payload,omitempty"`
	Signature []byte `json:"signature,omitempty"`
	Error     string `json:"error,omitempty"`
}

// directoryPayload is what busd signs: the snapshot and when it was made.
type directoryPayload struct {
	Directory
	IssuedAt time.Time `json:"issued_at"`
}

// SignDirectory makes the reply that carries directory, made at issuedAt.
func SignDirectory(ca *tlsinit.CA, directory Directory, issuedAt time.Time) (DirectoryReply, error) {
	payload, err := json.Marshal(directoryPayload{Directory: directory, IssuedAt: issuedAt.UTC()})
	if err != nil {
		return DirectoryReply{}, err
	}
	signature, err := ca.SignData(append([]byte(directoryDomain), payload...))
	return DirectoryReply{Payload: payload, Signature: signature}, err
}

// Verify checks the reply against the CA certificate in caPEM and returns the directory with the
// time busd made it. It reads nothing from a reply that is unsigned or signed by another key.
func (reply DirectoryReply) Verify(caPEM []byte) (Directory, time.Time, error) {
	if len(reply.Payload) == 0 || len(reply.Signature) == 0 {
		return Directory{}, time.Time{}, errors.New("the directory is not signed")
	}
	if err := tlsinit.CheckDataSignature(caPEM, append([]byte(directoryDomain), reply.Payload...), reply.Signature); err != nil {
		return Directory{}, time.Time{}, fmt.Errorf("the directory is not signed by the CA: %w", err)
	}
	var payload directoryPayload
	decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return Directory{}, time.Time{}, fmt.Errorf("the signed directory is malformed: %w", err)
	}
	return payload.Directory, payload.IssuedAt, nil
}
