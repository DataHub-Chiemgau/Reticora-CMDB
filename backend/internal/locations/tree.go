package locations

import "sort"

// TreeNode is a location with its children, for the tree view.
type TreeNode struct {
	Location
	Children []*TreeNode `json:"children"`
}

// BuildTree arranges nodes into a forest. Every node is allocated once and
// linked by pointer, so a child attached after its parent was attached to
// the grandparent still appears in the result (LOC-07: the former location
// node tree appended value copies and lost those children). A node whose
// parent is not among nodes, because it is outside the caller's scope or
// was filtered out, becomes a root. Siblings are sorted by name and id.
func BuildTree(nodes []Location) []*TreeNode {
	byID := make(map[string]*TreeNode, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = &TreeNode{Location: nodes[i], Children: []*TreeNode{}}
	}
	roots := []*TreeNode{}
	for i := range nodes {
		n := byID[nodes[i].ID]
		if parent, ok := byID[n.ParentID]; ok && n.ParentID != "" && parent != n {
			parent.Children = append(parent.Children, n)
			continue
		}
		roots = append(roots, n)
	}
	sortNodes(roots)
	return roots
}

func sortNodes(nodes []*TreeNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Name != nodes[j].Name {
			return nodes[i].Name < nodes[j].Name
		}
		return nodes[i].ID < nodes[j].ID
	})
	for _, n := range nodes {
		sortNodes(n.Children)
	}
}
