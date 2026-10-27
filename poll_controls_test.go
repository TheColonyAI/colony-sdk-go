package colony

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// Poll controls and the preview-image opt-out (platform release 2026-10-27e):
// RetractPollVote, ClosePoll, UpdatePoll, DisableOGImage and
// CreatePostOptions.OGImageDisabled, and the three PollResults fields that
// release added.

const pollControlsPostID = "9cb7ff7d-51a0-46c6-acd2-d731164c792a"

// Shaped like a real PollResults from the platform.
const pollControlsResults = `{
	"total_votes": 5, "voter_count": 3, "user_voted": false, "user_option_ids": [],
	"options": [
		{"id": "opt_a", "text": "Yes", "vote_count": 3, "percentage": 100.0},
		{"id": "opt_b", "text": "No", "vote_count": 2, "percentage": 66.7}
	],
	"is_closed": false, "multiple_choice": true, "max_choices": 2,
	"show_results_before_voting": true, "closing_soon": false,
	"closes_at": "2026-11-01T12:00:00Z"
}`

func TestPollResultsDecodesTheNewFields(t *testing.T) {
	var r PollResults
	if err := json.Unmarshal([]byte(pollControlsResults), &r); err != nil {
		t.Fatal(err)
	}
	if r.VoterCount != 3 || r.MaxChoices == nil || *r.MaxChoices != 2 {
		t.Errorf("VoterCount %d, MaxChoices %v", r.VoterCount, r.MaxChoices)
	}
	if r.ClosesAt == nil || !r.ClosesAt.Equal(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("ClosesAt = %v", r.ClosesAt)
	}

	// A poll with no limit and no close time, as the platform sends it.
	var open PollResults
	if err := json.Unmarshal([]byte(`{"options": [], "max_choices": null, "closes_at": null}`), &open); err != nil {
		t.Fatal(err)
	}
	if open.MaxChoices != nil || open.ClosesAt != nil {
		t.Errorf("null decoded as %v / %v", open.MaxChoices, open.ClosesAt)
	}
}

func TestPollControlRoutes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, method, path string
		call               func(*Client) error
	}{
		{"RetractPollVote", http.MethodDelete, "/polls/" + pollControlsPostID + "/vote", func(c *Client) error {
			_, err := c.RetractPollVote(ctx, pollControlsPostID)
			return err
		}},
		{"ClosePoll", http.MethodPost, "/polls/" + pollControlsPostID + "/close", func(c *Client) error {
			_, err := c.ClosePoll(ctx, pollControlsPostID)
			return err
		}},
	} {
		c, rec := wikiServer(t, http.StatusOK, pollControlsResults)
		if err := tc.call(c); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if rec.method != tc.method || rec.path != tc.path || len(rec.body) != 0 {
			t.Errorf("%s sent %s %s with body %q", tc.name, rec.method, rec.path, rec.body)
		}
	}
}

func TestUpdatePollSendsOnlyWhatIsSet(t *testing.T) {
	ctx := context.Background()
	options := []PollOptionEdit{{ID: "opt_a", Text: "Yes, now"}, {ID: "opt_b", Text: "Not yet"}}

	c, rec := wikiServer(t, http.StatusOK, pollControlsResults)
	if _, err := c.UpdatePoll(ctx, pollControlsPostID, UpdatePollRequest{Options: options}); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch || rec.path != "/polls/"+pollControlsPostID {
		t.Errorf("sent %s %s", rec.method, rec.path)
	}
	if got := string(rec.body); got != `{"options":[{"id":"opt_a","text":"Yes, now"},{"id":"opt_b","text":"Not yet"}]}` {
		t.Errorf("body %s", got)
	}

	when := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	c, rec = wikiServer(t, http.StatusOK, pollControlsResults)
	if _, err := c.UpdatePoll(ctx, pollControlsPostID, UpdatePollRequest{ClosesAt: &when}); err != nil {
		t.Fatal(err)
	}
	if got := string(rec.body); got != `{"closes_at":"2026-11-02T09:00:00Z"}` {
		t.Errorf("body %s", got)
	}
}

func TestUpdatePollRefusesAnEmptyChangeBeforeTheRequest(t *testing.T) {
	c, rec := wikiServer(t, http.StatusOK, pollControlsResults)
	if _, err := c.UpdatePoll(context.Background(), pollControlsPostID, UpdatePollRequest{}); err == nil {
		t.Error("an empty change was accepted")
	}
	if rec.method != "" {
		t.Error("refused, but the request was still sent")
	}
}

func TestDisableOGImage(t *testing.T) {
	c, rec := wikiServer(t, http.StatusOK,
		`{"post_id":"`+pollControlsPostID+`","og_image_disabled":true,"detached":true}`)
	out, err := c.DisableOGImage(context.Background(), pollControlsPostID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.path != "/posts/"+pollControlsPostID+"/og-image/disable" {
		t.Errorf("sent %s %s", rec.method, rec.path)
	}
	if !out.OGImageDisabled || !out.Detached || out.PostID != pollControlsPostID {
		t.Errorf("decoded %+v", out)
	}
}

// The opt-out goes out only when set, so every existing CreatePost call sends
// exactly what it did.
func TestCreatePostSendsTheOptOutOnlyWhenSet(t *testing.T) {
	ctx := context.Background()
	colonyID := "2e549d01-99f2-459f-8924-48b2690b2170"
	for _, set := range []bool{true, false} {
		c, rec := wikiServer(t, http.StatusOK, `{"id":"`+pollControlsPostID+`","title":"t","body":"b"}`)
		if _, err := c.CreatePost(ctx, "A title", "A body", &CreatePostOptions{Colony: colonyID, OGImageDisabled: set}); err != nil {
			t.Fatal(err)
		}
		var sent map[string]any
		if err := json.Unmarshal(rec.body, &sent); err != nil {
			t.Fatal(err)
		}
		v, present := sent["og_image_disabled"]
		if set && v != true {
			t.Errorf("set: og_image_disabled = %v", v)
		}
		if !set && present {
			t.Errorf("unset: og_image_disabled sent as %v", v)
		}
	}
}

func TestPollControlsRefuseADotSegment(t *testing.T) {
	ctx := context.Background()
	when := time.Now().Add(time.Hour)
	calls := map[string]func(*Client) error{
		"RetractPollVote": func(c *Client) error { _, err := c.RetractPollVote(ctx, ".."); return err },
		"ClosePoll":       func(c *Client) error { _, err := c.ClosePoll(ctx, ".."); return err },
		"UpdatePoll": func(c *Client) error {
			_, err := c.UpdatePoll(ctx, "..", UpdatePollRequest{ClosesAt: &when})
			return err
		},
		"DisableOGImage": func(c *Client) error { _, err := c.DisableOGImage(ctx, ".."); return err },
	}
	for name, call := range calls {
		c, rec := wikiServer(t, http.StatusOK, pollControlsResults)
		if err := call(c); err == nil {
			t.Errorf("%s accepted %q", name, "..")
		}
		if rec.method != "" {
			t.Errorf("%s refused but still sent the request", name)
		}
	}
}
