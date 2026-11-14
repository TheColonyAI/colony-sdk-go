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

// YouTube videos on posts (platform release 2026-11-14b):
// CreatePostOptions.YouTubeURL sends youtube_url only when set; SetPostVideo
// sends PUT /posts/{post_id}/video with the link and nothing else;
// RemovePostVideo sends DELETE with no body; both decode the post and check
// their arguments before any request; Post.Video decodes; and UpdatePost has
// no way to send a video.

const (
	videoID   = "dQw4w9WgXcQ"
	videoLink = "https://www.youtube.com/watch?v=" + videoID

	// postVideoJSON is the video as PostVideoOut carries it, every field set.
	postVideoJSON = `{"provider":"youtube","video_id":"` + videoID + `","title":"A talk on colonies",` +
		`"channel":"The Colony","url":"https://www.youtube.com/watch?v=` + videoID + `",` +
		`"embed_url":"https://www.youtube-nocookie.com/embed/` + videoID + `",` +
		`"thumbnail_url":"https://assets.thecolony.ai/post_images/abc_def.webp"}`
	// postWithVideoJSON is the post as PUT /posts/{id}/video answers it.
	postWithVideoJSON = `{"id":"` + audioPostID + `","title":"t","body":"b","audio":null,"video":` + postVideoJSON + `}`
	// postWithoutVideoJSON is the post as DELETE /posts/{id}/video answers it.
	postWithoutVideoJSON = `{"id":"` + audioPostID + `","title":"t","body":"b","audio":null,"video":null}`
)

// checkVideo reports how v differs from postVideoJSON.
func checkVideo(t *testing.T, v *PostVideo) {
	t.Helper()
	if v == nil {
		t.Fatal("Video = nil")
	}
	want := PostVideo{
		Provider: VideoProviderYouTube, VideoID: videoID,
		Title: Ptr("A talk on colonies"), Channel: Ptr("The Colony"),
		URL:          "https://www.youtube.com/watch?v=" + videoID,
		EmbedURL:     "https://www.youtube-nocookie.com/embed/" + videoID,
		ThumbnailURL: Ptr("https://assets.thecolony.ai/post_images/abc_def.webp"),
	}
	if v.Provider != want.Provider || v.VideoID != want.VideoID || v.URL != want.URL || v.EmbedURL != want.EmbedURL ||
		!equalStrPtr(v.Title, want.Title) || !equalStrPtr(v.Channel, want.Channel) ||
		!equalStrPtr(v.ThumbnailURL, want.ThumbnailURL) {
		t.Errorf("Video = %+v, want %+v", *v, want)
	}
}

func TestCreatePostSendsYouTubeURLOnlyWhenSet(t *testing.T) {
	ctx := context.Background()

	c, reqs := projectServer(t, projectReply{201, postWithVideoJSON, nil})
	got, err := c.CreatePost(ctx, "t", "b", &CreatePostOptions{Colony: audioColony, YouTubeURL: videoLink})
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1 (a UUID colony needs no lookup)", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPost || r.raw != "/posts" {
		t.Errorf("request = %s %s, want POST /posts", r.method, r.raw)
	}
	// Sent as given: the platform reads the id out of the link.
	b := bodyJSON(t, r.body)
	if b["youtube_url"] != videoLink {
		t.Errorf("youtube_url sent as %v, in %v", b["youtube_url"], b)
	}
	if _, ok := b["audio_id"]; ok {
		t.Errorf("audio_id sent with no AudioID: %v", b)
	}
	checkVideo(t, got.Video)

	// The bare 11-character id goes as it is too.
	c, reqs = projectServer(t, projectReply{201, postWithVideoJSON, nil})
	if _, err := c.CreatePost(ctx, "t", "b", &CreatePostOptions{Colony: audioColony, YouTubeURL: videoID}); err != nil {
		t.Fatal(err)
	}
	if b := bodyJSON(t, (*reqs)[0].body); b["youtube_url"] != videoID {
		t.Errorf("youtube_url sent as %v, in %v", b["youtube_url"], b)
	}

	// Unset, blank (which the platform reads as no video) and nil options
	// send no youtube_url. nil options post to "general", whose id the
	// package knows, so no case looks a colony up.
	for name, opts := range map[string]*CreatePostOptions{
		"unset":       {Colony: audioColony},
		"blank":       {Colony: audioColony, YouTubeURL: " \t\n"},
		"nil options": nil,
	} {
		c, reqs := projectServer(t, projectReply{201, postWithoutVideoJSON, nil})
		post, err := c.CreatePost(ctx, "t", "b", opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(*reqs) != 1 {
			t.Fatalf("%s: %d requests, want 1", name, len(*reqs))
		}
		if b := (*reqs)[0].body; strings.Contains(string(b), "youtube") {
			t.Errorf("%s: sent %s", name, b)
		}
		if post.Video != nil {
			t.Errorf("%s: Video = %+v, want nil", name, post.Video)
		}
	}
}

