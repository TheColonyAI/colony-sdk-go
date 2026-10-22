package colony

// TestRequestsMatchTheDocument compares what the recorder captured with the platform's API document:
// for every recorded request, the documented operation it hits (method and path template), its query
// keys and body keys (declared, a deprecated alias named by x-deprecated-alias-of, or undeclared),
// integer and boolean query values, and an id-shaped value sent under a name parameter X when the
// operation also declares X_id as a uuid. A method whose four drives sent nothing of its own is
// reported as unread, not as clean.
//
// Opt-in, because it needs the full document, which this repository does not vendor:
//
//	curl -o /tmp/openapi.json https://thecolony.ai/openapi.json
//	COLONY_OPENAPI=/tmp/openapi.json go test -run TestRequestsMatchTheDocument -v .
//
// Findings already known and filed are listed in knownRequestFindings with where they are tracked.
// Anything else fails the test, so a new disagreement surfaces here rather than in production.

import (
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// knownRequestFindings: each entry is a finding this reader reports today, with where it is tracked.
// Remove an entry when the SDK is fixed; the test then fails if the finding comes back.
var knownRequestFindings = map[string]string{
	"route_unmatched: ExchangeToken POST /oauth/token":                      "the OAuth token endpoint is outside this API document",
	"route_unmatched: GetPoll GET /posts/{}/poll":                           "dead route, fixed in #70",
	"route_unmatched: VotePoll POST /posts/{}/poll/vote":                    "dead route, fixed in #70",
	"route_unmatched: ReactPost POST /posts/{}/react":                       "dead route, fixed in #70",
	"route_unmatched: ReactComment POST /comments/{}/react":                 "dead route, fixed in #70",
	"route_unmatched: MarkPostScanned PUT /posts/{}/sentinel-scanned":       "served but undeclared (#70 allowlist, probe evidence)",
	"route_unmatched: MarkCommentScanned PUT /comments/{}/sentinel-scanned": "served but undeclared (#70 allowlist, probe evidence)",
	"route_unmatched: MovePostToColony PUT /posts/{}/colony":                "served but undeclared (#70 allowlist, probe evidence)",
	"deprecated_body: MarkConversationSpam reason_code -> reason":           "#73",
	"deprecated_body: ModQueueAction source_kind -> source":                 "#73",
	"deprecated_body: ModQueueBulkAction items[].source_kind -> source":     "#73",
	"deprecated_query: GetModQueue page_size -> limit":                      "#73",
	"deprecated_query: GetModQueue queue_status -> status":                  "#73",
	"deprecated_query: GetPosts search -> q":                                "#73 (found by this reader)",
	"deprecated_query: IterPosts search -> q":                               "#73 (found by this reader)",
	"deprecated_query: ListWikiPages search -> q":                           "#73 (found by this reader)",
	"deprecated_query: IterWikiPages search -> q":                           "#73 (found by this reader)",
	"deprecated_query: MuteGroupConversation until -> duration":             "#73 (found by this reader)",
	"id_under_name: Search colony":                                          "#73: an id goes as colony=, which the server reads as a name (404)",
	"undeclared_query: Directory query":                                     "#73 (found by this reader): the server ignores it; the document's name is q",
	"undeclared_query: ListOrgMembers limit":                                "#73 (found by this reader): the document declares no query parameters here",
	"undeclared_query: ListOrgMembers offset":                               "#73 (found by this reader): the document declares no query parameters here",
	"body_type_undeclared: SetMyStatus application/json":                    "#73 (found by this reader): the document takes presence_status and custom_status_text as query parameters",
	"query_type: ListColdBudgetPeers cursor":                                "the recorder's fill, not the SDK: a real cursor is passed through as given (the document types it integer in the request, string in the response)",
}

type docOp struct {
	method, path string
	segs         []string
	query        map[string]map[string]any
	queryAlias   map[string]string
	body         map[string]docProps // by content type
	bodyRequired bool
}

type docProps struct {
	props    map[string]map[string]any
	alias    map[string]string
	required map[string]bool
}

type requestDoc struct{ root map[string]any }

func (d requestDoc) deref(o any) map[string]any {
	m, _ := o.(map[string]any)
	for i := 0; i < 20 && m != nil; i++ {
		ref, ok := m["$ref"].(string)
		if !ok {
			break
		}
		var x any = d.root
		for _, p := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			x = x.(map[string]any)[p]
		}
		m, _ = x.(map[string]any)
	}
	return m
}

