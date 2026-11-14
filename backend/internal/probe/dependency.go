package probe

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Simple dependencies between probes (PRB-03, PRB-05, WP-965). A probe
// depends on its parent probe (probe.parent_probe_id, transitively up to
// MaxDependencyDepth levels) and on the anchor probe of its collector group
// (collector_group.anchor_probe_id); the anchor itself and its ancestors are
// exempt from the anchor. While one of them is down or unknown:
//
//   - a down change of the child is applied as unknown with reason
//     parent_down (history notify false; the event probe.state_changed
//     names the blocking probe in suppressed_by);
//   - when a parent or anchor becomes down or unknown, every probe behind
//     it that is down at that moment becomes unknown/parent_down in the same
//     transaction (a history row of the server: collector_id NULL, the
//     epoch and sequence of the child's state; notify false).
//
// When the parent returns (up or degraded) the child stays
// unknown/parent_down until its next change or its next result: an ok result
// without remaining blocker makes it up with reason resync (notify false,
// the rule of WP-964 AK3); a failed one leaves it unknown until the next
// change of the collector.
//
// The chain is read through a cache (DependencyCache) per lane of the state
// worker, not walked per change in the database: the parent of the changing
// probe comes fresh with its row (a changed parent_probe_id replaces the
// entry), the entries of the ancestors and the anchors of the groups expire
// after DependencyCacheTTL. The query that reads the states of the chain
// returns the current parent_probe_id of every probe in it and the current
// anchor_probe_id of the group as well; an entry that no longer matches
// (a change by the API in any process) is dropped and the chain read again
// in the same change (reticora_probe_dependency_cache_stale_total). A chain deeper than
// MaxDependencyDepth is cut there: the deeper levels count as without
// parent (reticora_probe_dependency_depth_exceeded_total).

// ReasonResync is the reason of the server when an ok result ends an
// unknown/parent_down without a change of the collector (WP-965 AK3, the
// recovery rule of WP-964 AK3).
const ReasonResync = "resync"

// MaxDependencyDepth is the deepest level of the parent chain that is
// evaluated (AK4).
const MaxDependencyDepth = 8

// DependencyCacheTTL is how long a cached parent or anchor holds.
const DependencyCacheTTL = 30 * time.Second

var dependencyDepthExceeded = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: "reticora", Name: "probe_dependency_depth_exceeded_total",
	Help: "Parent chains of probes cut at the depth limit of 8 levels (PRB-05, WP-965).",
})

var dependencyCacheStale = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: "reticora", Name: "probe_dependency_cache_stale_total",
	Help: "Cached parent chains or anchors of probes found changed and read again (WP-965 AK4).",
})

// DependencyCacheStale returns the counter of cache entries found changed
// (tests).
func DependencyCacheStale() prometheus.Counter { return dependencyCacheStale }

// DependencyDepthExceeded returns the counter of cut chains (tests).
func DependencyDepthExceeded() prometheus.Counter { return dependencyDepthExceeded }

// DepNode is a probe as the dependency sees it: its parent and its group.
type DepNode struct {
	Parent string
	Group  string
}

// Chain returns the ancestors of id (parent first) as far as lookup knows
// them, at most MaxDependencyDepth; exceeded reports a cut chain. A cycle
// (refused on write, WP-939) ends the chain.
func Chain(id string, lookup func(id string) (DepNode, bool)) (ancestors []string, exceeded bool) {
	seen := map[string]bool{id: true}
	cur := id
	for {
		n, ok := lookup(cur)
		if !ok || n.Parent == "" || seen[n.Parent] {
			return ancestors, false
		}
		if len(ancestors) == MaxDependencyDepth {
			return ancestors, true
		}
		ancestors = append(ancestors, n.Parent)
		seen[n.Parent] = true
		cur = n.Parent
	}
}

// Blocking reports whether a state blocks the probes behind it: down or
// unknown.
func Blocking(state string) bool {
	return state == string(probewire.StateDown) || state == string(probewire.StateUnknown)
}

