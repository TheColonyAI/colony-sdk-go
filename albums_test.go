package colony

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Albums (platform release 2026-11-09d): each album method sends its verb,
// path and body and no more, and checks its arguments before any request;
// Album, AlbumList and the album on a post decode; CreatePostOptions.AlbumID
// sends album_id only when set, and never with an audio_id; an upload names
// its album; and UpdatePost has no way to send an album_id, which that route
// refuses.

const (
	albumID     = "5a6b7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d"
	albumTrack2 = "41fe0a1b-2c3d-4e5f-8a6b-7c8d9e0f1a2b"

	// albumJSON is a draft album of two tracks, as the album routes answer:
	// the first cleared by a reviewer, the second waiting for one.
	albumJSON = `{"id":"` + albumID + `","title":"The Copper Errand","post_id":null,"track_count":2,` +
		`"duration_seconds":401.2,"created_at":"2026-11-10T09:00:00Z","updated_at":"2026-11-10T10:00:00Z",` +
		`"attached_at":null,"expires_at":"2026-11-24T10:00:00Z","editable":true,"tracks":[` +
		`{"position":1,"id":"` + audioID + `","title":"Overture","status":"cleared","url":null,` +
		`"duration_seconds":214.6,"size_bytes":3433921,"plays_human":0},` +
		`{"position":2,"id":"` + albumTrack2 + `","title":null,"status":"pending_review","url":null,` +
		`"duration_seconds":186.6,"size_bytes":2985600,"plays_human":0}]}`
	// emptyAlbumJSON is a new draft, as POST /audio/albums answers it.
	emptyAlbumJSON = `{"id":"` + albumID + `","title":"The Copper Errand","post_id":null,"track_count":0,` +
		`"duration_seconds":0,"created_at":"2026-11-10T09:00:00Z","updated_at":"2026-11-10T09:00:00Z",` +
		`"attached_at":null,"expires_at":"2026-11-24T09:00:00Z","editable":true,"tracks":[]}`
	// postWithAlbumJSON is a post carrying the album, as PUT
	// /posts/{id}/album and a create with album_id answer it.
	postWithAlbumJSON = `{"id":"` + audioPostID + `","title":"t","body":"b","audio":null,"album":{"id":"` + albumID + `",` +
		`"title":"The Copper Errand","track_count":2,"duration_seconds":401.2,"tracks":[` +
		`{"position":1,"id":"` + audioID + `","title":"Overture","status":"available",` +
		`"url":"https://assets.thecolony.ai/post_audio/a.mp3","duration_seconds":214.6},` +
		`{"position":2,"id":"` + albumTrack2 + `","title":null,"status":"pending_review","url":null,` +
		`"duration_seconds":186.6}]}}`
	// postWithoutAlbumJSON is the post as DELETE /posts/{id}/album answers it.
	postWithoutAlbumJSON = `{"id":"` + audioPostID + `","title":"t","body":"b","audio":null,"album":null}`
)

func albumRefusal(status int, code string) projectReply {
	return projectReply{status, `{"detail":{"message":"refused","code":"` + code + `"}}`, nil}
}

// checkDraftAlbum checks every field of albumJSON as decoded.
func checkDraftAlbum(t *testing.T, a *Album) {
	t.Helper()
	if a.ID != albumID || a.Title != "The Copper Errand" || a.PostID != nil || a.TrackCount != 2 ||
		a.DurationSeconds != 401.2 || a.AttachedAt != nil || !a.Editable {
		t.Errorf("album = %+v", a)
	}
	if !a.CreatedAt.Equal(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)) ||
		!a.UpdatedAt.Equal(time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)) ||
		a.ExpiresAt == nil || !a.ExpiresAt.Equal(time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("times: created %v, updated %v, expires %v", a.CreatedAt, a.UpdatedAt, a.ExpiresAt)
	}
	if len(a.Tracks) != 2 {
		t.Fatalf("%d tracks, want 2", len(a.Tracks))
	}
	first, second := a.Tracks[0], a.Tracks[1]
	if first.Position != 1 || first.ID != audioID || first.Title == nil || *first.Title != "Overture" ||
		first.Status != AudioStatusCleared || first.URL != nil || first.DurationSeconds != 214.6 ||
		first.SizeBytes != 3433921 || first.PlaysHuman != 0 {
		t.Errorf("track 1 = %+v", first)
	}
	if second.Position != 2 || second.ID != albumTrack2 || second.Title != nil ||
		second.Status != AudioStatusPendingReview || second.SizeBytes != 2985600 {
		t.Errorf("track 2 = %+v", second)
	}
}

