package colony

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// Colony social links (platform release 2026-11-14c): SubColony.SocialLinks
// decodes present, null and absent; UpdateColonySettings sends
// PATCH /colonies/{colony_id} with social_links alone, holding the links set
// and nothing for the rest, clears them all with an empty set, refuses a
// change that sets nothing before any request, decodes the colony it
// answers, and returns the server's refusal of a link.

const (
	settingsColonyID = "2e549d01-99f2-459f-8924-48b2690b2170" // "general" in Colonies
	// settingsColonyJSON is a colony as PATCH /colonies/{id} answers it, up
	// to its social_links value; append that and a closing brace.
	settingsColonyJSON = `{"id":"` + settingsColonyID + `","name":"general","display_name":"General",` +
		`"description":"","member_count":3,"is_default":true,"is_sandbox":false,` +
		`"community_type":"public","crowd_control_level":"off","created_at":"2026-01-01T00:00:00Z",` +
		`"notice":null,"social_links":`
	storedLinksJSON = `{"website":"https://example.com","github":"TheColonyAI","x":"thecolonyai",` +
		`"youtube":"@thecolony"}`
)

func TestSubColonySocialLinksDecode(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *ColonySocialLinks
	}{
		{"every link", `{"id":"c1","social_links":` + storedLinksJSON + `}`, &ColonySocialLinks{
			Website: Ptr("https://example.com"), GitHub: Ptr("TheColonyAI"),
			X: Ptr("thecolonyai"), YouTube: Ptr("@thecolony"),
		}},
		// The server sends a link that is not set as null; a key left out
		// must read the same.
		{"some links null", `{"id":"c1","social_links":{"website":null,"github":"TheColonyAI","x":null,` +
			`"youtube":"channel/UCabcdefghijklmnopqrstuv"}}`, &ColonySocialLinks{
			GitHub: Ptr("TheColonyAI"), YouTube: Ptr("channel/UCabcdefghijklmnopqrstuv"),
		}},
		{"some links absent", `{"id":"c1","social_links":{"github":"TheColonyAI"}}`,
			&ColonySocialLinks{GitHub: Ptr("TheColonyAI")}},
		// A colony with no links.
		{"null", `{"id":"c1","social_links":null}`, nil},
		// A platform before 2026-11-14c.
		{"absent", `{"id":"c1"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c SubColony
			if err := json.Unmarshal([]byte(tc.body), &c); err != nil {
				t.Fatal(err)
			}
			checkLinks(t, c.SocialLinks, tc.want)
			// Modelled, so no longer collected as an unknown field.
			if v, ok := c.Extra["social_links"]; ok {
				t.Errorf("social_links is in Extra as well: %v", v)
			}
		})
	}
}

// checkLinks reports how got differs from want, nil meaning no links.
func checkLinks(t *testing.T, got, want *ColonySocialLinks) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("SocialLinks = %+v, want nil", *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("SocialLinks = nil, want %+v", *want)
	}
	for _, f := range []struct {
		name      string
		got, want *string
	}{
		{"Website", got.Website, want.Website},
		{"GitHub", got.GitHub, want.GitHub},
		{"X", got.X, want.X},
		{"YouTube", got.YouTube, want.YouTube},
	} {
		if !equalStrPtr(f.got, f.want) {
			t.Errorf("SocialLinks.%s = %s, want %s", f.name, strPtrText(f.got), strPtrText(f.want))
		}
	}
}

func strPtrText(p *string) string {
	if p == nil {
		return "nil"
	}
	return `"` + *p + `"`
}

func TestUpdateColonySettingsSendsTheWholeSet(t *testing.T) {
	cases := []struct {
		name  string
		links ColonySocialLinks
		want  string // the social_links object the request carries
	}{
		{
			// Sent as given: the server normalises, so example.com and the
			// github.com URL go as they are.
			name: "every link",
			links: ColonySocialLinks{
				Website: Ptr("example.com"), GitHub: Ptr("https://github.com/TheColonyAI"),
				X: Ptr("@thecolonyai"), YouTube: Ptr("https://www.youtube.com/@thecolony"),
			},
			want: `{"website":"example.com","github":"https://github.com/TheColonyAI",` +
				`"x":"@thecolonyai","youtube":"https://www.youtube.com/@thecolony"}`,
		},
		{
			// A link left nil is left out, which the server reads as cleared.
			name:  "one link",
			links: ColonySocialLinks{GitHub: Ptr("TheColonyAI")},
			want:  `{"github":"TheColonyAI"}`,
		},
		{
			// An empty set clears every link: the server's documented {}.
			name:  "clear all",
			links: ColonySocialLinks{},
			want:  `{}`,
		},
		{
			// A pointer to "" goes as "", which the server also reads as no
			// link; the client does not second-guess it.
			name:  "empty string",
			links: ColonySocialLinks{Website: Ptr(""), X: Ptr("thecolonyai")},
			want:  `{"website":"","x":"thecolonyai"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{200, settingsColonyJSON + storedLinksJSON + `}`, nil})
			links := tc.links
			got, err := c.UpdateColonySettings(context.Background(), "general",
				UpdateColonySettingsRequest{SocialLinks: &links})
			if err != nil {
				t.Fatal(err)
			}
			if len(*reqs) != 1 {
				t.Fatalf("%d requests, want 1 (general's id is known, so no lookup)", len(*reqs))
			}
			r := (*reqs)[0]
			if r.method != http.MethodPatch || r.raw != "/colonies/"+settingsColonyID {
				t.Errorf("request = %s %s, want PATCH /colonies/%s", r.method, r.raw, settingsColonyID)
			}
			if ct := r.header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(r.body, &body); err != nil {
				t.Fatalf("body %q: %v", r.body, err)
			}
			// social_links and nothing else: every other setting is left as it is.
			if len(body) != 1 {
				t.Errorf("body = %s, want social_links alone", r.body)
			}
			if !sameJSON(t, body["social_links"], tc.want) {
				t.Errorf("social_links sent as %s, want %s", body["social_links"], tc.want)
			}
			if got.ID != settingsColonyID || got.Name != "general" {
				t.Errorf("decoded colony = %+v", got)
			}
			checkLinks(t, got.SocialLinks, &ColonySocialLinks{
				Website: Ptr("https://example.com"), GitHub: Ptr("TheColonyAI"),
				X: Ptr("thecolonyai"), YouTube: Ptr("@thecolony"),
			})
		})
	}
}

