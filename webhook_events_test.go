package colony

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// Issue #36: the SDK declared 14 event constants against the server's 58, and
// nothing here could notice, because the list was authored and read only by
// this package. A model of the platform that only its author reads cannot
// drift detectably — the same shape as #33 one level up.
//
// There are two checks, and the division is the point.
//
//   - This file is OFFLINE and gates every PR: the constants must match the
//     committed catalogue snapshot exactly, both directions.
//   - webhook_events_live_test.go is ONLINE, behind the `live` build tag: the
//     snapshot must match the server.
//
// The offline test cannot detect the server adding an event — a snapshot is
// only ever as fresh as the last person who ran the generator. Saying so and
// putting the detection somewhere else is the honest structure; a single
// offline test would look like coverage of a question it cannot answer.

type catalogueSnapshot struct {
	FetchedAt string `json:"fetched_at"`
	Source    string `json:"source"`
	Events    []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"events"`
}

func loadSnapshot(t *testing.T) catalogueSnapshot {
	t.Helper()
	b, err := os.ReadFile("testdata/webhook_events.json")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap catalogueSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Events) == 0 {
		// An empty snapshot would make every comparison below pass
		// vacuously, which is exactly how a checker that returns zero reads
		// as a clean result.
		t.Fatal("snapshot contains 0 events — the comparison would be vacuous")
	}
	return snap
}

func TestEventConstantsMatchTheCatalogue(t *testing.T) {
	snap := loadSnapshot(t)

	inCatalogue := map[string]bool{}
	for _, e := range snap.Events {
		inCatalogue[e.Name] = true
	}
	declared := map[string]bool{}
	for _, v := range AllWebhookEvents {
		declared[v] = true
	}

	var missing, phantom []string
	for name := range inCatalogue {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	// The other direction matters as much: a constant for an event the server
	// does not serve is a subscription that silently never fires.
	for name := range declared {
		if !inCatalogue[name] {
			phantom = append(phantom, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(phantom)

	if len(missing) > 0 {
		t.Errorf("%d catalogue events have no constant — run `go generate ./...`:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
	if len(phantom) > 0 {
		t.Errorf("%d constants name events the catalogue does not serve:\n  %s",
			len(phantom), strings.Join(phantom, "\n  "))
	}
	if len(AllWebhookEvents) != len(snap.Events) {
		t.Errorf("AllWebhookEvents has %d entries, snapshot has %d",
			len(AllWebhookEvents), len(snap.Events))
	}
}

// The 14 identifiers that shipped before the generator existed must keep their
// names. The mechanical rule would rewrite EventFacilitationRevisionReq to
// EventFacilitationRevisionRequested — a silent breaking change delivered by a
// refresh, which is the worst way to ship one.
func TestPreGeneratorConstantNamesAreUnchanged(t *testing.T) {
	pinned := map[string]string{
		"post_created":                    EventPostCreated,
		"comment_created":                 EventCommentCreated,
		"bid_received":                    EventBidReceived,
		"bid_accepted":                    EventBidAccepted,
		"payment_received":                EventPaymentReceived,
		"direct_message":                  EventDirectMessage,
		"mention":                         EventMention,
		"task_matched":                    EventTaskMatched,
		"referral_completed":              EventReferralCompleted,
		"tip_received":                    EventTipReceived,
		"facilitation_claimed":            EventFacilitationClaimed,
		"facilitation_submitted":          EventFacilitationSubmitted,
		"facilitation_accepted":           EventFacilitationAccepted,
		"facilitation_revision_requested": EventFacilitationRevisionReq,
	}
	for want, got := range pinned {
		if got != want {
			t.Errorf("constant value changed: got %q, want %q", got, want)
		}
	}
	if len(pinned) != 14 {
		t.Errorf("pinned set has %d entries, want the original 14", len(pinned))
	}
}

func TestAllWebhookEventsIsSortedAndUnique(t *testing.T) {
	if !sort.StringsAreSorted(AllWebhookEvents) {
		t.Error("AllWebhookEvents is not sorted")
	}
	seen := map[string]bool{}
	for _, v := range AllWebhookEvents {
		if seen[v] {
			t.Errorf("duplicate entry %q", v)
		}
		seen[v] = true
	}
}

// The snapshot records when it was taken. Without that, "the snapshot matches
// the constants" is a statement with no date on it.
func TestSnapshotRecordsItsProvenance(t *testing.T) {
	snap := loadSnapshot(t)
	if snap.FetchedAt == "" {
		t.Error("snapshot has no fetched_at — its age is unknowable")
	}
	if !strings.HasSuffix(snap.Source, "/webhooks/events") {
		t.Errorf("snapshot source = %q", snap.Source)
	}
}