// Blocker returns the probe that suppresses a down of id: the nearest
// ancestor in chain whose state blocks, else the anchor when it applies
// (id is neither the anchor nor one of its ancestors in anchorChain) and
// blocks; "" when none.
func Blocker(id string, chain []string, anchor string, anchorChain []string, states map[string]string) string {
	for _, a := range chain {
		if Blocking(states[a]) {
			return a
		}
	}
	if anchor == "" || anchor == id || slices.Contains(anchorChain, id) {
		return ""
	}
	if Blocking(states[anchor]) {
		return anchor
	}
	return ""
}

type depEntry struct {
	node DepNode
	at   time.Time
}

type anchorEntry struct {
	anchor string
	at     time.Time
}

// DependencyCache holds the parents of probes and the anchors of groups.
type DependencyCache struct {
	mu      sync.Mutex
	now     func() time.Time
	nodes   map[string]depEntry
	anchors map[string]anchorEntry
}

// NewDependencyCache returns an empty cache on the clock now (nil:
// time.Now).
func NewDependencyCache(now func() time.Time) *DependencyCache {
	if now == nil {
		now = time.Now
	}
	return &DependencyCache{now: now, nodes: map[string]depEntry{}, anchors: map[string]anchorEntry{}}
}

// Node returns the cached node of a probe that has not expired.
func (c *DependencyCache) Node(id string) (DepNode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.nodes[id]
	if !ok || c.now().Sub(e.at) > DependencyCacheTTL {
		return DepNode{}, false
	}
	return e.node, true
}

// SetNode stores the node of a probe; a node that differs from the cached
// one (a changed parent_probe_id or group) replaces it.
func (c *DependencyCache) SetNode(id string, n DepNode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes[id] = depEntry{node: n, at: c.now()}
}

// Anchor returns the cached anchor of a group that has not expired.
func (c *DependencyCache) Anchor(group string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.anchors[group]
	if !ok || c.now().Sub(e.at) > DependencyCacheTTL {
		return "", false
	}
	return e.anchor, true
}

// SetAnchor stores the anchor of a group ("" for none).
func (c *DependencyCache) SetAnchor(group, anchor string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.anchors[group] = anchorEntry{anchor: anchor, at: c.now()}
}

// Invalidate drops the entries of the probes and groups (a change of
// parent_probe_id or anchor_probe_id); without arguments it drops all.
func (c *DependencyCache) Invalidate(probes, groups []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(probes) == 0 && len(groups) == 0 {
		c.nodes, c.anchors = map[string]depEntry{}, map[string]anchorEntry{}
		return
	}
	for _, p := range probes {
		delete(c.nodes, p)
	}
	for _, g := range groups {
		delete(c.anchors, g)
	}
}

// dependencies resolves the dependency of probes in a transaction, through
// cache (nil: always from the database).
type dependencies struct {
	tx    pgx.Tx
	org   string
	cache *DependencyCache
}

// load reads the nodes of the probes and their ancestors up to the depth
// limit (one recursive query) into the cache.
func (d *dependencies) load(ctx context.Context, ids []string) (map[string]DepNode, error) {
	rows, err := d.tx.Query(ctx, `WITH RECURSIVE up (id, parent, grp, depth) AS (
			SELECT p.id, p.parent_probe_id, p.collector_group_id, 0 FROM probe p
			 WHERE p.organization_id = $1 AND p.id = ANY ($2::uuid[])
			UNION
			SELECT p.id, p.parent_probe_id, p.collector_group_id, up.depth + 1 FROM probe p
			  JOIN up ON p.id = up.parent
			 WHERE p.organization_id = $1 AND up.depth < $3
		)
		SELECT DISTINCT id::text, COALESCE(parent::text, ''), grp::text FROM up`, d.org, ids, MaxDependencyDepth+1)
	if err != nil {
		return nil, fmt.Errorf("read probe dependencies: %w", err)
	}
	defer rows.Close()
	out := map[string]DepNode{}
	for rows.Next() {
		var id string
		var n DepNode
		if err := rows.Scan(&id, &n.Parent, &n.Group); err != nil {
			return nil, fmt.Errorf("read probe dependencies: %w", err)
		}
		out[id] = n
		if d.cache != nil {
			d.cache.SetNode(id, n)
		}
	}
	return out, rows.Err()
}