func (d requestDoc) props(schema any, depth int) docProps {
	out := docProps{props: map[string]map[string]any{}, alias: map[string]string{}, required: map[string]bool{}}
	s := d.deref(schema)
	if s == nil || depth > 6 {
		return out
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		subs, _ := s[key].([]any)
		for _, sub := range subs {
			p := d.props(sub, depth+1)
			for k, v := range p.props {
				out.props[k] = v
			}
			for k, v := range p.alias {
				out.alias[k] = v
			}
			if key == "allOf" {
				for k := range p.required {
					out.required[k] = true
				}
			}
		}
	}
	ps, _ := s["properties"].(map[string]any)
	for name, p := range ps {
		pm := d.deref(p)
		out.props[name] = pm
		if a, ok := pm["x-deprecated-alias-of"].(string); ok {
			out.alias[name] = a
		}
	}
	req, _ := s["required"].([]any)
	for _, r := range req {
		out.required[r.(string)] = true
	}
	return out
}

func (d requestDoc) types(schema any) map[string]bool {
	out := map[string]bool{}
	s := d.deref(schema)
	if s == nil {
		return out
	}
	switch t := s["type"].(type) {
	case string:
		out[t] = true
	case []any:
		for _, x := range t {
			out[x.(string)] = true
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		subs, _ := s[key].([]any)
		for _, sub := range subs {
			for k := range d.types(sub) {
				out[k] = true
			}
		}
	}
	return out
}

func (d requestDoc) formats(schema any) map[string]bool {
	out := map[string]bool{}
	s := d.deref(schema)
	if s == nil {
		return out
	}
	if f, ok := s["format"].(string); ok {
		out[f] = true
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		subs, _ := s[key].([]any)
		for _, sub := range subs {
			for k := range d.formats(sub) {
				out[k] = true
			}
		}
	}
	return out
}

func (d requestDoc) operations() []docOp {
	var ops []docOp
	paths, _ := d.root["paths"].(map[string]any)
	for path, item := range paths {
		im := item.(map[string]any)
		common, _ := im["parameters"].([]any)
		for m, opAny := range im {
			switch m {
			case "get", "post", "put", "patch", "delete":
			default:
				continue
			}
			op := opAny.(map[string]any)
			params, _ := op["parameters"].([]any)
			o := docOp{method: strings.ToUpper(m), path: path, segs: strings.Split(strings.Trim(path, "/"), "/"),
				query: map[string]map[string]any{}, queryAlias: map[string]string{}, body: map[string]docProps{}}
			for _, p := range append(append([]any{}, common...), params...) {
				pm := d.deref(p)
				if pm["in"] != "query" {
					continue
				}
				name := pm["name"].(string)
				o.query[name] = pm
				if a, ok := pm["x-deprecated-alias-of"].(string); ok {
					o.queryAlias[name] = a
				} else if a, ok := d.deref(pm["schema"])["x-deprecated-alias-of"].(string); ok {
					o.queryAlias[name] = a
				}
			}
			if rb := d.deref(op["requestBody"]); rb != nil {
				o.bodyRequired, _ = rb["required"].(bool)
				content, _ := rb["content"].(map[string]any)
				for ct, c := range content {
					o.body[ct] = d.props(c.(map[string]any)["schema"], 0)
				}
			}
			ops = append(ops, o)
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].method+ops[i].path < ops[j].method+ops[j].path })
	return ops
}