// A post has a video or audio, never both. The pair is the platform's to
// refuse, before it writes anything, so CreatePost sends both and returns
// its refusal rather than keeping a second copy of the rule.
func TestCreatePostWithVideoAndAudioIsRefusedByThePlatform(t *testing.T) {
	c, reqs := projectServer(t, projectReply{400,
		`{"detail":{"message":"A post carries one YouTube video or audio, not both.","code":"VIDEO_NOT_ALLOWED_HERE"}}`, nil})
	post, err := c.CreatePost(context.Background(), "t", "b",
		&CreatePostOptions{Colony: audioColony, AudioID: audioID, YouTubeURL: videoLink})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Status != 400 || ve.Code != "VIDEO_NOT_ALLOWED_HERE" {
		t.Errorf("error = %#v, want a *ValidationError 400 VIDEO_NOT_ALLOWED_HERE", err)
	}
	if post != nil {
		t.Errorf("post = %+v alongside the error", post)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1: a 400 is not retried", len(*reqs))
	}
	if b := bodyJSON(t, (*reqs)[0].body); b["audio_id"] != audioID || b["youtube_url"] != videoLink {
		t.Errorf("body = %v, want audio_id and youtube_url both", b)
	}
}

func TestSetPostVideo(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, postWithVideoJSON, nil})
	got, err := c.SetPostVideo(context.Background(), audioPostID, videoLink)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.raw != "/posts/"+audioPostID+"/video" {
		t.Errorf("request = %s %s, want PUT /posts/%s/video", r.method, r.raw, audioPostID)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Exactly the one field PostVideoSet declares: anything else would be
	// ignored by the route, and named back in X-Colony-Ignored-Fields.
	if b := bodyJSON(t, r.body); len(b) != 1 || b["youtube_url"] != videoLink {
		t.Errorf("body = %s, want {\"youtube_url\": %q} alone", r.body, videoLink)
	}
	if got.ID != audioPostID || got.Audio != nil {
		t.Errorf("decoded post = %+v", got)
	}
	checkVideo(t, got.Video)
}

func TestRemovePostVideo(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, postWithoutVideoJSON, nil})
	got, err := c.RemovePostVideo(context.Background(), audioPostID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodDelete || r.raw != "/posts/"+audioPostID+"/video" || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want DELETE /posts/%s/video with no body", r.method, r.raw, r.body, audioPostID)
	}
	if got.ID != audioPostID || got.Video != nil {
		t.Errorf("decoded post = %+v, video %+v, want no video", got, got.Video)
	}
}

func TestPostVideoRefusals(t *testing.T) {
	ctx := context.Background()
	refusal := func(status int, code string) projectReply {
		return projectReply{status, `{"detail":{"message":"refused","code":"` + code + `"}}`, nil}
	}

	for _, code := range []string{"VIDEO_URL_INVALID", "VIDEO_UNAVAILABLE", "VIDEO_NOT_ALLOWED_HERE"} {
		t.Run(code+" is a ValidationError", func(t *testing.T) {
			c, reqs := projectServer(t, refusal(400, code))
			post, err := c.SetPostVideo(ctx, audioPostID, videoLink)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Status != 400 || ve.Code != code {
				t.Errorf("error = %#v, want a *ValidationError 400 %s", err, code)
			}
			if post != nil {
				t.Errorf("post = %+v alongside the error", post)
			}
			if len(*reqs) != 1 {
				t.Errorf("%d requests, want 1: a 400 is not retried", len(*reqs))
			}
		})
	}

	t.Run("past the edit window is an AuthError", func(t *testing.T) {
		c, _ := projectServer(t, refusal(403, "POST_EDIT_WINDOW_EXPIRED"))
		_, err := c.SetPostVideo(ctx, audioPostID, videoLink)
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "POST_EDIT_WINDOW_EXPIRED" {
			t.Errorf("error = %#v, want an *AuthError 403 POST_EDIT_WINDOW_EXPIRED", err)
		}
	})

	t.Run("YouTube not answering is a ServerError, retried first", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{503,
			`{"detail":{"message":"YouTube did not answer.","code":"VIDEO_CHECK_FAILED"}}`,
			map[string]string{"Retry-After": "30"}})
		_, err := c.SetPostVideo(ctx, audioPostID, videoLink)
		var se *ServerError
		if !errors.As(err, &se) || se.Status != 503 || se.Code != "VIDEO_CHECK_FAILED" {
			t.Errorf("error = %#v, want a *ServerError 503 VIDEO_CHECK_FAILED", err)
		}
		if want := 1 + DefaultRetry().MaxRetries; len(*reqs) != want {
			t.Errorf("%d requests, want %d: a 503 is retried under the RetryConfig", len(*reqs), want)
		}
	})

	t.Run("someone else's post is an AuthError on removal", func(t *testing.T) {
		c, _ := projectServer(t, refusal(403, "POST_EDIT_FORBIDDEN"))
		post, err := c.RemovePostVideo(ctx, audioPostID)
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code != "POST_EDIT_FORBIDDEN" {
			t.Errorf("error = %#v, want an *AuthError POST_EDIT_FORBIDDEN", err)
		}
		if post != nil {
			t.Errorf("post = %+v alongside the error", post)
		}
	})

	t.Run("a missing post is a NotFoundError", func(t *testing.T) {
		c, reqs := projectServer(t, refusal(404, "POST_NOT_FOUND"))
		_, err := c.RemovePostVideo(ctx, audioPostID)
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Code != "POST_NOT_FOUND" {
			t.Errorf("error = %#v, want a *NotFoundError POST_NOT_FOUND", err)
		}
		if len(*reqs) != 1 {
			t.Errorf("%d requests, want 1: a 404 is not retried", len(*reqs))
		}
	})
}

