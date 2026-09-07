package colony_test

import (
	"encoding/json"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// Fields the server added between the 2026-09-07 and 2026-09-10 schema
// snapshots. Each is decoded both present and absent, because a field
// modelled with the wrong zero value reads as data the server never sent.

func TestPostHeldFields(t *testing.T) {
	var held colony.Post
	if err := json.Unmarshal([]byte(`{"id":"p1","held":true,"held_explanation":"awaiting review"}`), &held); err != nil {
		t.Fatal(err)
	}
	if !held.Held || held.HeldExplanation == nil || *held.HeldExplanation != "awaiting review" {
		t.Errorf("held fields not decoded: held=%v explanation=%v", held.Held, held.HeldExplanation)
	}

	var plain colony.Post
	if err := json.Unmarshal([]byte(`{"id":"p2"}`), &plain); err != nil {
		t.Fatal(err)
	}
	if plain.Held || plain.HeldExplanation != nil {
		t.Errorf("absent held fields must decode to the server's defaults (false, nil), got %v %v",
			plain.Held, plain.HeldExplanation)
	}
}

func TestNotificationMessageReferences(t *testing.T) {
	const conv, msg = "5fd54353-4a01-45ec-9133-13c75b48956a", "0e305ef8-392e-430f-9ee4-6c6869fccbdc"
	var dm colony.Notification
	body := `{"id":"n1","notification_type":"direct_message","conversation_id":"` + conv + `","message_id":"` + msg + `"}`
	if err := json.Unmarshal([]byte(body), &dm); err != nil {
		t.Fatal(err)
	}
	if dm.ConversationID == nil || *dm.ConversationID != conv || dm.MessageID == nil || *dm.MessageID != msg {
		t.Errorf("message references not decoded: %v %v", dm.ConversationID, dm.MessageID)
	}

	var reply colony.Notification
	if err := json.Unmarshal([]byte(`{"id":"n2","notification_type":"reply","conversation_id":null,"message_id":null}`), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ConversationID != nil || reply.MessageID != nil {
		t.Errorf("null message references must decode to nil, got %v %v", reply.ConversationID, reply.MessageID)
	}
}

// TestCursorFoundKeepsItsDefault is why CursorFound is a *bool. The server's
// default is true, so the three states must stay distinct: an explicit
// false, an explicit true, and a response that does not mention it at all.
func TestCursorFoundKeepsItsDefault(t *testing.T) {
	cases := []struct {
		body string
		want *bool
	}{
		{`{"messages":[],"has_more":false,"cursor_found":false}`, colony.Ptr(false)},
		{`{"messages":[],"has_more":false,"cursor_found":true}`, colony.Ptr(true)},
		{`{"messages":[],"has_more":false}`, nil},
	}
	for _, tc := range cases {
		var h colony.ConversationHistory
		if err := json.Unmarshal([]byte(tc.body), &h); err != nil {
			t.Fatal(err)
		}
		switch {
		case tc.want == nil && h.CursorFound != nil:
			t.Errorf("%s: an absent cursor_found must stay nil, got %v", tc.body, *h.CursorFound)
		case tc.want != nil && (h.CursorFound == nil || *h.CursorFound != *tc.want):
			t.Errorf("%s: want %v, got %v", tc.body, *tc.want, h.CursorFound)
		}
	}
}
