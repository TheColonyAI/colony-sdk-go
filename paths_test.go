package colony

// The path guard, tested three ways.
//
// TestEveryPathValueIsGuarded reads the source. Every request call in the
// package has its path argument taken apart, and every piece of it that is
// not a string literal, a number or part of the query string must come from
// one of the guards in pathGuards. A method that puts a raw parameter, or
// url.PathEscape of one, into a path fails it, so a new method cannot skip the
// guard.
//
// TestPathGuardRefusesBeforeAnyRequest calls every method that builds a path
// from a caller's value, found by the same reading rather than listed by hand.
// For each such parameter, "", "." and ".." (and a "/", except in a vault
// filename) must be refused with no request made, the token exchange
// included; and the controls ("...", "a.b", ".well-known", a UUID, a username
// with a hyphen) must still be sent, escaped into one segment.
//
// TestPathSegment and TestVaultFilePath pin the helpers themselves.

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type pathKind int

const (
	kindSegment pathKind = iota // any one segment
	kindUUID                    // also checked to be a UUID
	kindSlug                    // also checked against a slug grammar
	kindColony                  // a colony name or UUID, resolved to the UUID
	kindVault                   // a vault filename, which may name folders
	kindAuthor                  // an AuthorRef, whose Username or UserID is the segment
)

func (k pathKind) String() string {
	return [...]string{"segment", "uuid", "slug", "colony", "vault", "author"}[k]
}

// pathGuards are the functions whose first result is a value checked for the
// path. Each takes the caller's value as its LAST argument, except
// AuthorRef.path, whose receiver is the value. Every one of them but
// pathSegment itself must call pathSegment or another guard, and every
// function in the package named *Segment must be listed here: both are
// checked, so a new helper cannot bypass the guard by name.
var pathGuards = map[string]pathKind{
	"pathSegment":        kindSegment,
	"projectSlugSegment": kindSegment,
	"userSegment":        kindSegment,
	"uuidSegment":        kindUUID,
	"fileIDSegment":      kindUUID,
	"wikiSlugSegment":    kindSlug,
	"orgSlugSegment":     kindSlug,
	"colonySegment":      kindColony,
	"vaultFilePath":      kindVault,
	"path":               kindAuthor, // AuthorRef.path
}

// trustedPathValues are path pieces that are not a caller's value, with why.
var trustedPathValues = map[string]string{
	"memberAction verb": `one of the literals its three callers pass: "promote", "demote" or ""`,
}

// emptyIsDefault are guarded parameters for which "" is not refused, because
// the method sends a default in its place, with what it sends.
var emptyIsDefault = map[string]string{
	"GetMessageAttachment.variant": `"full", as documented`,
}

// requestBases are the functions that build a request URL themselves, with
// the index of their path argument. Every other function that sends a request
// does so through one of these, directly or through a forwarder.
var requestBases = map[string]int{"doRaw": 2, "oauthFormPost": 1}

type pkgSource struct {
	fset  *token.FileSet
	files []*ast.File
}

func parseSource(t *testing.T) pkgSource {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	src := pkgSource{fset: token.NewFileSet()}
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(src.fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		src.files = append(src.files, f)
	}
	return src
}

func (s pkgSource) funcs() []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, f := range s.files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				out = append(out, fd)
			}
		}
	}
	return out
}

func recvType(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return ""
	}
	switch x := fd.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return "*" + id.Name
		}
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// paramNames lists fd's parameters in order; for a Client method, index i is
// reflect's In(i) on the bound method value.
func paramNames(fd *ast.FuncDecl) []string {
	var out []string
	for _, f := range fd.Type.Params.List {
		if len(f.Names) == 0 {
			out = append(out, "_")
		}
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

func paramIndex(fd *ast.FuncDecl, name string) int {
	for i, n := range paramNames(fd) {
		if n == name {
			return i
		}
	}
	return -1
}

func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func calleeQualified(c *ast.CallExpr) string {
	if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			return id.Name + "." + sel.Sel.Name
		}
	}
	return calleeName(c)
}

