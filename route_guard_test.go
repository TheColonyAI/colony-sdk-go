package colony_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every route this package requests must be one the platform declares.
//
// Until 2026-10-21, ReactPost, ReactComment, GetPoll and VotePoll called
// POST /posts/{id}/react, POST /comments/{id}/react, GET /posts/{id}/poll and
// POST /posts/{id}/poll/vote. The platform serves none of them: each answers
// the router's 404, exactly as a made-up path does. Every unit test passed,
// because each mocked the route its own method called, so the mock and the
// method agreed with each other and with nothing else. This test reads the
// route out of the source instead and checks it against every route in the
// OpenAPI document (the "routes" list in testdata/openapi_schemas.json).

// requestHelpers are the Client methods that send a request, with where their
// HTTP method and path arguments sit. method -1 means the helper always sends
// verb.
var requestHelpers = map[string]struct {
	method int
	verb   string
	path   int
}{
	"do":          {method: 1, path: 2},
	"doWithRetry": {method: 1, path: 2},
	"doRaw":       {method: 1, path: 2},
	"orgResult":   {method: 1, path: 2},
	"uploadFile":  {method: -1, verb: "POST", path: 1},
	// answerCognition(ctx, path, token, answer) always POSTs.
	"answerCognition": {method: -1, verb: "POST", path: 1},
	// writeProjectFile(ctx, method, path, body, ifMatch): a project file write.
	"writeProjectFile": {method: 1, path: 2},
}

// forwarders pass a caller's method or path through, so their own request
// calls cannot be read here; their callers are read through requestHelpers.
// Raw is the caller's escape hatch to any path.
var forwarders = map[string]bool{
	"do": true, "doWithRetry": true, "doRaw": true, "orgResult": true, "uploadFile": true,
	"answerCognition": true, "writeProjectFile": true, "Raw": true,
}

// handExpanded are methods whose route this test cannot read, with every route
// each can request, written out by hand from the source. A name that no longer
// exists fails the test, so a renamed method cannot keep a stale expansion.
var handExpanded = map[string][]string{
	// memberAction appends its verb in a branch (moderation.go).
	"memberAction": {
		"POST /colonies/{}/members/{}/promote",
		"POST /colonies/{}/members/{}/demote",
		"DELETE /colonies/{}/members/{}",
	},
	// AuthorRef.path() answers one of two prefixes (notarisation.go).
	"GetUserComments":      {"GET /users/{}/comments", "GET /users/by-username/{}/comments"},
	"GetUserNotarisations": {"GET /users/{}/notarisations", "GET /users/by-username/{}/notarisations"},
}

// undeclaredButServed are routes this package calls that the OpenAPI document
// does not declare but the platform does serve. Each was probed logged out on
// 2026-10-21 and answered 403 "Not authenticated", where a made-up path under
// the same prefix answered the router's 404. An entry no request uses any more
// fails the test, so the list cannot outlive its reason.
var undeclaredButServed = map[string]string{
	"PUT /posts/{}/sentinel-scanned":    "MarkPostScanned; 403 logged out vs 404 for a made-up path, 2026-10-21; colony-sdk-python calls it too",
	"PUT /comments/{}/sentinel-scanned": "MarkCommentScanned; 403 logged out vs 404 for a made-up path, 2026-10-21; colony-sdk-python calls it too",
	"PUT /posts/{}/colony":              "MovePostToColony; 403 logged out vs 404 for a made-up path, 2026-10-21; colony-sdk-python calls it too",
}

type requestedRoute struct {
	fn, route string // route is "METHOD /path", with {} for every value
}