func matchOperation(ops []docOp, method, path string) *docOp {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	var best *docOp
	bestLit := -1
	for i := range ops {
		o := &ops[i]
		if o.method != method || len(o.segs) != len(segs) {
			continue
		}
		lit, ok := 0, true
		for k, a := range o.segs {
			if strings.HasPrefix(a, "{") && strings.HasSuffix(a, "}") {
				if segs[k] == "" {
					ok = false
					break
				}
			} else if a == segs[k] {
				lit++
			} else {
				ok = false
				break
			}
		}
		if ok && lit > bestLit {
			best, bestLit = o, lit
		}
	}
	return best
}

var templSeg = regexp.MustCompile(`/(x1|` + recorderUUID + `)(/|$)`)

func bodyKeys(r recordedRequest) (string, []string, map[string]any) {
	ct, params, _ := mime.ParseMediaType(r.ContentType)
	if r.Body == "" {
		return ct, nil, nil
	}
	switch ct {
	case "application/json":
		var v any
		if err := json.Unmarshal([]byte(r.Body), &v); err != nil {
			return ct, []string{"<unparseable json>"}, nil
		}
		m, ok := v.(map[string]any)
		if !ok {
			return ct, []string{fmt.Sprintf("<json %T>", v)}, nil
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return ct, keys, m
	case "multipart/form-data":
		mr := multipart.NewReader(strings.NewReader(r.Body), params["boundary"])
		var keys []string
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			keys = append(keys, p.FormName())
		}
		sort.Strings(keys)
		return ct, keys, nil
	case "application/x-www-form-urlencoded":
		v, _ := url.ParseQuery(r.Body)
		var keys []string
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return ct, keys, nil
	}
	return ct, []string{"<" + ct + ">"}, nil
}