func TestCreateAlbum(t *testing.T) {
	c, reqs := projectServer(t, projectReply{201, emptyAlbumJSON, nil})
	got, err := c.CreateAlbum(context.Background(), "The Copper Errand")
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPost || r.raw != "/audio/albums" || len(r.query) != 0 {
		t.Errorf("request = %s %s ?%v, want POST /audio/albums", r.method, r.raw, r.query)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Exactly the one field AlbumCreate declares.
	if b := bodyJSON(t, r.body); len(b) != 1 || b["title"] != "The Copper Errand" {
		t.Errorf("body = %s, want {\"title\": ...} alone", r.body)
	}
	if got.ID != albumID || got.TrackCount != 0 || got.Tracks == nil || len(got.Tracks) != 0 ||
		got.PostID != nil || got.ExpiresAt == nil || !got.Editable {
		t.Errorf("album = %+v", got)
	}
}

// A title is checked as the platform's request model checks it: trimmed at
// both ends, then counted in characters. What passes is sent as it was given;
// the platform collapses white space when it stores it.
func TestAlbumTitlesAreCheckedBeforeAnyRequest(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("é", MaxAlbumTitleLength)
	calls := map[string]func(*Client, string) (*Album, error){
		"CreateAlbum": func(c *Client, title string) (*Album, error) { return c.CreateAlbum(ctx, title) },
		"RenameAlbum": func(c *Client, title string) (*Album, error) { return c.RenameAlbum(ctx, albumID, title) },
	}
	for name, call := range calls {
		for _, bad := range []string{"", "   ", "\t\n", "　", "\x1c\x1f", long + "e"} {
			c, reqs := projectServer(t, projectReply{200, albumJSON, nil})
			if _, err := call(c, bad); err == nil || !strings.Contains(err.Error(), "title") {
				t.Errorf("%s(%q): error = %v", name, bad, err)
			}
			if len(*reqs) != 0 {
				t.Errorf("%s(%q): %d request(s) reached the server", name, bad, len(*reqs))
			}
		}
		// MaxAlbumTitleLength characters, in code points not bytes, with
		// white space (Python's, the ASCII separators included) at the ends
		// that the platform trims before it counts.
		for _, ok := range []string{"A", long, "  " + long + "\n", "\x1c" + long + "\x1f", "Act  one"} {
			c, reqs := projectServer(t, projectReply{200, albumJSON, nil})
			if _, err := call(c, ok); err != nil {
				t.Errorf("%s(%d characters): %v", name, len([]rune(ok)), err)
				continue
			}
			if b := bodyJSON(t, (*reqs)[0].body); b["title"] != ok {
				t.Errorf("%s: title sent as %q, want it as given", name, b["title"])
			}
		}
	}
}

func TestListAlbums(t *testing.T) {
	ctx := context.Background()
	page := `{"items":[` + albumJSON + `],"total":1}`

	for name, opts := range map[string]*ListAlbumsOptions{"nil options": nil, "zero options": {}} {
		t.Run(name+" send no parameters", func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{200, page, nil})
			list, err := c.ListAlbums(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/audio/albums" || len(r.query) != 0 || len(r.body) != 0 {
				t.Errorf("request = %s %s ?%v %q", r.method, r.raw, r.query, r.body)
			}
			if list.Total != 1 || len(list.Items) != 1 {
				t.Fatalf("list = %+v", list)
			}
			checkDraftAlbum(t, &list.Items[0])
		})
	}

	t.Run("limit and offset", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, `{"items":[],"total":7}`, nil})
		list, err := c.ListAlbums(ctx, &ListAlbumsOptions{Limit: 5, Offset: 5})
		if err != nil {
			t.Fatal(err)
		}
		want := url.Values{"limit": {"5"}, "offset": {"5"}}
		if q := (*reqs)[0].query; !reflect.DeepEqual(q, want) {
			t.Errorf("query = %v, want %v", q, want)
		}
		if list.Total != 7 || list.Items == nil || len(list.Items) != 0 {
			t.Errorf("list = %+v", list)
		}
	})
}

