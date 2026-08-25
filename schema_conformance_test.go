package colony

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

//go:generate go run ./internal/cmd/genschema

// Structs in this package are claims about what the server sends. This checks
// them against the server's own OpenAPI document.
//
// Four bugs in one week were the same shape and none of them errored: a field
// with the wrong type (cognition difficulty), the wrong name (invite_status),
// no counterpart at all (MyRole/MyInviteStatus/TotalOthers), or a whole struct
// wrong in every field (GroupSearchResults). Each was caught by a human
// reading the platform source. This is that reading, mechanised.
//
// Two tiers, same division as the webhook catalogue:
//
//   - this file is OFFLINE and gates every PR, against a committed snapshot;
//   - schema_conformance_live_test.go (-tags live) checks the SNAPSHOT against
//     the running API, which the offline test structurally cannot.

// schemaBinding ties one Go type to the schema it claims to mirror.
//
// The mapping is explicit on purpose. Matching by name would silently skip
// every type whose name does not happen to line up, and a checker that skips
// what it cannot match reports success for work it did not do.
type schemaBinding struct {
	schema string
	goType any
	// optional names schema properties this package deliberately does not
	// model. Each entry is a decision on record rather than an omission.
	optional []string
	// elsewhere names Go fields that a DIFFERENT schema populates. A struct
	// shared across endpoints has fields no single schema fills, and calling
	// those phantoms would be wrong — but leaving them unnamed would mean a
	// genuine phantom hides among them. Each entry says "another endpoint
	// fills this", and the notes field should say which.
	elsewhere []string
	// notes explains an entry that needs it.
	notes string
}

var schemaBindings = []schemaBinding{
	{schema: "BootstrapProfile", goType: BootstrapProfile{}},
	{schema: "Capability", goType: Capability{}},
	{schema: "ClaimOut", goType: Claim{}},
	{schema: "ColdBudgetWindow", goType: ColdBudgetWindow{}},
	{schema: "CommentOut", goType: Comment{}},
	{schema: "ConversationOut", goType: Conversation{}},
	{schema: "ConversationDetail", goType: ConversationDetail{}},
	{schema: "ConversationHistoryOut", goType: ConversationHistory{}},
	{schema: "ConversationTailOut", goType: ConversationTail{}},
	{schema: "DetailResult", goType: DetailResult{}},
	{schema: "DmSpamMarkOut", goType: DmSpamMark{}},
	{schema: "EchoOut", goType: Echo{}},
	{schema: "EchoPost", goType: EchoPost{}},
	{schema: "ForYouFeedOut", goType: ForYouFeed{}},
	{schema: "ForYouItemOut", goType: ForYouItem{}},
	{schema: "MessageOut", goType: Message{}},
	{
		schema: "AttachmentUploadOut", goType: MessageAttachment{},
		notes: "MessageAttachment models the UPLOAD response, which is " +
			"AttachmentUploadOut (it carries `deduped`), not MessageAttachmentOut.",
	},
	{schema: "MessageEditVersion", goType: MessageEditVersion{}},
	{schema: "MessageReactionOut", goType: MessageReaction{}},
	{schema: "MessageReadsOut", goType: MessageReads{}},
	{schema: "NotificationOut", goType: Notification{}},
	{schema: "PageMeta", goType: PageMeta{}},
	{schema: "PollResults", goType: PollResults{}},
	{schema: "PostOut", goType: Post{}},
	{schema: "ReportOut", goType: Report{}},
	{schema: "RotateKeyResponse", goType: RotateKeyResponse{}},
	{schema: "SavedMessageEntry", goType: SavedMessageEntry{}},
	{schema: "SavedMessagesOut", goType: SavedMessages{}},
	{schema: "SearchResults", goType: SearchResults{}},
	{schema: "SystemNotificationOut", goType: SystemNotification{}},
	{schema: "TrustLevelOut", goType: TrustLevel{}},
	{schema: "UnreadCountOut", goType: UnreadCount{}},
	{
		schema: "UserOut", goType: User{},
		elsewhere: []string{"post_count"},
		notes: "User spans two schemas: UserOut and the directory listing's " +
			"DirectoryUserOut, which is where post_count comes from.",
	},
	{schema: "VaultSearchResult", goType: VaultSearchResult{}},
	{schema: "WebhookOut", goType: Webhook{}},
}

type openAPISnapshot struct {
	FetchedAt string                   `json:"fetched_at"`
	Source    string                   `json:"source"`
	Count     int                      `json:"count"`
	Schemas   map[string]openAPISchema `json:"schemas"`
	// Refs holds the schemas a bound schema's $ref points at — enum aliases,
	// mostly. Without them a $ref has to be guessed at, and guessing
	// "object" reports every enum-typed string as a type error.
	Refs map[string]openAPISchema `json:"refs"`
}

type openAPISchema struct {
	Properties map[string]openAPIProp `json:"properties"`
	Required   []string               `json:"required"`
	Title      string                 `json:"title"`
	// Type and Enum are set on the small alias schemas a $ref names, e.g.
	// PostType = {"type":"string","enum":[...]}.
	Type string   `json:"type"`
	Enum []string `json:"enum"`
}

