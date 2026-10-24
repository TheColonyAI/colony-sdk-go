package colony

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// projectReq is one request the stub saw.
type projectReq struct {
	method string
	path   string // decoded, as the server routes it
	raw    string // as sent on the wire, escapes intact
	query  url.Values
	header http.Header
	body   []byte
}

// projectReply is one scripted answer. header is set on the response.
type projectReply struct {
	status int
	body   string
	header map[string]string
}

// projectServer answers each request with the next reply in the script,
// repeating the last, and records every request but the auth exchange.
func projectServer(t *testing.T, script ...projectReply) (*Client, *[]projectReq) {
	t.Helper()
	var (
		mu   sync.Mutex
		reqs []projectReq
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		b, err := io.ReadAll(r.Body) // the whole body: see TestWikiServerRecordsTheWholeBody
		if err != nil {
			t.Errorf("stub: reading request body: %v", err)
		}
		mu.Lock()
		n := len(reqs)
		reqs = append(reqs, projectReq{r.Method, r.URL.Path, r.URL.EscapedPath(), r.URL.Query(), r.Header.Clone(), b})
		mu.Unlock()
		reply := script[len(script)-1]
		if n < len(script) {
			reply = script[n]
		}
		for k, v := range reply.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)
	fast := DefaultRetry()
	fast.BaseDelay, fast.MaxDelay = time.Millisecond, time.Millisecond
	return NewClient("col_x", WithBaseURL(srv.URL), WithRetry(fast)), &reqs
}

const (
	pjFileID  = "a1000000-0000-4000-8000-000000000001"
	pjOwnerID = "6f1c2d3e-4b5a-4c6d-8e7f-901234567801"
	pjID      = "b2000000-0000-4000-8000-000000000001"
)

// bodyJSON decodes a recorded body, or returns nil for an empty one.
func bodyJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", b, err)
	}
	return m
}