// chain returns the ancestors of id; fresh is the probe's own node as just
// read with its row (nil: from the cache or the database).
func (d *dependencies) chain(ctx context.Context, id string, fresh *DepNode) ([]string, error) {
	if fresh != nil && d.cache != nil {
		d.cache.SetNode(id, *fresh)
	}
	var loaded map[string]DepNode
	lookup := func(x string) (DepNode, bool) {
		if fresh != nil && x == id {
			return *fresh, true
		}
		if n, ok := loaded[x]; ok {
			return n, true
		}
		if d.cache != nil {
			return d.cache.Node(x)
		}
		return DepNode{}, false
	}
	// Walk; on the first miss load the rest of the chain once.
	walk := func() (ancestors []string, exceeded bool, last string, complete bool) {
		ancestors, exceeded = Chain(id, lookup)
		last = id
		if len(ancestors) > 0 {
			last = ancestors[len(ancestors)-1]
		}
		_, known := lookup(last)
		return ancestors, exceeded, last, exceeded || known
	}
	ancestors, exceeded, last, complete := walk()
	if !complete {
		var err error
		if loaded, err = d.load(ctx, []string{last}); err != nil {
			return nil, err
		}
		ancestors, exceeded, _, _ = walk()
	}
	if exceeded {
		dependencyDepthExceeded.Inc()
	}
	return ancestors, nil
}

// anchor returns the anchor probe of a group ("" for none).
func (d *dependencies) anchor(ctx context.Context, group string) (string, error) {
	if group == "" {
		return "", nil
	}
	if d.cache != nil {
		if a, ok := d.cache.Anchor(group); ok {
			return a, nil
		}
	}
	var a string
	err := d.tx.QueryRow(ctx, `SELECT COALESCE(anchor_probe_id::text, '') FROM collector_group WHERE organization_id = $1 AND id = $2`,
		d.org, group).Scan(&a)
	if err != nil && err != pgx.ErrNoRows {
		return "", fmt.Errorf("read anchor probe: %w", err)
	}
	if d.cache != nil {
		d.cache.SetAnchor(group, a)
	}
	return a, nil
}

