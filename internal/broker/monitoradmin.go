package broker

import (
	"context"
	"encoding/json"
	"errors"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
)

// BusdReader is an Admin whose registry and catalogue come from busd instead of CONTROL's
// JetStream: the monitor's CONTROL login has no JetStream access, because a raw read grant lets
// its holder redirect the answer onto a subject that writes (docs/design/access-control.md). The
// rest of the Admin, the pending count included, works as before.
type BusdReader struct {
	*Admin
	requester *Requester
}

// NewBusdReader asks busd over its own connection as conn, which must be a login allowed to
// subscribe to enroll.MonitorInboxPrefix and publish the monitor subjects.
func NewBusdReader(admin *Admin, conn Connection) (*BusdReader, error) {
	requester, err := NewRequester(conn, enroll.MonitorInboxPrefix)
	if err != nil {
		return nil, err
	}
	return &BusdReader{Admin: admin, requester: requester}, nil
}

// Close ends the connection to busd; the Admin is closed by its owner.
func (reader *BusdReader) Close() { reader.requester.Close() }

func (reader *BusdReader) Agents(ctx context.Context) ([]bus.AgentRecord, error) {
	var reply enroll.RegistryReply
	if err := reader.ask(ctx, enroll.SubjectMonitorRegistry, &reply); err != nil {
		return nil, err
	}
	return reply.Agents, nil
}

func (reader *BusdReader) GroupChannels(ctx context.Context) ([]bus.TopicRecord, error) {
	var reply enroll.CatalogueReply
	if err := reader.ask(ctx, enroll.SubjectMonitorCatalogue, &reply); err != nil {
		return nil, err
	}
	return reply.Groups, nil
}

// ask sends an empty request and decodes the reply, which carries a failure of busd's own in its
// Error field.
func (reader *BusdReader) ask(ctx context.Context, subject string, reply any) error {
	answer, err := reader.requester.Request(ctx, subject, nil)
	if err != nil {
		return err
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := errors.Join(json.Unmarshal(answer, reply), json.Unmarshal(answer, &failure)); err != nil {
		return err
	}
	if failure.Error != "" {
		return errors.New(failure.Error)
	}
	return nil
}