// TestProjectRoutes pins every projects method to its verb, path and body.
func TestProjectRoutes(t *testing.T) {
	ctx := context.Background()
	project := string(loadWikiFixture(t, "project.json"))
	file := `{"id":"` + pjFileID + `","filename":"index.html","file_type":"html","version":4,` +
		`"created_at":"2026-10-24T09:00:00Z","updated_at":"2026-10-24T12:00:00Z"}`

	cases := []struct {
		name     string
		reply    projectReply
		call     func(c *Client) error
		method   string
		path     string
		wantBody map[string]any // nil: no body at all
	}{
		{
			name:   "ListProjects",
			reply:  projectReply{200, string(loadWikiFixture(t, "project_list.json")), nil},
			call:   func(c *Client) error { _, err := c.ListProjects(ctx, nil); return err },
			method: http.MethodGet, path: "/projects",
		},
		{
			name:   "GetProject",
			reply:  projectReply{200, project, nil},
			call:   func(c *Client) error { _, err := c.GetProject(ctx, "orbit-clock"); return err },
			method: http.MethodGet, path: "/projects/orbit-clock",
		},
		{
			name:  "CreateProject",
			reply: projectReply{201, project, nil},
			call: func(c *Client) error {
				_, err := c.CreateProject(ctx, ProjectCreate{Name: "Orbit Clock", Slug: "orbit-clock"})
				return err
			},
			method: http.MethodPost, path: "/projects",
			wantBody: map[string]any{"name": "Orbit Clock", "slug": "orbit-clock"},
		},
		{
			name:  "UpdateProject",
			reply: projectReply{200, project, nil},
			call: func(c *Client) error {
				_, err := c.UpdateProject(ctx, "orbit-clock", ProjectUpdate{IsPublished: Ptr(true)})
				return err
			},
			method: http.MethodPatch, path: "/projects/orbit-clock",
			wantBody: map[string]any{"is_published": true},
		},
		{
			name:   "DeleteProject",
			reply:  projectReply{204, "", nil},
			call:   func(c *Client) error { return c.DeleteProject(ctx, "orbit-clock") },
			method: http.MethodDelete, path: "/projects/orbit-clock",
		},
		{
			name:   "RemoveProjectFromColony",
			reply:  projectReply{200, project, nil},
			call:   func(c *Client) error { _, err := c.RemoveProjectFromColony(ctx, "orbit-clock"); return err },
			method: http.MethodDelete, path: "/projects/orbit-clock/colony",
		},
		{
			name:   "GetProjectFile",
			reply:  projectReply{200, string(loadWikiFixture(t, "project_file.json")), nil},
			call:   func(c *Client) error { _, err := c.GetProjectFile(ctx, "orbit-clock", pjFileID); return err },
			method: http.MethodGet, path: "/projects/orbit-clock/files/" + pjFileID,
		},
		{
			name:   "AddProjectFile",
			reply:  projectReply{201, file, nil},
			call:   func(c *Client) error { _, err := c.AddProjectFile(ctx, "orbit-clock", "about.md"); return err },
			method: http.MethodPost, path: "/projects/orbit-clock/files",
			wantBody: map[string]any{"filename": "about.md"},
		},
		{
			name:  "UpdateProjectFile",
			reply: projectReply{200, file, nil},
			call: func(c *Client) error {
				_, err := c.UpdateProjectFile(ctx, "orbit-clock", pjFileID, "<p>hi</p>", nil)
				return err
			},
			method: http.MethodPut, path: "/projects/orbit-clock/files/" + pjFileID,
			wantBody: map[string]any{"content": "<p>hi</p>"},
		},
		{
			name:   "DeleteProjectFile",
			reply:  projectReply{204, "", nil},
			call:   func(c *Client) error { return c.DeleteProjectFile(ctx, "orbit-clock", pjFileID) },
			method: http.MethodDelete, path: "/projects/orbit-clock/files/" + pjFileID,
		},
		{
			name:  "ListProjectFileVersions",
			reply: projectReply{200, string(loadWikiFixture(t, "project_file_versions.json")), nil},
			call: func(c *Client) error {
				_, err := c.ListProjectFileVersions(ctx, "orbit-clock", pjFileID)
				return err
			},
			method: http.MethodGet, path: "/projects/orbit-clock/files/" + pjFileID + "/versions",
		},
		{
			name:  "GetProjectFileVersion",
			reply: projectReply{200, string(loadWikiFixture(t, "project_file_version.json")), nil},
			call: func(c *Client) error {
				_, err := c.GetProjectFileVersion(ctx, "orbit-clock", pjFileID, 2)
				return err
			},
			method: http.MethodGet, path: "/projects/orbit-clock/files/" + pjFileID + "/versions/2",
		},
		{
			name:  "RestoreProjectFileVersion",
			reply: projectReply{200, file, nil},
			call: func(c *Client) error {
				_, err := c.RestoreProjectFileVersion(ctx, "orbit-clock", pjFileID, 2, nil)
				return err
			},
			method: http.MethodPost, path: "/projects/orbit-clock/files/" + pjFileID + "/versions/2/restore",
		},
		{
			name:   "AddProjectCollaborator",
			reply:  projectReply{204, "", nil},
			call:   func(c *Client) error { return c.AddProjectCollaborator(ctx, "orbit-clock", "arch-colony") },
			method: http.MethodPost, path: "/projects/orbit-clock/collaborators/arch-colony",
		},
		{
			name:   "RemoveProjectCollaborator",
			reply:  projectReply{204, "", nil},
			call:   func(c *Client) error { return c.RemoveProjectCollaborator(ctx, "orbit-clock", pjOwnerID) },
			method: http.MethodDelete, path: "/projects/orbit-clock/collaborators/" + pjOwnerID,
		},
		{
			name:   "TransferProject",
			reply:  projectReply{200, project, nil},
			call:   func(c *Client) error { _, err := c.TransferProject(ctx, "orbit-clock", "arch-colony"); return err },
			method: http.MethodPost, path: "/projects/orbit-clock/transfer/arch-colony",
		},
		{
			name: "ReportProject",
			reply: projectReply{201, `{"id":"r1","reporter":{"id":"u1","username":"me"},"colony_id":null,` +
				`"post_id":null,"comment_id":null,"project_id":"` + pjID + `","reason":"spam",` +
				`"description":null,"status":"pending","created_at":"2026-10-24T12:00:00Z"}`, nil},
			call:   func(c *Client) error { _, err := c.ReportProject(ctx, pjID, "spam"); return err },
			method: http.MethodPost, path: "/reports",
			wantBody: map[string]any{"target_type": "project", "target_id": pjID, "reason": "spam"},
		},
	}

	// Every projects method is in the table: the Client methods whose names
	// say Project, counted by reflection, so a new one cannot go unrouted.
	var named []string
	ct := reflect.TypeOf(&Client{})
	for i := 0; i < ct.NumMethod(); i++ {
		if n := ct.Method(i).Name; strings.Contains(n, "Project") && n != "IterProjects" {
			named = append(named, n)
		}
	}
	sort.Strings(named)
	inTable := map[string]bool{}
	for _, tc := range cases {
		inTable[tc.name] = true
	}
	for _, n := range named {
		if !inTable[n] {
			t.Errorf("%s is a projects method with no route case", n)
		}
	}
	if len(cases) != len(named) {
		t.Errorf("%d route cases for %d projects methods %v", len(cases), len(named), named)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, reqs := projectServer(t, tc.reply)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if len(*reqs) != 1 {
				t.Fatalf("%d requests, want 1", len(*reqs))
			}
			r := (*reqs)[0]
			if r.method != tc.method || r.path != tc.path {
				t.Errorf("sent %s %s, want %s %s", r.method, r.path, tc.method, tc.path)
			}
			if len(r.query) != 0 {
				t.Errorf("query %v, want none", r.query)
			}
			got := bodyJSON(t, r.body)
			if !reflect.DeepEqual(got, tc.wantBody) {
				t.Errorf("body = %v, want %v", got, tc.wantBody)
			}
			if tc.wantBody != nil && r.header.Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q", r.header.Get("Content-Type"))
			}
			if im := r.header.Get("If-Match"); im != "" {
				t.Errorf("If-Match = %q with none asked for", im)
			}
		})
	}
}

// TestUpdateProjectColonyIsThreeState is the omit handling. On PATCH
// /projects/{slug}, a colony left out leaves the project where it is, "" takes
// it out of its colony, and a name moves it, so nil, "" and a name must be
// three different requests.
func TestUpdateProjectColonyIsThreeState(t *testing.T) {
	project := string(loadWikiFixture(t, "project.json"))
	cases := []struct {
		name string
		edit ProjectUpdate
		want map[string]any
	}{
		{"nil: not sent, the project stays put", ProjectUpdate{Name: Ptr("Orbit")},
			map[string]any{"name": "Orbit"}},
		{`"": sent, the project leaves its colony`, ProjectUpdate{Colony: Ptr("")},
			map[string]any{"colony": ""}},
		{"a name: sent, the project moves", ProjectUpdate{Colony: Ptr("art")},
			map[string]any{"colony": "art"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{200, project, nil})
			if _, err := c.UpdateProject(context.Background(), "orbit-clock", tc.edit); err != nil {
				t.Fatal(err)
			}
			raw := (*reqs)[0].body
			if got := bodyJSON(t, raw); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("body = %s, want %v", raw, tc.want)
			}
		})
	}

	// The same three states for every field: a zero value that matters
	// ("" clears a description, false unpublishes) is sent, and nil is not.
	t.Run("zero values are sent, nil is not", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, project, nil})
		if _, err := c.UpdateProject(context.Background(), "orbit-clock", ProjectUpdate{
			Description: Ptr(""), IsPublished: Ptr(false),
		}); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"description": "", "is_published": false}
		if got := bodyJSON(t, (*reqs)[0].body); !reflect.DeepEqual(got, want) {
			t.Errorf("body = %s, want %v", (*reqs)[0].body, want)
		}
	})

	t.Run("every field set", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, project, nil})
		if _, err := c.UpdateProject(context.Background(), "orbit-clock", ProjectUpdate{
			Name: Ptr("N"), Description: Ptr("D"), IsPublished: Ptr(true), Colony: Ptr("art"),
		}); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"name": "N", "description": "D", "is_published": true, "colony": "art"}
		if got := bodyJSON(t, (*reqs)[0].body); !reflect.DeepEqual(got, want) {
			t.Errorf("body = %s, want %v", (*reqs)[0].body, want)
		}
	})

	t.Run("an update that sets nothing is refused before any request", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, project, nil})
		if _, err := c.UpdateProject(context.Background(), "orbit-clock", ProjectUpdate{}); err == nil {
			t.Fatal("an empty update was accepted")
		}
		if len(*reqs) != 0 {
			t.Errorf("refused, but %d request(s) were sent", len(*reqs))
		}
	})
}