// requestHelpers derives, from requestBases, every function that sends a
// request with a path it is given, and where that path argument sits: a
// function that passes one of its own parameters as a helper's path is a
// helper too (do, doWithRetry, Raw, orgResult, uploadFile, ...).
func requestHelpers(src pkgSource) map[string]int {
	helpers := map[string]int{}
	for k, v := range requestBases {
		helpers[k] = v
	}
	for changed := true; changed; {
		changed = false
		for _, fd := range src.funcs() {
			if _, done := helpers[fd.Name.Name]; done || fd.Recv == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				pi, isHelper := helpers[calleeName(call)]
				if !isHelper || len(call.Args) <= pi {
					return true
				}
				if id, ok := call.Args[pi].(*ast.Ident); ok {
					if i := paramIndex(fd, id.Name); i >= 0 {
						helpers[fd.Name.Name] = i
						changed = true
						return false
					}
				}
				return true
			})
		}
	}
	return helpers
}

// sources maps each local name to every expression assigned to it in body:
// "x := e", "x = e" and "x += e" give e; "x, err := f()" gives the call.
type assignment struct {
	expr  ast.Expr
	multi bool // the first of several results of a call
	plus  bool // x += e
}

func sources(body *ast.BlockStmt) map[string][]assignment {
	out := map[string][]assignment{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if len(as.Lhs) == len(as.Rhs) {
			for i, l := range as.Lhs {
				if id, ok := l.(*ast.Ident); ok {
					out[id.Name] = append(out[id.Name], assignment{expr: as.Rhs[i], plus: as.Tok == token.ADD_ASSIGN})
				}
			}
			return true
		}
		if len(as.Rhs) == 1 {
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				out[id.Name] = append(out[id.Name], assignment{expr: as.Rhs[0], multi: true})
			}
		}
		return true
	})
	return out
}

func flattenAdd(e ast.Expr) []ast.Expr {
	switch x := e.(type) {
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			return append(flattenAdd(x.X), flattenAdd(x.Y)...)
		}
	case *ast.ParenExpr:
		return flattenAdd(x.X)
	}
	return []ast.Expr{e}
}

// pathChecker reports the pieces of a path expression that do not come from
// a guard.
type pathChecker struct {
	fset    *token.FileSet
	fn      string
	params  map[string]bool
	assigns map[string][]assignment
	seen    map[string]bool
	trusted map[string]bool // trustedPathValues entries seen
	bad     []string
}

func (pc *pathChecker) check(e ast.Expr) {
	if call, ok := e.(*ast.CallExpr); ok && calleeName(call) == "withQuery" && len(call.Args) == 2 {
		pc.check(call.Args[0]) // the second argument is the query
		return
	}
	for _, piece := range flattenAdd(e) {
		if lit, ok := piece.(*ast.BasicLit); ok {
			if s, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(s, "?") {
				return // the rest is the query string
			}
			continue
		}
		pc.piece(piece)
	}
}

func (pc *pathChecker) piece(e ast.Expr) {
	switch x := e.(type) {
	case *ast.Ident:
		if _, ok := trustedPathValues[pc.fn+" "+x.Name]; ok {
			pc.trusted[pc.fn+" "+x.Name] = true
			return
		}
		as, ok := pc.assigns[x.Name]
		if pc.params[x.Name] || !ok {
			pc.bad = append(pc.bad, x.Name)
			return
		}
		if pc.seen[x.Name] {
			return
		}
		pc.seen[x.Name] = true
		for _, a := range as {
			switch {
			case a.multi:
				call, ok := a.expr.(*ast.CallExpr)
				if _, guard := pathGuards[calleeName(call)]; !ok || !guard {
					pc.bad = append(pc.bad, x.Name+" (from a call that is not a path guard)")
				}
			default:
				pc.check(a.expr) // "path := ..." and "path += ..." alike
			}
		}
	case *ast.CallExpr:
		switch calleeQualified(x) {
		case "strconv.Itoa", "strconv.FormatInt", "strconv.FormatUint":
			return // a number is never a dot segment and holds no "/"
		case "withQuery":
			pc.check(x)
			return
		}
		pc.bad = append(pc.bad, exprString(pc.fset, x))
	default:
		pc.bad = append(pc.bad, exprString(pc.fset, x))
	}
}

