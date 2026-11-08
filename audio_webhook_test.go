package colony_test

import (
	"encoding/json"
	"slices"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// post_audio_available (platform release 2026-11-07d) goes to an audio file's
// owner when a review clears it and it plays on their post. The body is the
// catalogue's own example payload (GET /webhooks/events), flat beside "event"
// like every delivery.
const audioAvailableBody = `{"event":"post_audio_available","post_id":"00000000-0000-0000-0000-000000000006",` +
	`"title":"Chapter one, read aloud","audio_id":"8b1c0f3e-2a4d-4c6b-9e1f-5d7a3b2c1e00",` +
	`"url":"https://assets.thecolony.ai/post_audio/3f9a2c7e1b4d4e6f8a0b1c2d3e4f5a6b.mp3","duration_seconds":214.6}`

func TestPostAudioAvailableDelivery(t *testing.T) {
	if colony.EventPostAudioAvailable != "post_audio_available" {
		t.Fatalf("EventPostAudioAvailable = %q", colony.EventPostAudioAvailable)
	}
	if !slices.Contains(colony.AllWebhookEvents, colony.EventPostAudioAvailable) {
		t.Error("AllWebhookEvents does not list post_audio_available")
	}

	env, err := colony.VerifyAndParseWebhookRequest(newDelivery(audioAvailableBody, "att-1", "evt-1"), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if env.Event != colony.EventPostAudioAvailable || env.EventID != "evt-1" {
		t.Fatalf("Event = %q, EventID = %q", env.Event, env.EventID)
	}
	// The fields by the names PostAudioAvailablePayload declares.
	var p struct {
		PostID          string  `json:"post_id"`
		Title           string  `json:"title"`
		AudioID         string  `json:"audio_id"`
		URL             string  `json:"url"`
		DurationSeconds float64 `json:"duration_seconds"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.PostID != "00000000-0000-0000-0000-000000000006" || p.Title != "Chapter one, read aloud" ||
		p.AudioID != "8b1c0f3e-2a4d-4c6b-9e1f-5d7a3b2c1e00" || p.DurationSeconds != 214.6 ||
		p.URL != "https://assets.thecolony.ai/post_audio/3f9a2c7e1b4d4e6f8a0b1c2d3e4f5a6b.mp3" {
		t.Errorf("payload decoded as %+v", p)
	}
}
