package rls_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
)

// TestTeamRoomAndPersonScope covers migration 000065 (WP-029, MGT-05..07,
// TKT-01, TEN-05, CH25, E-11): tickets carry a team predicate that
// intersects with client and site, training assignments follow the team
// membership of the assigned user, and desks and bookings inherit client and
// site from their room.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTeamRoomAndPersonScope(t *testing.T) {
	f := scopetest.Seed(t, "4a")
	bg := context.Background()
	u1 := f.AppUser(t, f.OrgA, "team-u1")
	u2 := f.AppUser(t, f.OrgA, "team-u2")
	ci1 := f.CI(t, f.OrgA, f.Client1, "team-ci1")
	room1 := f.Room(t, f.Client1, "team-room-c1")
	room2 := f.Room(t, f.Client2, "team-room-c2")

	ids := map[string]string{}
	seed := func(key, sql string, args ...any) {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, sql+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
		ids[key] = id
	}
	seed("team1", `INSERT INTO team (organization_id, name) VALUES ($1, 'team 1')`, f.OrgA)
	seed("team2", `INSERT INTO team (organization_id, name) VALUES ($1, 'team 2')`, f.OrgA)
	seed("member1", `INSERT INTO team_member (organization_id, team_id, user_id) VALUES ($1, $2, $3)`, f.OrgA, ids["team1"], u1)
	seed("member2", `INSERT INTO team_member (organization_id, team_id, user_id) VALUES ($1, $2, $3)`, f.OrgA, ids["team2"], u2)
	seed("ticket1", `INSERT INTO ticket (organization_id, title, reporter_id, team_id) VALUES ($1, 't1', $2, $3)`, f.OrgA, u1, ids["team1"])
	seed("ticket2", `INSERT INTO ticket (organization_id, title, reporter_id, team_id) VALUES ($1, 't2', $2, $3)`, f.OrgA, u1, ids["team2"])
	seed("ticket0", `INSERT INTO ticket (organization_id, title, reporter_id) VALUES ($1, 't0', $2)`, f.OrgA, u1)
	seed("ticketC1", `INSERT INTO ticket (organization_id, title, reporter_id, team_id, related_ci_id) VALUES ($1, 'tc1', $2, $3, $4)`, f.OrgA, u1, ids["team1"], ci1)
	seed("training", `INSERT INTO training (organization_id, title) VALUES ($1, 'safety')`, f.OrgA)
	seed("training2", `INSERT INTO training (organization_id, title) VALUES ($1, 'first aid')`, f.OrgA)
	seed("assign1", `INSERT INTO training_assignment (organization_id, training_id, user_id) VALUES ($1, $2, $3)`, f.OrgA, ids["training"], u1)
	seed("assign2", `INSERT INTO training_assignment (organization_id, training_id, user_id) VALUES ($1, $2, $3)`, f.OrgA, ids["training"], u2)
	// The writer claims client 2 for a desk in a room of client 1.
	seed("desk1", `INSERT INTO desk (organization_id, room_id, name, client_id) VALUES ($1, $2, 'd1', $3)`, f.OrgA, room1, f.Client2)
	seed("desk2", `INSERT INTO desk (organization_id, room_id, name) VALUES ($1, $2, 'd2')`, f.OrgA, room2)
	seed("booking1", `INSERT INTO desk_booking (organization_id, desk_id, user_id, starts_at, ends_at) VALUES ($1, $2, $3, now(), now() + interval '1 hour')`, f.OrgA, ids["desk1"], u1)
	seed("booking2", `INSERT INTO desk_booking (organization_id, desk_id, user_id, starts_at, ends_at) VALUES ($1, $2, $3, now(), now() + interval '1 hour')`, f.OrgA, ids["desk2"], u2)

	var site1 string
	if err := f.Admin.QueryRow(bg, `SELECT site_id::text FROM location WHERE id = $1`, room1).Scan(&site1); err != nil {
		t.Fatalf("site of room 1: %v", err)
	}
	clientOf := func(table, key string) (client, site string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM `+table+` WHERE id = $1`,
			ids[key]).Scan(&client, &site); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		return client, site
	}
	if c, s := clientOf("desk", "desk1"); c != f.Client1 || s != site1 {
		t.Errorf("desk1 derived client %s site %s, want client 1 and the room's site", c, s)
	}
	if c, _ := clientOf("desk_booking", "booking1"); c != f.Client1 {
		t.Errorf("booking1 derived client %s, want client 1", c)
	}

	scopes := map[string]database.TenantScope{}
	team1 := database.OrgWideScope(f.OrgA, u1)
	team1.Teams = database.ScopeIDs(ids["team1"])
	scopes["team 1"] = team1
	both := team1
	both.Clients = database.ScopeIDs(f.Client2)
	scopes["team 1 and client 2"] = both
	client1 := database.OrgWideScope(f.OrgA, u1)
	client1.Clients = database.ScopeIDs(f.Client1)
	scopes["client 1"] = client1
	site := database.OrgWideScope(f.OrgA, u1)
	site.Sites = database.ScopeIDs(site1)
	scopes["site of room 1"] = site

	run := func(scope string, fn func(ctx context.Context, tx pgx.Tx) error) error {
		s := scopes[scope]
		return database.WithTenant(bg, f.App, &s, fn)
	}
	for _, c := range []struct {
		scope, table, key string
		visible           bool
	}{
		{"team 1", "ticket", "ticket1", true},
		{"team 1", "ticket", "ticket2", false},
		{"team 1", "ticket", "ticket0", true},
		{"team 1", "ticket", "ticketC1", true},
		// Intersection (E-11): team in scope, client not.
		{"team 1 and client 2", "ticket", "ticketC1", false},
		{"team 1 and client 2", "ticket", "ticket1", true},
		{"team 1", "training_assignment", "assign1", true},
		{"team 1", "training_assignment", "assign2", false},
		{"client 1", "training_assignment", "assign2", true},
		{"client 1", "desk", "desk1", true},
		{"client 1", "desk", "desk2", false},
		{"client 1", "desk_booking", "booking1", true},
		{"client 1", "desk_booking", "booking2", false},
		{"site of room 1", "desk", "desk1", true},
		{"site of room 1", "desk", "desk2", false},
	} {
		var n int
		if err := run(c.scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+c.table+` WHERE id = $1`, ids[c.key]).Scan(&n)
		}); err != nil {
			t.Fatalf("%s: read %s: %v", c.scope, c.table, err)
		}
		if (n == 1) != c.visible {
			t.Errorf("%s: %s %s visible=%v, want %v", c.scope, c.table, c.key, n == 1, c.visible)
		}
	}

	for _, c := range []struct {
		scope, name, sql string
		args             []any
		ok               bool
	}{
		{"team 1", "ticket of team 1", `INSERT INTO ticket (organization_id, title, reporter_id, team_id) VALUES ($1, 'x', $2, $3)`, []any{f.OrgA, u1, ids["team1"]}, true},
		{"team 1", "ticket of team 2", `INSERT INTO ticket (organization_id, title, reporter_id, team_id) VALUES ($1, 'x', $2, $3)`, []any{f.OrgA, u1, ids["team2"]}, false},
		{"team 1", "ticket without team", `INSERT INTO ticket (organization_id, title, reporter_id) VALUES ($1, 'x', $2)`, []any{f.OrgA, u1}, false},
		{"team 1", "hand ticket to team 2", `UPDATE ticket SET team_id = $2 WHERE id = $1`, []any{ids["ticket1"], ids["team2"]}, false},
		{"team 1", "training for a member", `INSERT INTO training_assignment (organization_id, training_id, user_id) VALUES ($1, $2, $3)`, []any{f.OrgA, ids["training2"], u1}, true},
		{"team 1", "training for a non-member", `INSERT INTO training_assignment (organization_id, training_id, user_id) VALUES ($1, $2, $3)`, []any{f.OrgA, ids["training2"], u2}, false},
		{"client 1", "desk in a room of client 2", `INSERT INTO desk (organization_id, room_id, name) VALUES ($1, $2, 'x')`, []any{f.OrgA, room2}, false},
		{"client 1", "desk without room", `INSERT INTO desk (organization_id, name) VALUES ($1, 'x')`, []any{f.OrgA}, false},
		{"client 1", "booking of a desk of client 2", `INSERT INTO desk_booking (organization_id, desk_id, user_id, starts_at, ends_at) VALUES ($1, $2, $3, now() + interval '1 day', now() + interval '25 hours')`, []any{f.OrgA, ids["desk2"], u1}, false},
	} {
		err := run(c.scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, c.sql, c.args...)
			if err == nil && tag.RowsAffected() == 0 {
				return errors.New("no row written")
			}
			return err
		})
		if (err == nil) != c.ok {
			t.Errorf("%s: %s: err=%v, want ok=%v", c.scope, c.name, err, c.ok)
		}
	}

	// The site of room 1 moves to client 2: desk and booking follow.
	if _, err := f.Admin.Exec(bg, `UPDATE site SET client_id = $2 WHERE id = $1`, site1, f.Client2); err != nil {
		t.Fatalf("move site: %v", err)
	}
	if c, _ := clientOf("desk", "desk1"); c != f.Client2 {
		t.Errorf("desk1 after site move: client %s, want client 2", c)
	}
	if c, _ := clientOf("desk_booking", "booking1"); c != f.Client2 {
		t.Errorf("booking1 after site move: client %s, want client 2", c)
	}
}
