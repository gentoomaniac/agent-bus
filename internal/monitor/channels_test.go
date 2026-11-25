package monitor

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
)

func TestTheBusOwnerChangesChannelsThroughTheAPI(t *testing.T) {
	sender := &fakeSender{}
	server := serve(monitorWithSender(t, sender))
	defer server.Close()
	post := func(action, body string) (int, string) {
		response, err := http.Post(server.URL+"/api/v1/channels/"+action, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var reply bytes.Buffer
		reply.ReadFrom(response.Body)
		return response.StatusCode, reply.String()
	}

	for action, body := range map[string]string{
		"create":        `{"channel":"ops","description":"operations"}`,
		"invite":        `{"channel":"ops","agent":"lib-x@bob"}`,
		"remove_member": `{"channel":"ops","agent":"lib-x@bob"}`,
		"update":        `{"channel":"ops","description":"the operations team"}`,
	} {
		if status, reply := post(action, body); status != http.StatusOK || !strings.Contains(reply, `"name":"ops"`) {
			t.Errorf("%s: status %d, body %s", action, status, reply)
		}
	}
	if _, reply := post("remove_member", `{"channel":"ops","agent":"lib-x@bob"}`); !strings.Contains(reply, `"members":[]`) {
		t.Errorf("a channel without members: %s, want an empty members list", reply)
	}
	if !slices.Contains(sender.sent, "channel invite ops lib-x@bob ") || !slices.Contains(sender.sent, "channel update ops  the operations team") {
		t.Errorf("requests made: %q", sender.sent)
	}
	if status, _ := post("leave", `{"channel":"ops"}`); status != http.StatusBadRequest {
		t.Errorf("an action the interface does not offer: status %d", status)
	}
	sender.refuse = channelapi.Refusal{Reason: "only the owner of channel ops (owner@x) or the bus owner may invite"}
	if status, reply := post("invite", `{"channel":"ops","agent":"lib-x@bob"}`); status != http.StatusConflict || !strings.Contains(reply, "only the owner") {
		t.Errorf("busd's refusal: status %d, body %s", status, reply)
	}

	readOnly := serve(monitorWithSender(t, nil))
	defer readOnly.Close()
	response, err := http.Post(readOnly.URL+"/api/v1/channels/create", "application/json", bytes.NewBufferString(`{"channel":"x","description":"y"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Errorf("a read-only monitor: status %d", response.StatusCode)
	}
}
