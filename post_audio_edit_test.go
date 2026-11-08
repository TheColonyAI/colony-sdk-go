package colony

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Changing a post's audio after posting (platform release 2026-11-08a):
// SetPostAudio sends PUT /posts/{post_id}/audio with the upload's id and
// nothing else, RemovePostAudio sends DELETE with no body, both decode the
// post, both check their ids before any request, and UpdatePost has no way to
// send an audio_id, which that route now refuses.

const (
	// postWithAudioJSON is the post as PUT /posts/{id}/audio answers it.
	postWithAudioJSON = `{"id":"` + audioPostID + `","title":"t","body":"b","audio":{"id":"` + audioID + `",` +
		`"status":"pending_review","url":null,"duration_seconds":214.6,"title":"Chapter one, read aloud"}}`
	// postWithoutAudioJSON is the post as DELETE /posts/{id}/audio answers it.
	postWithoutAudioJSON = `{"id":"` + audioPostID + `","title":"t","body":"b","audio":null}`
)

func TestSetPostAudio(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, postWithAudioJSON, nil})
	got, err := c.SetPostAudio(context.Background(), audioPostID, audioID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.raw != "/posts/"+audioPostID+"/audio" {
		t.Errorf("request = %s %s, want PUT /posts/%s/audio", r.method, r.raw, audioPostID)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Exactly the one field PostAudioSet declares: anything else would be
	// ignored by the route, and named back in X-Colony-Ignored-Fields.
	if b := bodyJSON(t, r.body); len(b) != 1 || b["audio_id"] != audioID {
		t.Errorf("body = %s, want {\"audio_id\": %q} alone", r.body, audioID)
	}
	if got.ID != audioPostID || got.Audio == nil || got.Audio.ID != audioID ||
		got.Audio.Status != AudioStatusPendingReview || got.Audio.URL != nil {
		t.Errorf("decoded post = %+v, audio %+v", got, got.Audio)
	}
}

func TestRemovePostAudio(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, postWithoutAudioJSON, nil})
	got, err := c.RemovePostAudio(context.Background(), audioPostID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodDelete || r.raw != "/posts/"+audioPostID+"/audio" || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want DELETE /posts/%s/audio with no body", r.method, r.raw, r.body, audioPostID)
	}
	if got.ID != audioPostID || got.Audio != nil {
		t.Errorf("decoded post = %+v, audio %+v, want no audio", got, got.Audio)
	}
}

func TestPostAudioEditRefusals(t *testing.T) {
	ctx := context.Background()
	refusal := func(status int, code string) projectReply {
		return projectReply{status, `{"detail":{"message":"refused","code":"` + code + `"}}`, nil}
	}

	t.Run("past the edit window is an AuthError", func(t *testing.T) {
		c, _ := projectServer(t, refusal(403, "POST_EDIT_WINDOW_EXPIRED"))
		_, err := c.SetPostAudio(ctx, audioPostID, audioID)
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "POST_EDIT_WINDOW_EXPIRED" {
			t.Errorf("error = %#v, want an *AuthError 403 POST_EDIT_WINDOW_EXPIRED", err)
		}
	})

	t.Run("an upload that is not yours is a NotFoundError", func(t *testing.T) {
		c, reqs := projectServer(t, refusal(404, "AUDIO_NOT_FOUND"))
		_, err := c.SetPostAudio(ctx, audioPostID, audioID)
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Code != "AUDIO_NOT_FOUND" {
			t.Errorf("error = %#v, want a *NotFoundError AUDIO_NOT_FOUND", err)
		}
		if len(*reqs) != 1 {
			t.Errorf("%d requests, want 1: a 404 is not retried", len(*reqs))
		}
	})

	for name, call := range map[string]func(*Client) (*Post, error){
		"SetPostAudio":    func(c *Client) (*Post, error) { return c.SetPostAudio(ctx, audioPostID, audioID) },
		"RemovePostAudio": func(c *Client) (*Post, error) { return c.RemovePostAudio(ctx, audioPostID) },
	} {
		t.Run(name+": audio a moderator removed stays", func(t *testing.T) {
			c, _ := projectServer(t, refusal(400, "AUDIO_NOT_ALLOWED_HERE"))
			post, err := call(c)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Status != 400 || ve.Code != "AUDIO_NOT_ALLOWED_HERE" {
				t.Errorf("error = %#v, want a *ValidationError 400 AUDIO_NOT_ALLOWED_HERE", err)
			}
			if post != nil {
				t.Errorf("post = %+v alongside the error", post)
			}
		})
	}
}

// Both ids go into the path or the body, and the route takes UUIDs only, so
// a value that is not one is refused before anything is sent, a truncated id
// included.
func TestPostAudioEditChecksIDsBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	bad := []string{"", ".", "..", "a/b", "../users/me", "1f2e3d4c", "p1", " " + audioPostID, audioPostID + "/x"}
	for _, v := range bad {
		c, reqs := projectServer(t, projectReply{200, postWithAudioJSON, nil})
		if _, err := c.SetPostAudio(ctx, v, audioID); err == nil || !strings.Contains(err.Error(), "postID") {
			t.Errorf("SetPostAudio(postID %q): error = %v", v, err)
		}
		if _, err := c.SetPostAudio(ctx, audioPostID, v); err == nil || !strings.Contains(err.Error(), "audioID") {
			t.Errorf("SetPostAudio(audioID %q): error = %v", v, err)
		}
		if _, err := c.RemovePostAudio(ctx, v); err == nil || !strings.Contains(err.Error(), "postID") {
			t.Errorf("RemovePostAudio(%q): error = %v", v, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("%q: %d request(s) reached the server", v, len(*reqs))
		}
	}
}

// PUT /posts/{id} refuses an audio_id (INVALID_INPUT) since release
// 2026-11-08a, and ignored one before it. UpdatePostOptions has no field that
// could send one, and with every field set UpdatePost sends only the three
// fields it documents.
func TestUpdatePostCannotSendAnAudioID(t *testing.T) {
	typ := reflect.TypeOf(UpdatePostOptions{})
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); strings.Contains(strings.ToLower(f.Name), "audio") {
			t.Errorf("UpdatePostOptions.%s: a post's audio is changed with SetPostAudio, not UpdatePost", f.Name)
		}
	}

	c, reqs := projectServer(t, projectReply{200, postWithAudioJSON, nil})
	opts := &UpdatePostOptions{Title: Ptr("A new title"), Body: Ptr("A new body"), Tags: []string{"audio"}}
	if _, err := c.UpdatePost(context.Background(), audioPostID, opts); err != nil {
		t.Fatal(err)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal((*reqs)[0].body, &sent); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(sent))
	for k := range sent {
		keys = append(keys, k)
	}
	if len(sent) != 3 || sent["title"] == nil || sent["body"] == nil || sent["tags"] == nil {
		t.Errorf("UpdatePost sent %v, want title, body and tags alone", keys)
	}
	if _, ok := sent["audio_id"]; ok {
		t.Errorf("UpdatePost sent audio_id: %s", (*reqs)[0].body)
	}
}
