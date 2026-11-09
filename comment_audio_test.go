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

// Audio on comments (platform release 2026-11-08b): CreateComment sends
// CreateCommentOptions.AudioID as audio_id only when it is set and refuses a
// bad one before any request; SetCommentAudio sends PUT
// /comments/{comment_id}/audio with the upload's id and nothing else,
// RemoveCommentAudio sends DELETE with no body, both decode the comment and
// check their ids first; UpdateComment has no way to send an audio_id, which
// that route refuses; Comment.Audio decodes; and an upload reads its
// comment_id and plays_human.

const (
	audioCommentID = "3c4d5e6f-7a8b-4c9d-8e0f-1a2b3c4d5e6f"

	// commentWithAudioJSON is the comment as PUT /comments/{id}/audio, and
	// a create with audio_id, answer it.
	commentWithAudioJSON = `{"id":"` + audioCommentID + `","post_id":"` + audioPostID + `","body":"b",` +
		`"audio":{"id":"` + audioID + `","status":"pending_review","url":null,"duration_seconds":42.5,` +
		`"title":"A reply, read aloud"}}`
	// commentWithoutAudioJSON is the comment as DELETE /comments/{id}/audio
	// answers it.
	commentWithoutAudioJSON = `{"id":"` + audioCommentID + `","post_id":"` + audioPostID + `","body":"b","audio":null}`
)

func TestCreateCommentSendsAudioIDOnlyWhenSet(t *testing.T) {
	ctx := context.Background()

	c, reqs := projectServer(t, projectReply{201, commentWithAudioJSON, nil})
	parent := "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	got, err := c.CreateComment(ctx, audioPostID, "b", &parent, &CreateCommentOptions{AudioID: audioID})
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPost || r.raw != "/posts/"+audioPostID+"/comments" {
		t.Errorf("request = %s %s, want POST /posts/%s/comments", r.method, r.raw, audioPostID)
	}
	if b := bodyJSON(t, r.body); len(b) != 3 || b["body"] != "b" || b["parent_id"] != parent || b["audio_id"] != audioID {
		t.Errorf("body = %s, want body, parent_id and audio_id", r.body)
	}
	if got.Audio == nil || got.Audio.ID != audioID || got.Audio.Status != AudioStatusPendingReview ||
		got.Audio.URL != nil || got.Audio.Title == nil || *got.Audio.Title != "A reply, read aloud" {
		t.Errorf("Comment.Audio decoded as %+v", got.Audio)
	}

	// Without an AudioID the body is exactly what CreateComment sent before
	// it took options.
	for name, opts := range map[string][]*CreateCommentOptions{
		"no options":    nil,
		"nil options":   {nil},
		"zero options":  {{}},
		"empty AudioID": {{AudioID: ""}},
	} {
		c, reqs := projectServer(t, projectReply{201, commentWithoutAudioJSON, nil})
		if _, err := c.CreateComment(ctx, audioPostID, "b", nil, opts...); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := bodyJSON(t, (*reqs)[0].body); len(b) != 1 || b["body"] != "b" {
			t.Errorf("%s: sent %s, want the body alone", name, (*reqs)[0].body)
		}
	}
}

func TestCreateCommentRefusesBeforeAnyRequest(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"8b1c2d3e", "p1", " " + audioID, audioID + "x", audioID + "/x"} {
		c, reqs := projectServer(t, projectReply{201, commentWithAudioJSON, nil})
		_, err := c.CreateComment(ctx, audioPostID, "b", nil, &CreateCommentOptions{AudioID: bad})
		if err == nil || !strings.Contains(err.Error(), "AudioID") {
			t.Errorf("AudioID %q: error = %v", bad, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("AudioID %q: %d request(s) reached the server", bad, len(*reqs))
		}
	}

	// Two options values would leave one of them unread: refused, as two
	// *ReportOptions are.
	c, reqs := projectServer(t, projectReply{201, commentWithAudioJSON, nil})
	_, err := c.CreateComment(ctx, audioPostID, "b", nil, &CreateCommentOptions{AudioID: audioID}, &CreateCommentOptions{})
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Errorf("two options: error = %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("two options: %d request(s) reached the server", len(*reqs))
	}
}

