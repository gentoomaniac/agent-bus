package directory

import (
	"fmt"
	"testing"
)

func TestAHeadVerifiesAsAHeadOnly(t *testing.T) {
	ca, caPEM := newCA(t)
	_, otherPEM := newCA(t)
	version := Version{Epoch: "e1", Revision: 4, StateHash: "h"}
	signed, err := SignHead(ca, version, []Change{{Kind: Joined, AgentID: "carol@x"}})
	if err != nil {
		t.Fatal(err)
	}

	head, err := VerifyHead(caPEM, signed)
	if err != nil || head.Version != version || len(head.Changes) != 1 || head.Truncated {
		t.Errorf("head = %+v, %v", head, err)
	}
	if _, err := VerifyHead(otherPEM, signed); err == nil {
		t.Error("a head verified against another CA")
	}
	request := NewSyncRequest(Version{})
	reply, _ := SignSyncReply(ca, SyncReply{RequestHash: RequestHash(request), Kind: Unchanged})
	if _, err := VerifyHead(caPEM, reply); err == nil {
		t.Error("a sync reply passed for a head")
	}
	if _, err := VerifySyncReply(caPEM, signed, request); err == nil {
		t.Error("a head passed for a sync reply")
	}
}

func TestAHeadCarriesABoundedNumberOfHints(t *testing.T) {
	ca, caPEM := newCA(t)
	var changes []Change
	for i := range MaxHeadChanges + 5 {
		changes = append(changes, Change{Kind: Joined, AgentID: fmt.Sprintf("a%d@x", i)})
	}
	signed, _ := SignHead(ca, Version{Epoch: "e", Revision: 1, StateHash: "h"}, changes)

	head, err := VerifyHead(caPEM, signed)

	if err != nil || len(head.Changes) != MaxHeadChanges || !head.Truncated {
		t.Errorf("%d hints, truncated %v, %v", len(head.Changes), head.Truncated, err)
	}
}