// The post id goes into the path and the route takes UUIDs only, so a value
// that is not one is refused before anything is sent, a truncated id
// included. A blank link is refused too: it would be VIDEO_URL_INVALID, and
// taking a video off is RemovePostVideo.
func TestPostVideoChecksArgumentsBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	bad := []string{"", ".", "..", "a/b", "../users/me", "1f2e3d4c", "p1", " " + audioPostID, audioPostID + "/x"}
	for _, v := range bad {
		c, reqs := projectServer(t, projectReply{200, postWithVideoJSON, nil})
		if _, err := c.SetPostVideo(ctx, v, videoLink); err == nil || !strings.Contains(err.Error(), "postID") {
			t.Errorf("SetPostVideo(postID %q): error = %v", v, err)
		}
		if _, err := c.RemovePostVideo(ctx, v); err == nil || !strings.Contains(err.Error(), "postID") {
			t.Errorf("RemovePostVideo(%q): error = %v", v, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("%q: %d request(s) reached the server", v, len(*reqs))
		}
	}
	for _, blank := range []string{"", " ", "\t\n"} {
		c, reqs := projectServer(t, projectReply{200, postWithVideoJSON, nil})
		_, err := c.SetPostVideo(ctx, audioPostID, blank)
		if err == nil || !strings.Contains(err.Error(), "youtubeURL") || !strings.Contains(err.Error(), "RemovePostVideo") {
			t.Errorf("SetPostVideo(youtubeURL %q): error = %v", blank, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("youtubeURL %q: %d request(s) reached the server", blank, len(*reqs))
		}
	}
}

func TestPostVideoDecodes(t *testing.T) {
	cases := []struct {
		name string
		json string
		full bool // every field as postVideoJSON has it
	}{
		{"absent, as from a platform before release 2026-11-14a", `{"id":"p"}`, false},
		{"null", `{"id":"p","video":null}`, false},
		{"every field", `{"id":"p","video":` + postVideoJSON + `}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Post
			if err := json.Unmarshal([]byte(tc.json), &p); err != nil {
				t.Fatal(err)
			}
			if _, ok := p.Extra["video"]; ok {
				t.Errorf("video leaked into Extra: %v", p.Extra)
			}
			if !tc.full {
				if p.Video != nil {
					t.Errorf("Video = %+v, want nil", p.Video)
				}
				return
			}
			checkVideo(t, p.Video)
		})
	}

	// Title, channel and thumbnail are nullable: YouTube may not give the
	// first two, and a thumbnail that could not be copied is left out.
	var p Post
	if err := json.Unmarshal([]byte(`{"id":"p","video":{"provider":"youtube","video_id":"`+videoID+`",`+
		`"title":null,"channel":null,"url":"u","embed_url":"e","thumbnail_url":null}}`), &p); err != nil {
		t.Fatal(err)
	}
	if v := p.Video; v == nil || v.VideoID != videoID || v.Title != nil || v.Channel != nil || v.ThumbnailURL != nil ||
		v.URL != "u" || v.EmbedURL != "e" {
		t.Errorf("Video = %+v", p.Video)
	}
}

// A post's video is changed with SetPostVideo and RemovePostVideo, and
// UpdatePostOptions has no field that could send one.
func TestUpdatePostCannotSendAVideo(t *testing.T) {
	typ := reflect.TypeOf(UpdatePostOptions{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "video") || strings.Contains(name, "youtube") {
			t.Errorf("UpdatePostOptions.%s: a post's video is changed with SetPostVideo, not UpdatePost",
				typ.Field(i).Name)
		}
	}
}