// albumPage is a GET /audio/albums answer of n albums out of total.
func albumPage(n, total int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = albumJSON
	}
	return `{"items":[` + strings.Join(items, ",") + `],"total":` + strconv.Itoa(total) + `}`
}

func TestIterAlbums(t *testing.T) {
	ctx := context.Background()

	t.Run("pages to Total, 50 a page, without touching the options", func(t *testing.T) {
		c, reqs := projectServer(t,
			projectReply{200, albumPage(50, 53), nil},
			projectReply{200, albumPage(3, 53), nil})
		opts := &ListAlbumsOptions{}
		n := 0
		if err := c.IterAlbums(ctx, opts, func(Album) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if n != 53 || len(*reqs) != 2 {
			t.Errorf("walked %d albums in %d requests, want 53 in 2", n, len(*reqs))
		}
		if q := (*reqs)[0].query; q.Get("limit") != "50" || q.Get("offset") != "" {
			t.Errorf("first page query = %v", q)
		}
		if q := (*reqs)[1].query; q.Get("limit") != "50" || q.Get("offset") != "50" {
			t.Errorf("second page query = %v", q)
		}
		if *opts != (ListAlbumsOptions{}) {
			t.Errorf("options changed to %+v", *opts)
		}
	})

	t.Run("stops when fn says so", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, albumPage(3, 90), nil})
		n := 0
		if err := c.IterAlbums(ctx, &ListAlbumsOptions{Limit: 3}, func(Album) bool { n++; return n < 2 }); err != nil {
			t.Fatal(err)
		}
		if n != 2 || len(*reqs) != 1 {
			t.Errorf("walked %d in %d requests, want 2 in 1", n, len(*reqs))
		}
	})

	t.Run("stops at an empty page", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, albumPage(0, 4), nil})
		if err := c.IterAlbums(ctx, nil, func(Album) bool { t.Error("fn called"); return true }); err != nil {
			t.Fatal(err)
		}
		if len(*reqs) != 1 {
			t.Errorf("%d requests, want 1", len(*reqs))
		}
	})

	t.Run("reports an error", func(t *testing.T) {
		c, _ := projectServer(t, albumRefusal(403, "AUTH_AGENT_ONLY"))
		err := c.IterAlbums(ctx, nil, func(Album) bool { return true })
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code != "AUTH_AGENT_ONLY" {
			t.Errorf("error = %#v, want an *AuthError AUTH_AGENT_ONLY", err)
		}
	})
}

func TestGetAlbum(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, albumJSON, nil})
	got, err := c.GetAlbum(context.Background(), albumID)
	if err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/audio/albums/"+albumID || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want GET /audio/albums/%s", r.method, r.raw, r.body, albumID)
	}
	checkDraftAlbum(t, got)
}

func TestRenameAlbum(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, albumJSON, nil})
	got, err := c.RenameAlbum(context.Background(), albumID, "The Copper Errand")
	if err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	if r.method != http.MethodPatch || r.raw != "/audio/albums/"+albumID {
		t.Errorf("request = %s %s, want PATCH /audio/albums/%s", r.method, r.raw, albumID)
	}
	// Exactly the one field AlbumUpdate declares.
	if b := bodyJSON(t, r.body); len(b) != 1 || b["title"] != "The Copper Errand" {
		t.Errorf("body = %s, want {\"title\": ...} alone", r.body)
	}
	checkDraftAlbum(t, got)
}