func exprString(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return fmt.Sprintf("%T", e)
	}
	return b.String()
}

func TestEveryPathValueIsGuarded(t *testing.T) {
	src := parseSource(t)
	helpers := requestHelpers(src)

	// The bases are the only places a URL is built.
	for _, fd := range src.funcs() {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if q := calleeQualified(call); strings.HasPrefix(q, "http.NewRequest") {
					if _, ok := requestBases[fd.Name.Name]; !ok {
						t.Errorf("%s builds a request itself (%s): add it to requestBases and check its callers", fd.Name.Name, q)
					}
				}
			}
			return true
		})
	}

	// The guards: each listed one exists and goes through pathSegment, and
	// nothing named like a guard is missing from the list.
	defined := map[string]*ast.FuncDecl{}
	for _, fd := range src.funcs() {
		name := fd.Name.Name
		if name == "path" && recvType(fd) != "AuthorRef" {
			t.Errorf("%s.path is a method named like AuthorRef.path, which this test treats as a path guard", recvType(fd))
		}
		if strings.HasSuffix(name, "Segment") || name == "vaultFilePath" {
			if _, ok := pathGuards[name]; !ok {
				t.Errorf("%s looks like a path guard but is not in pathGuards", name)
			}
		}
		if _, ok := pathGuards[name]; ok {
			defined[name] = fd
		}
	}
	for name := range pathGuards {
		fd, ok := defined[name]
		if !ok {
			t.Errorf("pathGuards lists %s, which no longer exists", name)
			continue
		}
		if name == "pathSegment" {
			continue
		}
		calls := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if _, ok := pathGuards[calleeName(call)]; ok && calleeName(call) != name {
					calls = true
				}
			}
			return !calls
		})
		if !calls {
			t.Errorf("%s is listed as a path guard but calls neither pathSegment nor another guard", name)
		}
	}

	checked := 0
	trusted := map[string]bool{}
	for _, fd := range src.funcs() {
		if _, base := requestBases[fd.Name.Name]; base {
			continue
		}
		pi, isHelper := helpers[fd.Name.Name]
		assigns := sources(fd.Body)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			hi, ok := helpers[calleeName(call)]
			if !ok || len(call.Args) <= hi {
				return true
			}
			arg := call.Args[hi]
			// A forwarder passing on the path it was given: its callers are
			// checked instead.
			if id, ok := arg.(*ast.Ident); ok && isHelper && paramIndex(fd, id.Name) == pi {
				return true
			}
			params := map[string]bool{}
			for _, n := range paramNames(fd) {
				params[n] = true
			}
			pc := &pathChecker{fset: src.fset, fn: fd.Name.Name, params: params, assigns: assigns, seen: map[string]bool{}, trusted: trusted}
			pc.check(arg)
			checked++
			for _, b := range pc.bad {
				t.Errorf("%s: the path includes %s, which did not come through a path guard (pathSegment, or one of pathGuards)", fd.Name.Name, b)
			}
			return true
		})
	}
	for k := range trustedPathValues {
		if !trusted[k] {
			t.Errorf("trustedPathValues lists %q, which no path uses any more: remove it", k)
		}
	}
	if checked < 200 {
		t.Errorf("only %d request calls were checked; the reader has stopped seeing them", checked)
	}
	t.Logf("%d request calls checked; helpers %v", checked, helpers)
}

// guardedParam is one parameter of an exported Client method that goes into
// a request path.
type guardedParam struct {
	method string
	index  int // reflect In(index) of the bound method
	name   string
	kind   pathKind
}