// TestCreateProjectBody pins the optional fields, which are left out rather
// than sent empty, and the refusals made before any request.
func TestCreateProjectBody(t *testing.T) {
	ctx := context.Background()
	project := string(loadWikiFixture(t, "project.json"))

	t.Run("optional fields set", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{201, project, nil})
		got, err := c.CreateProject(ctx, ProjectCreate{
			Name: "Orbit Clock", Slug: "orbit-clock", Description: Ptr("planets"), Colony: Ptr("art"),
		})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"name": "Orbit Clock", "slug": "orbit-clock", "description": "planets", "colony": "art"}
		if b := bodyJSON(t, (*reqs)[0].body); !reflect.DeepEqual(b, want) {
			t.Errorf("body = %v, want %v", b, want)
		}
		if got.ColonyName == nil || *got.ColonyName != "art" {
			t.Errorf("ColonyName = %v, want art", got.ColonyName)
		}
	})

	for _, tc := range []struct {
		name string
		p    ProjectCreate
		want string
	}{
		{"no slug", ProjectCreate{Name: "N"}, "slug is required"},
		{"capitals", ProjectCreate{Name: "N", Slug: "Orbit"}, "not a project slug"},
		{"double hyphen", ProjectCreate{Name: "N", Slug: "a--b"}, "not a project slug"},
		{"a space", ProjectCreate{Name: "N", Slug: "a b"}, "not a project slug"},
		{"a slash", ProjectCreate{Name: "N", Slug: "a/b"}, "single segment"},
		{"reserved", ProjectCreate{Name: "N", Slug: "new"}, "/projects/new"},
		{"no name", ProjectCreate{Slug: "orbit-clock"}, "name is required"},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{201, project, nil})
			_, err := c.CreateProject(ctx, tc.p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
			if len(*reqs) != 0 {
				t.Errorf("refused, but %d request(s) were sent", len(*reqs))
			}
		})
	}
}

// TestProjectFileIfMatch covers the conditional write: the header is sent
// verbatim when asked for and never otherwise, it rides every retry, the new
// ETag comes back from the response header, and a 412 is typed and carries
// what the server says the file is now.
func TestProjectFileIfMatch(t *testing.T) {
	ctx := context.Background()
	file := `{"id":"` + pjFileID + `","filename":"index.html","file_type":"html","version":4,` +
		`"created_at":"2026-10-24T09:00:00Z","updated_at":"2026-10-24T12:00:00Z"}`
	newTag := `W/"5f0e"` // the server's header reaches clients weakened
	writes := map[string]func(c *Client, ifMatch string) (*ProjectFile, error){
		"UpdateProjectFile": func(c *Client, ifMatch string) (*ProjectFile, error) {
			return c.UpdateProjectFile(ctx, "orbit-clock", pjFileID, "<p>hi</p>", &UpdateProjectFileOptions{IfMatch: ifMatch})
		},
		"RestoreProjectFileVersion": func(c *Client, ifMatch string) (*ProjectFile, error) {
			return c.RestoreProjectFileVersion(ctx, "orbit-clock", pjFileID, 2, &RestoreProjectFileVersionOptions{IfMatch: ifMatch})
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			for _, tag := range []string{`"da416e26"`, `W/"da416e26"`, `"a", "b"`} {
				c, reqs := projectServer(t, projectReply{200, file, map[string]string{"ETag": newTag}})
				got, err := write(c, tag)
				if err != nil {
					t.Fatal(err)
				}
				if im := (*reqs)[0].header.Get("If-Match"); im != tag {
					t.Errorf("If-Match = %q, want %q exactly", im, tag)
				}
				if got.ETag != newTag {
					t.Errorf("ETag = %q, want the response header %q", got.ETag, newTag)
				}
				if got.Version != 4 {
					t.Errorf("Version = %d, want 4", got.Version)
				}
			}

			t.Run("no IfMatch, no header", func(t *testing.T) {
				c, reqs := projectServer(t, projectReply{200, file, nil})
				got, err := write(c, "")
				if err != nil {
					t.Fatal(err)
				}
				if _, sent := (*reqs)[0].header["If-Match"]; sent {
					t.Errorf("If-Match sent as %q with none asked for", (*reqs)[0].header.Get("If-Match"))
				}
				if got.ETag != "" {
					t.Errorf("ETag = %q from a response with no ETag header", got.ETag)
				}
			})

			t.Run("every attempt carries it", func(t *testing.T) {
				c, reqs := projectServer(t, projectReply{503, `{"detail":"busy"}`, nil}, projectReply{200, file, nil})
				if _, err := write(c, `"v1"`); err != nil {
					t.Fatal(err)
				}
				if len(*reqs) != 2 {
					t.Fatalf("%d attempts, want 2 (one 503, one retry)", len(*reqs))
				}
				for i, r := range *reqs {
					if im := r.header.Get("If-Match"); im != `"v1"` {
						t.Errorf("attempt %d If-Match = %q, want \"v1\"", i+1, im)
					}
				}
			})

			t.Run("it does not leak into a later call", func(t *testing.T) {
				c, reqs := projectServer(t, projectReply{200, file, nil}, projectReply{200, string(loadWikiFixture(t, "project_file.json")), nil})
				if _, err := write(c, `"v1"`); err != nil {
					t.Fatal(err)
				}
				if _, err := c.GetProjectFile(ctx, "orbit-clock", pjFileID); err != nil {
					t.Fatal(err)
				}
				if im := (*reqs)[1].header.Get("If-Match"); im != "" {
					t.Errorf("If-Match = %q on a later GetProjectFile", im)
				}
			})

			t.Run("412 is an APIError carrying the current version and etag", func(t *testing.T) {
				c, _ := projectServer(t, projectReply{412, `{"detail":{"message":"index.html was changed since you read it",` +
					`"code":"PRECONDITION_FAILED","version":5,"etag":"\"9c1d\""}}`, nil})
				got, err := write(c, `"v1"`)
				if got != nil {
					t.Errorf("a refused write returned a file: %+v", got)
				}
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("err = %T %v, want *APIError", err, err)
				}
				if apiErr.Status != 412 || apiErr.Code != "PRECONDITION_FAILED" {
					t.Errorf("Status %d Code %q, want 412 PRECONDITION_FAILED", apiErr.Status, apiErr.Code)
				}
				var ve *ValidationError
				var ce *ConflictError
				if errors.As(err, &ve) || errors.As(err, &ce) {
					t.Errorf("a 412 matched a 4xx class it is not: %T", err)
				}
				detail, _ := apiErr.Response["detail"].(map[string]any)
				if detail["version"] != float64(5) || detail["etag"] != `"9c1d"` {
					t.Errorf("detail = %v, want version 5 and etag \"9c1d\"", detail)
				}
			})
		})
	}
}

