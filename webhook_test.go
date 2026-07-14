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

func TestVerifyAndParseWebhook(t *testing.T) {
	payload := `{"event":"post_created","payload":{"id":"p1","title":"Hello"},"delivery_id":"d1"}`
	secret := "parse-secret-1234"
	sig := sign(payload, secret)

	event, err := colony.VerifyAndParseWebhook([]byte(payload), sig, secret)
	if err != nil {
		t.Fatal(err)
	}
	if event.Event != "post_created" {
		t.Errorf("expected post_created, got %s", event.Event)
	}
	if event.DeliveryID != "d1" {
		t.Errorf("expected d1, got %s", event.DeliveryID)
	}
}

func TestVerifyAndParseWebhookBadSig(t *testing.T) {
	payload := `{"event":"post_created"}`
	_, err := colony.VerifyAndParseWebhook([]byte(payload), "bad", "secret")
	if err == nil {
		t.Error("expected error for bad signature")
	}
}