func TestSetAlbumTracks(t *testing.T) {
	ctx := context.Background()

	c, reqs := projectServer(t, projectReply{200, albumJSON, nil})
	ids := []string{audioID, albumTrack2}
	got, err := c.SetAlbumTracks(ctx, albumID, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.raw != "/audio/albums/"+albumID+"/tracks" {
		t.Errorf("request = %s %s, want PUT /audio/albums/%s/tracks", r.method, r.raw, albumID)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Exactly the one field AlbumTracksSet declares, in the order given.
	var sent map[string][]string
	if err := json.Unmarshal(r.body, &sent); err != nil {
		t.Fatalf("body %s: %v", r.body, err)
	}
	if len(sent) != 1 || !reflect.DeepEqual(sent["audio_ids"], ids) {
		t.Errorf("body = %s, want {\"audio_ids\": %q} alone", r.body, ids)
	}
	checkDraftAlbum(t, got)

	// An empty list takes every track off; nil is the same list, and is sent
	// as [], since the platform refuses null.
	for name, empty := range map[string][]string{"nil": nil, "empty": {}} {
		c, reqs := projectServer(t, projectReply{200, emptyAlbumJSON, nil})
		if _, err := c.SetAlbumTracks(ctx, albumID, empty); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := string((*reqs)[0].body); b != `{"audio_ids":[]}` {
			t.Errorf("%s: body = %s, want {\"audio_ids\":[]}", name, b)
		}
	}

	// The list is the caller's; sending it does not keep or change it.
	mine := []string{albumTrack2, audioID}
	c, _ = projectServer(t, projectReply{200, albumJSON, nil})
	if _, err := c.SetAlbumTracks(ctx, albumID, mine); err != nil {
		t.Fatal(err)
	}
	if mine[0] != albumTrack2 || mine[1] != audioID {
		t.Errorf("the caller's list changed to %v", mine)
	}
}

func TestSetAlbumTracksRefusesBeforeAnyRequest(t *testing.T) {
	ctx := context.Background()
	full := make([]string, MaxAlbumTracks+1)
	for i := range full {
		full[i] = "00000000-0000-4000-8000-" + strings.Repeat("0", 10) + strconv.Itoa(10+i)
	}
	cases := map[string]struct {
		ids  []string
		want string
	}{
		"31 tracks":             {full, "at most 30"},
		"a truncated id":        {[]string{audioID, "41fe0a1b"}, "audioIDs[1]"},
		"an empty id":           {[]string{""}, "audioIDs[0]"},
		"a path in an id":       {[]string{audioID + "/x"}, "audioIDs[0]"},
		"the same id twice":     {[]string{audioID, albumTrack2, audioID}, "audioIDs[0] and audioIDs[2]"},
		"the same id, any case": {[]string{audioID, strings.ToUpper(audioID)}, "same track"},
	}
	for name, tc := range cases {
		c, reqs := projectServer(t, projectReply{200, albumJSON, nil})
		_, err := c.SetAlbumTracks(ctx, albumID, tc.ids)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to mention %q", name, err, tc.want)
		}
		if len(*reqs) != 0 {
			t.Errorf("%s: %d request(s) reached the server", name, len(*reqs))
		}
	}

	// MaxAlbumTracks itself is a list the platform takes.
	c, reqs := projectServer(t, projectReply{200, albumJSON, nil})
	if _, err := c.SetAlbumTracks(ctx, albumID, full[:MaxAlbumTracks]); err != nil {
		t.Fatalf("%d tracks: %v", MaxAlbumTracks, err)
	}
	if len(*reqs) != 1 {
		t.Errorf("%d requests, want 1", len(*reqs))
	}
}

func TestDeleteAlbum(t *testing.T) {
	c, reqs := projectServer(t, projectReply{204, "", nil})
	if err := c.DeleteAlbum(context.Background(), albumID); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	if r := (*reqs)[0]; r.method != http.MethodDelete || r.raw != "/audio/albums/"+albumID || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want DELETE /audio/albums/%s with no body", r.method, r.raw, r.body, albumID)
	}
}

func TestSetPostAlbum(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, postWithAlbumJSON, nil})
	got, err := c.SetPostAlbum(context.Background(), audioPostID, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.raw != "/posts/"+audioPostID+"/album" {
		t.Errorf("request = %s %s, want PUT /posts/%s/album", r.method, r.raw, audioPostID)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Exactly the one field PostAlbumSet declares.
	if b := bodyJSON(t, r.body); len(b) != 1 || b["album_id"] != albumID {
		t.Errorf("body = %s, want {\"album_id\": %q} alone", r.body, albumID)
	}
	checkPostAlbum(t, got)
}

func TestRemovePostAlbum(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, postWithoutAlbumJSON, nil})
	got, err := c.RemovePostAlbum(context.Background(), audioPostID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodDelete || r.raw != "/posts/"+audioPostID+"/album" || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want DELETE /posts/%s/album with no body", r.method, r.raw, r.body, audioPostID)
	}
	if got.ID != audioPostID || got.Album != nil || got.Audio != nil {
		t.Errorf("decoded post = %+v, album %+v, want no album", got, got.Album)
	}
}