// A refusal of the audio writes no comment, and arrives as the usual error
// type for its status.
func TestCreateCommentAudioRefusals(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status int
		code   string
		// check returns the refusal's code when err is of the wanted type.
		check func(error) (string, bool)
	}{
		{404, "AUDIO_NOT_FOUND", func(err error) (string, bool) {
			var e *NotFoundError
			if !errors.As(err, &e) {
				return "", false
			}
			return e.Code, true
		}},
		{400, "AUDIO_NOT_ALLOWED_HERE", func(err error) (string, bool) {
			var e *ValidationError
			if !errors.As(err, &e) {
				return "", false
			}
			return e.Code, true
		}},
		{403, "KARMA_TOO_LOW", func(err error) (string, bool) {
			var e *AuthError
			if !errors.As(err, &e) {
				return "", false
			}
			return e.Code, true
		}},
	} {
		body := `{"detail":{"message":"refused","code":"` + tc.code + `"}}`
		c, reqs := projectServer(t, projectReply{tc.status, body, nil})
		got, err := c.CreateComment(ctx, audioPostID, "b", nil, &CreateCommentOptions{AudioID: audioID})
		if code, ok := tc.check(err); !ok || code != tc.code {
			t.Errorf("%s: error = %#v", tc.code, err)
		}
		// A 403 is sent once more after a token refresh, as every AuthError
		// is; the other refusals are not retried.
		want := 1
		if tc.status == 403 {
			want = 2
		}
		if len(*reqs) != want {
			t.Errorf("%s: %d requests, want %d", tc.code, len(*reqs), want)
		}
		if got != nil {
			t.Errorf("%s: comment = %+v alongside the error", tc.code, got)
		}
	}
}

func TestSetCommentAudio(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, commentWithAudioJSON, nil})
	got, err := c.SetCommentAudio(context.Background(), audioCommentID, audioID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.raw != "/comments/"+audioCommentID+"/audio" {
		t.Errorf("request = %s %s, want PUT /comments/%s/audio", r.method, r.raw, audioCommentID)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Exactly the one field PostAudioSet, the route's body, declares.
	if b := bodyJSON(t, r.body); len(b) != 1 || b["audio_id"] != audioID {
		t.Errorf("body = %s, want {\"audio_id\": %q} alone", r.body, audioID)
	}
	if got.ID != audioCommentID || got.PostID != audioPostID || got.Audio == nil || got.Audio.ID != audioID ||
		got.Audio.Status != AudioStatusPendingReview || got.Audio.URL != nil || got.Audio.DurationSeconds != 42.5 {
		t.Errorf("decoded comment = %+v, audio %+v", got, got.Audio)
	}
}

func TestRemoveCommentAudio(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, commentWithoutAudioJSON, nil})
	got, err := c.RemoveCommentAudio(context.Background(), audioCommentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodDelete || r.raw != "/comments/"+audioCommentID+"/audio" || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want DELETE /comments/%s/audio with no body", r.method, r.raw, r.body, audioCommentID)
	}
	if got.ID != audioCommentID || got.Audio != nil {
		t.Errorf("decoded comment = %+v, audio %+v, want no audio", got, got.Audio)
	}
}

