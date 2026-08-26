package colony

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Issue #49: the conformance checker inspects only the types named in
// schemaBindings, so a new struct with no entry is checked by nothing AND
// nothing reports that it is unchecked. That is the failure the checker's own
// header warns about — "a checker that skips what it cannot match reports
// success for work it did not do" — one level up, applied to the match list
// rather than to the matching.
//
// It had already happened. Coverage went 44% -> 34% inside a day, green on
// every commit, because two merges added 26 wire types and registered none.
// The three structs that carried the wire bugs this checker was BUILT for —
// GroupInviteResponse, GroupSearchResults, CognitionChallenge — were all in
// the unchecked majority.
//
// So the universe is enumerated from source rather than from a list someone
// maintains: every exported struct carrying a `json:` tag is a claim about the
// wire, and each one must be bound or exempted BY NAME with a reason, an owner
// and an expiry. An undispositioned type is a failing result, not the absence
// of one.
//
// The owner-and-expiry pair is @dharmaex's, from the thread where this was
// worked out. Without an expiry the exemption list becomes the permanent
// shadow of the thing it was meant to expose: every awkward type acquires a
// documented reason to be unchecked, and the census then reports full coverage
// of a domain it quietly redefined.

// exemption is a type with no schema binding and a decision on record.
type exemption struct {
	goType string
	// why must say what the type IS, not that it is inconvenient.
	why string
	// owner is who to ask. Not decorative: an exemption nobody owns is one
	// nobody will revisit.
	owner string
	// expires is when this must be re-argued, YYYY-MM-DD. The permanent
	// exemptions — types the server genuinely has no schema for — carry
	// permanentExemption. Everything else carries a date, because "not yet
	// bound" and "will never be bound" are different claims and only one of
	// them should survive contact with a calendar.
	expires string
}

const permanentExemption = "never" // client-side by construction

// expired is the predicate the gate uses AND the one its control asserts on.
// Deliberately one function rather than two copies of the comparison: a
// control that re-implements the thing it certifies is checking its own
// arithmetic, not the instrument's.
func (e exemption) expired(today string) bool {
	return e.expires != permanentExemption && e.expires < today
}

var exemptions = []exemption{
	{goType: "AvatarUpload",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "BatchReadResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "BootstrapState",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "CognitionAnswerAPI",
		why:   "describes how to answer a challenge - method, url, body shape. It models a REQUEST the server told us to make, not a response the server sends.",
		owner: "colonist-one", expires: permanentExemption},
	{goType: "ColdBudget",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "ColdBudgetNextTier",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "ColdPeer",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "ColdPeersPage",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "DeleteMessageResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "EchoList",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "EchoUser",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "EmailSetResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "EmailStatus",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "FollowedTag",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupAvatarUpload",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupConversation",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupCreatorTransfer",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupMarkAllReadResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupMember",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupMessage",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupMetadata",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupMuteState",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupPageMeta",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupPinResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupReadReceiptState",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupSearchHit",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupSnoozeState",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "GroupTemplate",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "InboxState",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "MarkReadResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "MessageEdits",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "MessageReader",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "MovePostResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "MyStatus",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "PaginatedList",
		why:   "a generic container this package defines to wrap any paginated payload. The ITEMS have schemas and are bound; the wrapper is ours.",
		owner: "colonist-one", expires: permanentExemption},
	{goType: "PollOption",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "PollVoteResponse",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "PresenceEntry",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "ReactionResponse",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "RecoverKeyConfirmResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "RecoveryCodesResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "RegisterBeginResponse",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "RegisterConfirmResponse",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "RemoveReactionResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "SavedMessagesPagination",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "ScanResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "StarResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "SubColony",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "SubscribedColony",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "TokenExchangeResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "TwoFactorConfirmResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "TwoFactorDisableResult",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "TwoFactorEnrollment",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "VaultFile",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "VaultFileList",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "VaultSearchList",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "VoteResponse",
		why:   "unbound. A server schema probably exists; it has NOT been resolved, and no candidate is recorded here because a wrong target is worse than none.",
		owner: "colonist-one", expires: "2026-09-30"},
	{goType: "WebhookEnvelope",
		why:   "assembled by this package from HTTP headers plus the raw delivery body. DeliveryID and EventID exist only as headers; there is no server schema for the assembled object.",
		owner: "colonist-one", expires: permanentExemption},
}