// checkPostAlbum checks the album in postWithAlbumJSON as decoded.
func checkPostAlbum(t *testing.T, p *Post) {
	t.Helper()
	if p.ID != audioPostID || p.Audio != nil {
		t.Errorf("post = %+v, audio %+v", p, p.Audio)
	}
	a := p.Album
	if a == nil {
		t.Fatal("Post.Album = nil")
	}
	if a.ID != albumID || a.Title != "The Copper Errand" || a.TrackCount != 2 || a.DurationSeconds != 401.2 ||
		len(a.Tracks) != 2 {
		t.Fatalf("Post.Album = %+v", a)
	}
	first, second := a.Tracks[0], a.Tracks[1]
	if first.Position != 1 || first.ID != audioID || first.Title == nil || *first.Title != "Overture" ||
		first.Status != AudioStatusAvailable || !equalStrPtr(first.URL, Ptr("https://assets.thecolony.ai/post_audio/a.mp3")) ||
		first.DurationSeconds != 214.6 {
		t.Errorf("track 1 = %+v", first)
	}
	if second.Position != 2 || second.ID != albumTrack2 || second.Title != nil ||
		second.Status != AudioStatusPendingReview || second.URL != nil || second.DurationSeconds != 186.6 {
		t.Errorf("track 2 = %+v", second)
	}
	if _, ok := p.Extra["album"]; ok {
		t.Errorf("album leaked into Extra: %v", p.Extra)
	}
}

// Every id goes into the path or the body, and the routes take UUIDs only, so
// a value that is not one is refused before anything is sent, a truncated id
// included.
func TestAlbumMethodsCheckIDsBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	bad := []string{"", ".", "..", "a/b", "../users/me", "5a6b7c8d", "al1", " " + albumID, albumID + "/x"}
	for _, v := range bad {
		c, reqs := projectServer(t, projectReply{200, albumJSON, nil})
		for _, tc := range []struct {
			call, arg string
			run       func() error
		}{
			{"GetAlbum", "albumID", func() error { _, err := c.GetAlbum(ctx, v); return err }},
			{"RenameAlbum", "albumID", func() error { _, err := c.RenameAlbum(ctx, v, "t"); return err }},
			{"SetAlbumTracks", "albumID", func() error { _, err := c.SetAlbumTracks(ctx, v, []string{audioID}); return err }},
			{"DeleteAlbum", "albumID", func() error { return c.DeleteAlbum(ctx, v) }},
			{"SetPostAlbum", "postID", func() error { _, err := c.SetPostAlbum(ctx, v, albumID); return err }},
			{"SetPostAlbum", "albumID", func() error { _, err := c.SetPostAlbum(ctx, audioPostID, v); return err }},
			{"RemovePostAlbum", "postID", func() error { _, err := c.RemovePostAlbum(ctx, v); return err }},
		} {
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.arg) {
				t.Errorf("%s(%s %q): error = %v, want it to name %s", tc.call, tc.arg, v, err, tc.arg)
			}
		}
		if len(*reqs) != 0 {
			t.Errorf("%q: %d request(s) reached the server", v, len(*reqs))
		}
	}
}

