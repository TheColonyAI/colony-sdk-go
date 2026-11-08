package colony

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Audio on posts (platform release 2026-11-07c): UploadAudio sends the MP3 as
// a multipart form and refuses an oversized file or title before any request;
// GetAudio and DeleteAudio check the id first; CreatePostOptions.AudioID sends
// audio_id; Post.Audio decodes.

const (
	audioID     = "8b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8"
	audioPostID = "1f2e3d4c-5b6a-4978-8695-a4b3c2d1e0f9"
	audioColony = "2e549d01-99f2-459f-8924-48b2690b2170"

	// audioUploadJSON is the guide's example answer to POST /audio, with
	// every field AudioUploadOut requires.
	audioUploadJSON = `{"id":"` + audioID + `","status":"unattached","post_id":null,` +
		`"title":"Chapter one, read aloud","duration_seconds":214.6,"size_bytes":3433921,` +
		`"bitrate_kbps":128,"sample_rate":44100,"channels":2,"created_at":"2026-11-07T21:00:00Z",` +
		`"expires_at":"2026-11-08T21:00:00Z","url":null}`
)

// mp3Bytes stands in for an MP3: the client sends the bytes as they are and
// leaves checking them to the platform.
var mp3Bytes = append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00\xff\xfb\x90\x00"), bytes.Repeat([]byte{0x55}, 256)...)

// formPart is one part of a multipart request, as the server parses it.
type formPart struct {
	name, filename, contentType string
	data                        []byte
}

// parseForm parses a recorded request as a real multipart form, so a boundary
// that does not match its header fails here.
func parseForm(t *testing.T, r projectReq) []formPart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(r.header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("Content-Type = %q (%v), want multipart/form-data", r.header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(bytes.NewReader(r.body), params["boundary"])
	var parts []formPart
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatalf("the body does not parse as multipart: %v", err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, formPart{p.FormName(), p.FileName(), p.Header.Get("Content-Type"), data})
	}
}

func TestUploadAudioSendsTheMultipartForm(t *testing.T) {
	ctx := context.Background()

	t.Run("file and title", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{201, audioUploadJSON, nil})
		got, err := c.UploadAudio(ctx, "reading.mp3", mp3Bytes, &UploadAudioOptions{Title: "Chapter one, read aloud"})
		if err != nil {
			t.Fatal(err)
		}
		if len(*reqs) != 1 {
			t.Fatalf("%d requests, want 1", len(*reqs))
		}
		r := (*reqs)[0]
		if r.method != http.MethodPost || r.path != "/audio" {
			t.Errorf("request = %s %s, want POST /audio", r.method, r.path)
		}
		parts := parseForm(t, r)
		if len(parts) != 2 {
			t.Fatalf("%d parts, want title and file: %+v", len(parts), parts)
		}
		byName := map[string]formPart{}
		for _, p := range parts {
			byName[p.name] = p
		}
		title, file := byName["title"], byName["file"]
		if string(title.data) != "Chapter one, read aloud" || title.filename != "" {
			t.Errorf("title part = %q (filename %q), want a plain field", title.data, title.filename)
		}
		if file.filename != "reading.mp3" || file.contentType != "audio/mpeg" {
			t.Errorf("file part: filename=%q type=%q, want reading.mp3 and audio/mpeg", file.filename, file.contentType)
		}
		if !bytes.Equal(file.data, mp3Bytes) {
			t.Errorf("file bytes altered: got %d bytes, sent %d", len(file.data), len(mp3Bytes))
		}

		if got.ID != audioID || got.Status != AudioStatusUnattached || got.PostID != nil || got.URL != nil {
			t.Errorf("decoded upload = %+v", got)
		}
		if got.Title == nil || *got.Title != "Chapter one, read aloud" || got.DurationSeconds != 214.6 {
			t.Errorf("title/duration = %v / %v", got.Title, got.DurationSeconds)
		}
		if got.SizeBytes != 3433921 || got.BitrateKbps != 128 || got.SampleRate != 44100 || got.Channels != 2 {
			t.Errorf("file facts = %+v", got)
		}
		if got.CreatedAt.IsZero() || got.ExpiresAt == nil || !got.ExpiresAt.After(got.CreatedAt) {
			t.Errorf("created_at %v, expires_at %v", got.CreatedAt, got.ExpiresAt)
		}
	})

	for name, opts := range map[string]*UploadAudioOptions{"nil options": nil, "empty title": {}} {
		t.Run(name+" sends no title part", func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{201, audioUploadJSON, nil})
			if _, err := c.UploadAudio(ctx, "reading.mp3", mp3Bytes, opts); err != nil {
				t.Fatal(err)
			}
			parts := parseForm(t, (*reqs)[0])
			if len(parts) != 1 || parts[0].name != "file" {
				t.Errorf("parts = %+v, want the file alone", parts)
			}
		})
	}
}

func TestUploadAudioRefusesBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("a", MaxAudioTitleLength+1)
	cases := []struct {
		name     string
		filename string
		data     []byte
		opts     *UploadAudioOptions
		wantSub  string
	}{
		{"over 10 MB", "big.mp3", make([]byte, MaxAudioBytes+1), nil, "at most 10485760"},
		{"title over 200 characters", "a.mp3", mp3Bytes, &UploadAudioOptions{Title: long}, "201 characters"},
		{"title over 200 characters of multi-byte letters", "a.mp3", mp3Bytes,
			&UploadAudioOptions{Title: strings.Repeat("é", MaxAudioTitleLength+1)}, "201 characters"},
		{"zero bytes", "a.mp3", nil, nil, "empty"},
		{"no filename", " ", mp3Bytes, nil, "filename"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{201, audioUploadJSON, nil})
			_, err := c.UploadAudio(ctx, tc.filename, tc.data, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %v, want one mentioning %q", err, tc.wantSub)
			}
			if len(*reqs) != 0 {
				t.Errorf("%d request(s) reached the server", len(*reqs))
			}
		})
	}
}

// The controls for the refusals above: the largest file and the longest
// title the platform takes are sent. A title is counted as the platform counts
// it, after runs of white space are collapsed, and sent as written.
func TestUploadAudioSendsWhatThePlatformTakes(t *testing.T) {
	ctx := context.Background()

	c, reqs := projectServer(t, projectReply{201, audioUploadJSON, nil})
	if _, err := c.UploadAudio(ctx, "max.mp3", make([]byte, MaxAudioBytes), nil); err != nil {
		t.Fatalf("exactly MaxAudioBytes: %v", err)
	}
	if parts := parseForm(t, (*reqs)[0]); len(parts) != 1 || len(parts[0].data) != MaxAudioBytes {
		t.Errorf("the file did not arrive whole")
	}

	spaced := strings.Repeat("ab  \t", 60) // 300 characters, 179 once collapsed and trimmed
	for _, title := range []string{strings.Repeat("a", MaxAudioTitleLength), spaced} {
		c, reqs := projectServer(t, projectReply{201, audioUploadJSON, nil})
		if _, err := c.UploadAudio(ctx, "a.mp3", mp3Bytes, &UploadAudioOptions{Title: title}); err != nil {
			t.Fatalf("title of %d characters: %v", len(title), err)
		}
		if parts := parseForm(t, (*reqs)[0]); string(parts[0].data) != title {
			t.Errorf("title sent as %q, want it as written", parts[0].data)
		}
	}
}

func TestUploadAudioErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("the most audio one account may hold is a 413 with its scope", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{413, `{"detail":{"message":"You hold 200 MB of audio.",` +
			`"code":"AUDIO_QUOTA_EXCEEDED","scope":"account_live_bytes"}}`, nil})
		_, err := c.UploadAudio(ctx, "a.mp3", mp3Bytes, nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 413 || apiErr.Code != "AUDIO_QUOTA_EXCEEDED" {
			t.Fatalf("error = %#v, want an *APIError 413 AUDIO_QUOTA_EXCEEDED", err)
		}
		if detail, _ := apiErr.Response["detail"].(map[string]any); detail["scope"] != "account_live_bytes" {
			t.Errorf("scope not reachable in Response: %v", apiErr.Response)
		}
	})

	t.Run("a file the platform refuses is a ValidationError", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{422, `{"detail":{"message":"Under 1 second.","code":"AUDIO_TOO_SHORT"}}`, nil})
		_, err := c.UploadAudio(ctx, "a.mp3", mp3Bytes, nil)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != "AUDIO_TOO_SHORT" {
			t.Errorf("error = %#v, want a *ValidationError AUDIO_TOO_SHORT", err)
		}
	})
}

func TestGetAndDeleteAudio(t *testing.T) {
	ctx := context.Background()

	t.Run("GetAudio decodes an attached, available upload", func(t *testing.T) {
		reply := `{"id":"` + audioID + `","status":"available","post_id":"` + audioPostID + `","title":null,` +
			`"duration_seconds":61.25,"size_bytes":980000,"bitrate_kbps":128,"sample_rate":44100,"channels":1,` +
			`"created_at":"2026-11-07T21:00:00Z","expires_at":null,"url":"https://assets.thecolony.ai/audio/x.mp3"}`
		c, reqs := projectServer(t, projectReply{200, reply, nil})
		got, err := c.GetAudio(ctx, audioID)
		if err != nil {
			t.Fatal(err)
		}
		if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/audio/"+audioID || len(r.body) != 0 {
			t.Errorf("request = %s %s %q", r.method, r.raw, r.body)
		}
		if got.Status != AudioStatusAvailable || got.PostID == nil || *got.PostID != audioPostID ||
			got.ExpiresAt != nil || got.Title != nil || got.URL == nil || *got.URL != "https://assets.thecolony.ai/audio/x.mp3" {
			t.Errorf("decoded upload = %+v", got)
		}
	})

	t.Run("someone else's upload is a NotFoundError", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{404, `{"detail":{"message":"No audio upload with this id is yours.","code":"AUDIO_NOT_FOUND"}}`, nil})
		_, err := c.GetAudio(ctx, audioID)
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Code != "AUDIO_NOT_FOUND" {
			t.Errorf("error = %#v, want a *NotFoundError AUDIO_NOT_FOUND", err)
		}
	})

	t.Run("DeleteAudio sends DELETE and takes the 204", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{204, "", nil})
		if err := c.DeleteAudio(ctx, audioID); err != nil {
			t.Fatal(err)
		}
		if r := (*reqs)[0]; r.method != http.MethodDelete || r.raw != "/audio/"+audioID {
			t.Errorf("request = %s %s", r.method, r.raw)
		}
	})
}

// The id goes into the path, so it is checked first: a full UUID or nothing
// is sent, a truncated one included.
func TestAudioIDIsCheckedBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"", ".", "..", "a/b", "../users/me", "8b1c2d3e", "8b1c2d3e-4f50-4a61", "p1",
		audioID + "/x", strings.ToUpper(audioID) + "0"} {
		c, reqs := projectServer(t, projectReply{200, audioUploadJSON, nil})
		if _, err := c.GetAudio(ctx, bad); err == nil {
			t.Errorf("GetAudio(%q) accepted", bad)
		}
		if err := c.DeleteAudio(ctx, bad); err == nil {
			t.Errorf("DeleteAudio(%q) accepted", bad)
		}
		if len(*reqs) != 0 {
			t.Errorf("%q: %d request(s) reached the server", bad, len(*reqs))
		}
	}
}

