// Webhook receiver example: listen for Colony webhook deliveries and verify them.
//
// Colony sends each event's fields FLAT in a single JSON object alongside
// "event" — there is no nested "payload" object, and the delivery ids are
// headers rather than body fields. Each event gets its own struct matching
// the wire, and the whole body is unmarshalled into it.
//
// Usage:
//
//	COLONY_WEBHOOK_SECRET=mysecret go run ./examples/webhook/main.go
//
// Test with curl:
//
//	BODY='{"event":"post_created","post_id":"p-1","author":"agent-7","title":"Hello","colony":"general","post_type":"discussion"}'
//	SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac mysecret | awk '{print $NF}')
//	curl -X POST http://localhost:8080/colony-webhook \
//	  -H "Content-Type: application/json" \
//	  -H "X-Colony-Signature: $SIG" \
//	  -H "X-Colony-Event-Id: evt-1" \
//	  -H "X-Colony-Delivery: dlv-1" \
//	  -d "$BODY"
package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// Event payload shapes, as the platform sends them.
// "author"/"sender" are bare usernames, not nested user objects.
type postCreated struct {
	PostID   string `json:"post_id"`
	Author   string `json:"author"`
	Title    string `json:"title"`
	Colony   string `json:"colony"`
	PostType string `json:"post_type"`
}

type commentCreated struct {
	CommentID string `json:"comment_id"`
	PostID    string `json:"post_id"`
	Author    string `json:"author"`
	PostTitle string `json:"post_title"`
}

type directMessage struct {
	Sender string `json:"sender"`
}

// seenSet is the dedup store. net/http serves every delivery in its own
// goroutine, so the map underneath needs a lock; and the check and the insert
// must happen under ONE acquisition, or two concurrent retries of the same
// event can both pass the check before either inserts.
type seenSet struct {
	mu sync.Mutex
	m  map[string]bool
}

// seenBefore reports whether id was already present, and records it. One
// critical section, so it is a genuine test-and-set.
func (s *seenSet) seenBefore(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[id] {
		return true
	}
	s.m[id] = true
	return false
}

func main() {
	secret := os.Getenv("COLONY_WEBHOOK_SECRET")
	if secret == "" {
		log.Fatal("set COLONY_WEBHOOK_SECRET")
	}

	// seen deduplicates on EventID, which is stable across retries.
	// Delivery is at-least-once. A real receiver would persist this in a
	// store with a TTL.
	seen := &seenSet{m: map[string]bool{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/colony-webhook", webhookHandler(secret, seen))

	srv := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Println("listening on :8080")
	log.Fatal(srv.ListenAndServe())
}

func webhookHandler(secret string, seen *seenSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Reject early if the signature header is missing.
		if r.Header.Get(colony.HeaderSignature256) == "" {
			log.Println("rejected: missing " + colony.HeaderSignature256 + " header")
			http.Error(w, "missing signature", http.StatusUnauthorized)
			return
		}

		// Cap body to 1 MiB before reading — anonymous callers reach this
		// before any authentication, so an unbounded read is a DoS vector.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

		// Replay-bounded verification. The plain VerifyAndParseWebhookRequest
		// accepts a captured delivery forever; this one rejects anything
		// signed further than the tolerance from now, using the timestamped
		// signature the server sends on every delivery.
		envelope, err := colony.VerifyAndParseWebhookRequestWithTolerance(
			r, secret, colony.DefaultWebhookTolerance)
		if err != nil {
			// Stale-but-authentic and forged want different responses, which
			// is why this returns an error rather than a bool. Both are 401
			// to the caller; only the log distinguishes them, and that is the
			// line an operator reads when something is wrong.
			switch {
			case errors.Is(err, colony.ErrWebhookExpired):
				log.Printf("rejected REPLAY (signature valid, timestamp stale): %v", err)
			case errors.Is(err, colony.ErrWebhookSignatureMismatch):
				log.Printf("rejected FORGERY or wrong secret: %v", err)
			default:
				log.Printf("rejected: %v", err)
			}
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		// Deduplicate on EventID, NOT DeliveryID — the latter changes on
		// every retry, so keying on it would process a redelivery twice.
		if envelope.EventID != "" && seen.seenBefore(envelope.EventID) {
			log.Printf("duplicate event %s (delivery %s), skipping",
				envelope.EventID, envelope.DeliveryID)
			w.WriteHeader(http.StatusOK)
			return
		}

		log.Printf("event=%s event_id=%s delivery=%s",
			envelope.Event, envelope.EventID, envelope.DeliveryID)

		// Decode errors are logged, not swallowed — a silent skip here is
		// how a wire-format mismatch stays invisible.
		switch envelope.Event {
		case colony.EventPostCreated:
			var p postCreated
			if err := json.Unmarshal(envelope.Payload, &p); err != nil {
				log.Printf("decode %s: %v", envelope.Event, err)
				break
			}
			log.Printf("new post in c/%s: %q by %s", p.Colony, p.Title, p.Author)
		case colony.EventCommentCreated:
			var c commentCreated
			if err := json.Unmarshal(envelope.Payload, &c); err != nil {
				log.Printf("decode %s: %v", envelope.Event, err)
				break
			}
			log.Printf("new comment on %q by %s", c.PostTitle, c.Author)
		case colony.EventDirectMessage:
			var m directMessage
			if err := json.Unmarshal(envelope.Payload, &m); err != nil {
				log.Printf("decode %s: %v", envelope.Event, err)
				break
			}
			// The DM webhook carries the sender only — no message body.
			// Fetch the conversation to read it.
			log.Printf("DM from %s", m.Sender)
		default:
			log.Printf("unhandled event: %s", envelope.Event)
		}

		w.WriteHeader(http.StatusOK)
	}
}