// extractRoutes reads every request call in the Client methods of files. A
// call it cannot read lands in unread, named by its enclosing method.
func extractRoutes(t *testing.T, fset *token.FileSet, files []*ast.File) (routes []requestedRoute, unread []string) {
	t.Helper()
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil || !isClientRecv(fd.Recv) {
				continue
			}
			recv := fd.Recv.List[0].Names[0].Name
			r := &resolver{assigns: assignments(fd.Body), seen: map[string]bool{}}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				x, ok := sel.X.(*ast.Ident)
				h, isHelper := requestHelpers[sel.Sel.Name]
				if !ok || x.Name != recv || !isHelper || len(call.Args) <= h.path {
					return true
				}
				if forwarders[fd.Name.Name] || handExpanded[fd.Name.Name] != nil {
					return true
				}
				verb := h.verb
				if h.method >= 0 {
					v, ok := r.verb(call.Args[h.method])
					if !ok {
						unread = append(unread, fd.Name.Name+" (method)")
						return true
					}
					verb = v
				}
				p, ok := r.path(call.Args[h.path])
				if !ok || !strings.HasPrefix(p, "/") {
					unread = append(unread, fd.Name.Name+" (path)")
					return true
				}
				if i := strings.IndexByte(p, '?'); i >= 0 {
					p = p[:i]
				}
				routes = append(routes, requestedRoute{fd.Name.Name, verb + " " + p})
				return true
			})
		}
	}
	return routes, unread
}

func isClientRecv(fl *ast.FieldList) bool {
	if len(fl.List) != 1 || len(fl.List[0].Names) != 1 {
		return false
	}
	star, ok := fl.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Client"
}

// assignments maps each local name to the expression it was first given with
// := or =. Later "path +=" statements in this package only add a query string,
// which the route drops anyway.
func assignments(body *ast.BlockStmt) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || (as.Tok != token.DEFINE && as.Tok != token.ASSIGN) || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, l := range as.Lhs {
			if id, ok := l.(*ast.Ident); ok {
				if _, seen := out[id.Name]; !seen {
					out[id.Name] = as.Rhs[i]
				}
			}
		}
		return true
	})
	return out
}

type resolver struct {
	assigns map[string]ast.Expr
	seen    map[string]bool
}

func (r *resolver) verb(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" && strings.HasPrefix(x.Sel.Name, "Method") {
			return strings.ToUpper(strings.TrimPrefix(x.Sel.Name, "Method")), true
		}
	case *ast.BasicLit:
		if s, err := strconv.Unquote(x.Value); err == nil {
			return strings.ToUpper(s), true
		}
	case *ast.Ident:
		if a, ok := r.assigns[x.Name]; ok && !r.seen[x.Name] {
			r.seen[x.Name] = true
			defer delete(r.seen, x.Name)
			return r.verb(a)
		}
	}
	return "", false
}

// path renders e as a route: string literals kept, every other piece {}.
func (r *resolver) path(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return r.path(x.X)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := r.path(x.X)
		b, ok2 := r.path(x.Y)
		return a + b, ok1 && ok2
	case *ast.Ident:
		if a, ok := r.assigns[x.Name]; ok && !r.seen[x.Name] {
			r.seen[x.Name] = true
			defer delete(r.seen, x.Name)
			return r.path(a)
		}
		return "{}", true // a parameter or value, e.g. an ID
	case *ast.CallExpr:
		switch name := callName(x); name {
		case "withQuery":
			return r.path(x.Args[0])
		case "vaultFilePath":
			return "/vault/files/{}", true
		case "fmt.Sprintf":
			lit, ok := x.Args[0].(*ast.BasicLit)
			if !ok {
				return "", false
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return "", false
			}
			for _, v := range []string{"%s", "%d", "%v", "%q"} {
				s = strings.ReplaceAll(s, v, "{}")
			}
			return s, true
		}
		return "{}", true // url.PathEscape(id), strconv.Itoa(n), q.Encode(): a value
	}
	return "{}", true
}

func callName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			return id.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// declared reports whether route matches one of the document's routes: same
// verb, same number of segments, and every literal segment of ours equal to
// the document's (a {} of ours matches any segment).
func declared(route string, docRoutes []string) bool {
	verb, path, _ := strings.Cut(route, " ")
	segs := strings.Split(path, "/")
	for _, d := range docRoutes {
		dv, dp, _ := strings.Cut(d, " ")
		if dv != verb {
			continue
		}
		ds := strings.Split(strings.TrimPrefix(dp, "/api/v1"), "/")
		if len(ds) != len(segs) {
			continue
		}
		match := true
		for i := range segs {
			if segs[i] == ds[i] || strings.Contains(segs[i], "{}") || (strings.HasPrefix(ds[i], "{") && strings.HasSuffix(ds[i], "}")) {
				continue
			}
			match = false
			break
		}
		if match {
			return true
		}
	}
	return false
}

