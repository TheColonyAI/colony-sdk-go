//go:build live

package colony

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Run with: go test -tags live -run TestSchemaSnapshotIsCurrent ./...
//
// The offline check compares this package's structs to a COMMITTED snapshot,
// and a snapshot is only ever as fresh as the last person who ran the
// generator. It therefore cannot notice the server changing a field — which is
// the failure it exists to catch, one level up.
//
// This asks the running API. GET /openapi.json is public, so it needs no
// credentials and cannot leak one.
func TestSchemaSnapshotIsCurrent(t *testing.T) {
	snap := loadSchemas(t)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get("https://thecolony.ai/openapi.json")
	if err != nil {
		t.Fatalf("fetch spec: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /openapi.json: HTTP %d", resp.StatusCode)
	}
	var spec struct {
		Components struct {
			Schemas map[string]openAPISchema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if len(spec.Components.Schemas) == 0 {
		t.Fatal("the live spec declares 0 schemas — refusing to compare against nothing")
	}

	// Same function the per-PR control in snapshot_compare_test.go exercises,
	// so the weekly green and the control are statements about one comparator.
	drift := schemaDrift(snap, spec.Components.Schemas)
	if len(drift) > 0 {
		t.Errorf("the snapshot (taken %s) is %d change(s) behind the API — "+
			"run `go generate ./...`:\n  %s",
			snap.FetchedAt, len(drift), strings.Join(drift, "\n  "))
	}
}