// guardedParams reads, for every Client method, the parameters it passes to a
// path guard. An unexported Client method that guards its own parameters
// (memberAction) passes them on to its callers.
func guardedParams(t *testing.T, src pkgSource) []guardedParam {
	t.Helper()
	type key struct {
		method string
		index  int
	}
	direct := map[string]map[int]pathKind{}
	methods := map[string]*ast.FuncDecl{}
	for _, fd := range src.funcs() {
		if recvType(fd) != "*Client" {
			continue
		}
		methods[fd.Name.Name] = fd
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			kind, ok := pathGuards[calleeName(call)]
			if !ok {
				return true
			}
			var value ast.Expr
			if kind == kindAuthor {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					value = sel.X
				}
			} else if len(call.Args) > 0 {
				value = call.Args[len(call.Args)-1]
			}
			if id, ok := value.(*ast.Ident); ok {
				if i := paramIndex(fd, id.Name); i >= 0 {
					if direct[fd.Name.Name] == nil {
						direct[fd.Name.Name] = map[int]pathKind{}
					}
					direct[fd.Name.Name][i] = kind
				}
			}
			return true
		})
	}
	all := map[key]pathKind{}
	for m, ps := range direct {
		for i, k := range ps {
			all[key{m, i}] = k
		}
	}
	for name, fd := range methods {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee := calleeName(call)
			if _, guard := pathGuards[callee]; guard || ast.IsExported(callee) || direct[callee] == nil {
				return true // a guard's own kind was read above
			}
			if _, isMethod := methods[callee]; !isMethod {
				return true
			}
			for i, a := range call.Args {
				id, ok := a.(*ast.Ident)
				if !ok {
					continue
				}
				if k, ok := direct[callee][i]; ok {
					if j := paramIndex(fd, id.Name); j >= 0 {
						all[key{name, j}] = k
					}
				}
			}
			return true
		})
	}
	var out []guardedParam
	for k, kind := range all {
		if !ast.IsExported(k.method) {
			continue
		}
		out = append(out, guardedParam{k.method, k.index, paramNames(methods[k.method])[k.index], kind})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].method != out[j].method {
			return out[i].method < out[j].method
		}
		return out[i].index < out[j].index
	})
	return out
}

const (
	pathTestUUID   = "0b5e1f3c-0000-4000-8000-0000000000b2"
	pathTestColony = "0b5e1f3c-0000-4000-8000-0000000000c3" // what "user-name" resolves to
	pathTestX1     = "0b5e1f3c-0000-4000-8000-0000000000d4" // what "x1", the name fill, resolves to
)

// pathControls are values each kind of parameter must still send, mapped to
// the one segment each must arrive as.
func pathControls(kind pathKind) map[string]string {
	segment := map[string]string{
		"...":         "...",
		"a.b":         "a.b",
		".well-known": ".well-known",
		pathTestUUID:  pathTestUUID,
		"user-name":   "user-name",
		"a b":         "a%20b",
		"a?b#c":       "a%3Fb%23c",  // not a query, not a fragment (#73)
		"%2e%2e":      "%252e%252e", // an escaped dot segment stays escaped
	}
	switch kind {
	case kindUUID:
		return map[string]string{pathTestUUID: pathTestUUID}
	case kindSlug:
		return map[string]string{"user-name": "user-name", pathTestUUID: pathTestUUID}
	case kindColony:
		return map[string]string{pathTestUUID: pathTestUUID, "general": Colonies["general"], "user-name": pathTestColony}
	case kindVault:
		segment["notes/2026/aug.md"] = "notes%2F2026%2Faug.md"
		segment["notes/.well-known/a.b"] = "notes%2F.well-known%2Fa.b"
	}
	return segment
}

// pathRefusals are values each kind of parameter must refuse with no request.
func pathRefusals(kind pathKind) []string {
	if kind == kindVault {
		return []string{"", ".", "..", "a/../b", "/a", "a/", "a//b", "./a", "a/..", "../users/me"}
	}
	return []string{"", ".", "..", "/", "a/b", "../users/me"}
}

type pathRequest struct{ method, escaped string }

// pathServer answers every request and records each one, the token exchange
// included.
type pathServer struct {
	*httptest.Server
	mu    sync.Mutex
	reqs  []pathRequest
	calls int
}

func newPathServer(t *testing.T) *pathServer {
	t.Helper()
	ps := &pathServer{}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		ps.mu.Lock()
		ps.reqs = append(ps.reqs, pathRequest{r.Method, r.URL.EscapedPath()})
		ps.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/auth/token":
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
		case r.URL.Path == "/colonies" && r.Method == http.MethodGet:
			// Name lookups resolve, so a method taking a colony name goes on
			// to its own request.
			_, _ = fmt.Fprintf(w, `[{"id":%q,"name":"user-name"},{"id":%q,"name":"x1"}]`, pathTestColony, pathTestX1)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *pathServer) take() []pathRequest {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := ps.reqs
	ps.reqs = nil
	return out
}