// TestProjectFileETagIsPerCall shows why the ETag is read per call rather than
// from LastResponseHeaders: concurrent writes on one client each get their own
// response's header. The stub answers each write with an ETag naming the
// content it was sent.
func TestProjectFileETagIsPerCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		time.Sleep(time.Millisecond) // let the calls overlap
		w.Header().Set("ETag", `"`+body.Content+`"`)
		_, _ = w.Write([]byte(`{"id":"` + pjFileID + `","filename":"a.md","file_type":"md","version":2,` +
			`"created_at":"2026-10-24T09:00:00Z","updated_at":"2026-10-24T12:00:00Z"}`))
	}))
	defer srv.Close()
	c := NewClient("col_x", WithBaseURL(srv.URL))

	var wg sync.WaitGroup
	errs := make(chan string, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			content := "c" + strings.Repeat("x", i)
			f, err := c.UpdateProjectFile(context.Background(), "orbit-clock", pjFileID, content, nil)
			if err != nil {
				errs <- err.Error()
				return
			}
			if f.ETag != `"`+content+`"` {
				errs <- "write of " + content + " got ETag " + f.ETag
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestProjectArgumentsAreCheckedBeforeTheRequest covers every value that goes
// into a path. DeleteProject and RemoveProjectCollaborator are why this is a
// refusal and not a server error after the fact: a value carrying "/" stops
// being one segment, and a deletion goes somewhere the caller did not aim it.
func TestProjectArgumentsAreCheckedBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	bySlug := map[string]func(c *Client, slug string) error{
		"GetProject": func(c *Client, s string) error { _, err := c.GetProject(ctx, s); return err },
		"UpdateProject": func(c *Client, s string) error {
			_, err := c.UpdateProject(ctx, s, ProjectUpdate{Name: Ptr("N")})
			return err
		},
		"DeleteProject":           func(c *Client, s string) error { return c.DeleteProject(ctx, s) },
		"RemoveProjectFromColony": func(c *Client, s string) error { _, err := c.RemoveProjectFromColony(ctx, s); return err },
		"GetProjectFile":          func(c *Client, s string) error { _, err := c.GetProjectFile(ctx, s, pjFileID); return err },
		"AddProjectFile":          func(c *Client, s string) error { _, err := c.AddProjectFile(ctx, s, "a.md"); return err },
		"UpdateProjectFile": func(c *Client, s string) error {
			_, err := c.UpdateProjectFile(ctx, s, pjFileID, "x", nil)
			return err
		},
		"DeleteProjectFile": func(c *Client, s string) error { return c.DeleteProjectFile(ctx, s, pjFileID) },
		"ListProjectFileVersions": func(c *Client, s string) error {
			_, err := c.ListProjectFileVersions(ctx, s, pjFileID)
			return err
		},
		"GetProjectFileVersion": func(c *Client, s string) error {
			_, err := c.GetProjectFileVersion(ctx, s, pjFileID, 1)
			return err
		},
		"RestoreProjectFileVersion": func(c *Client, s string) error {
			_, err := c.RestoreProjectFileVersion(ctx, s, pjFileID, 1, nil)
			return err
		},
		"AddProjectCollaborator":    func(c *Client, s string) error { return c.AddProjectCollaborator(ctx, s, "u") },
		"RemoveProjectCollaborator": func(c *Client, s string) error { return c.RemoveProjectCollaborator(ctx, s, "u") },
		"TransferProject":           func(c *Client, s string) error { _, err := c.TransferProject(ctx, s, "u"); return err },
		"CreateProject": func(c *Client, s string) error {
			_, err := c.CreateProject(ctx, ProjectCreate{Name: "N", Slug: s})
			return err
		},
	}
	refuse := func(t *testing.T, what string, call func(c *Client) error) {
		t.Helper()
		c, reqs := projectServer(t, projectReply{200, `{}`, nil})
		if err := call(c); err == nil {
			t.Errorf("%s was accepted", what)
		}
		if len(*reqs) != 0 {
			t.Errorf("%s was refused but still sent %s %s", what, (*reqs)[0].method, (*reqs)[0].raw)
		}
	}

	for _, slug := range []string{"", "orbit/../../users/me", "a/b", ".", "..", strings.Repeat("a", 201)} {
		for name, call := range bySlug {
			call := call
			refuse(t, name+" with slug "+slug, func(c *Client) error { return call(c, slug) })
		}
	}
	for _, id := range []string{"", ".", "..", "../../users/x", "index.html", "a1000000-0000-4000-8000", pjFileID + "/../x"} {
		refuse(t, "GetProjectFile with file id "+id, func(c *Client) error { _, err := c.GetProjectFile(ctx, "p", id); return err })
		refuse(t, "UpdateProjectFile with file id "+id, func(c *Client) error {
			_, err := c.UpdateProjectFile(ctx, "p", id, "x", nil)
			return err
		})
		refuse(t, "DeleteProjectFile with file id "+id, func(c *Client) error { return c.DeleteProjectFile(ctx, "p", id) })
		refuse(t, "ListProjectFileVersions with file id "+id, func(c *Client) error {
			_, err := c.ListProjectFileVersions(ctx, "p", id)
			return err
		})
		refuse(t, "GetProjectFileVersion with file id "+id, func(c *Client) error {
			_, err := c.GetProjectFileVersion(ctx, "p", id, 1)
			return err
		})
		refuse(t, "RestoreProjectFileVersion with file id "+id, func(c *Client) error {
			_, err := c.RestoreProjectFileVersion(ctx, "p", id, 1, nil)
			return err
		})
	}
	for _, n := range []int{0, -1} {
		refuse(t, "GetProjectFileVersion with number "+itoa(n), func(c *Client) error {
			_, err := c.GetProjectFileVersion(ctx, "p", pjFileID, n)
			return err
		})
		refuse(t, "RestoreProjectFileVersion with number "+itoa(n), func(c *Client) error {
			_, err := c.RestoreProjectFileVersion(ctx, "p", pjFileID, n, nil)
			return err
		})
	}
	for _, user := range []string{"", ".", "..", "a/b", "../../users/me", strings.Repeat("u", 65)} {
		refuse(t, "AddProjectCollaborator with user "+user, func(c *Client) error { return c.AddProjectCollaborator(ctx, "p", user) })
		refuse(t, "RemoveProjectCollaborator with user "+user, func(c *Client) error { return c.RemoveProjectCollaborator(ctx, "p", user) })
		refuse(t, "TransferProject with user "+user, func(c *Client) error { _, err := c.TransferProject(ctx, "p", user); return err })
	}
	refuse(t, "AddProjectFile with no filename", func(c *Client) error { _, err := c.AddProjectFile(ctx, "p", ""); return err })
	refuse(t, "ReportProject with a slug for an id", func(c *Client) error { _, err := c.ReportProject(ctx, "orbit-clock", "spam"); return err })

	// The control: values the guards must let through, and how they arrive.
	// A slug the creation grammar refuses may still name a project made on
	// the website before it applied that grammar, so it is sent, escaped
	// into one segment.
	t.Run("valid values ARE sent, escaped into one segment", func(t *testing.T) {
		for _, tc := range []struct{ slug, user, path, raw string }{
			{"orbit-clock", "arch-colony", "/projects/orbit-clock/transfer/arch-colony", "/projects/orbit-clock/transfer/arch-colony"},
			{"Orbit Clock", "a b", "/projects/Orbit Clock/transfer/a b", "/projects/Orbit%20Clock/transfer/a%20b"},
			{"a?x=1", "u#v", "/projects/a?x=1/transfer/u#v", "/projects/a%3Fx=1/transfer/u%23v"},
			{"x", pjOwnerID, "/projects/x/transfer/" + pjOwnerID, "/projects/x/transfer/" + pjOwnerID},
		} {
			c, reqs := projectServer(t, projectReply{200, string(loadWikiFixture(t, "project.json")), nil})
			if _, err := c.TransferProject(ctx, tc.slug, tc.user); err != nil {
				t.Errorf("TransferProject(%q, %q) refused: %v", tc.slug, tc.user, err)
				continue
			}
			r := (*reqs)[0]
			if r.path != tc.path || r.raw != tc.raw {
				t.Errorf("TransferProject(%q, %q) reached %q (sent %q), want %q (%q)", tc.slug, tc.user, r.path, r.raw, tc.path, tc.raw)
			}
			if len(r.query) != 0 {
				t.Errorf("a ? in the slug became a query: %v", r.query)
			}
		}
	})
}

// TestPathSegment pins the one helper every projects path segment goes
// through. url.PathEscape leaves dots alone, so without it DeleteProject("..")
// would send DELETE /projects/.., which resolves to /api/v1/ on the way to the
// server.
func TestPathSegment(t *testing.T) {
	for _, v := range []string{"", ".", "..", "/", "a/b", "../x"} {
		if got, err := pathSegment("v", v); err == nil {
			t.Errorf("pathSegment(%q) = %q, want a refusal", v, got)
		}
	}
	for v, want := range map[string]string{
		"orbit-clock": "orbit-clock",
		"...":         "...", // not a dot segment
		".hidden":     ".hidden",
		"a b":         "a%20b",
		"a?b#c":       "a%3Fb%23c",
		"%2e%2e":      "%252e%252e", // an escaped dot segment stays escaped
	} {
		got, err := pathSegment("v", v)
		if err != nil || got != want {
			t.Errorf("pathSegment(%q) = %q, %v, want %q", v, got, err, want)
		}
	}

	// And the methods that delete: a dot segment never leaves the client.
	ctx := context.Background()
	for _, v := range []string{".", ".."} {
		c, reqs := projectServer(t, projectReply{204, "", nil})
		if err := c.DeleteProject(ctx, v); err == nil {
			t.Errorf("DeleteProject(%q) was accepted", v)
		}
		if err := c.RemoveProjectCollaborator(ctx, "orbit-clock", v); err == nil {
			t.Errorf("RemoveProjectCollaborator(%q) was accepted", v)
		}
		if err := c.DeleteProjectFile(ctx, v, pjFileID); err == nil {
			t.Errorf("DeleteProjectFile(%q, ...) was accepted", v)
		}
		if len(*reqs) != 0 {
			t.Errorf("%q: %d request(s) reached the server, first %s %s", v, len(*reqs), (*reqs)[0].method, (*reqs)[0].raw)
		}
	}
}

// TestListProjectsQuery pins the three filters' wire names.
func TestListProjectsQuery(t *testing.T) {
	reply := projectReply{200, `{"items":[],"total":0,"has_more":false}`, nil}
	for _, tc := range []struct {
		opts *ListProjectsOptions
		want url.Values
	}{
		{nil, url.Values{}},
		{&ListProjectsOptions{}, url.Values{}},
		{&ListProjectsOptions{Colony: "art"}, url.Values{"colony": {"art"}}},
		{&ListProjectsOptions{Colony: "art", Limit: 20, Offset: 40}, url.Values{"colony": {"art"}, "limit": {"20"}, "offset": {"40"}}},
	} {
		c, reqs := projectServer(t, reply)
		if _, err := c.ListProjects(context.Background(), tc.opts); err != nil {
			t.Fatal(err)
		}
		if got := (*reqs)[0].query; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ListProjects(%+v) query = %v, want %v", tc.opts, got, tc.want)
		}
	}
}