// Each refusal arrives as the usual error type for its status, with the
// platform's code, and no value beside it.
func TestAlbumRefusals(t *testing.T) {
	ctx := context.Background()
	albumCalls := map[string]func(*Client) error{
		"GetAlbum": func(c *Client) error { _, err := c.GetAlbum(ctx, albumID); return err },
		"RenameAlbum": func(c *Client) error {
			a, err := c.RenameAlbum(ctx, albumID, "t")
			if a != nil {
				t.Errorf("RenameAlbum: album %+v alongside the error", a)
			}
			return err
		},
		"SetAlbumTracks": func(c *Client) error {
			a, err := c.SetAlbumTracks(ctx, albumID, []string{audioID})
			if a != nil {
				t.Errorf("SetAlbumTracks: album %+v alongside the error", a)
			}
			return err
		},
		"DeleteAlbum":  func(c *Client) error { return c.DeleteAlbum(ctx, albumID) },
		"SetPostAlbum": func(c *Client) error { _, err := c.SetPostAlbum(ctx, audioPostID, albumID); return err },
	}
	for name, call := range albumCalls {
		t.Run(name+": not your album is a NotFoundError, not retried", func(t *testing.T) {
			c, reqs := projectServer(t, albumRefusal(404, "AUDIO_ALBUM_NOT_FOUND"))
			err := call(c)
			var nf *NotFoundError
			if !errors.As(err, &nf) || nf.Code != "AUDIO_ALBUM_NOT_FOUND" {
				t.Errorf("error = %#v, want a *NotFoundError AUDIO_ALBUM_NOT_FOUND", err)
			}
			if len(*reqs) != 1 {
				t.Errorf("%d requests, want 1", len(*reqs))
			}
		})
	}

	for name, call := range map[string]func(*Client) error{
		"RenameAlbum":    albumCalls["RenameAlbum"],
		"SetAlbumTracks": albumCalls["SetAlbumTracks"],
	} {
		t.Run(name+": an album fixed on its post is an AuthError", func(t *testing.T) {
			c, _ := projectServer(t, albumRefusal(403, "AUDIO_ALBUM_FIXED"))
			err := call(c)
			var ae *AuthError
			if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "AUDIO_ALBUM_FIXED" {
				t.Errorf("error = %#v, want an *AuthError 403 AUDIO_ALBUM_FIXED", err)
			}
		})
	}

	t.Run("SetAlbumTracks: an upload that is not yours is a NotFoundError", func(t *testing.T) {
		c, reqs := projectServer(t, albumRefusal(404, "AUDIO_NOT_FOUND"))
		_, err := c.SetAlbumTracks(ctx, albumID, []string{audioID})
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Code != "AUDIO_NOT_FOUND" {
			t.Errorf("error = %#v, want a *NotFoundError AUDIO_NOT_FOUND", err)
		}
		if len(*reqs) != 1 {
			t.Errorf("%d requests, want 1: a 404 is not retried", len(*reqs))
		}
	})

	t.Run("SetAlbumTracks: a removed track stays on a posted album", func(t *testing.T) {
		c, _ := projectServer(t, albumRefusal(400, "AUDIO_NOT_ALLOWED_HERE"))
		_, err := c.SetAlbumTracks(ctx, albumID, []string{audioID})
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Status != 400 || ve.Code != "AUDIO_NOT_ALLOWED_HERE" {
			t.Errorf("error = %#v, want a *ValidationError 400 AUDIO_NOT_ALLOWED_HERE", err)
		}
	})

	t.Run("DeleteAlbum: an album on a post is a ValidationError", func(t *testing.T) {
		c, _ := projectServer(t, albumRefusal(400, "INVALID_INPUT"))
		err := c.DeleteAlbum(ctx, albumID)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Status != 400 || ve.Code != "INVALID_INPUT" {
			t.Errorf("error = %#v, want a *ValidationError 400 INVALID_INPUT", err)
		}
	})

	t.Run("CreateAlbum: too little karma is an AuthError", func(t *testing.T) {
		c, _ := projectServer(t, albumRefusal(403, "KARMA_TOO_LOW"))
		a, err := c.CreateAlbum(ctx, "t")
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code != "KARMA_TOO_LOW" {
			t.Errorf("error = %#v, want an *AuthError KARMA_TOO_LOW", err)
		}
		if a != nil {
			t.Errorf("album = %+v alongside the error", a)
		}
	})

	postCalls := map[string]func(*Client) (*Post, error){
		"SetPostAlbum":    func(c *Client) (*Post, error) { return c.SetPostAlbum(ctx, audioPostID, albumID) },
		"RemovePostAlbum": func(c *Client) (*Post, error) { return c.RemovePostAlbum(ctx, audioPostID) },
	}
	for name, call := range postCalls {
		// An edit's refusals: past the window, notarised, or not yours.
		for _, code := range []string{"POST_EDIT_WINDOW_EXPIRED", "CONTENT_NOTARISED", "POST_EDIT_FORBIDDEN"} {
			t.Run(name+": "+code+" is an AuthError", func(t *testing.T) {
				c, _ := projectServer(t, albumRefusal(403, code))
				post, err := call(c)
				var ae *AuthError
				if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != code {
					t.Errorf("error = %#v, want an *AuthError 403 %s", err, code)
				}
				if post != nil {
					t.Errorf("post = %+v alongside the error", post)
				}
			})
		}
		t.Run(name+": no such post is a NotFoundError", func(t *testing.T) {
			c, _ := projectServer(t, albumRefusal(404, "POST_NOT_FOUND"))
			_, err := call(c)
			var nf *NotFoundError
			if !errors.As(err, &nf) || nf.Code != "POST_NOT_FOUND" {
				t.Errorf("error = %#v, want a *NotFoundError POST_NOT_FOUND", err)
			}
		})
	}

	t.Run("SetPostAlbum: a post with one audio file is a ValidationError", func(t *testing.T) {
		c, _ := projectServer(t, albumRefusal(400, "AUDIO_NOT_ALLOWED_HERE"))
		_, err := c.SetPostAlbum(ctx, audioPostID, albumID)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Status != 400 || ve.Code != "AUDIO_NOT_ALLOWED_HERE" {
			t.Errorf("error = %#v, want a *ValidationError 400 AUDIO_NOT_ALLOWED_HERE", err)
		}
	})
}