// callWith calls the method with value in the guarded parameter and the
// others filled by recorderFill (mode "zero": left at their zero values), and
// returns what reached the server.
func (ps *pathServer) callWith(p guardedParam, value, mode string, minimal bool) (reqs []pathRequest, panicked any, err error) {
	// A key of its own, so no token is cached from an earlier call: a refusal
	// that came after the token exchange would show as a request.
	ps.mu.Lock()
	ps.calls++
	key := fmt.Sprintf("col_paths_%d", ps.calls)
	ps.mu.Unlock()
	c := NewClient(key, WithBaseURL(ps.URL), WithRetry(RetryConfig{}), WithTimeout(2*time.Second))
	m := reflect.ValueOf(c).MethodByName(p.method)
	mt := m.Type()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := []reflect.Value{reflect.ValueOf(ctx)}
	for j := 1; j < mt.NumIn(); j++ {
		in := mt.In(j)
		if mt.IsVariadic() && j == mt.NumIn()-1 {
			in = in.Elem()
		}
		if mode == "zero" {
			args = append(args, reflect.New(in).Elem())
			continue
		}
		args = append(args, recorderFill(in, mode, 0, minimal))
	}
	switch arg := args[p.index]; {
	case arg.Kind() == reflect.String:
		arg.SetString(value)
	case arg.Type() == reflect.TypeOf(AuthorRef{}):
		args[p.index] = reflect.ValueOf(AuthorRef{Username: value})
	default:
		panic(fmt.Sprintf("%s.%s is a %s; teach callWith how to set it", p.method, p.name, arg.Type()))
	}
	ps.take()
	func() {
		defer func() { panicked = recover() }()
		for _, r := range m.Call(args) {
			if e, ok := r.Interface().(error); ok && e != nil {
				err = e
			}
		}
	}()
	return ps.take(), panicked, err
}

// sentAs reports whether one of reqs, other than the token exchange and a
// colony lookup, has want as one of its path segments.
func sentAs(reqs []pathRequest, want string) bool {
	for _, r := range reqs {
		if r.escaped == "/auth/token" || (r.method == http.MethodGet && r.escaped == "/colonies") {
			continue
		}
		for _, seg := range strings.Split(r.escaped, "/") {
			if seg == want {
				return true
			}
		}
	}
	return false
}

var pathFillModes = []struct {
	mode    string
	minimal bool
}{{"id", true}, {"name", true}, {"id", false}, {"name", false}, {"zero", false}}

