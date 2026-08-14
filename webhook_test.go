package colony_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhook(t *testing.T) {
	payload := `{"event":"post_created","payload":{"id":"p1"}}`
	secret := "my-webhook-secret"
	sig := sign(payload, secret)

	if !colony.VerifyWebhook([]byte(payload), sig, secret) {
		t.Error("expected valid signature")
	}

	if colony.VerifyWebhook([]byte(payload), "wrong", secret) {
		t.Error("expected invalid signature")
	}

	if colony.VerifyWebhook([]byte("tampered"), sig, secret) {
		t.Error("expected invalid for tampered payload")
	}
}

func TestVerifyWebhookSha256Prefix(t *testing.T) {
	payload := `{"event":"comment_created"}`
	secret := "test-secret-1234"
	sig := "sha256=" + sign(payload, secret)

	if !colony.VerifyWebhook([]byte(payload), sig, secret) {
		t.Error("expected valid with sha256= prefix")
	}
}

// This test used to build its body as {"event", "payload", "delivery_id"}
// — the shape the SDK's struct expected — and assert the SDK read it back.
// Since the SDK was both author and reader of that shape, it passed while
// every real delivery produced an empty Payload (issue #33). It now uses a
// body in the platform's actual flat shape; the wire-format cases live in
// webhook_flat_body_test.go.
func TestVerifyAndParseWebhook(t *testing.T) {
	payload := `{"event":"post_created","id":"p1","title":"Hello"}`
	secret := "parse-secret-1234"
	sig := sign(payload, secret)

	event, err := colony.VerifyAndParseWebhook([]byte(payload), sig, secret)
	if err != nil {
		t.Fatal(err)
	}
	if event.Event != "post_created" {
		t.Errorf("expected post_created, got %s", event.Event)
	}
	if string(event.Payload) != payload {
		t.Errorf("Payload should be the verbatim body; got %s", event.Payload)
	}
}

func TestVerifyAndParseWebhookBadSig(t *testing.T) {
	payload := `{"event":"post_created"}`
	_, err := colony.VerifyAndParseWebhook([]byte(payload), "bad", "secret")
	if err == nil {
		t.Error("expected error for bad signature")
	}
}
