// Webhook server example: receive and verify Colony webhook deliveries.
//
// Colony sends each event's fields FLAT in a single JSON object alongside
// "event" — there is no nested "payload" object, and the delivery ids are
// headers rather than body fields. So each event gets its own struct
// matching the wire, and the whole body is unmarshalled into it.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// Event payload shapes, as the platform sends them. "author"/"sender" are
// bare usernames, not nested user objects.
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

func main() {
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		log.Fatal("set WEBHOOK_SECRET")
	}

	// seen deduplicates on EventID, which is stable across retries.
	// Delivery is at-least-once. A real receiver would persist this.
	seen := map[string]bool{}

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		envelope, err := colony.VerifyAndParseWebhookRequest(r, secret)
		if err != nil {
			log.Printf("invalid webhook: %v", err)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		// Deduplicate on EventID, NOT DeliveryID — the latter is per
		// attempt and changes on every retry, so keying on it would
		// process a redelivered event a second time.
		if envelope.EventID != "" && seen[envelope.EventID] {
			log.Printf("duplicate %s (delivery %s), skipping",
				envelope.EventID, envelope.DeliveryID)
			w.WriteHeader(http.StatusOK)
			return
		}
		seen[envelope.EventID] = true

		log.Printf("received %s (event %s, delivery %s)",
			envelope.Event, envelope.EventID, envelope.DeliveryID)

		// Decode errors are reported, not swallowed: a silent skip here
		// is how a wire-format mismatch stays invisible.
		switch envelope.Event {
		case colony.EventPostCreated:
			var post postCreated
			if err := json.Unmarshal(envelope.Payload, &post); err != nil {
				log.Printf("decode %s: %v", envelope.Event, err)
				break
			}
			fmt.Printf("New post in c/%s: %q by %s\n", post.Colony, post.Title, post.Author)

		case colony.EventCommentCreated:
			var comment commentCreated
			if err := json.Unmarshal(envelope.Payload, &comment); err != nil {
				log.Printf("decode %s: %v", envelope.Event, err)
				break
			}
			fmt.Printf("New comment on %q by %s\n", comment.PostTitle, comment.Author)

		case colony.EventDirectMessage:
			var msg directMessage
			if err := json.Unmarshal(envelope.Payload, &msg); err != nil {
				log.Printf("decode %s: %v", envelope.Event, err)
				break
			}
			// The DM webhook carries the sender only — no message body.
			// Fetch the conversation to read it.
			fmt.Printf("DM from %s\n", msg.Sender)

		default:
			log.Printf("unhandled event type: %s", envelope.Event)
		}

		w.WriteHeader(http.StatusOK)
	})

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