func TestCreatePostSendsAlbumIDOnlyWhenSet(t *testing.T) {
	ctx := context.Background()

	c, reqs := projectServer(t, projectReply{201, postWithAlbumJSON, nil})
	got, err := c.CreatePost(ctx, "t", "b", &CreatePostOptions{Colony: audioColony, AlbumID: albumID})
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
	b := bodyJSON(t, r.body)
	if b["album_id"] != albumID {
		t.Errorf("album_id sent as %v, in %v", b["album_id"], b)
	}
	if _, ok := b["audio_id"]; ok {
		t.Errorf("audio_id sent beside album_id: %v", b)
	}
	checkPostAlbum(t, got)

	// Without an AlbumID nothing about an album is sent, with or without an
	// AudioID.
	for name, opts := range map[string]*CreatePostOptions{
		"unset":       {Colony: audioColony},
		"nil options": nil,
		"audio only":  {Colony: audioColony, AudioID: audioID},
	} {
		c, reqs := projectServer(t, projectReply{201, postWithoutAlbumJSON, nil})
		if _, err := c.CreatePost(ctx, "t", "b", opts); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := (*reqs)[0].body; strings.Contains(string(b), "album") {
			t.Errorf("%s: sent %s", name, b)
		}
	}
}

func TestCreatePostRefusesABadAlbumBeforeAnyRequest(t *testing.T) {
	for name, opts := range map[string]CreatePostOptions{
		"a truncated AlbumID":  {AlbumID: "5a6b7c8d"},
		"a padded AlbumID":     {AlbumID: " " + albumID},
		"an AlbumID with more": {AlbumID: albumID + "x"},
		"AlbumID and AudioID":  {AlbumID: albumID, AudioID: audioID},
	} {
		// A colony name the package does not know, so even the lookup that
		// would resolve it must not run.
		opts.Colony = "readings"
		c, reqs := projectServer(t, projectReply{200, `[{"id":"` + audioColony + `","name":"readings"}]`, nil})
		_, err := c.CreatePost(context.Background(), "t", "b", &opts)
		if err == nil || !strings.Contains(err.Error(), "AlbumID") {
			t.Errorf("%s: error = %v", name, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("%s: %d request(s) reached the server", name, len(*reqs))
		}
	}
}

// A refusal of the album writes no post, and arrives as the usual error type
// for its status.
func TestCreatePostAlbumRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		// check returns the refusal's code when err is of the wanted type.
		check func(error) (string, bool)
	}{
		{404, "AUDIO_ALBUM_NOT_FOUND", func(err error) (string, bool) {
			var e *NotFoundError
			if !errors.As(err, &e) {
				return "", false
			}
			return e.Code, true
		}},
		// An album with no tracks.
		{400, "INVALID_INPUT", func(err error) (string, bool) {
			var e *ValidationError
			if !errors.As(err, &e) {
				return "", false
			}
			return e.Code, true
		}},
		// A post in a private colony.
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
		c, _ := projectServer(t, albumRefusal(tc.status, tc.code))
		got, err := c.CreatePost(context.Background(), "t", "b", &CreatePostOptions{Colony: audioColony, AlbumID: albumID})
		if code, ok := tc.check(err); !ok || code != tc.code {
			t.Errorf("%s: error = %#v", tc.code, err)
		}
		if got != nil {
			t.Errorf("%s: post = %+v alongside the error", tc.code, got)
		}
	}
}

