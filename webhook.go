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
// DOES NOT BOUND REPLAY. The signature covers the body and nothing else, so a
// captured delivery verifies forever and an attacker who records one request
// can re-send it indefinitely. Defending against that with this function means
// keeping every delivery id you have ever seen, which is unbounded.
//
// Prefer [VerifyWebhookWithTolerance], which uses the server's
// [HeaderSignature256] header — signed over timestamp AND body — and rejects a
// stale delivery without any storage on your side. This function stays for
// receivers built against the legacy header.
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
	// HeaderSignature is the LEGACY body-only signature. [HeaderSignature256]
	// (webhook_replay.go) is the replay-resistant one, present on every
	// delivery alongside it.
	HeaderSignature  = "X-Colony-Signature"
	HeaderTimestamp  = "X-Colony-Timestamp"
	HeaderDeliveryID = "X-Colony-Delivery"
	HeaderEventID    = "X-Colony-Event-Id"
	// HeaderEvent duplicates the body's "event" field.
	HeaderEvent = "X-Colony-Event"
	// HeaderAttempt is 1-based, so a receiver can log "3rd try" without
	// keeping its own counter. The receiver-visible half of at-least-once.
	HeaderAttempt = "X-Colony-Attempt"
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
	//
	// CAVEAT, and it bites exactly the test you are most likely to run:
	// the server computes the header as `event_id or delivery_id`, and the
	// synthetic "send test ping" is the one caller that passes no event id.
	// So for a test ping EventID and DeliveryID hold the SAME value, and a
	// receiver that wrongly deduplicates on DeliveryID looks correct —
	// right up until the first real retry, which it double-processes. A
	// test ping cannot demonstrate that your deduplication is keyed
	// correctly; only a real redelivery can.
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
// X-Colony-Signature-256, which [VerifyWebhookWithTolerance] verifies.
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