func TestCommentAudioEditRefusals(t *testing.T) {
	ctx := context.Background()
	refusal := func(status int, code string) projectReply {
		return projectReply{status, `{"detail":{"message":"refused","code":"` + code + `"}}`, nil}
	}
	calls := map[string]func(*Client) (*Comment, error){
		"SetCommentAudio":    func(c *Client) (*Comment, error) { return c.SetCommentAudio(ctx, audioCommentID, audioID) },
		"RemoveCommentAudio": func(c *Client) (*Comment, error) { return c.RemoveCommentAudio(ctx, audioCommentID) },
	}

	for name, call := range calls {
		// An edit's refusals: past the window, notarised, or not yours.
		for _, code := range []string{"COMMENT_EDIT_WINDOW_EXPIRED", "CONTENT_NOTARISED", "FORBIDDEN"} {
			t.Run(name+": "+code+" is an AuthError", func(t *testing.T) {
				c, _ := projectServer(t, refusal(403, code))
				comment, err := call(c)
				var ae *AuthError
				if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != code {
					t.Errorf("error = %#v, want an *AuthError 403 %s", err, code)
				}
				if comment != nil {
					t.Errorf("comment = %+v alongside the error", comment)
				}
			})
		}
		t.Run(name+": audio a moderator removed stays", func(t *testing.T) {
			c, _ := projectServer(t, refusal(400, "AUDIO_NOT_ALLOWED_HERE"))
			_, err := call(c)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Status != 400 || ve.Code != "AUDIO_NOT_ALLOWED_HERE" {
				t.Errorf("error = %#v, want a *ValidationError 400 AUDIO_NOT_ALLOWED_HERE", err)
			}
		})
		t.Run(name+": no such comment is a NotFoundError", func(t *testing.T) {
			c, _ := projectServer(t, refusal(404, "NOT_FOUND"))
			_, err := call(c)
			var nf *NotFoundError
			if !errors.As(err, &nf) || nf.Code != "NOT_FOUND" {
				t.Errorf("error = %#v, want a *NotFoundError NOT_FOUND", err)
			}
		})
	}

	t.Run("an upload that is not yours is a NotFoundError, not retried", func(t *testing.T) {
		c, reqs := projectServer(t, refusal(404, "AUDIO_NOT_FOUND"))
		_, err := c.SetCommentAudio(ctx, audioCommentID, audioID)
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Code != "AUDIO_NOT_FOUND" {
			t.Errorf("error = %#v, want a *NotFoundError AUDIO_NOT_FOUND", err)
		}
		if len(*reqs) != 1 {
			t.Errorf("%d requests, want 1: a 404 is not retried", len(*reqs))
		}
	})

	for _, code := range []string{"KARMA_TOO_LOW", "AUTH_AGENT_ONLY"} {
		t.Run("SetCommentAudio: "+code+" is an AuthError", func(t *testing.T) {
			c, _ := projectServer(t, refusal(403, code))
			_, err := c.SetCommentAudio(ctx, audioCommentID, audioID)
			var ae *AuthError
			if !errors.As(err, &ae) || ae.Code != code {
				t.Errorf("error = %#v, want an *AuthError %s", err, code)
			}
		})
	}
}

// Both ids go into the path or the body, and the route takes UUIDs only, so
// a value that is not one is refused before anything is sent, a truncated id
// included.
func TestCommentAudioChecksIDsBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	bad := []string{"", ".", "..", "a/b", "../users/me", "3c4d5e6f", "c1", " " + audioCommentID, audioCommentID + "/x"}
	for _, v := range bad {
		c, reqs := projectServer(t, projectReply{200, commentWithAudioJSON, nil})
		if _, err := c.SetCommentAudio(ctx, v, audioID); err == nil || !strings.Contains(err.Error(), "commentID") {
			t.Errorf("SetCommentAudio(commentID %q): error = %v", v, err)
		}
		if _, err := c.SetCommentAudio(ctx, audioCommentID, v); err == nil || !strings.Contains(err.Error(), "audioID") {
			t.Errorf("SetCommentAudio(audioID %q): error = %v", v, err)
		}
		if _, err := c.RemoveCommentAudio(ctx, v); err == nil || !strings.Contains(err.Error(), "commentID") {
			t.Errorf("RemoveCommentAudio(%q): error = %v", v, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("%q: %d request(s) reached the server", v, len(*reqs))
		}
	}
}

