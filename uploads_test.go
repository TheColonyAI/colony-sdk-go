package colony

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captured holds what the server actually received, parsed as a real
// multipart form rather than by string-matching the raw body.
type captured struct {
	path        string
	method      string
	contentType string
	fieldName   string
	filename    string
	partType    string
	fileBytes   []byte
	parseErr    error
}

func uploadServer(t *testing.T, reply string) (*Client, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		got.path, got.method = r.URL.Path, r.Method
		got.contentType = r.Header.Get("Content-Type")

		if r.Method == http.MethodPost {
			// Parse it the way the server does: if the boundary in the header
			// does not match the body, this fails.
			mr, err := r.MultipartReader()
			if err != nil {
				got.parseErr = err
			} else if part, err := mr.NextPart(); err != nil {
				got.parseErr = err
			} else {
				got.fieldName = part.FormName()
				got.filename = part.FileName()
				got.partType = part.Header.Get("Content-Type")
				got.fileBytes, got.parseErr = io.ReadAll(part)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL)), got
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))

func TestUploadProfileAvatar(t *testing.T) {
	c, got := uploadServer(t, `{"avatar_path":"/a/1.webp","uploaded_at":"2026-08-25T09:00:00Z",
	  "urls":{"sm":"/a/1-32.webp","md":"/a/1-96.webp","lg":"/a/1-256.webp"}}`)

	res, err := c.UploadProfileAvatar(context.Background(), "me.png", "image/png", pngBytes)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if got.parseErr != nil {
		t.Fatalf("server could not parse the multipart body: %v", got.parseErr)
	}
	if got.method != http.MethodPost || got.path != "/users/me/avatar/upload" {
		t.Errorf("%s %s", got.method, got.path)
	}
	if !strings.HasPrefix(got.contentType, "multipart/form-data;") {
		t.Errorf("Content-Type = %q", got.contentType)
	}
	if got.fieldName != "file" || got.filename != "me.png" || got.partType != "image/png" {
		t.Errorf("part: name=%q filename=%q type=%q", got.fieldName, got.filename, got.partType)
	}
	// Bytes must survive verbatim. An image that arrives re-encoded or
	// truncated is a corrupt avatar the server will happily accept.
	if !bytes.Equal(got.fileBytes, pngBytes) {
		t.Errorf("file bytes altered: got %d bytes, sent %d", len(got.fileBytes), len(pngBytes))
	}
	if res.AvatarPath != "/a/1.webp" || res.URLs["lg"] != "/a/1-256.webp" {
		t.Errorf("result = %+v", res)
	}
}

// The header names the boundary that separates the parts, so the two cannot
// be written independently. Pin that they agree.
func TestMultipartBoundaryMatchesTheHeader(t *testing.T) {
	c, got := uploadServer(t, `{}`)
	if _, err := c.UploadProfileAvatar(context.Background(), "a.png", "image/png", pngBytes); err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(got.contentType)
	if err != nil {
		t.Fatalf("unparseable Content-Type %q: %v", got.contentType, err)
	}
	if params["boundary"] == "" {
		t.Fatal("no boundary in the Content-Type header")
	}
	if got.parseErr != nil {
		t.Fatalf("boundary does not match the body: %v", got.parseErr)
	}
}

// A filename with a quote must not break the Content-Disposition header.
// mime/multipart's CreateFormFile does not escape these.
func TestFilenameQuotesAreEscaped(t *testing.T) {
	c, got := uploadServer(t, `{}`)
	name := `we"ird\name.png`
	if _, err := c.UploadProfileAvatar(context.Background(), name, "image/png", pngBytes); err != nil {
		t.Fatal(err)
	}
	if got.parseErr != nil {
		t.Fatalf("header became unparseable: %v", got.parseErr)
	}
	if got.filename != name {
		t.Errorf("filename = %q, want %q", got.filename, name)
	}
}

// Control for the escaping: the raw bytes must carry the RFC 6266 form, so
// this asserts the encoder rather than the SDK's opinion of it.
func TestEncodedHeaderCarriesTheEscapedForm(t *testing.T) {
	body, err := encodeMultipart("file", `we"ird.png`, "image/png", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body.data), `filename="we\"ird.png"`) {
		t.Errorf("quote not escaped per RFC 6266:\n%s", firstLines(string(body.data), 3))
	}
	// And NOT double-escaped, which is what %q on top of the escaper gives.
	if strings.Contains(string(body.data), `we\\"ird.png`) {
		t.Errorf("filename double-escaped:\n%s", firstLines(string(body.data), 3))
	}
}

// A caller-supplied filename containing a newline would otherwise end the
// header and let the remainder be read as headers of its own.
func TestFilenameNewlinesCannotInjectHeaders(t *testing.T) {
	c, got := uploadServer(t, `{}`)
	evil := "ok.png\r\nX-Injected: yes"
	if _, err := c.UploadProfileAvatar(context.Background(), evil, "image/png", pngBytes); err != nil {
		t.Fatal(err)
	}
	if got.parseErr != nil {
		t.Fatalf("body became unparseable: %v", got.parseErr)
	}
	if strings.ContainsAny(got.filename, "\r\n") {
		t.Errorf("filename kept its line breaks: %q", got.filename)
	}
	if got.filename != "ok.pngX-Injected: yes" {
		t.Errorf("filename = %q", got.filename)
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\r\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}

// An empty part is a well-formed multipart request, so a zero-byte upload
// would be a real upload of nothing rather than an obvious client error.
func TestZeroByteUploadIsRefusedBeforeSending(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/token" {
			sent = true
		}
		_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
	}))
	defer srv.Close()
	c := NewClient("col_x", WithBaseURL(srv.URL))

	if _, err := c.UploadProfileAvatar(context.Background(), "a.png", "image/png", nil); err == nil {
		t.Error("zero bytes accepted")
	}
	if _, err := c.UploadProfileAvatar(context.Background(), "", "image/png", pngBytes); err == nil {
		t.Error("empty filename accepted")
	}
	if sent {
		t.Error("a request was sent for input the client should have refused")
	}
}

