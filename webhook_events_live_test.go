//go:build live

package colony

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Run with: go test -tags live -run TestCatalogueSnapshotIsCurrent ./...
//
// This is the check the offline suite structurally cannot make. A committed
// snapshot is only as fresh as the last person who ran the generator, so
// TestEventConstantsMatchTheCatalogue can confirm the constants agree with the
// snapshot and learn nothing about whether either agrees with the platform.
//
// It needs no credentials — GET /webhooks/events is public — so it can run on
// a schedule in CI rather than depending on someone remembering.
func TestCatalogueSnapshotIsCurrent(t *testing.T) {
	snap := loadSnapshot(t)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("https://thecolony.ai/api/v1/webhooks/events")
	if err != nil {
		t.Fatalf("fetch catalogue: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /webhooks/events: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Events []struct {
			Name string `json:"name"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode catalogue: %v", err)
	}
	if len(body.Events) == 0 {
		t.Fatal("live catalogue returned 0 events — refusing to compare against nothing")
	}

	live := map[string]bool{}
	for _, e := range body.Events {
		live[e.Name] = true
	}
	local := map[string]bool{}
	for _, e := range snap.Events {
		local[e.Name] = true
	}

	// Same function the per-PR control in snapshot_compare_test.go exercises.
	// The weekly job and the control must run the SAME comparator, or the
	// control certifies a different instrument than the one it gates.
	added, removed := catalogueDiff(live, local)

	if len(added) > 0 {
		t.Errorf("the platform serves %d event(s) the snapshot does not know "+
			"(snapshot taken %s) — run `go generate ./...`:\n  %s",
			len(added), snap.FetchedAt, strings.Join(added, "\n  "))
	}
	if len(removed) > 0 {
		t.Errorf("the snapshot has %d event(s) the platform no longer serves:\n  %s",
			len(removed), strings.Join(removed, "\n  "))
	}
}
