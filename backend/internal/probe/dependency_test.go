package probe_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// chainOf builds a parent chain p0 ← p1 ← … ← pn (p<i> has the parent
// p<i-1>) in one group.
func chainOf(n int) map[string]probe.DepNode {
	nodes := map[string]probe.DepNode{"p0": {Group: "g"}}
	for i := 1; i <= n; i++ {
		nodes[fmt.Sprintf("p%d", i)] = probe.DepNode{Parent: fmt.Sprintf("p%d", i-1), Group: "g"}
	}
	return nodes
}

func lookupIn(nodes map[string]probe.DepNode) func(string) (probe.DepNode, bool) {
	return func(id string) (probe.DepNode, bool) {
		n, ok := nodes[id]
		return n, ok
	}
}

// TestDependencyChainDepth covers WP-965 AK4: the chain is followed up to 8
// levels; a deeper chain is cut there (deeper levels count as without
// parent); a cycle ends the chain.
func TestDependencyChainDepth(t *testing.T) {
	nodes := chainOf(12)
	got, exceeded := probe.Chain("p8", lookupIn(nodes))
	want := []string{"p7", "p6", "p5", "p4", "p3", "p2", "p1", "p0"}
	if !slices.Equal(got, want) || exceeded {
		t.Errorf("chain of p8 = %v (exceeded %v), want %v within the limit", got, exceeded, want)
	}
	got, exceeded = probe.Chain("p12", lookupIn(nodes))
	if len(got) != probe.MaxDependencyDepth || got[0] != "p11" || got[7] != "p4" || !exceeded {
		t.Errorf("chain of p12 = %v (exceeded %v), want 8 levels p11…p4 and cut", got, exceeded)
	}
	// A cycle (refused on write) ends the chain.
	cyc := map[string]probe.DepNode{"a": {Parent: "b"}, "b": {Parent: "a"}}
	if got, exceeded := probe.Chain("a", lookupIn(cyc)); !slices.Equal(got, []string{"b"}) || exceeded {
		t.Errorf("cycle: %v (exceeded %v)", got, exceeded)
	}
	// An unknown parent ends the chain after it.
	if got, _ := probe.Chain("x", lookupIn(map[string]probe.DepNode{"x": {Parent: "gone"}})); !slices.Equal(got, []string{"gone"}) {
		t.Errorf("unknown parent: %v", got)
	}
}

// TestDependencyBlockerAndAnchorExemption covers AK1: the nearest blocking
// ancestor (down or unknown) suppresses; the anchor suppresses every probe
// of the group but itself and its ancestors; up and degraded do not block.
func TestDependencyBlockerAndAnchorExemption(t *testing.T) {
	states := map[string]string{"gw": "down", "sw": "up", "anchor": "unknown", "anchorParent": "up"}
	if b := probe.Blocker("cpe", []string{"sw", "gw"}, "", nil, states); b != "gw" {
		t.Errorf("blocker %q, want the down grandparent gw", b)
	}
	if b := probe.Blocker("cpe", []string{"sw"}, "", nil, states); b != "" {
		t.Errorf("blocker %q behind an up parent, want none", b)
	}
	anchorChain := []string{"anchorParent"}
	if b := probe.Blocker("cpe", []string{"sw"}, "anchor", anchorChain, states); b != "anchor" {
		t.Errorf("blocker %q, want the unknown anchor", b)
	}
	if b := probe.Blocker("anchor", nil, "anchor", anchorChain, states); b != "" {
		t.Errorf("the anchor itself blocked by %q", b)
	}
	if b := probe.Blocker("anchorParent", nil, "anchor", anchorChain, states); b != "" {
		t.Errorf("an ancestor of the anchor blocked by %q", b)
	}
	for state, want := range map[string]bool{"down": true, "unknown": true, "up": false, "degraded": false, "": false} {
		if probe.Blocking(state) != want {
			t.Errorf("Blocking(%q) = %v", state, !want)
		}
	}
}

// TestDependencyCacheInvalidation covers AK4: cached parents and anchors
// hold for DependencyCacheTTL, a new node replaces the old one (a changed
// parent_probe_id), Invalidate drops probes, groups or everything.
func TestDependencyCacheInvalidation(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	c := probe.NewDependencyCache(func() time.Time { return now })
	c.SetNode("p", probe.DepNode{Parent: "a", Group: "g"})
	c.SetAnchor("g", "x")
	if n, ok := c.Node("p"); !ok || n.Parent != "a" {
		t.Fatalf("node %+v %v", n, ok)
	}
	c.SetNode("p", probe.DepNode{Parent: "b", Group: "g"})
	if n, _ := c.Node("p"); n.Parent != "b" {
		t.Errorf("a changed parent was not replaced: %+v", n)
	}
	c.Invalidate([]string{"p"}, nil)
	if _, ok := c.Node("p"); ok {
		t.Error("an invalidated probe is still cached")
	}
	if a, ok := c.Anchor("g"); !ok || a != "x" {
		t.Errorf("anchor %q %v after invalidating a probe only", a, ok)
	}
	c.Invalidate(nil, []string{"g"})
	if _, ok := c.Anchor("g"); ok {
		t.Error("an invalidated anchor is still cached")
	}
	c.SetNode("q", probe.DepNode{})
	c.SetAnchor("h", "")
	now = now.Add(probe.DependencyCacheTTL + time.Second)
	if _, ok := c.Node("q"); ok {
		t.Error("an expired node is still cached")
	}
	if _, ok := c.Anchor("h"); ok {
		t.Error("an expired anchor is still cached")
	}
	c.SetNode("q", probe.DepNode{})
	c.Invalidate(nil, nil)
	if _, ok := c.Node("q"); ok {
		t.Error("Invalidate without arguments kept a node")
	}
}
