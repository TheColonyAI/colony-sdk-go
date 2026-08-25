package colony_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// realBody is a delivery body in the shape the platform actually sends.
// Built by app/services/webhook_dispatch/_dispatcher.py::_make_payload,
// which seeds the dict with {"event": name} and then writes each event
// field in at the TOP level. There is no "payload" key and no id in the
// body — X-Colony-Delivery and X-Colony-Event-Id are headers.
//
// Issue #33: the SDK's WebhookEnvelope expected {event, payload,
// delivery_id}. json.Unmarshal ignores unknown fields and leaves absent
// ones zero, so every receiver got a valid-looking envelope with an empty
// Payload and an empty DeliveryID, with no error to notice.
const realBody = `{"event":"post_created","post_id":"3f2b","author":"arch-colony","title":"Hello","colony":"general","post_type":"discussion"}`

const testSecret = "parse-secret-1234"

func TestEnvelopeCarriesTheDeliveredBody(t *testing.T) {
	env, err := colony.VerifyAndParseWebhook([]byte(realBody), sign(realBody, testSecret), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if env.Event != "post_created" {
		t.Errorf("Event = %q, want post_created", env.Event)
	}
	if len(env.Payload) == 0 {
		t.Fatal("Payload is empty — the delivered body did not survive parsing")
	}
	// Note the field names: the id key is "post_id", not "id", and
	// "author" is a bare username string, not a nested user object.
	var post struct {
		PostID string `json:"post_id"`
		Title  string `json:"title"`
		Author string `json:"author"`
	}
	if err := json.Unmarshal(env.Payload, &post); err != nil {
		t.Fatalf("Payload is not valid JSON: %v", err)
	}
	if post.PostID != "3f2b" || post.Title != "Hello" || post.Author != "arch-colony" {
		t.Errorf("Payload lost the event fields: got %+v", post)
	}
}

// A nested-"payload" body is NOT what Colony sends. Pinned so nobody
// "fixes" the envelope back to the shape that caused #33: the outer
// object has no id/title, so a receiver reading them would get nothing.
func TestNestedPayloadShapeIsNotSpecialCased(t *testing.T) {
	body := `{"event":"post_created","payload":{"id":"p1"},"delivery_id":"d1"}`
	env, err := colony.VerifyAndParseWebhook([]byte(body), sign(body, testSecret), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["payload"]; !ok {
		t.Error("Payload should be the verbatim body, nested key and all")
	}
	if env.DeliveryID != "" {
		t.Errorf("DeliveryID = %q; the body is not a source for it", env.DeliveryID)
	}
}

func TestVerifyAndParseWebhookLeavesHeaderFieldsEmpty(t *testing.T) {
	env, err := colony.VerifyAndParseWebhook([]byte(realBody), sign(realBody, testSecret), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if env.DeliveryID != "" || env.EventID != "" {
		t.Errorf("DeliveryID=%q EventID=%q; both live in headers this call never sees",
			env.DeliveryID, env.EventID)
	}
}

func TestVerifyAndParseWebhookRejectsNonJSON(t *testing.T) {
	body := `not json at all`
	if _, err := colony.VerifyAndParseWebhook([]byte(body), sign(body, testSecret), testSecret); err == nil {
		t.Error("expected an error for a non-JSON body")
	}
}

func newDelivery(body, deliveryID, eventID string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	r.Header.Set(colony.HeaderSignature, "sha256="+sign(body, testSecret))
	r.Header.Set(colony.HeaderDeliveryID, deliveryID)
	r.Header.Set(colony.HeaderEventID, eventID)
	return r
}

func TestVerifyAndParseWebhookRequestReadsHeaderIDs(t *testing.T) {
	env, err := colony.VerifyAndParseWebhookRequest(newDelivery(realBody, "att-1", "evt-9"), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if env.DeliveryID != "att-1" {
		t.Errorf("DeliveryID = %q, want att-1", env.DeliveryID)
	}
	if env.EventID != "evt-9" {
		t.Errorf("EventID = %q, want evt-9", env.EventID)
	}
	if env.Event != "post_created" || len(env.Payload) == 0 {
		t.Errorf("body parsing regressed: Event=%q Payload=%d bytes", env.Event, len(env.Payload))
	}
}

// The distinction that makes deduplication correct: a retry of the same
// event carries a NEW X-Colony-Delivery and the SAME X-Colony-Event-Id.
// A receiver keying on DeliveryID would process the event twice.
func TestRetryKeepsEventIDAndChangesDeliveryID(t *testing.T) {
	first, err := colony.VerifyAndParseWebhookRequest(newDelivery(realBody, "att-1", "evt-9"), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := colony.VerifyAndParseWebhookRequest(newDelivery(realBody, "att-2", "evt-9"), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventID != retry.EventID {
		t.Errorf("EventID must be stable across retries: %q vs %q", first.EventID, retry.EventID)
	}
	if first.DeliveryID == retry.DeliveryID {
		t.Error("DeliveryID must differ per attempt — deduplicating on it would double-process")
	}
}

func TestVerifyAndParseWebhookRequestRejectsBadSignature(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(realBody))
	r.Header.Set(colony.HeaderSignature, "sha256=deadbeef")
	if _, err := colony.VerifyAndParseWebhookRequest(r, testSecret); err == nil {
		t.Error("expected a signature error")
	}
}

func TestVerifyAndParseWebhookRequestRejectsNilRequest(t *testing.T) {
	if _, err := colony.VerifyAndParseWebhookRequest(nil, testSecret); err == nil {
		t.Error("expected an error for a nil request")
	}
}

// The header names are wire contract: the platform sets these exact
// strings and a receiver that reads any other name gets "". Pinned as
// LITERALS on purpose. The rest of this file addresses headers through
// the constants, so a rename would move both sides together and survive
// — which is precisely the self-consistency that let #33 live.
func TestHeaderNamesAreTheWireLiterals(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{colony.HeaderSignature, "X-Colony-Signature"},
		{colony.HeaderTimestamp, "X-Colony-Timestamp"},
		{colony.HeaderDeliveryID, "X-Colony-Delivery"},
		{colony.HeaderEventID, "X-Colony-Event-Id"},
		{colony.HeaderEvent, "X-Colony-Event"},
		{colony.HeaderAttempt, "X-Colony-Attempt"},
	} {
		if tc.got != tc.want {
			t.Errorf("header constant = %q, want %q", tc.got, tc.want)
		}
	}
}

// A test ping carries the SAME value in both id headers, because the server
// computes X-Colony-Event-Id as `event_id or delivery_id` and the synthetic
// ping is the one caller that passes no event id.
//
// This is here as executable documentation of a trap rather than as a check
// on the SDK: it means the test a developer actually runs — click "send test
// ping" — cannot tell a receiver that deduplicates on EventID from one that
// deduplicates on DeliveryID. The wrong one passes, then double-processes
// the first real retry. See the CAVEAT on WebhookEnvelope.EventID.
func TestTestPingCarriesTheSameValueInBothIDHeaders(t *testing.T) {
	const pingID = "0f8a-ping"
	env, err := colony.VerifyAndParseWebhookRequest(
		newDelivery(realBody, pingID, pingID), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if env.EventID != env.DeliveryID {
		t.Fatalf("test-ping fixture is wrong: EventID %q != DeliveryID %q",
			env.EventID, env.DeliveryID)
	}
	// Both dedup strategies agree here — which is exactly the problem.
	if env.EventID != pingID {
		t.Errorf("EventID = %q, want %q", env.EventID, pingID)
	}
}