// TestProjectDecodesTheServersAnswers decodes answers serialised by the
// server's own response models. testdata/project*.json was produced on
// 2026-10-24 by app/schemas/project.py at datax acf9eff85 (release
// 2026-10-24b), through model_dump(mode="json") as FastAPI does: not fetched
// from production, where that release was not yet live. Every field the
// server sends must be modelled, so Extra must come back empty at every level.
func TestProjectDecodesTheServersAnswers(t *testing.T) {
	noExtra := func(t *testing.T, what string, extra map[string]any) {
		t.Helper()
		if len(extra) != 0 {
			t.Errorf("%s: unmodelled fields %v", what, wikiExtraKeys(extra))
		}
	}

	t.Run("project", func(t *testing.T) {
		var p Project
		if err := json.Unmarshal(loadWikiFixture(t, "project.json"), &p); err != nil {
			t.Fatal(err)
		}
		noExtra(t, "Project", p.Extra)
		noExtra(t, "Project.Creator", p.Creator.Extra)
		if p.Slug != "orbit-clock" || p.Creator.Username != "excelsior" || !p.IsPublished {
			t.Errorf("decoded thin: %+v", p)
		}
		if p.ColonyName == nil || *p.ColonyName != "art" {
			t.Errorf("ColonyName = %v, want art", p.ColonyName)
		}
		if len(p.Files) != 3 || p.Files[0].Filename != "index.html" || p.Files[0].Version != 3 || p.Files[0].FileType != "html" {
			t.Errorf("Files = %+v", p.Files)
		}
		for _, f := range p.Files {
			noExtra(t, "Project.Files", f.Extra)
			if f.ETag != "" {
				t.Errorf("%s: ETag %q from a body that carries none", f.Filename, f.ETag)
			}
		}
		if len(p.Collaborators) != 1 || p.Collaborators[0].Username != "arch-colony" {
			t.Errorf("Collaborators = %+v", p.Collaborators)
		}
		if p.FilesUpdatedAt == nil || p.FilesUpdatedAt.Nanosecond() != 417263000 {
			t.Errorf("FilesUpdatedAt = %v, want the server's microseconds", p.FilesUpdatedAt)
		}
		if p.ViewURL == nil || p.PageURL == "" || p.NotServedReason != nil {
			t.Errorf("urls: page %q view %v not served %v", p.PageURL, p.ViewURL, p.NotServedReason)
		}
	})

	t.Run("listing", func(t *testing.T) {
		var l PaginatedList[ProjectListItem]
		if err := json.Unmarshal(loadWikiFixture(t, "project_list.json"), &l); err != nil {
			t.Fatal(err)
		}
		if len(l.Items) != 2 || l.Total != 5 || l.HasMore == nil || !*l.HasMore {
			t.Fatalf("envelope: %d items, total %d, has_more %v", len(l.Items), l.Total, l.HasMore)
		}
		for _, it := range l.Items {
			noExtra(t, it.Slug, it.Extra)
			noExtra(t, it.Slug+".Creator", it.Creator.Extra)
			if it.FileCount != 3 || it.CreatedAt.IsZero() {
				t.Errorf("%s decoded thin: %+v", it.Slug, it)
			}
		}
		// The draft: every nullable field null, decoding to nil.
		d := l.Items[1]
		if d.IsPublished || d.ColonyName != nil || d.Description != nil || d.ViewURL != nil || d.FilesUpdatedAt != nil {
			t.Errorf("draft's nulls did not decode to nil: %+v", d)
		}
	})

	t.Run("file", func(t *testing.T) {
		var f ProjectFileWithContent
		if err := json.Unmarshal(loadWikiFixture(t, "project_file.json"), &f); err != nil {
			t.Fatal(err)
		}
		noExtra(t, "ProjectFileWithContent", f.Extra)
		if !strings.HasPrefix(f.Content, "<!doctype html>") || f.Version != 3 {
			t.Errorf("decoded thin: %+v", f)
		}
		if len(f.ETag) != 66 || f.ETag[0] != '"' || f.ETag[65] != '"' {
			t.Errorf("ETag = %q, want a quoted sha256", f.ETag)
		}
	})

	t.Run("versions", func(t *testing.T) {
		var vs ProjectFileVersions
		if err := json.Unmarshal(loadWikiFixture(t, "project_file_versions.json"), &vs); err != nil {
			t.Fatal(err)
		}
		noExtra(t, "ProjectFileVersions", vs.Extra)
		if vs.FileID != pjFileID || len(vs.Versions) != 3 {
			t.Fatalf("decoded thin: %+v", vs)
		}
		for _, v := range vs.Versions {
			noExtra(t, "ProjectFileVersion", v.Extra)
			if v.Author != nil {
				noExtra(t, "ProjectFileVersion.Author", v.Author.Extra)
			}
		}
		first, last := vs.Versions[0], vs.Versions[2]
		if first.Author != nil || first.CurrentUntil == nil || first.Current {
			t.Errorf("first version: %+v (a gone author is null)", first)
		}
		if !last.Current || last.CurrentUntil != nil || last.Number != 3 || last.Size == 0 {
			t.Errorf("current version: %+v", last)
		}
	})

	t.Run("one version", func(t *testing.T) {
		var v ProjectFileVersionWithContent
		if err := json.Unmarshal(loadWikiFixture(t, "project_file_version.json"), &v); err != nil {
			t.Fatal(err)
		}
		noExtra(t, "ProjectFileVersionWithContent", v.Extra)
		if v.Number != 2 || v.Author == nil || v.Author.Username != "arch-colony" || v.Content == "" {
			t.Errorf("decoded thin: %+v", v)
		}
	})
}