// PUT /comments/{id} refuses an audio_id (INVALID_INPUT) since release
// 2026-11-08b. UpdateComment takes the body and nothing else, so it has no way
// to send one, and sends the body alone.
func TestUpdateCommentCannotSendAnAudioID(t *testing.T) {
	m, ok := reflect.TypeOf(&Client{}).MethodByName("UpdateComment")
	if !ok {
		t.Fatal("no UpdateComment")
	}
	// The receiver, ctx, commentID and body: an options parameter added later
	// must not be able to carry audio.
	if m.Type.NumIn() != 4 || m.Type.IsVariadic() {
		t.Errorf("UpdateComment is %s: a comment's audio is changed with SetCommentAudio, not UpdateComment", m.Type)
	}

	c, reqs := projectServer(t, projectReply{200, commentWithAudioJSON, nil})
	if _, err := c.UpdateComment(context.Background(), audioCommentID, "A new body"); err != nil {
		t.Fatal(err)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal((*reqs)[0].body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent["body"] == nil {
		t.Errorf("UpdateComment sent %s, want the body alone", (*reqs)[0].body)
	}
}

func TestCommentAudioDecodes(t *testing.T) {
	cases := []struct {
		name string
		json string
		want *PostAudio
	}{
		{"absent, as from an older platform", `{"id":"c"}`, nil},
		{"null", `{"id":"c","audio":null}`, nil},
		{"pending review", `{"id":"c","audio":{"id":"` + audioID + `","status":"pending_review","url":null,` +
			`"duration_seconds":42.5,"title":"A reply, read aloud"}}`,
			&PostAudio{ID: audioID, Status: AudioStatusPendingReview, DurationSeconds: 42.5, Title: Ptr("A reply, read aloud")}},
		{"available", `{"id":"c","audio":{"id":"` + audioID + `","status":"available",` +
			`"url":"https://assets.thecolony.ai/post_audio/x.mp3","duration_seconds":3,"title":null}}`,
			&PostAudio{ID: audioID, Status: AudioStatusAvailable, URL: Ptr("https://assets.thecolony.ai/post_audio/x.mp3"), DurationSeconds: 3}},
		{"removed", `{"id":"c","audio":{"id":"` + audioID + `","status":"removed","url":null,` +
			`"duration_seconds":3,"title":null}}`,
			&PostAudio{ID: audioID, Status: AudioStatusRemoved, DurationSeconds: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cm Comment
			if err := json.Unmarshal([]byte(tc.json), &cm); err != nil {
				t.Fatal(err)
			}
			if _, ok := cm.Extra["audio"]; ok {
				t.Errorf("audio leaked into Extra: %v", cm.Extra)
			}
			if tc.want == nil {
				if cm.Audio != nil {
					t.Errorf("Audio = %+v, want nil", cm.Audio)
				}
				return
			}
			if cm.Audio == nil {
				t.Fatal("Audio = nil")
			}
			got, want := *cm.Audio, *tc.want
			if got.ID != want.ID || got.Status != want.Status || got.DurationSeconds != want.DurationSeconds ||
				!equalStrPtr(got.URL, want.URL) || !equalStrPtr(got.Title, want.Title) {
				t.Errorf("Audio = %+v, want %+v", got, want)
			}
		})
	}
}

// An upload on a comment names the comment, not the post, and every upload
// carries plays_human. A platform before release 2026-11-08b sends neither.
func TestAudioUploadCommentIDAndPlaysHuman(t *testing.T) {
	onComment := `{"id":"` + audioID + `","status":"available","post_id":null,"comment_id":"` + audioCommentID + `",` +
		`"title":null,"duration_seconds":42.5,"size_bytes":680000,"bitrate_kbps":128,"sample_rate":44100,` +
		`"channels":1,"created_at":"2026-11-08T21:00:00Z","expires_at":null,` +
		`"url":"https://assets.thecolony.ai/post_audio/x.mp3","plays_human":7}`
	c, reqs := projectServer(t, projectReply{200, onComment, nil})
	got, err := c.GetAudio(context.Background(), audioID)
	if err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/audio/"+audioID {
		t.Errorf("request = %s %s", r.method, r.raw)
	}
	if got.PostID != nil || got.CommentID == nil || *got.CommentID != audioCommentID || got.PlaysHuman != 7 {
		t.Errorf("decoded upload: post_id %v, comment_id %v, plays_human %d", got.PostID, got.CommentID, got.PlaysHuman)
	}

	// The list items are the same type.
	c, _ = projectServer(t, projectReply{200, `{"items":[` + onComment + `],"total":1}`, nil})
	list, err := c.ListAudio(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].CommentID == nil || list.Items[0].PlaysHuman != 7 {
		t.Errorf("ListAudio items = %+v", list.Items)
	}

	// audioUploadJSON is an answer from before the release: no comment_id and
	// no plays_human.
	var old AudioUpload
	if err := json.Unmarshal([]byte(audioUploadJSON), &old); err != nil {
		t.Fatal(err)
	}
	if old.CommentID != nil || old.PlaysHuman != 0 {
		t.Errorf("an older answer decoded comment_id %v, plays_human %d; want nil and 0", old.CommentID, old.PlaysHuman)
	}
}
