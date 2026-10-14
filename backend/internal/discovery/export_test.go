package discovery

import "time"

// Hooks of the batch candidate queries for the integration tests of
// package discovery_test (WP-333).
var (
	BatchKeys        = batchKeys
	AssignCandidates = assignCandidates
	AssignDeleted    = assignDeleted
)

// IdentityBranchQuery returns the batch candidate query, live or of the
// soft-deleted CIs, reduced to its branch name, with the arguments of its
// first page; ok is false when keys yield no such branch. keys should carry
// only the kind of that branch, so that every argument is referenced.
func IdentityBranchQuery(orgID string, keys *IdentityKeys, deleted bool, name string) (query string, args []any, ok bool) {
	branches, base := identityBranches(keys, deleted)
	for _, b := range branches {
		if b.name != name {
			continue
		}
		args = append([]any{orgID}, base...)
		if deleted {
			return deletedHitsSQL([]identityBranch{b}), args, true
		}
		return identityHitsSQL([]identityBranch{b}, len(base)+2), append(args, "", time.Time{}, "", int64(0), identityPage), true
	}
	return "", nil, false
}