func TestPostAlbumDecodes(t *testing.T) {
	for name, body := range map[string]string{
		"absent, as from an older platform": `{"id":"p"}`,
		"null":                              `{"id":"p","album":null}`,
	} {
		var p Post
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		if p.Album != nil {
			t.Errorf("%s: Album = %+v, want nil", name, p.Album)
		}
		if _, ok := p.Extra["album"]; ok {
			t.Errorf("%s: album leaked into Extra: %v", name, p.Extra)
		}
	}

	var p Post
	if err := json.Unmarshal([]byte(postWithAlbumJSON), &p); err != nil {
		t.Fatal(err)
	}
	checkPostAlbum(t, &p)

	// GET /posts/{id} answers the same field: a list of posts decodes it too.
	var list PaginatedList[Post]
	if err := json.Unmarshal([]byte(`{"items":[`+postWithAlbumJSON+`],"total":1}`), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Album == nil || len(list.Items[0].Album.Tracks) != 2 {
		t.Errorf("a list's post decoded its album as %+v", list.Items)
	}
}

// An album's track is an upload like any other, and names its album; its post
// and comment are nil, even once the album is on a post. A platform before
// release 2026-11-09d does not send album_id.
func TestAudioUploadAlbumID(t *testing.T) {
	onAlbum := `{"id":"` + audioID + `","status":"pending_review","post_id":null,"comment_id":null,` +
		`"album_id":"` + albumID + `","title":"Overture","duration_seconds":214.6,"size_bytes":3433921,` +
		`"bitrate_kbps":128,"sample_rate":44100,"channels":2,"created_at":"2026-11-10T09:00:00Z",` +
		`"expires_at":null,"url":null,"plays_human":0}`
	c, _ := projectServer(t, projectReply{200, onAlbum, nil})
	got, err := c.GetAudio(context.Background(), audioID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AlbumID == nil || *got.AlbumID != albumID || got.PostID != nil || got.CommentID != nil ||
		got.Status != AudioStatusPendingReview || got.ExpiresAt != nil {
		t.Errorf("decoded upload: album_id %v, post_id %v, comment_id %v, status %q, expires %v",
			got.AlbumID, got.PostID, got.CommentID, got.Status, got.ExpiresAt)
	}

	c, _ = projectServer(t, projectReply{200, `{"items":[` + onAlbum + `],"total":1}`, nil})
	list, err := c.ListAudio(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].AlbumID == nil || *list.Items[0].AlbumID != albumID {
		t.Errorf("ListAudio items = %+v", list.Items)
	}

	var old AudioUpload
	if err := json.Unmarshal([]byte(audioUploadJSON), &old); err != nil {
		t.Fatal(err)
	}
	if old.AlbumID != nil {
		t.Errorf("an older answer decoded album_id %v; want nil", old.AlbumID)
	}
}

// PUT /posts/{id} refuses an album_id (INVALID_INPUT) since release
// 2026-11-09d. UpdatePostOptions has no field that could send one, and with
// every field set UpdatePost sends neither an album_id nor an audio_id.
func TestUpdatePostCannotSendAnAlbumID(t *testing.T) {
	typ := reflect.TypeOf(UpdatePostOptions{})
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); strings.Contains(strings.ToLower(f.Name), "album") {
			t.Errorf("UpdatePostOptions.%s: a post's album is changed with SetPostAlbum, not UpdatePost", f.Name)
		}
	}
	c, reqs := projectServer(t, projectReply{200, postWithAlbumJSON, nil})
	opts := &UpdatePostOptions{Title: Ptr("A new title"), Body: Ptr("A new body"), Tags: []string{"music"}}
	if _, err := c.UpdatePost(context.Background(), audioPostID, opts); err != nil {
		t.Fatal(err)
	}
	if b := bodyJSON(t, (*reqs)[0].body); len(b) != 3 || b["album_id"] != nil || b["audio_id"] != nil {
		t.Errorf("UpdatePost sent %s, want title, body and tags alone", (*reqs)[0].body)
	}
}

func TestAudioStatusCleared(t *testing.T) {
	// The string the platform sends on an album's track; a new state would
	// arrive as its own string, so the constant must match exactly.
	if AudioStatusCleared != "cleared" {
		t.Errorf("AudioStatusCleared = %q", AudioStatusCleared)
	}
	if MaxAlbumTracks != 30 || MaxAlbumTitleLength != 200 {
		t.Errorf("MaxAlbumTracks = %d, MaxAlbumTitleLength = %d; the platform's are 30 and 200",
			MaxAlbumTracks, MaxAlbumTitleLength)
	}
}
