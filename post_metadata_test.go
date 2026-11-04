package colony_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// A post's metadata, under both of its names.
//
// Release 2026-11-04a made `metadata` the name posts answer with, the name
// they are created with, and kept `metadata_` as a deprecated copy carrying
// the same value. Post reads `metadata`; a server before that release, or a
// response kept from one, sends only `metadata_`, which is read in its place.

func decodePost(t *testing.T, body string) colony.Post {
	t.Helper()
	var p colony.Post
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPostMetadataIsReadFromMetadata(t *testing.T) {
	p := decodePost(t, `{"id":"p1","metadata":{"poll_options":["yes","no"]}}`)
	opts, ok := p.Metadata["poll_options"].([]any)
	if !ok || len(opts) != 2 || opts[0] != "yes" {
		t.Errorf("Metadata = %v, want the poll options sent as metadata", p.Metadata)
	}
	if _, ok := p.Extra["metadata"]; ok {
		t.Error("Extra[\"metadata\"] is set; a modelled field belongs on the struct")
	}
}

func TestPostMetadataFallsBackToMetadataUnderscore(t *testing.T) {
	for name, body := range map[string]string{
		"absent": `{"id":"p1","metadata_":{"budget_min_sats":100}}`,
		"null":   `{"id":"p1","metadata":null,"metadata_":{"budget_min_sats":100}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := decodePost(t, body)
			if p.Metadata["budget_min_sats"] != float64(100) {
				t.Errorf("Metadata = %v, want metadata_'s value when metadata is %s", p.Metadata, name)
			}
			if _, ok := p.Extra["metadata_"]; ok {
				t.Error("Extra[\"metadata_\"] is set; it is read into Metadata")
			}
		})
	}
}

// The server sends the two with the same value. Were they ever to differ,
// the current name is the one that counts.
func TestPostMetadataPrefersMetadataToMetadataUnderscore(t *testing.T) {
	p := decodePost(t, `{"id":"p1","metadata":{"v":"new"},"metadata_":{"v":"old"}}`)
	if p.Metadata["v"] != "new" {
		t.Errorf("Metadata = %v, want metadata's value", p.Metadata)
	}
	if len(p.Extra) != 0 {
		t.Errorf("Extra = %v, want nothing: both names are read", p.Extra)
	}
}

// CONTROL: a post with neither name has no metadata, and nothing in Extra,
// so the tests above are not passing on a Metadata that is always set.
func TestPostWithoutMetadataHasNone(t *testing.T) {
	p := decodePost(t, `{"id":"p1","title":"T"}`)
	if p.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", p.Metadata)
	}
	if p.Extra != nil {
		t.Errorf("Extra = %v, want nil", p.Extra)
	}
}

func TestPostEncodesMetadataUnderItsCurrentName(t *testing.T) {
	b, err := json.Marshal(colony.Post{ID: "p1", Metadata: map[string]any{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["metadata"]; !ok {
		t.Errorf("encoded post has no metadata: %s", b)
	}
	if _, ok := m["metadata_"]; ok {
		t.Errorf("encoded post still writes metadata_: %s", b)
	}
	// And it reads back.
	if back := decodePost(t, string(b)); back.Metadata["k"] != "v" {
		t.Errorf("round trip lost Metadata: %v", back.Metadata)
	}
}

// The same through a call, on a list as the server sends it today: both
// names, with one value.
func TestGetPostsReadsMetadata(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"items":[`+
				`{"id":"p1","metadata":{"deadline":"2026-12-01"},"metadata_":{"deadline":"2026-12-01"}},`+
				`{"id":"p2","metadata_":{"deadline":"2026-12-02"}}`+
				`],"total":2,"has_more":false}`)
		},
	}))
	list, err := client.GetPosts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(list.Items))
	}
	if got := list.Items[0].Metadata["deadline"]; got != "2026-12-01" {
		t.Errorf("Items[0].Metadata[deadline] = %v", got)
	}
	if got := list.Items[1].Metadata["deadline"]; got != "2026-12-02" {
		t.Errorf("Items[1].Metadata[deadline] = %v, want the metadata_ fallback", got)
	}
}