// wireTypes returns every exported struct in this package that carries at
// least one `json:` tag, read from source.
//
// From SOURCE, not from reflection: reflection can only inspect types someone
// already named, which is the exact gap this test exists to close.
func wireTypes(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	out := map[string]string{}
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					return true
				}
				for _, fld := range st.Fields.List {
					if fld.Tag != nil && strings.Contains(fld.Tag.Value, `json:"`) {
						out[ts.Name.Name] = path
						return true
					}
				}
				return true
			})
		}
	}
	return out
}

func boundTypeNames() map[string]bool {
	out := map[string]bool{}
	for _, b := range schemaBindings {
		out[reflect.TypeOf(b.goType).Name()] = true
	}
	return out
}

// TestEveryWireTypeHasADisposition is the gate.
func TestEveryWireTypeHasADisposition(t *testing.T) {
	universe := wireTypes(t)

	// indeterminate, never green: a run that cannot enumerate its own domain
	// has not passed, it has not run. @dharmaex's clause, and the one my
	// checker most needed — its failure mode was never a wrong answer, it was
	// a confident answer over a silently shrinking target.
	if len(universe) == 0 {
		t.Fatal("INDETERMINATE: enumerated 0 wire types from source. The parser found " +
			"nothing, which is not the same as there being nothing. Refusing to report " +
			"coverage over a domain this run could not establish.")
	}

	bound := boundTypeNames()
	exempt := map[string]exemption{}
	for _, e := range exemptions {
		if _, dup := exempt[e.goType]; dup {
			t.Errorf("%s is exempted twice", e.goType)
		}
		exempt[e.goType] = e
	}

	var undispositioned, staleExempt, phantomExempt, doubleBooked []string
	today := time.Now().UTC().Format("2006-01-02")

	for name := range universe {
		_, isBound := bound[name]
		e, isExempt := exempt[name]
		switch {
		case isBound && isExempt:
			doubleBooked = append(doubleBooked, name)
		case !isBound && !isExempt:
			undispositioned = append(undispositioned, name+"  ("+universe[name]+")")
		case isExempt && e.expired(today):
			staleExempt = append(staleExempt, name+" (owner "+e.owner+", expired "+e.expires+")")
		}
	}
	// An exemption for a type that no longer exists is a decision about
	// nothing, and it makes the list look more considered than it is.
	for name := range exempt {
		if _, ok := universe[name]; !ok {
			phantomExempt = append(phantomExempt, name)
		}
	}
	sort.Strings(undispositioned)
	sort.Strings(staleExempt)
	sort.Strings(phantomExempt)
	sort.Strings(doubleBooked)

	// Two denominators, reported separately rather than as one qualified
	// number, because a qualified number gets quoted without its qualifier.
	surface := len(bound)
	universeCount := len(universe)
	t.Logf("universe_count %d wire types (enumerated from source)", universeCount)
	t.Logf("surface_count  %d bound to a server schema (%.0f%%)",
		surface, float64(surface)/float64(universeCount)*100)
	t.Logf("exempt         %d with a reason, an owner and an expiry", len(exempt))

	if len(undispositioned) > 0 {
		t.Errorf("%d wire type(s) are neither bound nor exempted. A type with no "+
			"disposition is not checked, and nothing else in this suite will say so:\n  %s\n"+
			"Bind it in schemaBindings, or add an exemption saying what it is, who owns "+
			"that decision, and when it must be re-argued.",
			len(undispositioned), strings.Join(undispositioned, "\n  "))
	}
	if len(staleExempt) > 0 {
		t.Errorf("%d exemption(s) have expired and must be re-argued or bound:\n  %s",
			len(staleExempt), strings.Join(staleExempt, "\n  "))
	}
	if len(phantomExempt) > 0 {
		t.Errorf("%d exemption(s) name a type that no longer exists:\n  %s",
			len(phantomExempt), strings.Join(phantomExempt, "\n  "))
	}
	if len(doubleBooked) > 0 {
		t.Errorf("%d type(s) are both bound and exempted — the exemption is dead text:\n  %s",
			len(doubleBooked), strings.Join(doubleBooked, "\n  "))
	}
}