// compareRequests returns findings keyed "kind: Method detail" (stable across fill values) with one
// example each, plus the methods that sent nothing of their own.
func compareRequests(d requestDoc, calls []recordedCall) (map[string]string, []string) {
	ops := d.operations()
	findings := map[string]string{}
	add := func(key, example string) {
		if _, ok := findings[key]; !ok {
			findings[key] = example
		}
	}
	byMethod := map[string][]recordedCall{}
	var names []string
	for _, c := range calls {
		if _, ok := byMethod[c.Method]; !ok {
			names = append(names, c.Method)
		}
		byMethod[c.Method] = append(byMethod[c.Method], c)
	}
	sort.Strings(names)
	var unread []string
	for _, name := range names {
		type modeReq struct {
			mode string
			r    recordedRequest
		}
		var reqs []modeReq
		for _, c := range byMethod[name] {
			rs := c.Requests
			// a GET /colonies before another request is the SDK resolving a colony name
			if len(rs) > 1 && rs[0].Method == "GET" && rs[0].Path == "/colonies" {
				rs = rs[1:]
			}
			if len(rs) == 1 && rs[0].Method == "GET" && rs[0].Path == "/colonies" && name != "GetColonies" && name != "ListColonies" {
				continue
			}
			for _, r := range rs {
				reqs = append(reqs, modeReq{c.Mode, r})
			}
		}
		if len(reqs) == 0 {
			unread = append(unread, name)
			continue
		}
		for _, mr := range reqs {
			r := mr.r
			path, _ := url.PathUnescape(r.Path)
			o := matchOperation(ops, r.Method, "/api/v1"+path)
			if o == nil {
				o = matchOperation(ops, r.Method, path)
			}
			where := fmt.Sprintf("%s [%s] %s %s", name, mr.mode, r.Method, path)
			if o == nil {
				add(fmt.Sprintf("route_unmatched: %s %s %s", name, r.Method, templSeg.ReplaceAllString(path, "/{}$2")), where)
				continue
			}
			for k, vals := range r.Query {
				p, declared := o.query[k]
				switch {
				case o.queryAlias[k] != "":
					add(fmt.Sprintf("deprecated_query: %s %s -> %s", name, k, o.queryAlias[k]), where)
				case !declared:
					add(fmt.Sprintf("undeclared_query: %s %s", name, k), where)
				default:
					ts, v := d.types(p["schema"]), vals[0]
					if ts["integer"] && !ts["string"] && !regexp.MustCompile(`^-?\d+$`).MatchString(v) {
						add(fmt.Sprintf("query_type: %s %s", name, k), where+" "+k+"="+v)
					}
					if ts["boolean"] && !ts["string"] && v != "true" && v != "false" && v != "1" && v != "0" {
						add(fmt.Sprintf("query_type: %s %s", name, k), where+" "+k+"="+v)
					}
					if strings.HasPrefix(mr.mode, "id") && v == recorderUUID && !d.formats(p["schema"])["uuid"] {
						if sib, ok := o.query[k+"_id"]; ok && d.formats(sib["schema"])["uuid"] {
							add(fmt.Sprintf("id_under_name: %s %s", name, k), where)
						}
					}
				}
			}
			for k, p := range o.query {
				if req, _ := p["required"].(bool); req {
					if _, sent := r.Query[k]; !sent {
						add(fmt.Sprintf("required_query_missing: %s %s", name, k), where)
					}
				}
			}
			ct, keys, obj := bodyKeys(r)
			if keys == nil {
				if o.bodyRequired {
					add(fmt.Sprintf("required_body_missing: %s <no body>", name), where)
				}
				continue
			}
			decl, ok := o.body[ct]
			if !ok {
				add(fmt.Sprintf("body_type_undeclared: %s %s", name, ct), where)
				continue
			}
			sent := map[string]bool{}
			for _, k := range keys {
				sent[k] = true
				switch {
				case strings.HasPrefix(k, "<"):
					add(fmt.Sprintf("body_shape: %s %s", name, k), where)
				case decl.alias[k] != "":
					add(fmt.Sprintf("deprecated_body: %s %s -> %s", name, k, decl.alias[k]), where)
					sent[decl.alias[k]] = true
				case len(decl.props) > 0 && decl.props[k] == nil:
					add(fmt.Sprintf("undeclared_body: %s %s", name, k), where)
				}
			}
			for k, v := range obj { // one level down: arrays of objects (bulk items)
				arr, ok := v.([]any)
				if !ok || len(arr) == 0 || decl.props[k] == nil {
					continue
				}
				item, ok := arr[0].(map[string]any)
				if !ok {
					continue
				}
				ip := d.props(d.deref(decl.props[k])["items"], 0)
				for k2 := range item {
					if ip.alias[k2] != "" {
						add(fmt.Sprintf("deprecated_body: %s %s[].%s -> %s", name, k, k2, ip.alias[k2]), where)
					} else if len(ip.props) > 0 && ip.props[k2] == nil {
						add(fmt.Sprintf("undeclared_body: %s %s[].%s", name, k, k2), where)
					}
				}
			}
			for k := range decl.required {
				if !sent[k] {
					add(fmt.Sprintf("required_body_missing: %s %s", name, k), where)
				}
			}
		}
	}
	return findings, unread
}

func TestRequestsMatchTheDocument(t *testing.T) {
	path := os.Getenv("COLONY_OPENAPI")
	if path == "" {
		t.Skip("set COLONY_OPENAPI=<openapi.json> (from https://thecolony.ai/openapi.json) to compare every request with the document")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	calls := recordEveryRequest(t)
	findings, unread := compareRequests(requestDoc{root}, calls)
	methods := map[string]bool{}
	for _, c := range calls {
		methods[c.Method] = true
	}
	// A reader that reads nothing must not pass: the known findings are its positive controls.
	if len(methods)-len(unread) < 200 || len(findings) == 0 {
		t.Fatalf("read too little to mean anything: %d of %d methods sent their own request, %d findings", len(methods)-len(unread), len(methods), len(findings))
	}
	t.Logf("%d methods, %d sent their own request; unread: %s", len(methods), len(methods)-len(unread), strings.Join(unread, ", "))
	keys := make([]string, 0, len(findings))
	for k := range findings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if why, ok := knownRequestFindings[k]; ok {
			t.Logf("known: %s (%s)", k, why)
		} else {
			t.Errorf("NEW: %s\n    e.g. %s", k, findings[k])
		}
	}
	for k := range knownRequestFindings {
		if _, ok := findings[k]; !ok {
			t.Errorf("known finding no longer reported (fixed? then delete its entry): %s", k)
		}
	}
}
