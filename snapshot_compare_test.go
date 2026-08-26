package colony

import (
	"sort"
	"strings"
	"testing"
)

// The comparison halves of the two live drift checks live here, OUT of the
// `live` build tag, for one reason: the arm that proves a comparator can
// discriminate does not need the network, and only the arm that asks the
// platform does.
//
// Why this file exists at all. `Catalogue drift` (.github/workflows) runs
// TestCatalogueSnapshotIsCurrent and TestSchemaSnapshotIsCurrent weekly. On
// 2026-08-26 it had run ZERO times — its cron is Mondays and it merged on a
// Tuesday — so I dispatched it by hand and read the logs rather than the tick.
// Both tests genuinely executed and both passed.
//
// Then I looked at what a pass meant. Neither test had a must-fail arm: two
// test functions, no mutation, no control. A weekly green from a comparator
// that has never been shown to go red certifies that nothing threw, not that
// anything was checked. That is the defect this package's own conformance
// checker exists to catch, sitting in the job that watches the checker.
//
// So the comparisons are functions now, and the controls below feed them a
// deliberately corrupted snapshot and require them to complain. Those controls
// run on EVERY pull request. The weekly job supplies the only thing they
// cannot: the platform's current answer.

// catalogueDiff reports events the platform serves that the snapshot lacks
// (added) and events the snapshot holds that the platform no longer serves
// (removed). Both directions matter: a constant naming an event the server does
// not serve is a subscription that silently never fires.
func catalogueDiff(live, local map[string]bool) (added, removed []string) {
	for n := range live {
		if !local[n] {
			added = append(added, n)
		}
	}
	for n := range local {
		if !live[n] {
			removed = append(removed, n)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// refNames returns every schema name this property reaches through a $ref,
// following anyOf. Used to tell "the two sides disagree about the type" from
// "one side could not resolve the reference", which are different findings
// and only one of them is drift.
func (p openAPIProp) refNames() []string {
	if len(p.AnyOf) > 0 {
		var out []string
		for _, alt := range p.AnyOf {
			out = append(out, alt.refNames()...)
		}
		return out
	}
	if p.Ref == "" {
		return nil
	}
	name := p.Ref
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return []string{name}
}

func unresolved(p openAPIProp, refs map[string]openAPISchema) []string {
	var missing []string
	for _, n := range p.refNames() {
		if _, ok := refs[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing
}

// schemaDrift reports every way the committed snapshot disagrees with a live
// schema catalogue: schemas withdrawn, properties added or removed, property
// types changed, and $refs that one side cannot resolve.
//
// That last category is here because the first run of the control below found
// it. jsonTypes returns nil for an unresolvable $ref -- deliberately, "say
// nothing rather than assert a type" -- and the old comparison then read nil
// against ["object"] as A TYPE CHANGE. So a ref table that is complete on one
// side and not the other produced seven confident, wrong findings, each
// phrased as though the platform had altered a field. A wrong diagnosis is
// worse than no diagnosis, and this one would have been read as real drift and
// sent someone to regenerate a snapshot that was already correct.
func schemaDrift(snap openAPISnapshot, live map[string]openAPISchema) []string {
	var drift []string
	for name, local := range snap.Schemas {
		liveSch, ok := live[name]
		if !ok {
			drift = append(drift, name+": no longer declared by the API")
			continue
		}
		for f, lp := range liveSch.Properties {
			sp, ok := local.Properties[f]
			if !ok {
				drift = append(drift, name+"."+f+": added by the API since the snapshot")
				continue
			}
			// Resolvability first: a missing $ref target is not a type change.
			if m := unresolved(lp, live); len(m) > 0 {
				drift = append(drift, name+"."+f+": $ref unresolvable in the live spec ("+
					strings.Join(m, ", ")+") — not a type change")
				continue
			}
			if m := unresolved(sp, snap.Refs); len(m) > 0 {
				drift = append(drift, name+"."+f+": $ref unresolvable in the snapshot ("+
					strings.Join(m, ", ")+") — regenerate rather than reading this as drift")
				continue
			}
			if strings.Join(lp.jsonTypes(live), ",") !=
				strings.Join(sp.jsonTypes(snap.Refs), ",") {
				drift = append(drift, name+"."+f+": type changed since the snapshot")
			}
		}
		for f := range local.Properties {
			if _, ok := liveSch.Properties[f]; !ok {
				drift = append(drift, name+"."+f+": removed by the API since the snapshot")
			}
		}
	}
	sort.Strings(drift)
	return drift
}

// TestCatalogueDiffCanFail is the control for the weekly catalogue check.
//
// Three arms, and the first one is as load-bearing as the other two: a
// comparator that reports a difference unconditionally would pass an
// injection test while certifying nothing.
func TestCatalogueDiffCanFail(t *testing.T) {
	base := map[string]bool{"post_created": true, "comment_created": true, "dm_received": true}

	t.Run("identical inputs report nothing", func(t *testing.T) {
		same := map[string]bool{}
		for k, v := range base {
			same[k] = v
		}
		added, removed := catalogueDiff(base, same)
		if len(added) != 0 || len(removed) != 0 {
			t.Fatalf("identical catalogues must be silent, got added=%v removed=%v", added, removed)
		}
	})

	t.Run("an event the snapshot lacks is reported as added", func(t *testing.T) {
		local := map[string]bool{"post_created": true, "comment_created": true}
		added, removed := catalogueDiff(base, local)
		if len(added) != 1 || added[0] != "dm_received" {
			t.Fatalf("want added=[dm_received], got %v", added)
		}
		if len(removed) != 0 {
			t.Fatalf("want no removals, got %v", removed)
		}
	})

	t.Run("an event the platform dropped is reported as removed", func(t *testing.T) {
		local := map[string]bool{}
		for k, v := range base {
			local[k] = v
		}
		local["event_that_never_existed"] = true
		added, removed := catalogueDiff(base, local)
		if len(removed) != 1 || removed[0] != "event_that_never_existed" {
			t.Fatalf("want removed=[event_that_never_existed], got %v", removed)
		}
		if len(added) != 0 {
			t.Fatalf("want no additions, got %v", added)
		}
	})
}

// TestSchemaDriftCanFail is the control for the weekly schema check, built
// from the REAL committed snapshot rather than a hand-made fixture — a
// comparator that works on a two-field toy and not on the shipped data would
// pass a toy control.
func TestSchemaDriftCanFail(t *testing.T) {
	snap := loadSchemas(t)
	if len(snap.Schemas) == 0 {
		t.Fatal("committed snapshot is empty — nothing to control against")
	}

	// The live side, faked as an exact copy of what is committed. This is the
	// arm that must be SILENT.
	//
	// It merges Refs as well as Schemas, because a real spec is ref-complete
	// and this stand-in has to be too. Omitting them is what surfaced the
	// unresolvable-ref defect above: the first version of this control fed a
	// ref-EMPTY live map and got seven "type changed" findings against the
	// snapshot's own contents.
	live := map[string]openAPISchema{}
	for k, v := range snap.Refs {
		live[k] = v
	}
	for k, v := range snap.Schemas {
		live[k] = v
	}

	t.Run("snapshot compared against itself is silent", func(t *testing.T) {
		if d := schemaDrift(snap, live); len(d) != 0 {
			t.Fatalf("the snapshot must not drift from itself, got %d finding(s):\n  %s",
				len(d), strings.Join(d, "\n  "))
		}
	})

	// Pick a schema that actually has properties, programmatically — never a
	// typed-in name, which would rot the first time the snapshot changed.
	var victim string
	var victimProp string
	for _, name := range sortedKeys(snap.Schemas) {
		for _, p := range sortedPropKeys(snap.Schemas[name].Properties) {
			victim, victimProp = name, p
			break
		}
		if victim != "" {
			break
		}
	}
	if victim == "" {
		t.Fatal("no schema in the snapshot has a property — cannot build a control")
	}

	t.Run("a property the platform added is reported", func(t *testing.T) {
		mutated := copySchemas(live)
		s := mutated[victim]
		props := copyProps(s.Properties)
		props["a_field_the_snapshot_has_never_seen"] = openAPIProp{}
		s.Properties = props
		mutated[victim] = s
		d := schemaDrift(snap, mutated)
		if !containsSubstr(d, victim+".a_field_the_snapshot_has_never_seen") {
			t.Fatalf("adding a property to the live side must be reported, got: %v", d)
		}
	})

	t.Run("a property the platform removed is reported", func(t *testing.T) {
		mutated := copySchemas(live)
		s := mutated[victim]
		props := copyProps(s.Properties)
		delete(props, victimProp)
		s.Properties = props
		mutated[victim] = s
		d := schemaDrift(snap, mutated)
		if !containsSubstr(d, victim+"."+victimProp+": removed") {
			t.Fatalf("removing %s.%s from the live side must be reported, got: %v",
				victim, victimProp, d)
		}
	})

	t.Run("a schema the platform withdrew is reported", func(t *testing.T) {
		mutated := copySchemas(live)
		delete(mutated, victim)
		d := schemaDrift(snap, mutated)
		if !containsSubstr(d, victim+": no longer declared") {
			t.Fatalf("withdrawing %s must be reported, got: %v", victim, d)
		}
	})
}

func sortedKeys(m map[string]openAPISchema) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedPropKeys(m map[string]openAPIProp) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func copySchemas(in map[string]openAPISchema) map[string]openAPISchema {
	out := make(map[string]openAPISchema, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyProps(in map[string]openAPIProp) map[string]openAPIProp {
	out := make(map[string]openAPIProp, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func containsSubstr(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}