func TestUploadMessageAttachment(t *testing.T) {
	c, got := uploadServer(t, `{"id":"at1","mime_type":"image/png","size_bytes":72,
	  "width":10,"height":10,"thumb_url":"/t","full_url":"/f","deduped":true}`)

	res, err := c.UploadMessageAttachment(context.Background(), "shot.png", "image/png", pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/messages/attachments/upload" {
		t.Errorf("path = %s", got.path)
	}
	if res.ID != "at1" || res.Width != 10 || res.FullURL != "/f" {
		t.Errorf("result = %+v", res)
	}
	// Deduped distinguishes "we stored your bytes" from "we already had
	// them" — a retried upload after a timeout is not a duplicate.
	if !res.Deduped {
		t.Error("Deduped lost")
	}
}

// The attachment fetch returns image bytes, not JSON. Decoding them as JSON
// fails on the first byte, so the raw path has to be real.
func TestGetMessageAttachmentReturnsRawBytes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()
	c := NewClient("col_x", WithBaseURL(srv.URL))

	b, err := c.GetMessageAttachment(context.Background(), "at1", "thumb")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !bytes.Equal(b, pngBytes) {
		t.Errorf("got %d bytes, want %d", len(b), len(pngBytes))
	}
	if gotPath != "/messages/attachments/at1/thumb" {
		t.Errorf("path = %s", gotPath)
	}

	// Empty variant means "full", the server's own default.
	if _, err := c.GetMessageAttachment(context.Background(), "at1", ""); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/messages/attachments/at1/full" {
		t.Errorf("default variant path = %s", gotPath)
	}
}

func TestGetMessageAttachmentPropagatesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":{"code":"AUTH_FORBIDDEN","message":"not a participant"}}`))
	}))
	defer srv.Close()
	if _, err := NewClient("col_x", WithBaseURL(srv.URL)).
		GetMessageAttachment(context.Background(), "at1", "full"); err == nil {
		t.Fatal("expected an error for 403")
	}
}

func TestColonyImageUploadsAndRemovals(t *testing.T) {
	// The icon/banner reply carries URLs that are not on SubColony, which is
	// why the result keeps the whole body rather than decoding into it.
	c, got := uploadServer(t, `{"id":"cid","name":"general","icon_url":"/i.webp",
	  "icon_urls":{"sm":"/i-32.webp"}}`)

	res, err := c.UploadColonyIcon(context.Background(),
		"2e549d01-99f2-459f-8924-48b2690b2170", "i.png", "image/png", pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/colonies/2e549d01-99f2-459f-8924-48b2690b2170/icon" {
		t.Errorf("path = %s", got.path)
	}
	if res.Raw["icon_url"] != "/i.webp" {
		t.Errorf("the icon URL the call was made to obtain was dropped: %v", res.Raw)
	}

	// The banner lives at /header on the wire, not /banner.
	if _, err := c.UploadColonyBanner(context.Background(),
		"2e549d01-99f2-459f-8924-48b2690b2170", "b.png", "image/png", pngBytes); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got.path, "/header") {
		t.Errorf("banner path = %s, want .../header", got.path)
	}

	for _, tc := range []struct {
		call func() error
		want string
	}{
		{func() error {
			return c.RemoveColonyIcon(context.Background(), "2e549d01-99f2-459f-8924-48b2690b2170")
		}, "/icon"},
		{func() error {
			return c.RemoveColonyBanner(context.Background(), "2e549d01-99f2-459f-8924-48b2690b2170")
		}, "/header"},
		{func() error { return c.DeleteProfileAvatar(context.Background()) }, "/users/me/avatar/upload"},
	} {
		if err := tc.call(); err != nil {
			t.Fatal(err)
		}
		if got.method != http.MethodDelete || !strings.HasSuffix(got.path, tc.want) {
			t.Errorf("%s %s, want DELETE ...%s", got.method, got.path, tc.want)
		}
	}
}

// A JSON request on the same client must be unaffected by the multipart path.
func TestJSONRequestsStillSendApplicationJSON(t *testing.T) {
	var ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		ct = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"id":"p1"}`))
	}))
	defer srv.Close()
	if _, err := NewClient("col_x", WithBaseURL(srv.URL)).
		CreatePost(context.Background(), "t", "b", nil); err != nil {
		t.Fatal(err)
	}
	if ct != "application/json" {
		t.Errorf("Content-Type = %q on a JSON request", ct)
	}
}

// A GET carries no body, so it must carry no Content-Type either.
func TestBodylessRequestsSendNoContentType(t *testing.T) {
	var ct string
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		ct, seen = r.Header.Get("Content-Type"), true
		_, _ = w.Write([]byte(`{"id":"u1"}`))
	}))
	defer srv.Close()
	if _, err := NewClient("col_x", WithBaseURL(srv.URL)).GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("request never arrived")
	}
	if ct != "" {
		t.Errorf("Content-Type = %q on a bodyless GET", ct)
	}
}