func TestPathGuardRefusesBeforeAnyRequest(t *testing.T) {
	params := guardedParams(t, parseSource(t))
	// A floor, so an analysis that stops finding methods cannot pass by
	// testing none. Measured 2026-10-24: 210 parameters of 176 methods.
	if len(params) < 210 {
		t.Fatalf("only %d guarded parameters found; the reader has stopped seeing them", len(params))
	}
	for k := range emptyIsDefault {
		found := false
		for _, p := range params {
			found = found || p.method+"."+p.name == k
		}
		if !found {
			t.Errorf("emptyIsDefault lists %s, which is not a guarded parameter: remove it", k)
		}
	}
	ps := newPathServer(t)
	methods := map[string]bool{}
	for _, p := range params {
		methods[p.method] = true
		p := p
		t.Run(p.method+"/"+p.name, func(t *testing.T) {
			if !reflect.ValueOf(&Client{}).MethodByName(p.method).IsValid() {
				// Declared in a file this Go version does not build.
				t.Skipf("%s is not built by %s", p.method, runtime.Version())
			}
			// The controls first, each in the first fill of the other
			// arguments that sends it. The refusals then use the fill the UUID
			// was sent with, so a refusal cannot come from some other argument.
			uuidMode := -1
			controls := pathControls(p.kind)
			names := make([]string, 0, len(controls))
			for v := range controls {
				names = append(names, v)
			}
			sort.Strings(names)
			for _, v := range names {
				want := controls[v]
				sent := false
				var last error
				for mi, fm := range pathFillModes {
					reqs, pan, err := ps.callWith(p, v, fm.mode, fm.minimal)
					if pan != nil {
						t.Fatalf("%s(%s=%q) panicked: %v", p.method, p.name, v, pan)
					}
					last = err
					if sentAs(reqs, want) {
						sent = true
						if v == pathTestUUID && uuidMode < 0 {
							uuidMode = mi
						}
						break
					}
				}
				if !sent {
					t.Errorf("%s(%s=%q), a %s parameter, was not sent as the segment %q (last error: %v)", p.method, p.name, v, p.kind, want, last)
				}
			}
			if uuidMode < 0 {
				t.Fatalf("%s: no fill of the other arguments sends a request, so the refusals would prove nothing", p.method)
			}
			fm := pathFillModes[uuidMode]
			for _, v := range pathRefusals(p.kind) {
				if _, ok := emptyIsDefault[p.method+"."+p.name]; ok && v == "" {
					continue
				}
				reqs, pan, err := ps.callWith(p, v, fm.mode, fm.minimal)
				if pan != nil {
					t.Fatalf("%s(%s=%q) panicked: %v", p.method, p.name, v, pan)
				}
				if err == nil {
					t.Errorf("%s(%s=%q) was accepted", p.method, p.name, v)
				}
				if len(reqs) != 0 {
					t.Errorf("%s(%s=%q): %d request(s) reached the server, first %s %s", p.method, p.name, v, len(reqs), reqs[0].method, reqs[0].escaped)
				}
			}
		})
	}
	t.Logf("%d guarded parameters of %d methods, %d calls", len(params), len(methods), ps.calls)
}

// notUUIDSegmentParams are the plain-segment parameters whose value is not
// always a UUID on the server: names, tags, emoji, a user (a username or an
// id, and a username may be all hex). With [uuidPathParams] they decide every
// plain-segment parameter, so a new one needs a decision.
var notUUIDSegmentParams = map[string]bool{
	"emoji": true, "slug": true, "tag": true, "user": true, "userID": true, "username": true, "variant": true,
}

// TestTruncatedIDsAreRefusedBeforeAnyRequest (jack, 2026-10-28): a plain-segment
// parameter whose value is always a UUID refuses the start of one, with no
// request made, and every other one still sends a hex-only value, which may be
// a real name.
func TestTruncatedIDsAreRefusedBeforeAnyRequest(t *testing.T) {
	ps := newPathServer(t)
	seen := map[string]bool{}
	for _, p := range guardedParams(t, parseSource(t)) {
		if p.kind != kindSegment {
			continue
		}
		if uuidPathParams[p.name] == notUUIDSegmentParams[p.name] {
			t.Errorf("%s.%s must be in exactly one of uuidPathParams and notUUIDSegmentParams", p.method, p.name)
			continue
		}
		seen[p.name] = true
		p := p
		t.Run(p.method+"/"+p.name, func(t *testing.T) {
			if !reflect.ValueOf(&Client{}).MethodByName(p.method).IsValid() {
				t.Skipf("%s is not built by %s", p.method, runtime.Version())
			}
			// The fill of the other arguments that sends a UUID, so a refusal
			// below cannot come from another argument.
			mode := -1
			for mi, fm := range pathFillModes {
				if reqs, _, _ := ps.callWith(p, pathTestUUID, fm.mode, fm.minimal); sentAs(reqs, pathTestUUID) {
					mode = mi
					break
				}
			}
			if mode < 0 {
				t.Fatalf("%s: no fill of the other arguments sends a request", p.method)
			}
			fm := pathFillModes[mode]
			if !uuidPathParams[p.name] {
				if reqs, _, err := ps.callWith(p, "deadbeef12", fm.mode, fm.minimal); !sentAs(reqs, "deadbeef12") {
					t.Errorf("%s(%s=\"deadbeef12\") was not sent: %v", p.method, p.name, err)
				}
				return
			}
			for _, v := range []string{"eae899e1", "EAE899E1", "eae899e1-ab0a-4ba2", "eae899e1ab0a4ba2a5a5ea82ea919445"} {
				reqs, pan, err := ps.callWith(p, v, fm.mode, fm.minimal)
				if pan != nil {
					t.Fatalf("%s(%s=%q) panicked: %v", p.method, p.name, v, pan)
				}
				if err == nil || !strings.Contains(err.Error(), p.name+" ") || !strings.Contains(err.Error(), "looks like a truncated id") {
					t.Errorf("%s(%s=%q) error = %v, want the truncated-id refusal", p.method, p.name, v, err)
				}
				if len(reqs) != 0 {
					t.Errorf("%s(%s=%q): %d request(s) reached the server", p.method, p.name, v, len(reqs))
				}
			}
		})
	}
	for name := range uuidPathParams {
		if !seen[name] {
			t.Errorf("uuidPathParams lists %s, which no method passes to pathSegment: remove it", name)
		}
	}
}