// current reads, in one query, the state and the current parent of the
// probes and the current anchor of group: the states decide, the parents
// and the anchor check the cached chain (a change of parent_probe_id or
// anchor_probe_id by the API of another process is seen at once, not only
// after DependencyCacheTTL). present holds the probes that exist.
func (d *dependencies) current(ctx context.Context, ids []string, group string) (states, parents map[string]string, anchor string, err error) {
	states, parents = map[string]string{}, map[string]string{}
	rows, err := d.tx.Query(ctx, `SELECT 'p', p.id::text, COALESCE(p.parent_probe_id::text, ''), COALESCE(s.state, '')
		  FROM probe p LEFT JOIN probe_state s ON s.organization_id = p.organization_id AND s.probe_id = p.id
		 WHERE p.organization_id = $1 AND p.id = ANY ($2::uuid[])
		UNION ALL
		SELECT 'g', '', COALESCE(g.anchor_probe_id::text, ''), '' FROM collector_group g WHERE g.organization_id = $1 AND g.id = $3::uuid`,
		d.org, ids, nullUUID(group))
	if err != nil {
		return nil, nil, "", fmt.Errorf("read dependency states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id, parent, state string
		if err := rows.Scan(&kind, &id, &parent, &state); err != nil {
			return nil, nil, "", fmt.Errorf("read dependency states: %w", err)
		}
		if kind == "g" {
			anchor = parent
			continue
		}
		parents[id] = parent
		if state != "" {
			states[id] = state
		}
	}
	return states, parents, anchor, rows.Err()
}

// chainMatches reports whether a cached chain (each probe followed by its
// parent) still matches the parents just read: every probe's parent is the
// next one, the last has none unless the chain was cut at the depth limit.
func chainMatches(chain []string, cut bool, parents map[string]string) bool {
	for i, x := range chain {
		p, ok := parents[x]
		switch {
		case !ok:
			return false
		case i+1 < len(chain):
			if p != chain[i+1] {
				return false
			}
		case !cut && p != "":
			return false
		}
	}
	return true
}

// blocker returns the probe that suppresses a down of the probe (Blocker);
// node is the probe's own node as just read with its row. The cached chain
// and anchor are checked against the rows read with the states; on a
// mismatch the entries are dropped and the chain is read again once.
func (d *dependencies) blocker(ctx context.Context, id string, node DepNode) (string, error) {
	for attempt := 0; ; attempt++ {
		chain, err := d.chain(ctx, id, &node)
		if err != nil {
			return "", err
		}
		anchor, err := d.anchor(ctx, node.Group)
		if err != nil {
			return "", err
		}
		var anchorChain []string
		if anchor != "" && anchor != id {
			if anchorChain, err = d.chain(ctx, anchor, nil); err != nil {
				return "", err
			}
		}
		ids := slices.Clone(chain)
		if anchor != "" {
			ids = append(ids, anchor)
			ids = append(ids, anchorChain...)
		}
		states, parents, current, err := d.current(ctx, ids, node.Group)
		if err != nil {
			return "", err
		}
		valid := current == anchor && chainMatches(chain, len(chain) == MaxDependencyDepth, parents)
		if anchor != "" && anchor != id {
			valid = valid && chainMatches(append([]string{anchor}, anchorChain...), len(anchorChain) == MaxDependencyDepth, parents)
		}
		if d.cache != nil && attempt == 0 && !valid {
			d.cache.Invalidate(ids, []string{node.Group})
			dependencyCacheStale.Inc()
			continue
		}
		return Blocker(id, chain, anchor, anchorChain, states), nil
	}
}

// suppressedChild is a probe behind a blocking parent or anchor whose down
// became unknown/parent_down.
type suppressedChild struct {
	id, clientID, siteID string
	collector            *string
	epoch, seq           int64
	depth                int
}

// suppressDownChildren rewrites the down probes behind blocker (its
// descendants up to the depth limit and, for every collector group whose
// anchor it is, the group's probes but the anchor's ancestors; the anchor of
// a group may be a probe of another group, WP-947) to unknown/parent_down in
// the transaction, with a history row of the server and the event
// probe.state_changed (notify false, suppressed_by). It returns the number
// of probes rewritten.
//
// The state rows behind the blocker are locked first (FOR UPDATE, in the
// order of probe_id) whatever their state, and rewritten only then: a child
// whose own down is still uncommitted (its transaction holds its row from
// the start of apply) is waited for and seen down afterwards; a child whose
// transaction starts later waits for this one at its own row and then reads
// the blocker down (WP-965 AK1/AK2 across the lanes and instances). Two
// blockers of one chain that fail at once can still lock each other's rows
// (each holds its own first); PostgreSQL breaks that deadlock and apply
// repeats the change at once (applyDeadlockRetries).
func (d *dependencies) suppressDownChildren(ctx context.Context, blocker string, at time.Time) (int, error) {
	// The ancestors of the blocker are exempt from it as an anchor; read
	// fresh (a blocker going down is rare, the chain must be current).
	fresh := &dependencies{tx: d.tx, org: d.org}
	anchorChain, err := fresh.chain(ctx, blocker, nil)
	if err != nil {
		return 0, err
	}
	if anchorChain == nil {
		anchorChain = []string{}
	}
	rows, err := d.tx.Query(ctx, `WITH RECURSIVE down (id, depth) AS (
			SELECT p.id, 1 FROM probe p WHERE p.organization_id = $1 AND p.parent_probe_id = $2
			UNION
			SELECT p.id, down.depth + 1 FROM probe p JOIN down ON p.parent_probe_id = down.id
			 WHERE p.organization_id = $1 AND down.depth < $3
		)
		SELECT id::text, min(depth) FROM (
			SELECT id, depth FROM down
			UNION ALL
			SELECT p.id, 1 FROM probe p
			  JOIN collector_group g ON g.organization_id = p.organization_id AND g.id = p.collector_group_id
			 WHERE g.organization_id = $1 AND g.anchor_probe_id = $2::uuid AND p.id <> $2::uuid
			   AND NOT (p.id::text = ANY ($4::text[]))
		) x WHERE id <> $2::uuid GROUP BY id`, d.org, blocker, MaxDependencyDepth, anchorChain)
	if err != nil {
		return 0, fmt.Errorf("read probes behind %s: %w", blocker, err)
	}
	depth := map[string]int{}
	var behind []string
	for rows.Next() {
		var id string
		var n int
		if serr := rows.Scan(&id, &n); serr != nil {
			rows.Close()
			return 0, fmt.Errorf("read probes behind %s: %w", blocker, serr)
		}
		depth[id] = n
		behind = append(behind, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, fmt.Errorf("read probes behind %s: %w", blocker, err)
	}
	if len(behind) == 0 {
		return 0, nil
	}
	if _, err = d.tx.Exec(ctx, `SELECT 1 FROM probe_state WHERE organization_id = $1 AND probe_id = ANY ($2::uuid[])
		ORDER BY probe_id FOR UPDATE`, d.org, behind); err != nil {
		return 0, fmt.Errorf("lock probes behind %s: %w", blocker, err)
	}
	rows, err = d.tx.Query(ctx, `UPDATE probe_state s SET state = 'unknown', reason = $3, since = GREATEST(s.since, $4)
		 WHERE s.organization_id = $1 AND s.probe_id = ANY ($2::uuid[]) AND s.state = 'down'
		RETURNING s.probe_id::text, s.client_id::text, s.site_id::text, s.collector_id::text, s.epoch, s.last_seq`,
		d.org, behind, probewire.ReasonParentDown, at.UTC())
	if err != nil {
		return 0, fmt.Errorf("suppress probes behind %s: %w", blocker, err)
	}
	var children []suppressedChild
	for rows.Next() {
		var c suppressedChild
		var client, site *string
		if serr := rows.Scan(&c.id, &client, &site, &c.collector, &c.epoch, &c.seq); serr != nil {
			rows.Close()
			return 0, fmt.Errorf("suppress probes behind %s: %w", blocker, serr)
		}
		c.clientID, c.siteID, c.depth = deref(client), deref(site), depth[c.id]
		children = append(children, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, fmt.Errorf("suppress probes behind %s: %w", blocker, err)
	}
	if len(children) == 0 {
		return 0, nil
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].depth != children[j].depth {
			return children[i].depth < children[j].depth
		}
		return children[i].id < children[j].id
	})
	ids := make([]string, len(children))
	for i := range children {
		ids[i] = children[i].id
	}
	cis := map[string]string{}
	crows, err := d.tx.Query(ctx, `SELECT id::text, COALESCE(ci_id::text, '') FROM probe WHERE organization_id = $1 AND id = ANY ($2::uuid[])`,
		d.org, ids)
	if err != nil {
		return 0, fmt.Errorf("read suppressed probes: %w", err)
	}
	for crows.Next() {
		var id, ci string
		if err := crows.Scan(&id, &ci); err != nil {
			crows.Close()
			return 0, fmt.Errorf("read suppressed probes: %w", err)
		}
		cis[id] = ci
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return 0, fmt.Errorf("read suppressed probes: %w", err)
	}
	// The history of the server: no collector, the child's epoch and
	// sequence (the key of a collector's change holds its collector).
	if _, err := d.tx.Exec(ctx, `INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, reason,
			observed_at, received_at, epoch, seq, collector_id, late, notify, stale)
		SELECT $1, u.probe_id, 'down', 'unknown', $3, $4, now(), u.epoch, u.seq, NULL, false, false, false
		  FROM unnest($2::uuid[], $5::bigint[], $6::bigint[]) AS u (probe_id, epoch, seq)`,
		d.org, ids, probewire.ReasonParentDown, at.UTC(), epochsOf(children), seqsOf(children)); err != nil {
		return 0, fmt.Errorf("write suppressed history: %w", err)
	}
	txCtx := database.ContextWithTx(ctx, d.tx)
	return len(children), database.BeforeCommit(txCtx, func() error {
		outbox := events.NewOutbox()
		for i := range children {
			c := &children[i]
			data := stateChangedData{
				ProbeStateChangedData: events.ProbeStateChangedData{ID: c.id, CIID: cis[c.id], ClientID: c.clientID, SiteID: c.siteID,
					From: string(probewire.StateDown), To: string(probewire.StateUnknown), Reason: probewire.ReasonParentDown},
				ProbeID: c.id, CollectorID: deref(c.collector), ObservedAt: at.UTC(), Notify: false, SuppressedBy: blocker,
			}
			if _, err := outbox.Append(ctx, d.tx, &events.Event{OrgID: d.org, Type: events.ProbeStateChanged, EntityID: c.id, Data: data}); err != nil {
				return err
			}
		}
		return nil
	})
}

func epochsOf(cs []suppressedChild) []int64 {
	out := make([]int64, len(cs))
	for i := range cs {
		out[i] = cs[i].epoch
	}
	return out
}

func seqsOf(cs []suppressedChild) []int64 {
	out := make([]int64, len(cs))
	for i := range cs {
		out[i] = cs[i].seq
	}
	return out
}

func nullUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resyncCandidate is a probe in unknown/parent_down with an ok result.
type resyncCandidate struct {
	id, parent, group, ciID, clientID, siteID string
	collector                                 *string
	since                                     time.Time
	epoch, seq                                int64
}

// resyncParentDown ends unknown/parent_down for the probes with an ok result
// at okAt (the youngest non-stale ok result of the batch) once nothing blocks
// them any more: up with reason resync from the result's time, a history row
// of the server and the event probe.state_changed (notify false; WP-965 AK3).
// A result not younger than the suppression changes nothing.
func resyncParentDown(ctx context.Context, tx pgx.Tx, orgID string, okAt map[string]time.Time) error {
	if len(okAt) == 0 {
		return nil
	}
	ids := make([]string, 0, len(okAt))
	for id := range okAt {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows, err := tx.Query(ctx, `SELECT s.probe_id::text, COALESCE(p.parent_probe_id::text, ''), p.collector_group_id::text,
			COALESCE(p.ci_id::text, ''), COALESCE(p.client_id::text, ''), COALESCE(p.site_id::text, ''), s.collector_id::text,
			s.since, s.epoch, s.last_seq
		  FROM probe_state s JOIN probe p ON p.organization_id = s.organization_id AND p.id = s.probe_id
		 WHERE s.organization_id = $1 AND s.probe_id = ANY ($2::uuid[]) AND s.state = 'unknown' AND s.reason = $3
		 ORDER BY s.probe_id`, orgID, ids, probewire.ReasonParentDown)
	if err != nil {
		return fmt.Errorf("read suppressed probes: %w", err)
	}
	cands, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (resyncCandidate, error) {
		var c resyncCandidate
		scanErr := row.Scan(&c.id, &c.parent, &c.group, &c.ciID, &c.clientID, &c.siteID, &c.collector, &c.since, &c.epoch, &c.seq)
		return c, scanErr
	})
	if err != nil {
		return fmt.Errorf("read suppressed probes: %w", err)
	}
	deps := &dependencies{tx: tx, org: orgID}
	var emit []stateChangedData
	for i := range cands {
		c := &cands[i]
		at := okAt[c.id].UTC()
		if !at.After(c.since) {
			continue
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('probe_state:' || $1, 0))`, c.id); err != nil {
			return fmt.Errorf("lock probe state: %w", err)
		}
		blocker, berr := deps.blocker(ctx, c.id, DepNode{Parent: c.parent, Group: c.group})
		if berr != nil {
			return berr
		}
		if blocker != "" {
			continue
		}
		tag, uerr := tx.Exec(ctx, `UPDATE probe_state SET state = 'up', reason = $3, since = $4
			WHERE organization_id = $1 AND probe_id = $2 AND state = 'unknown' AND reason = $5 AND since < $4`,
			orgID, c.id, ReasonResync, at, probewire.ReasonParentDown)
		if uerr != nil {
			return fmt.Errorf("resync probe state: %w", uerr)
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, reason,
				observed_at, received_at, epoch, seq, collector_id, late, notify, stale)
			VALUES ($1, $2, 'unknown', 'up', $3, $4, now(), $5, $6, NULL, false, false, false)`,
			orgID, c.id, ReasonResync, at, c.epoch, c.seq); err != nil {
			return fmt.Errorf("write resync history: %w", err)
		}
		emit = append(emit, stateChangedData{
			ProbeStateChangedData: events.ProbeStateChangedData{ID: c.id, CIID: c.ciID, ClientID: c.clientID, SiteID: c.siteID,
				From: string(probewire.StateUnknown), To: string(probewire.StateUp), Reason: ReasonResync},
			ProbeID: c.id, CollectorID: deref(c.collector), ObservedAt: at,
		})
	}
	if len(emit) == 0 {
		return nil
	}
	return database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
		outbox := events.NewOutbox()
		for i := range emit {
			if _, aerr := outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.ProbeStateChanged, EntityID: emit[i].ID,
				Data: emit[i]}); aerr != nil {
				return aerr
			}
		}
		return nil
	})
}