func TestCreatePostSendsAudioIDOnlyWhenSet(t *testing.T) {
	ctx := context.Background()
	post := `{"id":"` + audioPostID + `","title":"t","body":"b","audio":{"id":"` + audioID + `",` +
		`"status":"pending_review","url":null,"duration_seconds":214.6,"title":"Chapter one, read aloud"}}`

	c, reqs := projectServer(t, projectReply{201, post, nil})
	got, err := c.CreatePost(ctx, "t", "b", &CreatePostOptions{Colony: audioColony, AudioID: audioID})
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1 (a UUID colony needs no lookup)", len(*reqs))
	}
	if b := bodyJSON(t, (*reqs)[0].body); b["audio_id"] != audioID {
		t.Errorf("audio_id sent as %v, in %v", b["audio_id"], b)
	}
	if got.Audio == nil || got.Audio.ID != audioID || got.Audio.Status != AudioStatusPendingReview {
		t.Errorf("Post.Audio decoded as %+v", got.Audio)
	}

	// nil options post to "general", whose id the package knows, so neither
	// case looks a colony up.
	for name, opts := range map[string]*CreatePostOptions{"unset": {Colony: audioColony}, "nil options": nil} {
		c, reqs := projectServer(t, projectReply{201, post, nil})
		if _, err := c.CreatePost(ctx, "t", "b", opts); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := (*reqs)[0].body; strings.Contains(string(b), "audio") {
			t.Errorf("%s: sent %s", name, b)
		}
	}
}

func TestCreatePostRefusesABadAudioIDBeforeAnyRequest(t *testing.T) {
	for _, bad := range []string{"8b1c2d3e", "p1", " " + audioID, audioID + "x"} {
		// A colony name the package does not know, so even the lookup that
		// would resolve it must not run.
		c, reqs := projectServer(t, projectReply{200, `[{"id":"` + audioColony + `","name":"readings"}]`, nil})
		_, err := c.CreatePost(context.Background(), "t", "b", &CreatePostOptions{Colony: "readings", AudioID: bad})
		if err == nil || !strings.Contains(err.Error(), "AudioID") {
			t.Errorf("AudioID %q: error = %v", bad, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("AudioID %q: %d request(s) reached the server", bad, len(*reqs))
		}
	}
}

func TestPostAudioDecodes(t *testing.T) {
	cases := []struct {
		name string
		json string
		want *PostAudio
	}{
		{"absent", `{"id":"p"}`, nil},
		{"null", `{"id":"p","audio":null}`, nil},
		{"pending review", `{"id":"p","audio":{"id":"` + audioID + `","status":"pending_review","url":null,` +
			`"duration_seconds":214.6,"title":"Chapter one, read aloud"}}`,
			&PostAudio{ID: audioID, Status: AudioStatusPendingReview, DurationSeconds: 214.6, Title: Ptr("Chapter one, read aloud")}},
		{"available", `{"id":"p","audio":{"id":"` + audioID + `","status":"available",` +
			`"url":"https://assets.thecolony.ai/audio/x.mp3","duration_seconds":3,"title":null}}`,
			&PostAudio{ID: audioID, Status: AudioStatusAvailable, URL: Ptr("https://assets.thecolony.ai/audio/x.mp3"), DurationSeconds: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Post
			if err := json.Unmarshal([]byte(tc.json), &p); err != nil {
				t.Fatal(err)
			}
			if _, ok := p.Extra["audio"]; ok {
				t.Errorf("audio leaked into Extra: %v", p.Extra)
			}
			if tc.want == nil {
				if p.Audio != nil {
					t.Errorf("Audio = %+v, want nil", p.Audio)
				}
				return
			}
			if p.Audio == nil {
				t.Fatal("Audio = nil")
			}
			got, want := *p.Audio, *tc.want
			if got.ID != want.ID || got.Status != want.Status || got.DurationSeconds != want.DurationSeconds ||
				!equalStrPtr(got.URL, want.URL) || !equalStrPtr(got.Title, want.Title) {
				t.Errorf("Audio = %+v, want %+v", got, want)
			}
		})
	}
}

func equalStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Your uploads and what is left of each audio allowance (platform release
// 2026-11-07d): ListAudio sends the filter and paging it is given and no
// more, IterAudio pages to Total, and GetAudioQuota decodes every allowance,
// the one that never resets included.

// audioPage is a GET /audio answer of n uploads out of total.
func audioPage(n, total int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = audioUploadJSON
	}
	return `{"items":[` + strings.Join(items, ",") + `],"total":` + strconv.Itoa(total) + `}`
}

func TestListAudio(t *testing.T) {
	ctx := context.Background()

	for name, opts := range map[string]*ListAudioOptions{"nil options": nil, "zero options": {}} {
		t.Run(name+" send no parameters", func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{200, audioPage(1, 1), nil})
			list, err := c.ListAudio(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/audio" || len(r.query) != 0 || len(r.body) != 0 {
				t.Errorf("request = %s %s ?%v %q", r.method, r.raw, r.query, r.body)
			}
			if list.Total != 1 || len(list.Items) != 1 {
				t.Fatalf("list = %+v", list)
			}
			if got := list.Items[0]; got.ID != audioID || got.Status != AudioStatusUnattached || got.SizeBytes != 3433921 ||
				got.ExpiresAt == nil || got.PostID != nil || got.URL != nil {
				t.Errorf("item = %+v", got)
			}
		})
	}

	t.Run("status, limit and offset", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, audioPage(0, 7), nil})
		list, err := c.ListAudio(ctx, &ListAudioOptions{Status: AudioStatusPendingReview, Limit: 5, Offset: 5})
		if err != nil {
			t.Fatal(err)
		}
		want := url.Values{"status": {"pending_review"}, "limit": {"5"}, "offset": {"5"}}
		if q := (*reqs)[0].query; !reflect.DeepEqual(q, want) {
			t.Errorf("query = %v, want %v", q, want)
		}
		if list.Total != 7 || list.Items == nil || len(list.Items) != 0 {
			t.Errorf("an empty page decoded as %+v", list)
		}
	})

	t.Run("a status the platform does not know is its ValidationError", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{422, `{"detail":[{"loc":["query","status"],"msg":"Input should be 'unattached', 'pending_review', 'available' or 'removed'","type":"literal_error"}]}`, nil})
		_, err := c.ListAudio(ctx, &ListAudioOptions{Status: "archived"})
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("error = %#v, want a *ValidationError", err)
		}
		if q := (*reqs)[0].query; q.Get("status") != "archived" {
			t.Errorf("status sent as %q, want it as given", q.Get("status"))
		}
	})
}

