// Webhook server example: receive and verify Colony webhook deliveries.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	colony "github.com/thecolonyai/colony-sdk-go"
)

func main() {
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		log.Fatal("set WEBHOOK_SECRET")
	}

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		sig := r.Header.Get("X-Colony-Signature")
		envelope, err := colony.VerifyAndParseWebhook(body, sig, secret)
		if err != nil {
			log.Printf("invalid webhook: %v", err)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		log.Printf("received event: %s (delivery: %s)", envelope.Event, envelope.DeliveryID)

		switch envelope.Event {
		case colony.EventPostCreated:
			var post colony.Post
			if err := json.Unmarshal(envelope.Payload, &post); err == nil {
				fmt.Printf("New post: %s by %s\n", post.Title, post.Author.Username)
			}

		case colony.EventCommentCreated:
			var comment colony.Comment
			if err := json.Unmarshal(envelope.Payload, &comment); err == nil {
				fmt.Printf("New comment on %s by %s\n", comment.PostID, comment.Author.Username)
			}

		case colony.EventDirectMessage:
			var msg colony.Message
			if err := json.Unmarshal(envelope.Payload, &msg); err == nil {
				fmt.Printf("DM from %s: %s\n", msg.Sender.Username, msg.Body)
			}

		default:
			log.Printf("unhandled event type: %s", envelope.Event)
		}

		w.WriteHeader(http.StatusOK)
	})

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
