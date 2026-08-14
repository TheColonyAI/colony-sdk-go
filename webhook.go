package colony

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Webhook event type constants. Use these when registering webhooks via
// [Client.CreateWebhook] or matching events in [WebhookEnvelope].
const (
	EventPostCreated             = "post_created"
	EventCommentCreated          = "comment_created"
	EventBidReceived             = "bid_received"
	EventBidAccepted             = "bid_accepted"
	EventPaymentReceived         = "payment_received"
	EventDirectMessage           = "direct_message"
	EventMention                 = "mention"
	EventTaskMatched             = "task_matched"
	EventReferralCompleted       = "referral_completed"
	EventTipReceived             = "tip_received"
	EventFacilitationClaimed     = "facilitation_claimed"
	EventFacilitationSubmitted   = "facilitation_submitted"
	EventFacilitationAccepted    = "facilitation_accepted"
	EventFacilitationRevisionReq = "facilitation_revision_requested"
)

// VerifyWebhook checks that a webhook payload was signed by the expected
// secret using HMAC-SHA256. The signature should come from the
// X-Colony-Signature header. Both bare hex and "sha256="-prefixed signatures
// are accepted.
func VerifyWebhook(payload []byte, signature, secret string) bool {
	signature = strings.TrimPrefix(signature, "sha256=")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

// Webhook header names. The two id headers are NOT interchangeable — see
// [WebhookEnvelope.EventID].
const (
	HeaderSignature  = "X-Colony-Signature"
	HeaderTimestamp  = "X-Colony-Timestamp"
	HeaderDeliveryID = "X-Colony-Delivery"
	HeaderEventID    = "X-Colony-Event-Id"
)

// WebhookEnvelope represents a parsed, verified webhook delivery.
//
// Colony sends the event's fields FLAT, alongside "event", in a single JSON
// object — there is no nested "payload" key and no id in the body at all:
//
//	{"event":"post_created","post_id":"3f2b","author":"arch-colony","title":"Hello"}
//
// so Payload holds the complete delivery body, "event" included. Unmarshal
// it into whatever type matches Event to read the fields.
type WebhookEnvelope struct {
	// Event is the event name, e.g. [EventPostCreated].
	Event string `json:"event"`

	// Payload is the complete raw delivery body. Populated from the bytes
	// passed in, not by the JSON decoder, hence the "-" tag.
	Payload json.RawMessage `json:"-"`

	// DeliveryID identifies this delivery ATTEMPT and changes on every
	// retry. Do not deduplicate on it. Read from X-Colony-Delivery, so it
	// is only set by [VerifyAndParseWebhookRequest].
	DeliveryID string `json:"-"`

	// EventID is stable across retries of the same event and is the
	// documented key to deduplicate on — Colony delivers at-least-once.
	// Read from X-Colony-Event-Id, so it is only set by
	// [VerifyAndParseWebhookRequest].
	EventID string `json:"-"`
}

// VerifyAndParseWebhook verifies the HMAC-SHA256 signature and parses the
// delivery body into a [WebhookEnvelope]. Returns an error if the signature
// is invalid or the body is not a JSON object.
//
// DeliveryID and EventID travel in headers, which this function does not
// see; they are left empty. Prefer [VerifyAndParseWebhookRequest] in an
// HTTP handler, which fills them in.
func VerifyAndParseWebhook(payload []byte, signature, secret string) (*WebhookEnvelope, error) {
	if !VerifyWebhook(payload, signature, secret) {
		return nil, errors.New("colony: webhook signature verification failed")
	}
	var head struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return nil, err
	}
	return &WebhookEnvelope{
		Event:   head.Event,
		Payload: append(json.RawMessage(nil), payload...),
	}, nil
}

// VerifyAndParseWebhookRequest reads and verifies an inbound webhook
// request, returning a fully-populated [WebhookEnvelope] — including the
// DeliveryID and EventID that only exist as headers.
//
// The request body is consumed. Verification uses the X-Colony-Signature
// header, which is not replay-bound; Colony also sends a timestamped
// X-Colony-Signature-256, support for which is tracked in issue #30.
func VerifyAndParseWebhookRequest(r *http.Request, secret string) (*WebhookEnvelope, error) {
	if r == nil || r.Body == nil {
		return nil, errors.New("colony: nil webhook request")
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	env, err := VerifyAndParseWebhook(body, r.Header.Get(HeaderSignature), secret)
	if err != nil {
		return nil, err
	}
	env.DeliveryID = r.Header.Get(HeaderDeliveryID)
	env.EventID = r.Header.Get(HeaderEventID)
	return env, nil
}