type openAPIProp struct {
	Type   string        `json:"type"`
	Ref    string        `json:"$ref"`
	AnyOf  []openAPIProp `json:"anyOf"`
	Format string        `json:"format"`
}

// jsonTypes returns every JSON type this property may take, "null" included.
func (p openAPIProp) jsonTypes(refs map[string]openAPISchema) []string {
	if len(p.AnyOf) > 0 {
		var out []string
		for _, alt := range p.AnyOf {
			out = append(out, alt.jsonTypes(refs)...)
		}
		return out
	}
	if p.Ref != "" {
		// A $ref is NOT necessarily an object: PostType and UserType are
		// string enums behind one, and assuming "object" reported three
		// conforming fields as type errors on this checker's first run. A
		// checker's own false positives are the first thing to rule out
		// before believing its findings.
		name := p.Ref
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		target, ok := refs[name]
		if !ok {
			return nil // unresolved: say nothing rather than assert a type
		}
		if target.Type != "" {
			return []string{target.Type}
		}
		return []string{"object"}
	}
	if p.Type == "" {
		return nil // unconstrained; nothing to check
	}
	return []string{p.Type}
}

func loadSchemas(t *testing.T) openAPISnapshot {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi_schemas.json")
	if err != nil {
		t.Fatalf("read snapshot: %v — run `go generate ./...`", err)
	}
	var snap openAPISnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Schemas) == 0 {
		t.Fatal("snapshot declares 0 schemas — every check below would pass vacuously")
	}
	return snap
}

// goFields maps a struct's json tag -> reflect.Kind, following embedded types.
func goFields(t reflect.Type, into map[string]reflect.Kind) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue // Extra, and anything else deliberately off the wire
		}
		if f.Anonymous && name == "" {
			goFields(f.Type, into)
			continue
		}
		if name == "" {
			name = f.Name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		into[name] = ft.Kind()
	}
}

// kindMatches reports whether a Go kind can hold one of the JSON types.
func kindMatches(k reflect.Kind, jsonTypes []string) bool {
	if len(jsonTypes) == 0 {
		return true
	}
	for _, jt := range jsonTypes {
		switch jt {
		case "null":
			continue // nullability is orthogonal; not what this checks
		case "string":
			// time.Time and other struct types decode from a JSON string.
			if k == reflect.String || k == reflect.Struct {
				return true
			}
		case "integer":
			switch k {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
				reflect.Float32, reflect.Float64:
				return true
			}
		case "number":
			if k == reflect.Float32 || k == reflect.Float64 ||
				k == reflect.Int || k == reflect.Int64 {
				return true
			}
		case "boolean":
			if k == reflect.Bool {
				return true
			}
		case "array":
			if k == reflect.Slice || k == reflect.Array {
				return true
			}
		case "object":
			if k == reflect.Map || k == reflect.Struct || k == reflect.Interface {
				return true
			}
		}
	}
	return false
}

type finding struct {
	schema, field, detail string
	// blocking distinguishes a defect from a gap.
	//
	// A PHANTOM field (modelled, absent from the schema) is a lie: it can
	// never be populated, and a caller branching on it has a branch that
	// never fires. A TYPE mismatch means a real response fails to decode or
	// lands empty. Both are bugs and both fail the build.
	//
	// An UNMODELLED field is a gap, not a defect: the server sends it, this
	// package does not name it, and Extra makes it reachable anyway.
	// Requiring zero would mean this SDK could never lag the server by a
	// single field. It is reported and RATCHETED instead — see
	// unmodelledBaseline.
	blocking bool
}

// checkBinding is the whole checker, pulled out so the self-test can drive it
// with synthetic types.
func checkBinding(b schemaBinding, sch openAPISchema, refs map[string]openAPISchema) []finding {
	var out []finding
	opt := map[string]bool{}
	for _, o := range b.optional {
		opt[o] = true
	}
	elsewhere := map[string]bool{}
	for _, o := range b.elsewhere {
		elsewhere[o] = true
	}
	fields := map[string]reflect.Kind{}
	goFields(reflect.TypeOf(b.goType), fields)

	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		prop, ok := sch.Properties[name]
		if !ok {
			if elsewhere[name] {
				continue
			}
			out = append(out, finding{b.schema, name,
				"modelled here, absent from the schema — this field can never be populated", true})
			continue
		}
		if !kindMatches(fields[name], prop.jsonTypes(refs)) {
			out = append(out, finding{b.schema, name, fmt.Sprintf(
				"Go %s vs schema %v — a real response will fail to decode or land empty",
				fields[name], prop.jsonTypes(refs)), true})
		}
	}

	props := make([]string, 0, len(sch.Properties))
	for n := range sch.Properties {
		props = append(props, n)
	}
	sort.Strings(props)
	for _, name := range props {
		if _, ok := fields[name]; ok || opt[name] {
			continue
		}
		out = append(out, finding{b.schema, name,
			"sent by the server, not modelled — reachable only through Extra", false})
	}
	return out
}