// TestProjectErrorsAreTyped pins how the documented refusals arrive.
func TestProjectErrorsAreTyped(t *testing.T) {
	ctx := context.Background()
	t.Run("KARMA_TOO_LOW is an AuthError with its code", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{403, `{"detail":{"message":"You need at least 5 karma","code":"KARMA_TOO_LOW"}}`, nil})
		_, err := c.CreateProject(ctx, ProjectCreate{Name: "N", Slug: "n"})
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code != "KARMA_TOO_LOW" {
			t.Errorf("err = %T %v, want *AuthError KARMA_TOO_LOW", err, err)
		}
	})
	t.Run("a held slug is a ConflictError", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{409, `{"detail":{"message":"taken","code":"CONFLICT"}}`, nil})
		_, err := c.CreateProject(ctx, ProjectCreate{Name: "N", Slug: "n"})
		var ce *ConflictError
		if !errors.As(err, &ce) || ce.Code != "CONFLICT" {
			t.Errorf("err = %T %v, want *ConflictError CONFLICT", err, err)
		}
	})
	t.Run("a draft is a NotFoundError", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{404, `{"detail":{"message":"Project not found","code":"NOT_FOUND"}}`, nil})
		p, err := c.GetProject(ctx, "draft")
		var nf *NotFoundError
		if !errors.As(err, &nf) || p != nil {
			t.Errorf("got %v, %T %v, want nil and *NotFoundError", p, err, err)
		}
	})
	t.Run("too many files is a ValidationError with its code", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{400, `{"detail":{"message":"20 files at most","code":"LIMIT_EXCEEDED"}}`, nil})
		f, err := c.AddProjectFile(ctx, "p", "a.md")
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != "LIMIT_EXCEEDED" || f != nil {
			t.Errorf("got %v, %T %v, want nil and *ValidationError LIMIT_EXCEEDED", f, err, err)
		}
	})
	t.Run("a project with a pending report cannot be deleted", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{409, `{"detail":{"message":"a report is pending","code":"CONFLICT"}}`, nil})
		var ce *ConflictError
		if err := c.DeleteProject(ctx, "p"); !errors.As(err, &ce) {
			t.Errorf("err = %T %v, want *ConflictError", err, err)
		}
	})
	t.Run("every value-returning method returns nil on an error", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{404, `{"detail":"nope"}`, nil})
		for name, call := range map[string]func() (any, error){
			"ListProjects":            func() (any, error) { return c.ListProjects(ctx, nil) },
			"GetProject":              func() (any, error) { return c.GetProject(ctx, "p") },
			"CreateProject":           func() (any, error) { return c.CreateProject(ctx, ProjectCreate{Name: "N", Slug: "n"}) },
			"UpdateProject":           func() (any, error) { return c.UpdateProject(ctx, "p", ProjectUpdate{Name: Ptr("N")}) },
			"RemoveProjectFromColony": func() (any, error) { return c.RemoveProjectFromColony(ctx, "p") },
			"GetProjectFile":          func() (any, error) { return c.GetProjectFile(ctx, "p", pjFileID) },
			"AddProjectFile":          func() (any, error) { return c.AddProjectFile(ctx, "p", "a.md") },
			"UpdateProjectFile":       func() (any, error) { return c.UpdateProjectFile(ctx, "p", pjFileID, "x", nil) },
			"ListProjectFileVersions": func() (any, error) { return c.ListProjectFileVersions(ctx, "p", pjFileID) },
			"GetProjectFileVersion":   func() (any, error) { return c.GetProjectFileVersion(ctx, "p", pjFileID, 1) },
			"RestoreProjectFileVersion": func() (any, error) {
				return c.RestoreProjectFileVersion(ctx, "p", pjFileID, 1, nil)
			},
			"TransferProject": func() (any, error) { return c.TransferProject(ctx, "p", "u") },
			"ReportProject":   func() (any, error) { return c.ReportProject(ctx, pjID, "spam") },
		} {
			v, err := call()
			if err == nil {
				t.Errorf("%s: no error from a 404", name)
			}
			if !reflect.ValueOf(v).IsNil() {
				t.Errorf("%s: returned %v alongside the error", name, v)
			}
		}
		for name, call := range map[string]func() error{
			"DeleteProject":             func() error { return c.DeleteProject(ctx, "p") },
			"DeleteProjectFile":         func() error { return c.DeleteProjectFile(ctx, "p", pjFileID) },
			"AddProjectCollaborator":    func() error { return c.AddProjectCollaborator(ctx, "p", "u") },
			"RemoveProjectCollaborator": func() error { return c.RemoveProjectCollaborator(ctx, "p", "u") },
		} {
			if err := call(); err == nil {
				t.Errorf("%s: no error from a 404", name)
			}
		}
	})
}

