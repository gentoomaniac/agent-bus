// Package channelapi is busd's channel requests (docs/design/agent-channels.md): an agent asks busd
// to list the channels, to create one, to join or leave one, and as a channel owner to invite,
// remove members and change the description. The agent signs each request with its own key, which
// busd checks against the directory; busd signs each reply with the CA key, bound to the request.
package channelapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// SubjectRequest starts the subjects of busd's channel endpoint in the BUS account. As with the
// directory sync, each agent asks on its own Subject, the only one its grants allow, so busd knows
// who asks and answers only into that agent's inbox space.
const SubjectRequest = "busd.channel"

// Subject is the subject the agent with this token sends its channel requests on.
func Subject(agentToken string) string { return SubjectRequest + "." + agentToken }

const (
	// MaxRequestBytes bounds what busd reads of a request; a real one is well below 1 KiB.
	MaxRequestBytes = 4 << 10
	// MaxDescriptionBytes bounds a channel's description: a line that says what it is for.
	MaxDescriptionBytes = 300
	// MaxListedChannels bounds a list reply: with names and descriptions at their limits it stays
	// below 200 KiB, well inside a NATS message.
	MaxListedChannels = 500
)

// The domains are prefixed to what is signed, so that a signature made for one kind of message
// never passes for another (directory.SignAsBusd).
const (
	requestDomain = "agent-bus/channel-request/v1\n"
	replyDomain   = "agent-bus/channel-reply/v1\n"
)

// The actions of a Request.
const (
	List         = "list"          // every channel, with its owner and description, and whether the caller reads it
	Members      = "members"       // one channel, with its members
	Create       = "create"        // Channel with Description; the caller owns it and reads it
	Join         = "join"          // the caller reads Channel from now on
	Leave        = "leave"         // the caller stops reading Channel
	Invite       = "invite"        // Agent reads Channel from now on; channel owner or bus owner only
	RemoveMember = "remove_member" // Agent stops reading Channel; channel owner or bus owner only
	Update       = "update"        // Channel gets Description; channel owner or bus owner only
)

// Actions are the actions busd answers.
var Actions = []string{List, Members, Create, Join, Leave, Invite, RemoveMember, Update}

// Request is what an agent asks busd. The challenge makes every request, and so the request hash
// its reply is bound to, unique.
type Request struct {
	AgentID     string `json:"agent_id"`
	Challenge   string `json:"challenge"`
	Action      string `json:"action"`
	Channel     string `json:"channel,omitempty"`
	Description string `json:"description,omitempty"`
	Agent       string `json:"agent,omitempty"` // whom an invite or remove_member is about
}

// signedRequest is a Request as it travels: Payload is exactly the bytes Signature covers, after
// requestDomain.
type signedRequest struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// Sign is the request as the agent sends it, signed with its key, with a new challenge.
func Sign(request Request, key ed25519.PrivateKey) ([]byte, error) {
	challenge := make([]byte, 16)
	if _, err := rand.Read(challenge); err != nil {
		panic(err) // the system's random source is broken
	}
	request.Challenge = base64.RawURLEncoding.EncodeToString(challenge)
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(signedRequest{Payload: payload, Signature: ed25519.Sign(key, append([]byte(requestDomain), payload...))})
}

// Open reads a request busd got on the subject of caller, whose directory record is given: it must
// be signed with the caller's current key and name the caller, and its action must be one busd
// answers.
func Open(message []byte, caller bus.AgentRecord) (Request, error) {
	var request Request
	if len(message) > MaxRequestBytes {
		return request, errors.New("the channel request is too long")
	}
	var signed signedRequest
	if err := strictDecode(message, &signed); err != nil {
		return request, fmt.Errorf("not a signed channel request: %w", err)
	}
	if caller.Removed() {
		return request, fmt.Errorf("%s was removed from the bus", caller.AgentID)
	}
	key, err := base64.StdEncoding.DecodeString(caller.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return request, fmt.Errorf("the directory record of %s has a malformed key", caller.AgentID)
	}
	if !ed25519.Verify(key, append([]byte(requestDomain), signed.Payload...), signed.Signature) {
		return request, fmt.Errorf("the channel request is not signed with the current key of %s", caller.AgentID)
	}
	if err := strictDecode(signed.Payload, &request); err != nil {
		return request, fmt.Errorf("not a channel request: %w", err)
	}
	switch {
	case request.AgentID != caller.AgentID:
		return request, fmt.Errorf("the channel request names %s, but came from %s", request.AgentID, caller.AgentID)
	case request.Challenge == "":
		return request, errors.New("the channel request has no challenge")
	case !slices.Contains(Actions, request.Action):
		return request, fmt.Errorf("unknown channel action %q, want one of %v", request.Action, Actions)
	}
	return request, nil
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

// Channel is a group channel as busd describes it. An empty Owner is the bus owner. A list leaves
// the members out and says only whether the caller is one (Joined); the other answers name them.
type Channel struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Owner       string   `json:"owner,omitempty"`
	Members     []string `json:"members,omitempty"`
	Joined      bool     `json:"joined,omitempty"`
}

// Limits are the rules a new channel lives under, with the values busd enforces as it answers. A
// limit that is off is null: there is no cleanup of idle channels yet, so those two always are,
// and an agent exempt from the creation limit (the bus owner) has none.
type Limits struct {
	MessageRetention    *string `json:"message_retention"` // such as "90d"
	StreamMaxBytes      *int64  `json:"stream_max_bytes"`  // of all channels together
	IdleWarningAfter    *string `json:"idle_warning_after"`
	IdleDeleteAfter     *string `json:"idle_delete_after"`
	Kept                bool    `json:"kept"`
	ChannelsLeftToday   *int    `json:"channels_left_today"` // in the 24 hours up to now
	CreationLimitPerDay *int    `json:"creation_limit_per_day"`
}

// Reply is busd's signed answer. Channel is the channel the request was about, as it is now
// (Channels instead for list, the first MaxListedChannels by name, Truncated when there are more),
// Limits come with a created channel, and Error says why busd refused.
type Reply struct {
	RequestHash string    `json:"request_hash"`
	Channel     *Channel  `json:"channel,omitempty"`
	Channels    []Channel `json:"channels,omitempty"`
	Truncated   bool      `json:"truncated,omitempty"`
	Limits      *Limits   `json:"limits,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// SignReply is the reply as busd sends it.
func SignReply(ca *tlsinit.CA, reply Reply) ([]byte, error) {
	return directory.SignAsBusd(ca, replyDomain, reply)
}

// Refusal is busd's signed no: it understood the request and refused it, for a limit or a missing
// ownership, say. Reason is busd's own words.
type Refusal struct{ Reason string }

func (refusal Refusal) Error() string { return refusal.Reason }

// VerifyReply reads busd's answer to request: signed by the CA in caPEM and bound to exactly this
// request. A refusal is returned as a Refusal.
func VerifyReply(caPEM, answer, request []byte) (Reply, error) {
	var reply Reply
	if err := directory.OpenFromBusd(caPEM, replyDomain, answer, &reply); err != nil {
		return Reply{}, err
	}
	if reply.RequestHash != directory.RequestHash(request) {
		return Reply{}, errors.New("busd's signed answer is for another request: a replay")
	}
	if reply.Error != "" {
		return Reply{}, Refusal{reply.Error}
	}
	return reply, nil
}