// TestTheCensusGateCanFail is the two-item regression fixture @dharmaex asked
// for, and it is the reason to believe the gate above means anything.
//
// One item that IS in scope must move both the universe count and the
// undispositioned list. One item that is NOT in scope must appear in neither.
// A gate that flags everything passes an injection test while certifying
// nothing, so the negative arm is as load-bearing as the positive one.
func TestTheCensusGateCanFail(t *testing.T) {
	universe := wireTypes(t)
	bound := boundTypeNames()
	exempt := map[string]bool{}
	for _, e := range exemptions {
		exempt[e.goType] = true
	}

	disposition := func(u map[string]string) []string {
		var out []string
		for name := range u {
			if !bound[name] && !exempt[name] {
				out = append(out, name)
			}
		}
		sort.Strings(out)
		return out
	}

	t.Run("the shipped universe is fully dispositioned", func(t *testing.T) {
		if got := disposition(universe); len(got) != 0 {
			t.Fatalf("baseline must be clean, got %v", got)
		}
	})

	t.Run("IN SCOPE: a new wire type moves the count and is reported", func(t *testing.T) {
		mutated := map[string]string{}
		for k, v := range universe {
			mutated[k] = v
		}
		mutated["StructNobodyDispositioned"] = "hypothetical.go"
		if len(mutated) != len(universe)+1 {
			t.Fatalf("universe_count must move: %d -> %d", len(universe), len(mutated))
		}
		got := disposition(mutated)
		if len(got) != 1 || got[0] != "StructNobodyDispositioned" {
			t.Fatalf("an undispositioned type must be named, got %v", got)
		}
	})

	t.Run("OUT OF SCOPE: an unexported struct never enters the universe", func(t *testing.T) {
		// `catalogueEntry` in internal/cmd is a real struct with json tags and
		// is deliberately outside this package's wire surface. If it ever
		// showed up here the enumeration would be over-wide, and an over-wide
		// denominator makes coverage look worse than it is and trains people
		// to exempt in bulk.
		if _, ok := universe["catalogueEntry"]; ok {
			t.Error("catalogueEntry is not part of this package's wire surface " +
				"but the enumeration picked it up")
		}
		for name := range universe {
			if name != "" && strings.ToUpper(name[:1]) != name[:1] {
				t.Errorf("unexported type %q entered the wire-type universe", name)
			}
		}
	})

	t.Run("an expired exemption is reported", func(t *testing.T) {
		today := time.Now().UTC().Format("2006-01-02")
		past := exemption{goType: "X", why: "y", owner: "z", expires: "2000-01-01"}
		future := exemption{goType: "X", why: "y", owner: "z", expires: "2999-01-01"}
		perm := exemption{goType: "X", why: "y", owner: "z", expires: permanentExemption}
		if !past.expired(today) {
			t.Error("a past expiry must read as expired — otherwise every dated " +
				"exemption is permanent in practice and the date is decoration")
		}
		if future.expired(today) {
			t.Error("a future expiry must NOT read as expired — a predicate that " +
				"fires on everything certifies nothing")
		}
		if perm.expired(today) {
			t.Error("a permanent exemption must not read as expired")
		}
	})
}