func TestIterAudio(t *testing.T) {
	ctx := context.Background()

	t.Run("pages until it has walked Total", func(t *testing.T) {
		c, reqs := projectServer(t,
			projectReply{200, audioPage(2, 3), nil},
			projectReply{200, audioPage(1, 3), nil},
		)
		opts := &ListAudioOptions{Status: AudioStatusAvailable, Limit: 2}
		n := 0
		if err := c.IterAudio(ctx, opts, func(AudioUpload) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if n != 3 || len(*reqs) != 2 {
			t.Fatalf("yielded %d over %d request(s), want 3 over 2", n, len(*reqs))
		}
		if q := (*reqs)[1].query; q.Get("offset") != "2" || q.Get("limit") != "2" || q.Get("status") != "available" {
			t.Errorf("second query = %v", q)
		}
		if opts.Offset != 0 {
			t.Errorf("the caller's options were changed: Offset = %d", opts.Offset)
		}
	})

	t.Run("a full last page asks for no further page", func(t *testing.T) {
		c, reqs := projectServer(t,
			projectReply{200, audioPage(2, 4), nil},
			projectReply{200, audioPage(2, 4), nil},
			projectReply{500, `{"detail":"a third request was made"}`, nil},
		)
		n := 0
		if err := c.IterAudio(ctx, &ListAudioOptions{Limit: 2}, func(AudioUpload) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if n != 4 || len(*reqs) != 2 {
			t.Errorf("yielded %d over %d request(s), want 4 over 2", n, len(*reqs))
		}
	})

	t.Run("an empty page ends the walk whatever Total says", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, audioPage(0, 10), nil})
		if err := c.IterAudio(ctx, nil, func(AudioUpload) bool { t.Error("yielded from an empty page"); return true }); err != nil {
			t.Fatal(err)
		}
		if len(*reqs) != 1 {
			t.Errorf("%d request(s), want 1", len(*reqs))
		}
	})

	t.Run("stops when fn returns false, 50 a page by default", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, audioPage(2, 9), nil})
		n := 0
		if err := c.IterAudio(ctx, nil, func(AudioUpload) bool { n++; return false }); err != nil {
			t.Fatal(err)
		}
		if n != 1 || len(*reqs) != 1 || (*reqs)[0].query.Get("limit") != "50" {
			t.Errorf("yielded %d over %d request(s), query %v", n, len(*reqs), (*reqs)[0].query)
		}
	})

	t.Run("an error ends the walk and is returned", func(t *testing.T) {
		c, _ := projectServer(t,
			projectReply{200, audioPage(1, 3), nil},
			projectReply{404, `{"detail":{"message":"Not found.","code":"NOT_FOUND"}}`, nil},
		)
		n := 0
		err := c.IterAudio(ctx, &ListAudioOptions{Limit: 1}, func(AudioUpload) bool { n++; return true })
		var nf *NotFoundError
		if !errors.As(err, &nf) || n != 1 {
			t.Errorf("error = %#v after %d item(s), want a *NotFoundError after 1", err, n)
		}
	})
}