// unmodelledBaseline is how many server fields this package does not name
// today. It is a RATCHET, not a target: the test fails if the number grows,
// so the gap cannot widen unnoticed, and fails if it shrinks without this
// constant being lowered, so fixing one does not quietly restore headroom for
// the next.
const unmodelledBaseline = 21

// TestStructsMatchTheServerSchemas is the gate.
//
// A large finding count against ONE schema usually means the BINDING is wrong,
// not the struct — check the mapping first. That happened on the first run
// here: MessageAttachment was bound to MessageAttachmentOut when it models the
// upload response, AttachmentUploadOut.
func TestStructsMatchTheServerSchemas(t *testing.T) {
	snap := loadSchemas(t)
	if len(schemaBindings) == 0 {
		t.Fatal("no bindings — this test would pass having checked nothing")
	}

	var blocking, gaps []finding
	for _, b := range schemaBindings {
		sch, ok := snap.Schemas[b.schema]
		if !ok {
			t.Errorf("%s: bound here but not in the snapshot — run `go generate ./...`", b.schema)
			continue
		}
		if len(sch.Properties) == 0 {
			t.Errorf("%s: snapshot carries no properties; the comparison would be vacuous", b.schema)
			continue
		}
		for _, f := range checkBinding(b, sch, snap.Refs) {
			if f.blocking {
				blocking = append(blocking, f)
			} else {
				gaps = append(gaps, f)
			}
		}
	}

	if len(blocking) > 0 {
		var buf strings.Builder
		fmt.Fprintf(&buf, "%d field(s) diverge from the server's schemas (snapshot %s):\n",
			len(blocking), snap.FetchedAt)
		for _, f := range blocking {
			fmt.Fprintf(&buf, "  %-26s %-24s %s\n", f.schema, f.field, f.detail)
		}
		t.Error(buf.String())
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "%d server field(s) not modelled (reachable via Extra):\n", len(gaps))
	for _, f := range gaps {
		fmt.Fprintf(&buf, "  %-26s %s\n", f.schema, f.field)
	}
	t.Log(buf.String())

	if len(gaps) > unmodelledBaseline {
		t.Errorf("unmodelled fields grew to %d, baseline %d — model the new one "+
			"or raise the baseline deliberately", len(gaps), unmodelledBaseline)
	}
	if len(gaps) < unmodelledBaseline {
		t.Errorf("unmodelled fields fell to %d, baseline %d — lower "+
			"unmodelledBaseline so the ratchet keeps holding", len(gaps), unmodelledBaseline)
	}
}

// The checker must be able to FAIL, or a green run above means nothing. Each
// synthetic case is one of the four bug shapes that motivated this file.
func TestTheCheckerDetectsEachBugShape(t *testing.T) {
	sch := openAPISchema{Properties: map[string]openAPIProp{
		"difficulty":    {Type: "integer"},
		"invite_status": {Type: "string"},
		"member_count":  {Type: "integer"},
	}, Required: []string{"difficulty"}}

	cases := []struct {
		name    string
		goType  any
		wantSub string
	}{
		{
			name: "wrong type (the cognition difficulty bug)",
			goType: struct {
				D string `json:"difficulty"`
			}{},
			wantSub: "Go string vs schema [integer]",
		},
		{
			name: "wrong tag (the invite_status bug)",
			goType: struct {
				S string `json:"status"`
			}{},
			wantSub: "absent from the schema",
		},
		{
			name: "phantom field (the MyRole bug)",
			goType: struct {
				M string `json:"my_role"`
			}{},
			wantSub: "can never be populated",
		},
		{
			name: "unmodelled field (the member_count gap)",
			goType: struct {
				D int `json:"difficulty"`
			}{},
			wantSub: "sent by the server, not modelled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkBinding(schemaBinding{schema: "X", goType: tc.goType}, sch, nil)
			if len(got) == 0 {
				t.Fatalf("the checker found nothing in %s", tc.name)
			}
			joined := ""
			for _, f := range got {
				joined += f.detail + "\n"
			}
			if !strings.Contains(joined, tc.wantSub) {
				t.Errorf("findings did not mention %q:\n%s", tc.wantSub, joined)
			}
		})
	}

	// And the control: a struct that matches must produce NOTHING. Without
	// this, a checker that flags everything would pass the cases above.
	clean := struct {
		D  int    `json:"difficulty"`
		IS string `json:"invite_status"`
		MC int    `json:"member_count"`
	}{}
	if got := checkBinding(schemaBinding{schema: "X", goType: clean}, sch, nil); len(got) != 0 {
		t.Errorf("a conforming struct produced %d finding(s): %+v", len(got), got)
	}
}

// Fields tagged json:"-" are off the wire by definition and must not be
// reported as phantom — Extra is on nearly every type.
func TestExtraIsNotTreatedAsAWireField(t *testing.T) {
	sch := openAPISchema{Properties: map[string]openAPIProp{"id": {Type: "string"}}}
	v := struct {
		ID    string         `json:"id"`
		Extra map[string]any `json:"-"`
	}{}
	if got := checkBinding(schemaBinding{schema: "X", goType: v}, sch, nil); len(got) != 0 {
		t.Errorf("json:\"-\" field reported: %+v", got)
	}
}