func docRoutes(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi_schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Routes []string `json:"routes"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Routes) == 0 {
		t.Fatal("the snapshot has no routes list: run `go generate ./...`")
	}
	return doc.Routes
}

func parsePackage(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return fset, files
}

func TestEveryRequestedRouteIsDeclared(t *testing.T) {
	doc := docRoutes(t)
	fset, files := parsePackage(t)
	routes, unread := extractRoutes(t, fset, files)
	defined := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && isClientRecv(fd.Recv) {
				defined[fd.Name.Name] = true
			}
		}
	}
	for fn, rs := range handExpanded {
		if !defined[fn] {
			t.Errorf("handExpanded lists %s, which is no longer a Client method: remove or update it", fn)
		}
		for _, r := range rs {
			routes = append(routes, requestedRoute{fn, r})
		}
	}

	if len(unread) > 0 {
		sort.Strings(unread)
		t.Errorf("%d request call(s) whose route this test cannot read. Teach the resolver, or make the method a forwarder whose callers it reads:\n  %s",
			len(unread), strings.Join(unread, "\n  "))
	}

	used := map[string]bool{}
	var undeclared []string
	for _, r := range routes {
		if declared(r.route, doc) {
			continue
		}
		if _, ok := undeclaredButServed[r.route]; ok {
			used[r.route] = true
			continue
		}
		undeclared = append(undeclared, r.fn+": "+r.route)
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("%d request(s) go to a route the OpenAPI document does not declare. If the platform really serves it, add it to undeclaredButServed with the evidence:\n  %s",
			len(undeclared), strings.Join(undeclared, "\n  "))
	}
	for r := range undeclaredButServed {
		if !used[r] {
			t.Errorf("undeclaredButServed lists %q, which no request uses any more: remove it", r)
		}
	}
	t.Logf("%d request calls read from %d files; %d declared, %d served but undeclared, %d undeclared, %d unread",
		len(routes), len(files), len(routes)-len(used)-len(undeclared), len(used), len(undeclared), len(unread))
}

// TestTheRouteGuardCanFail runs the guard's own pieces on routes whose answer
// is known, so a guard that passes everything cannot pass this.
func TestTheRouteGuardCanFail(t *testing.T) {
	doc := docRoutes(t)
	for _, dead := range []string{"POST /posts/{}/react", "POST /comments/{}/react", "GET /posts/{}/poll", "POST /posts/{}/poll/vote"} {
		if declared(dead, doc) {
			t.Errorf("%s is not served by the platform, but the matcher calls it declared", dead)
		}
	}
	for _, live := range []string{"POST /reactions/toggle", "GET /polls/{}/results", "POST /polls/{}/vote", "GET /posts/{}"} {
		if !declared(live, doc) {
			t.Errorf("%s is declared, but the matcher misses it", live)
		}
	}

	// A planted method, read by the same extraction: its route must come out
	// as written and be reported as undeclared.
	src := `package colony
func (c *Client) Planted(ctx context.Context, id string) error {
	path := "/posts/" + id + "/zz-planted"
	return c.do(ctx, http.MethodPost, path+"?x=1", nil, nil)
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "planted.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	routes, unread := extractRoutes(t, fset, []*ast.File{f})
	if len(unread) != 0 || len(routes) != 1 || routes[0].route != "POST /posts/{}/zz-planted" {
		t.Fatalf("the planted call was not read as POST /posts/{}/zz-planted: routes %v, unread %v", routes, unread)
	}
	if declared(routes[0].route, doc) {
		t.Error("the planted route is not in the document, but the guard calls it declared")
	}
}