func TestPathSegment(t *testing.T) {
	for _, v := range []string{"", ".", "..", "/", "a/b", "../x", "x/.."} {
		if got, err := pathSegment("v", v); err == nil {
			t.Errorf("pathSegment(%q) = %q, want a refusal", v, got)
		}
	}
	for v, want := range map[string]string{
		"orbit-clock": "orbit-clock",
		"...":         "...", // not a dot segment
		".hidden":     ".hidden",
		".well-known": ".well-known",
		"a.b":         "a.b",
		"a b":         "a%20b",
		"a?b#c":       "a%3Fb%23c",
		"%2e%2e":      "%252e%252e", // an escaped dot segment stays escaped
		pathTestUUID:  pathTestUUID,
	} {
		got, err := pathSegment("v", v)
		if err != nil || got != want {
			t.Errorf("pathSegment(%q) = %q, %v, want %q", v, got, err, want)
		}
	}
	// The error names the parameter and the value.
	_, err := pathSegment("postID", "..")
	if err == nil || !strings.Contains(err.Error(), `postID ".." is a dot segment`) {
		t.Errorf("pathSegment(postID, ..) error = %v", err)
	}
}

func TestVaultFilePath(t *testing.T) {
	for _, v := range []string{"", ".", "..", "a/../b", "/a", "a/", "a//b", "./a", "a/..", "../users/me"} {
		if got, err := vaultFilePath(v); err == nil {
			t.Errorf("vaultFilePath(%q) = %q, want a refusal", v, got)
		}
	}
	for v, want := range map[string]string{
		"notes.md":          "/vault/files/notes.md",
		"notes/2026/aug.md": "/vault/files/notes%2F2026%2Faug.md", // what url.PathEscape made of it
		"...":               "/vault/files/...",
		".env":              "/vault/files/.env",
		"a b/c?d":           "/vault/files/a%20b%2Fc%3Fd",
	} {
		got, err := vaultFilePath(v)
		if err != nil || got != want {
			t.Errorf("vaultFilePath(%q) = %q, %v, want %q", v, got, err, want)
		}
		// Unchanged from before the guard, which escaped the whole name.
		if old := "/vault/files/" + url.PathEscape(v); got != old {
			t.Errorf("vaultFilePath(%q) = %q, but url.PathEscape gave %q", v, got, old)
		}
	}
}

func TestColonySegmentRefusesBeforeResolving(t *testing.T) {
	ps := newPathServer(t)
	c := NewClient("col_paths", WithBaseURL(ps.URL), WithRetry(RetryConfig{}))
	// A name the package does not know is resolved over the network, so the
	// refusal has to come first.
	for _, v := range []string{"", ".", "..", "a/b"} {
		if got, err := c.colonySegment(context.Background(), v); err == nil {
			t.Errorf("colonySegment(%q) = %q, want a refusal", v, got)
		}
	}
	if reqs := ps.take(); len(reqs) != 0 {
		t.Errorf("%d request(s) were made to refuse a colony, first %v", len(reqs), reqs[0])
	}
	got, err := c.colonySegment(context.Background(), "user-name")
	if err != nil || got != pathTestColony {
		t.Errorf("colonySegment(user-name) = %q, %v, want %q", got, err, pathTestColony)
	}
}