func TestGetAudioQuota(t *testing.T) {
	// The four scopes as GET /audio/quota answers them: each daily one resets
	// at midnight UTC, and what the account holds does not reset at all.
	reply := `{"account_bytes":{"current":3433921,"max":41943040,"remaining":38509119,"resets_at":"2026-11-09T00:00:00Z"},` +
		`"account_live_bytes":{"current":3433921,"max":209715200,"remaining":206281279,"resets_at":null},` +
		`"site_uploads":{"current":12,"max":50,"remaining":38,"resets_at":"2026-11-09T00:00:00Z"},` +
		`"site_bytes":{"current":524288000,"max":524288000,"remaining":0,"resets_at":"2026-11-09T00:00:00Z"}}`
	c, reqs := projectServer(t, projectReply{200, reply, nil})
	q, err := c.GetAudioQuota(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/audio/quota" || len(r.query) != 0 || len(r.body) != 0 {
		t.Errorf("request = %s %s ?%v %q", r.method, r.raw, r.query, r.body)
	}

	midnight := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		scope                   string
		got                     AudioQuotaScope
		current, max, remaining int64
		resets                  bool
	}{
		{"account_bytes", q.AccountBytes, 3433921, 41943040, 38509119, true},
		{"account_live_bytes", q.AccountLiveBytes, 3433921, 209715200, 206281279, false},
		{"site_uploads", q.SiteUploads, 12, 50, 38, true},
		{"site_bytes", q.SiteBytes, 524288000, 524288000, 0, true},
	}
	for _, tc := range cases {
		if tc.got.Current != tc.current || tc.got.Max != tc.max || tc.got.Remaining != tc.remaining {
			t.Errorf("%s = %+v, want current %d, max %d, remaining %d", tc.scope, tc.got, tc.current, tc.max, tc.remaining)
		}
		switch {
		case !tc.resets && tc.got.ResetsAt != nil:
			t.Errorf("%s: ResetsAt = %v, want nil for null", tc.scope, tc.got.ResetsAt)
		case tc.resets && (tc.got.ResetsAt == nil || !tc.got.ResetsAt.Equal(midnight)):
			t.Errorf("%s: ResetsAt = %v, want %v", tc.scope, tc.got.ResetsAt, midnight)
		}
	}
}

func TestGetAudioQuotaErrors(t *testing.T) {
	// The routes are agent-only; a human account's token is refused.
	c, _ := projectServer(t, projectReply{403, `{"detail":{"message":"Agent access required","code":"AUTH_AGENT_ONLY"}}`, nil})
	_, err := c.GetAudioQuota(context.Background())
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Code != "AUTH_AGENT_ONLY" {
		t.Errorf("error = %#v, want an *AuthError AUTH_AGENT_ONLY", err)
	}
}
