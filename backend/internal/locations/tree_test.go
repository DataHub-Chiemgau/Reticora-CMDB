package locations

import (
	"encoding/json"
	"testing"
)

// TestBuildTreeKeepsAllChildren covers WP-053 (LOC-07, LOC-10): the tree
// holds every node, also when a child is listed before its parent or after
// its parent was attached to the grandparent (the former tree attached value
// copies and lost these grandchildren).
func TestBuildTreeKeepsAllChildren(t *testing.T) {
	nodes := []Location{
		{ID: "bin", ParentID: "shelf", Kind: KindBin, Name: "Bin"},
		{ID: "site", Kind: KindSite, Name: "Site"},
		{ID: "wh", ParentID: "site", Kind: KindWarehouse, Name: "Warehouse"},
		{ID: "b", ParentID: "site", Kind: KindBuilding, Name: "Building"},
		{ID: "zone", ParentID: "wh", Kind: KindZone, Name: "Zone"},
		{ID: "room", ParentID: "b", Kind: KindRoom, Name: "Room"},
		{ID: "shelf", ParentID: "zone", Kind: KindShelf, Name: "Shelf"},
		{ID: "rack", ParentID: "room", Kind: KindRack, Name: "Rack"},
		{ID: "site2", Kind: KindSite, Name: "Another site"},
	}
	roots := BuildTree(nodes)
	if len(roots) != 2 || roots[0].ID != "site2" || roots[1].ID != "site" {
		t.Fatalf("roots %v, want site2 and site sorted by name", ids(roots))
	}
	if got := count(roots); got != len(nodes) {
		t.Errorf("tree holds %d nodes, want %d", got, len(nodes))
	}
	site := roots[1]
	if got := ids(site.Children); len(got) != 2 || got[0] != "b" || got[1] != "wh" {
		t.Fatalf("site children %v, want building and warehouse", got)
	}
	if path := chain(site.Children[1]); path != "wh/zone/shelf/bin" {
		t.Errorf("warehouse chain %q, want wh/zone/shelf/bin", path)
	}
	if path := chain(site.Children[0]); path != "b/room/rack" {
		t.Errorf("building chain %q, want b/room/rack", path)
	}

	// The JSON form carries the grandchildren and an empty list for leaves.
	raw, err := json.Marshal(roots)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		ID       string `json:"id"`
		Children []struct {
			ID       string            `json:"id"`
			Children []json.RawMessage `json:"children"`
		} `json:"children"`
	}
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded[1].Children) != 2 || len(decoded[1].Children[0].Children) != 1 || decoded[0].Children == nil {
		t.Errorf("JSON tree lost children: %s", raw)
	}
}

// TestBuildTreeOrphansBecomeRoots: a node whose parent is outside the
// caller's scope is shown as a root instead of being dropped.
func TestBuildTreeOrphansBecomeRoots(t *testing.T) {
	roots := BuildTree([]Location{
		{ID: "room", ParentID: "invisible-building", Kind: KindRoom, Name: "Room"},
		{ID: "rack", ParentID: "room", Kind: KindRack, Name: "Rack"},
		{ID: "self", ParentID: "self", Kind: KindZone, Name: "Self"},
	})
	if got := ids(roots); len(got) != 2 || got[0] != "room" || got[1] != "self" {
		t.Fatalf("roots %v, want room and self", got)
	}
	if count(roots) != 3 {
		t.Errorf("tree holds %d nodes, want 3", count(roots))
	}
	if BuildTree(nil) == nil {
		t.Error("empty tree is nil, want an empty list")
	}
}

func ids(nodes []*TreeNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}

func count(nodes []*TreeNode) int {
	total := len(nodes)
	for _, n := range nodes {
		total += count(n.Children)
	}
	return total
}

func chain(n *TreeNode) string {
	out := n.ID
	for len(n.Children) == 1 {
		n = n.Children[0]
		out += "/" + n.ID
	}
	return out
}