// sameJSON reports whether got and want are the same JSON value.
func sameJSON(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%q is not JSON: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	return string(gb) == string(wb)
}

func TestUpdateColonySettingsRefusesAnEmptyChange(t *testing.T) {
	// A slug the package does not know, so a lookup would show as a request.
	c, reqs := projectServer(t, projectReply{200, `[]`, nil})
	got, err := c.UpdateColonySettings(context.Background(), "not-a-known-colony", UpdateColonySettingsRequest{})
	if err == nil {
		t.Fatalf("an empty change was accepted: %+v", got)
	}
	if got != nil {
		t.Errorf("colony = %+v alongside the error", got)
	}
	if len(*reqs) != 0 {
		t.Errorf("%d request(s) made to refuse an empty change, first %s %s", len(*reqs), (*reqs)[0].method, (*reqs)[0].raw)
	}
}

// A link the server cannot use is its 422, returned as a *ValidationError
// without a retry.
func TestUpdateColonySettingsRefusedLink(t *testing.T) {
	c, reqs := projectServer(t, projectReply{422,
		`{"detail":[{"loc":["body","social_links","youtube"],"msg":"Value error, That is a video, not a channel.",` +
			`"type":"value_error"}]}`, nil})
	got, err := c.UpdateColonySettings(context.Background(), settingsColonyID, UpdateColonySettingsRequest{
		SocialLinks: &ColonySocialLinks{YouTube: Ptr("https://www.youtube.com/watch?v=dQw4w9WgXcQ")},
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Status != 422 {
		t.Errorf("error = %#v, want a *ValidationError 422", err)
	}
	if got != nil {
		t.Errorf("colony = %+v alongside the error", got)
	}
	if len(*reqs) != 1 {
		t.Errorf("%d requests, want 1: a 422 is not retried", len(*reqs))
	}
}
