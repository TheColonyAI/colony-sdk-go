//go:build live

package colony

import (
	"encoding/json"
	"net/http"
	"sort"
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

	var drift []string
	for name, local := range snap.Schemas {
		live, ok := spec.Components.Schemas[name]
		if !ok {
			drift = append(drift, name+": no longer declared by the API")
			continue
		}
		for f, lp := range live.Properties {
			sp, ok := local.Properties[f]
			if !ok {
				drift = append(drift, name+"."+f+": added by the API since the snapshot")
				continue
			}
			if strings.Join(lp.jsonTypes(spec.Components.Schemas), ",") !=
				strings.Join(sp.jsonTypes(snap.Refs), ",") {
				drift = append(drift, name+"."+f+": type changed since the snapshot")
			}
		}
		for f := range local.Properties {
			if _, ok := live.Properties[f]; !ok {
				drift = append(drift, name+"."+f+": removed by the API since the snapshot")
			}
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Errorf("the snapshot (taken %s) is %d change(s) behind the API — "+
			"run `go generate ./...`:\n  %s",
			snap.FetchedAt, len(drift), strings.Join(drift, "\n  "))
	}
}