// TestReportProject pins the report body and the id on the answer.
func TestReportProject(t *testing.T) {
	c, reqs := projectServer(t, projectReply{201, `{"id":"r1","reporter":{"id":"u1","username":"me"},"colony_id":null,` +
		`"post_id":null,"comment_id":null,"project_id":"` + pjID + `","reason":"spam",` +
		`"description":null,"status":"pending","created_at":"2026-10-24T12:00:00Z"}`, nil})
	rep, err := c.ReportProject(context.Background(), pjID, "spam")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"target_type": "project", "target_id": pjID, "reason": "spam"}
	if got := bodyJSON(t, (*reqs)[0].body); !reflect.DeepEqual(got, want) {
		t.Errorf("body = %v, want %v", got, want)
	}
	if rep.ProjectID == nil || *rep.ProjectID != pjID || rep.PostID != nil {
		t.Errorf("ProjectID = %v, PostID = %v", rep.ProjectID, rep.PostID)
	}
}

// TestIterProjectsWalksEveryPage walks pages by offset, stops when told, and
// reports an error rather than ending quietly.
func TestIterProjectsWalksEveryPage(t *testing.T) {
	ctx := context.Background()
	row := func(slug string) string {
		return `{"id":"` + pjID + `","name":"` + slug + `","slug":"` + slug + `","description":null,` +
			`"creator":{"id":"` + pjOwnerID + `","username":"a","display_name":"A"},"is_published":true,` +
			`"colony_name":null,"file_count":3,"created_at":"2026-10-24T09:00:00Z","updated_at":"2026-10-24T09:00:00Z",` +
			`"files_updated_at":null,"page_url":"","view_url":null,"not_served_reason":null}`
	}
	page := func(hasMore bool, slugs ...string) projectReply {
		rows := make([]string, len(slugs))
		for i, s := range slugs {
			rows[i] = row(s)
		}
		more := "false"
		if hasMore {
			more = "true"
		}
		return projectReply{200, `{"items":[` + strings.Join(rows, ",") + `],"total":3,"has_more":` + more + `}`, nil}
	}

	t.Run("walks past the first page, by offset, keeping the filter", func(t *testing.T) {
		c, reqs := projectServer(t, page(true, "a", "b"), page(false, "c"))
		var got []string
		if err := c.IterProjects(ctx, &ListProjectsOptions{Colony: "art", Limit: 2}, func(p ProjectListItem) bool {
			got = append(got, p.Slug)
			return true
		}); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != "a,b,c" {
			t.Errorf("walked %v, want [a b c]", got)
		}
		if len(*reqs) != 2 {
			t.Fatalf("%d requests, want 2", len(*reqs))
		}
		if q := (*reqs)[0].query; q.Get("offset") != "" || q.Get("limit") != "2" || q.Get("colony") != "art" {
			t.Errorf("first page query = %v", q)
		}
		if q := (*reqs)[1].query; q.Get("offset") != "2" || q.Get("colony") != "art" {
			t.Errorf("second page query = %v", q)
		}
	})

	t.Run("a full last page that says has_more false is the end", func(t *testing.T) {
		c, reqs := projectServer(t, page(false, "a", "b"), projectReply{200, `{"items":[],"total":2,"has_more":false}`, nil})
		n := 0
		if err := c.IterProjects(ctx, &ListProjectsOptions{Limit: 2}, func(ProjectListItem) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if len(*reqs) != 1 || n != 2 {
			t.Errorf("%d requests and %d rows, want 1 and 2", len(*reqs), n)
		}
	})

	t.Run("the default page is 50", func(t *testing.T) {
		c, reqs := projectServer(t, page(false, "a"))
		if err := c.IterProjects(ctx, nil, func(ProjectListItem) bool { return true }); err != nil {
			t.Fatal(err)
		}
		if l := (*reqs)[0].query.Get("limit"); l != "50" {
			t.Errorf("limit = %q, want 50", l)
		}
	})

	t.Run("stops when the callback says so", func(t *testing.T) {
		c, reqs := projectServer(t, page(true, "a", "b"))
		n := 0
		if err := c.IterProjects(ctx, &ListProjectsOptions{Limit: 2}, func(ProjectListItem) bool { n++; return false }); err != nil {
			t.Fatal(err)
		}
		if n != 1 || len(*reqs) != 1 {
			t.Errorf("%d rows and %d requests after the callback said stop, want 1 and 1", n, len(*reqs))
		}
	})

	t.Run("an error stops the walk and is reported", func(t *testing.T) {
		c, _ := projectServer(t, page(true, "a", "b"), projectReply{404, `{"detail":"unknown colony"}`, nil})
		n := 0
		err := c.IterProjects(ctx, &ListProjectsOptions{Limit: 2}, func(ProjectListItem) bool { n++; return true })
		if err == nil {
			t.Fatal("a failing page returned nil; a caller cannot tell a complete walk from a truncated one")
		}
		if n != 2 {
			t.Errorf("yielded %d rows, want the 2 before the failure", n)
		}
	})

	t.Run("the caller's options are not mutated", func(t *testing.T) {
		c, _ := projectServer(t, page(true, "a", "b"), page(false, "c"))
		opts := &ListProjectsOptions{Colony: "art", Limit: 2, Offset: 4}
		before := *opts
		if err := c.IterProjects(ctx, opts, func(ProjectListItem) bool { return true }); err != nil {
			t.Fatal(err)
		}
		if *opts != before {
			t.Errorf("options mutated: %+v -> %+v", before, *opts)
		}
	})
}
