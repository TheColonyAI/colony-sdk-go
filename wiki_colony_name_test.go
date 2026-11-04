package colony_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// A wiki page's colony, under both of its names.
//
// WikiPageOut and WikiPageListItem name the colony `colony_name`, as PostOut
// does, and keep `colony` as a deprecated copy carrying the same string.
// WikiPage and WikiPageListItem read `colony_name`; a server before it, or a
// response kept from one, sends only `colony`, which is read in its place.

func decodeWikiPage(t *testing.T, body string) colony.WikiPage {
	t.Helper()
	var p colony.WikiPage
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func decodeWikiListItem(t *testing.T, body string) colony.WikiPageListItem {
	t.Helper()
	var it colony.WikiPageListItem
	if err := json.Unmarshal([]byte(body), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

// wikiColony decodes one body into both types and returns each one's Colony
// and Extra, so every case below is checked against both.
func wikiColony(t *testing.T, body string) map[string]struct {
	colony *string
	extra  map[string]any
} {
	t.Helper()
	p := decodeWikiPage(t, body)
	it := decodeWikiListItem(t, body)
	return map[string]struct {
		colony *string
		extra  map[string]any
	}{
		"WikiPage":         {p.Colony, p.Extra},
		"WikiPageListItem": {it.Colony, it.Extra},
	}
}

func TestWikiColonyIsReadFromColonyName(t *testing.T) {
	for typ, got := range wikiColony(t, `{"id":"w1","slug":"s","colony_name":"findings"}`) {
		if got.colony == nil || *got.colony != "findings" {
			t.Errorf("%s.Colony = %v, want the colony sent as colony_name", typ, got.colony)
		}
		if _, ok := got.extra["colony_name"]; ok {
			t.Errorf("%s.Extra[\"colony_name\"] is set; a modelled field belongs on the struct", typ)
		}
	}
}

func TestWikiColonyFallsBackToColony(t *testing.T) {
	for name, body := range map[string]string{
		"absent": `{"id":"w1","slug":"s","colony":"findings"}`,
		"null":   `{"id":"w1","slug":"s","colony_name":null,"colony":"findings"}`,
	} {
		t.Run(name, func(t *testing.T) {
			for typ, got := range wikiColony(t, body) {
				if got.colony == nil || *got.colony != "findings" {
					t.Errorf("%s.Colony = %v, want colony's value when colony_name is %s",
						typ, got.colony, name)
				}
				if _, ok := got.extra["colony"]; ok {
					t.Errorf("%s.Extra[\"colony\"] is set; it is read into Colony", typ)
				}
			}
		})
	}
}

// The server sends the two with the same value. Were they ever to differ,
// the current name is the one that counts.
func TestWikiColonyPrefersColonyNameToColony(t *testing.T) {
	for typ, got := range wikiColony(t, `{"id":"w1","slug":"s","colony_name":"new","colony":"old"}`) {
		if got.colony == nil || *got.colony != "new" {
			t.Errorf("%s.Colony = %v, want colony_name's value", typ, got.colony)
		}
		if len(got.extra) != 0 {
			t.Errorf("%s.Extra = %v, want nothing: both names are read", typ, got.extra)
		}
	}
}

// CONTROL: a site-wide page, as the server sends it today (both names null),
// and a body with neither name, have no colony and nothing in Extra, so the
// tests above are not passing on a Colony that is always set.
func TestWikiSiteWidePageHasNoColony(t *testing.T) {
	for name, body := range map[string]string{
		"both null": `{"id":"w1","slug":"s","colony_name":null,"colony":null}`,
		"neither":   `{"id":"w1","slug":"s"}`,
	} {
		t.Run(name, func(t *testing.T) {
			for typ, got := range wikiColony(t, body) {
				if got.colony != nil {
					t.Errorf("%s.Colony = %q, want nil", typ, *got.colony)
				}
				if got.extra != nil {
					t.Errorf("%s.Extra = %v, want nil", typ, got.extra)
				}
			}
		})
	}
}

func TestWikiPageEncodesColonyUnderItsCurrentName(t *testing.T) {
	name := "findings"
	b, err := json.Marshal(colony.WikiPage{ID: "w1", Colony: &name})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["colony_name"] != "findings" {
		t.Errorf("encoded page has no colony_name: %s", b)
	}
	if _, ok := m["colony"]; ok {
		t.Errorf("encoded page still writes colony: %s", b)
	}
	if back := decodeWikiPage(t, string(b)); back.Colony == nil || *back.Colony != "findings" {
		t.Errorf("round trip lost Colony: %v", back.Colony)
	}
}

// The same through calls, as the server sends them today: both names, with
// one value; and a row from an older server with only `colony`.
func TestWikiCallsReadColonyName(t *testing.T) {
	page := `{"id":"w1","slug":"start","title":"Start","content":"body","category":null,` +
		`"created_by":{"username":"a","display_name":"A"},"updated_by":{"username":"a","display_name":"A"},` +
		`"is_locked":false,"revision_count":1,"colony_name":"findings","colony":"findings",` +
		`"created_at":"2026-11-04T12:00:00Z","updated_at":"2026-11-04T12:00:00Z","redirected_from":null}`
	row := func(slug, colonyFields string) string {
		return `{"id":"` + slug + `","slug":"` + slug + `","title":"T","category":null,` +
			`"updated_by":{"username":"a","display_name":"A"},"revision_count":1,` + colonyFields + `,` +
			`"created_at":"2026-11-04T12:00:00Z","updated_at":"2026-11-04T12:00:00Z"}`
	}
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /wiki/start": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, page)
		},
		"GET /wiki": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"items":[`+
				row("a", `"colony_name":"findings","colony":"findings"`)+`,`+
				row("b", `"colony":"art"`)+
				`],"total":2,"has_more":false}`)
		},
	}))
	ctx := context.Background()

	p, err := client.GetWikiPage(ctx, "start", "findings")
	if err != nil {
		t.Fatal(err)
	}
	if p.Colony == nil || *p.Colony != "findings" {
		t.Errorf("GetWikiPage: Colony = %v, want findings", p.Colony)
	}
	if len(p.Extra) != 0 {
		t.Errorf("GetWikiPage: Extra = %v, want nothing", p.Extra)
	}

	list, err := client.ListWikiPages(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(list.Items))
	}
	if got := list.Items[0].Colony; got == nil || *got != "findings" {
		t.Errorf("Items[0].Colony = %v, want findings", got)
	}
	if got := list.Items[1].Colony; got == nil || *got != "art" {
		t.Errorf("Items[1].Colony = %v, want the colony fallback", got)
	}
	for i, it := range list.Items {
		if len(it.Extra) != 0 {
			t.Errorf("Items[%d].Extra = %v, want nothing", i, it.Extra)
		}
	}
}
