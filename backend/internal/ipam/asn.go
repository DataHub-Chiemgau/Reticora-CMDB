package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// AS numbers and AS number ranges of the organization (IPM-16, WP-1066,
// E-217 option 1): tables asn and asn_range (migration 000316), not CI
// types. A row without client belongs to the whole organization: every
// principal reads it, only principals without client and site restriction
// write it; a principal restricted to clients reads and writes the rows of
// its clients. WP-1067 adds the allocation of the lowest free number of a
// range, the usage of a range and the origin AS of a subnet.

// Bounds of an AS number (RFC 6793): 32 bits without 0 and 4294967295;
// 23456 (AS_TRANS, RFC 6793) and 65535 (RFC 7300) are reserved.
const (
	minASN   = 1
	maxASN   = 4294967294
	asTrans  = 23456
	asLast16 = 65535
)

// ASN kinds and registries.
var (
	asnKinds = []string{"own", "customer", "peer", "upstream", "ixp"}
	asnRIRs  = []string{"ripe", "arin", "apnic", "lacnic", "afrinic", "private"}
)

// ErrInvalidASN reports an AS number outside 1 to 4294967294, a reserved
// one (23456, 65535) or a text that is no AS number.
var ErrInvalidASN = errors.New("not a valid AS number")

// ParseASN reads an AS number in asplain (65001, 4200000000) or asdot
// notation (1.10 = 65546, both parts 0 to 65535), optionally with the
// prefix AS or as (RFC 5396), and returns it in asplain. 0, 23456, 65535,
// 4294967295 and values beyond 32 bits are ErrInvalidASN.
func ParseASN(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && strings.EqualFold(s[:2], "as") {
		s = strings.TrimSpace(s[2:])
	}
	if s == "" {
		return 0, ErrInvalidASN
	}
	var n uint64
	if hi, lo, dot := strings.Cut(s, "."); dot {
		h, herr := strconv.ParseUint(hi, 10, 16)
		l, lerr := strconv.ParseUint(lo, 10, 16)
		if herr != nil || lerr != nil || strings.HasPrefix(hi, "+") || strings.HasPrefix(lo, "+") {
			return 0, ErrInvalidASN
		}
		n = h<<16 | l
	} else {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil || strings.HasPrefix(s, "+") {
			return 0, ErrInvalidASN
		}
		n = v
	}
	if n < minASN || n > maxASN || n == asTrans || n == asLast16 {
		return 0, ErrInvalidASN
	}
	return int64(n), nil
}

// ASDot returns the asdot notation of an AS number (RFC 5396): asplain
// below 65536, else high.low.
func ASDot(n int64) string {
	if n < 1<<16 {
		return strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n>>16, 10) + "." + strconv.FormatInt(n&0xffff, 10)
}

// ASNumber is an AS number of a request: a JSON number or a string in
// asplain or asdot notation, with or without the prefix AS.
type ASNumber string

// UnmarshalJSON takes a JSON number or a JSON string.
func (a *ASNumber) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = ASNumber(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("an AS number is a number or a string")
	}
	// A JSON number is asplain; a fraction or an exponent is no AS
	// number (and no asdot either).
	if strings.ContainsAny(n.String(), ".eE") {
		*a = "invalid"
		return nil
	}
	*a = ASNumber(n.String())
	return nil
}

// ASN is an AS number of the organization.
type ASN struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       *string   `json:"client_id"`
	Number         int64     `json:"number"`
	ASDot          string    `json:"asdot"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind"`
	RIR            *string   `json:"rir"`
	Description    string    `json:"description,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ASNRange is a block of AS numbers (Start..End, both inclusive).
type ASNRange struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       *string   `json:"client_id"`
	Name           string    `json:"name"`
	Start          int64     `json:"start"`
	End            int64     `json:"end"`
	StartASDot     string    `json:"start_asdot"`
	EndASDot       string    `json:"end_asdot"`
	Purpose        string    `json:"purpose,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateASNRequest is the payload of POST /api/v1/asns.
type CreateASNRequest struct {
	Number      ASNumber `json:"number"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	ClientID    string   `json:"client_id,omitempty"`
	RIR         string   `json:"rir,omitempty"`
	Description string   `json:"description,omitempty"`
}

// UpdateASNRequest is the payload of PATCH /api/v1/asns/{id}; an empty
// client_id makes the AS number org-wide, an empty rir clears it.
type UpdateASNRequest struct {
	Number      *ASNumber `json:"number,omitempty"`
	Name        *string   `json:"name,omitempty"`
	Kind        *string   `json:"kind,omitempty"`
	ClientID    *string   `json:"client_id,omitempty"`
	RIR         *string   `json:"rir,omitempty"`
	Description *string   `json:"description,omitempty"`

	number int64
}

// CreateASNRangeRequest is the payload of POST /api/v1/asn-ranges.
type CreateASNRangeRequest struct {
	Name     string   `json:"name"`
	Start    ASNumber `json:"start"`
	End      ASNumber `json:"end"`
	ClientID string   `json:"client_id,omitempty"`
	Purpose  string   `json:"purpose,omitempty"`
}

// UpdateASNRangeRequest is the payload of PATCH /api/v1/asn-ranges/{id}.
type UpdateASNRangeRequest struct {
	Name     *string   `json:"name,omitempty"`
	Start    *ASNumber `json:"start,omitempty"`
	End      *ASNumber `json:"end,omitempty"`
	ClientID *string   `json:"client_id,omitempty"`
	Purpose  *string   `json:"purpose,omitempty"`

	start, end *int64
}

// ASNFilter selects AS numbers: kind, client, the numbers inside a range
// and a text (name, asplain or asdot number).
type ASNFilter struct {
	Kind     string
	ClientID string
	RangeID  string
	Q        string
}

// ASNRepository persists AS numbers and ranges. Only the PostgreSQL
// repository implements it (optional interface, pattern DuplicateFinder);
// without a database the routes answer 501.
type ASNRepository interface {
	ListASNs(ctx context.Context, orgID string, f *ASNFilter, page api.PaginationParams) ([]ASN, int, error)
	GetASN(ctx context.Context, orgID, id string) (*ASN, error)
	CreateASN(ctx context.Context, a *ASN) error
	UpdateASN(ctx context.Context, orgID, id string, req *UpdateASNRequest) (*ASN, error)
	DeleteASN(ctx context.Context, orgID, id string) error
	ListASNRanges(ctx context.Context, orgID string, page api.PaginationParams) ([]ASNRange, int, error)
	GetASNRange(ctx context.Context, orgID, id string) (*ASNRange, error)
	CreateASNRange(ctx context.Context, rg *ASNRange) error
	UpdateASNRange(ctx context.Context, orgID, id string, req *UpdateASNRangeRequest) (*ASNRange, error)
	DeleteASNRange(ctx context.Context, orgID, id string) error
	// AllocateASN stores a as the lowest free AS number of the range
	// (WP-1067): a.Number is set by the store.
	AllocateASN(ctx context.Context, orgID, rangeID string, a *ASN) error
	// ASNRangeUsage counts the AS numbers in a visible range.
	ASNRangeUsage(ctx context.Context, orgID, rangeID string) (*ASNRangeUsage, error)
	// ListASNPrefixes lists the visible subnets with the AS number as
	// their origin AS.
	ListASNPrefixes(ctx context.Context, orgID, asnID string, page api.PaginationParams) ([]ASNPrefix, int, error)
}

// AllocateASNRequest is the payload of POST /api/v1/asn-ranges/{id}/allocate:
// the AS number takes the lowest free number of the range.
type AllocateASNRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	ClientID    string `json:"client_id,omitempty"`
	RIR         string `json:"rir,omitempty"`
	Description string `json:"description,omitempty"`
}

// ASNRangeUsage is the occupancy of a range (WP-1067): total usable
// numbers (the reserved 23456 and 65535 do not count), used AS numbers of
// the organization in it (also those created by hand), free and percent
// used. Scoped tells that the caller is restricted to clients or sites and
// used counts only the AS numbers it sees.
type ASNRangeUsage struct {
	RangeID string  `json:"range_id"`
	Total   int64   `json:"total"`
	Used    int64   `json:"used"`
	Free    int64   `json:"free"`
	Percent float64 `json:"percent"`
	Scoped  bool    `json:"scoped"`
}

// ASNPrefix is a subnet with the AS number as origin AS.
type ASNPrefix struct {
	ID       string  `json:"id"`
	CIDR     string  `json:"cidr"`
	VRFID    string  `json:"vrf_id"`
	ClientID *string `json:"client_id"`
	Name     string  `json:"name,omitempty"`
}

// SubnetOriginASN is the origin AS of a subnet as the subnet shows it.
type SubnetOriginASN struct {
	ID     string `json:"id"`
	Number int64  `json:"number"`
	ASDot  string `json:"asdot"`
	Name   string `json:"name"`
}

var _ ASNRepository = (*PGRepository)(nil)

// Fixed answers of the AS number rules.
var (
	errASNNumber = &api.FieldError{Field: "number",
		Message: "number must be an AS number from 1 to 4294967294 (asplain, asdot or ASnnn) other than 23456 and 65535",
		Status:  http.StatusUnprocessableEntity}
	errASNRangeStart = &api.FieldError{Field: "start",
		Message: "start must be an AS number from 1 to 4294967294 other than 23456 and 65535", Status: http.StatusUnprocessableEntity}
	errASNRangeEnd = &api.FieldError{Field: "end",
		Message: "end must be an AS number from 1 to 4294967294 other than 23456 and 65535, not below start",
		Status:  http.StatusUnprocessableEntity}
	errASNClient = &api.FieldError{Field: "client_id", Message: "client not found", Status: http.StatusUnprocessableEntity}
	errASNKind   = &api.FieldError{Field: "kind", Message: "kind must be one of own, customer, peer, upstream, ixp",
		Status: http.StatusUnprocessableEntity}
	errASNRIR = &api.FieldError{Field: "rir", Message: "rir must be one of ripe, arin, apnic, lacnic, afrinic, private",
		Status: http.StatusUnprocessableEntity}
	errASNNumberTaken = &api.ConflictError{Field: "number", Message: "the AS number already exists in the organization"}
	errASNRangeFull   = &api.ConflictError{Field: "range", Message: "the AS number range has no free AS number"}
	errASNRangeClient = &api.FieldError{Field: "client_id", Message: "the AS number range belongs to another client",
		Status: http.StatusUnprocessableEntity}
	errOriginASN       = &api.FieldError{Field: "origin_asn_id", Message: "AS number not found", Status: http.StatusUnprocessableEntity}
	errASNOriginClient = &api.FieldError{Field: "client_id", Message: "the AS number is the origin AS of a subnet of another client",
		Status: http.StatusUnprocessableEntity}
	errOriginASNClient = &api.FieldError{Field: "origin_asn_id", Message: "the AS number belongs to another client",
		Status: http.StatusUnprocessableEntity}
	errASNRangeName    = &api.ConflictError{Field: "name", Message: "an AS number range with this name already exists"}
	errASNRangeOverlap = &api.ConflictError{Field: "start",
		Message: "the range overlaps another AS number range of the organization"}
)

// asnError maps the constraint violations of asn and asn_range onto the
// fixed answers.
func asnError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "asn_number_key":
		return errASNNumberTaken
	case "asn_number_check":
		return errASNNumber
	case "asn_range_name_key":
		return errASNRangeName
	case "asn_range_no_overlap":
		return errASNRangeOverlap
	case "asn_range_bounds_check":
		return errASNRangeEnd
	case "asn_client_fkey", "asn_range_client_fkey":
		return errASNClient
	}
	return err
}

// checkASNClient accepts no client (org-wide) and a client the principal
// sees; everything else is 422 client_id. The read runs in the writing
// transaction under the principal's row level security.
func checkASNClient(ctx context.Context, tx pgx.Tx, orgID string, clientID *string) error {
	if clientID == nil || *clientID == "" {
		return nil
	}
	if !api.IsUUID(*clientID) {
		return errASNClient
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM client WHERE organization_id = $1 AND id = $2::uuid)`,
		orgID, *clientID).Scan(&ok); err != nil {
		return fmt.Errorf("read client: %w", err)
	}
	if !ok {
		return errASNClient
	}
	return nil
}

// emptyToNil returns nil for an empty client id.
func emptyToNil(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

// --- PostgreSQL ---

const asnCols = `id::text, organization_id::text, client_id::text, number, name, kind, rir, COALESCE(description, ''),
	created_at, updated_at`

const asnRangeCols = `id::text, organization_id::text, client_id::text, name, range_start, range_end, COALESCE(purpose, ''),
	created_at, updated_at`

func scanASN(s scanner) (*ASN, error) {
	a := &ASN{}
	if err := s.Scan(&a.ID, &a.OrganizationID, &a.ClientID, &a.Number, &a.Name, &a.Kind, &a.RIR, &a.Description,
		&a.CreatedAt, &a.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.ASDot = ASDot(a.Number)
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return a, nil
}

func scanASNRange(s scanner) (*ASNRange, error) {
	rg := &ASNRange{}
	if err := s.Scan(&rg.ID, &rg.OrganizationID, &rg.ClientID, &rg.Name, &rg.Start, &rg.End, &rg.Purpose,
		&rg.CreatedAt, &rg.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rg.StartASDot, rg.EndASDot = ASDot(rg.Start), ASDot(rg.End)
	rg.CreatedAt, rg.UpdatedAt = rg.CreatedAt.UTC(), rg.UpdatedAt.UTC()
	return rg, nil
}

// asnChangedData is the data of asn.changed: the frame of WP-870
// (events.ASNChangedData) with number and the kind of change (the schema
// allows further properties).
type asnChangedData struct {
	events.ASNChangedData
	Number int64  `json:"number"`
	Change string `json:"change"`
}

// emitASNChanged writes asn.changed for a as the last write of the
// transaction (Konvention 20); nothing without an outbox.
func (r *PGRepository) emitASNChanged(ctx context.Context, tx pgx.Tx, a *ASN, change string) error {
	if r.outbox == nil {
		return nil
	}
	data := asnChangedData{ASNChangedData: events.ASNChangedData{ID: a.ID, ASN: a.Number}, Number: a.Number, Change: change}
	if a.ClientID != nil {
		data.ClientID = *a.ClientID
	}
	orgID := a.OrganizationID
	return database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
		_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.ASNChanged, EntityID: a.ID, Data: data})
		return err
	})
}

// ListASNs lists the visible AS numbers by number.
func (r *PGRepository) ListASNs(ctx context.Context, orgID string, f *ASNFilter, page api.PaginationParams) ([]ASN, int, error) {
	out := []ASN{}
	where := []string{"a.organization_id = $1"}
	args := []any{orgID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Kind != "" {
		add("a.kind = $%d", f.Kind)
	}
	if f.ClientID != "" {
		if !api.IsUUID(f.ClientID) {
			return out, 0, nil
		}
		add("a.client_id = $%d::uuid", f.ClientID)
	}
	if f.RangeID != "" {
		if !api.IsUUID(f.RangeID) {
			return out, 0, nil
		}
		add("EXISTS (SELECT 1 FROM asn_range g WHERE g.organization_id = a.organization_id AND g.id = $%d::uuid "+
			"AND a.number BETWEEN g.range_start AND g.range_end)", f.RangeID)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		if n, err := ParseASN(q); err == nil {
			args = append(args, n, "%"+escapeLike(q)+"%")
			where = append(where, fmt.Sprintf("(a.number = $%d OR a.name ILIKE $%d)", len(args)-1, len(args)))
		} else {
			add("a.name ILIKE $%d", "%"+escapeLike(q)+"%")
		}
	}
	clause := strings.Join(where, " AND ")
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM asn a WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+asnCols+" FROM asn a WHERE "+clause+
			fmt.Sprintf(" ORDER BY a.number, a.id LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2),
			append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanASN(rows)
			if err != nil {
				return err
			}
			out = append(out, *a)
		}
		return rows.Err()
	})
	return out, total, err
}

// escapeLike escapes the wildcards of a LIKE pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// GetASN returns a visible AS number.
func (r *PGRepository) GetASN(ctx context.Context, orgID, id string) (*ASN, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var a *ASN
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = scanASN(tx.QueryRow(ctx, "SELECT "+asnCols+" FROM asn WHERE organization_id = $1 AND id = $2", orgID, id))
		return err
	})
	return a, err
}

// CreateASN stores an AS number and writes asn.changed (create). An
// org-wide AS number of a principal restricted in the client or site scope
// fails with the row level security error 42501 (403 at the API).
func (r *PGRepository) CreateASN(ctx context.Context, a *ASN) error {
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkASNClient(ctx, tx, a.OrganizationID, a.ClientID); err != nil {
			return err
		}
		return r.insertASN(ctx, tx, a)
	})
}

// insertASN writes a in tx and asn.changed (create) last.
func (r *PGRepository) insertASN(ctx context.Context, tx pgx.Tx, a *ASN) error {
	created, err := scanASN(tx.QueryRow(ctx, `INSERT INTO asn (organization_id, client_id, number, name, kind, rir, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+asnCols,
		a.OrganizationID, emptyToNil(a.ClientID), a.Number, a.Name, a.Kind, a.RIR, nilIfEmpty(a.Description)))
	if err != nil {
		return asnError(err)
	}
	*a = *created
	return r.emitASNChanged(ctx, tx, a, "create")
}

// asnAllocationLockSQL serializes the allocations from one range for the
// transaction (pattern allocationLockSQL of WP-904): a row lock (SELECT ...
// FOR UPDATE) would need the UPDATE policy of asn_range, which a principal
// restricted to clients does not hold for an org-wide range it may
// allocate from.
const asnAllocationLockSQL = "SELECT pg_advisory_xact_lock(hashtextextended('asn_range_alloc:' || $1::text, 0))"

// AllocateASN stores a as the lowest free AS number of the range in one
// transaction (IPM-16, WP-1067): the range must be visible (else
// ErrNotFound) and is locked; a client of the AS number other than the
// client of the range is 422 client_id (an org-wide range allocates to
// every client; an AS number without client from a client's range takes
// that client); the number is the lowest one in [start, end] that no AS
// number of the organization holds and that is not reserved
// (asn_next_free, set-based over asn_number_key); a full range is 409
// range. asn.changed (create) is the last write.
func (r *PGRepository) AllocateASN(ctx context.Context, orgID, rangeID string, a *ASN) error {
	if !api.IsUUID(rangeID) {
		return ErrNotFound
	}
	a.OrganizationID = orgID
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, asnAllocationLockSQL, rangeID); err != nil {
			return fmt.Errorf("lock AS number range: %w", err)
		}
		rg, err := scanASNRange(tx.QueryRow(ctx, "SELECT "+asnRangeCols+" FROM asn_range WHERE organization_id = $1 AND id = $2",
			orgID, rangeID))
		if err != nil {
			return err
		}
		switch {
		case rg.ClientID == nil:
		case a.ClientID == nil || *a.ClientID == "":
			a.ClientID = rg.ClientID
		case *a.ClientID != *rg.ClientID:
			return errASNRangeClient
		}
		if cerr := checkASNClient(ctx, tx, orgID, a.ClientID); cerr != nil {
			return cerr
		}
		var next *int64
		if err = tx.QueryRow(ctx, `SELECT asn_next_free($1, $2, $3)`, orgID, rg.Start, rg.End).Scan(&next); err != nil {
			return fmt.Errorf("find free AS number: %w", err)
		}
		if next == nil {
			return errASNRangeFull
		}
		a.Number = *next
		return r.insertASN(ctx, tx, a)
	})
}

// reservedInRange counts the reserved AS numbers 23456 and 65535 in
// [start, end].
func reservedInRange(start, end int64) int64 {
	var n int64
	for _, reserved := range []int64{asTrans, asLast16} {
		if reserved >= start && reserved <= end {
			n++
		}
	}
	return n
}

// ASNRangeUsage counts the AS numbers of a visible range under the
// caller's row level security; scoped reports a principal restricted to
// clients, whose count covers only the rows it sees. The read policy of
// asn (000316) narrows by client only: a principal restricted to sites
// sees every AS number, its count is complete and not scoped.
func (r *PGRepository) ASNRangeUsage(ctx context.Context, orgID, rangeID string) (*ASNRangeUsage, error) {
	if !api.IsUUID(rangeID) {
		return nil, ErrNotFound
	}
	var out *ASNRangeUsage
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rg, err := scanASNRange(tx.QueryRow(ctx, "SELECT "+asnRangeCols+" FROM asn_range WHERE organization_id = $1 AND id = $2",
			orgID, rangeID))
		if err != nil {
			return err
		}
		u := &ASNRangeUsage{RangeID: rg.ID, Total: rg.End - rg.Start + 1 - reservedInRange(rg.Start, rg.End)}
		if qerr := tx.QueryRow(ctx, `SELECT count(*),
				NULLIF(current_setting('app.client_scope', true), '') IS NOT NULL
			FROM asn WHERE organization_id = $1 AND number BETWEEN $2 AND $3`, orgID, rg.Start, rg.End).
			Scan(&u.Used, &u.Scoped); qerr != nil {
			return qerr
		}
		u.Free = max(u.Total-u.Used, 0)
		if u.Total > 0 {
			u.Percent = math.Round(float64(u.Used)*10000/float64(u.Total)) / 100
		}
		out = u
		return nil
	})
	return out, err
}

// ListASNPrefixes lists the visible subnets whose origin AS is a visible
// AS number (by idx_subnet_origin_asn), ordered by CIDR.
func (r *PGRepository) ListASNPrefixes(ctx context.Context, orgID, asnID string, page api.PaginationParams) ([]ASNPrefix, int, error) {
	if !api.IsUUID(asnID) {
		return nil, 0, ErrNotFound
	}
	out := []ASNPrefix{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM asn WHERE organization_id = $1 AND id = $2)`, orgID, asnID).
			Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM subnet WHERE organization_id = $1 AND origin_asn_id = $2`, orgID, asnID).
			Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, cidr::text, vrf_id::text, client_id::text, COALESCE(name, '') FROM subnet
			WHERE organization_id = $1 AND origin_asn_id = $2 ORDER BY cidr, id LIMIT $3 OFFSET $4`, orgID, asnID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p ASNPrefix
			if err := rows.Scan(&p.ID, &p.CIDR, &p.VRFID, &p.ClientID, &p.Name); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, total, err
}

// checkOriginASN accepts no origin AS and an AS number the principal sees
// that is org-wide or of the subnet's client (clientID; empty: the subnet
// is org-wide, then only an org-wide AS number); everything else is 422
// origin_asn_id. The read runs in the writing transaction under the
// principal's row level security, before origin_asn_id is written (the
// composite foreign key checks only the organization).
func checkOriginASN(ctx context.Context, tx pgx.Tx, orgID, asnID, clientID string) error {
	if strings.TrimSpace(asnID) == "" {
		return nil
	}
	if !api.IsUUID(asnID) {
		return errOriginASN
	}
	// Shared with other subnet writes, exclusive against a client change of
	// the AS number (UpdateASN): the client read here stays until commit.
	if _, err := tx.Exec(ctx, asnOriginLockSharedSQL, asnID); err != nil {
		return fmt.Errorf("lock asn: %w", err)
	}
	var visible, sameClient bool
	if err := tx.QueryRow(ctx, `SELECT count(*) > 0, COALESCE(bool_and(client_id IS NULL OR client_id = $3::uuid), false)
		FROM asn WHERE organization_id = $1 AND id = $2::uuid`, orgID, asnID, nilIfEmpty(clientID)).Scan(&visible, &sameClient); err != nil {
		return fmt.Errorf("read asn: %w", err)
	}
	switch {
	case !visible:
		return errOriginASN
	case !sameClient:
		return errOriginASNClient
	}
	return nil
}

// asnOriginLockSQL and asnOriginLockSharedSQL serialize a client change of
// an AS number against the subnets that name it as origin AS for the
// transaction (pattern asnAllocationLockSQL): a row lock (FOR SHARE) would
// need the UPDATE policy of asn, which a principal restricted to clients
// does not hold for an org-wide AS number it may use as origin AS.
const (
	asnOriginLockSQL       = "SELECT pg_advisory_xact_lock(hashtextextended('asn_origin:' || $1::text, 0))"
	asnOriginLockSharedSQL = "SELECT pg_advisory_xact_lock_shared(hashtextextended('asn_origin:' || $1::text, 0))"
)

// checkASNOriginSubnets refuses moving the visible AS number id to client
// clientID (not empty) while a subnet of another client or an org-wide
// subnet names it as origin AS: 422 client_id. The subnets are read with
// the client, site and team scope widened (app_scope_widen, pattern
// heldAddresses), since the caller's scope hides the subnets of other
// clients; only a boolean leaves the widened statement. An AS number the
// caller does not see is ErrNotFound before anything else is read.
func checkASNOriginSubnets(ctx context.Context, tx pgx.Tx, orgID, id, clientID string) error {
	if _, err := tx.Exec(ctx, asnOriginLockSQL, id); err != nil {
		return fmt.Errorf("lock asn: %w", err)
	}
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM asn WHERE organization_id = $1 AND id = $2)`, orgID, id).
		Scan(&visible); err != nil {
		return fmt.Errorf("read asn: %w", err)
	}
	if !visible {
		return ErrNotFound
	}
	var previous string
	if err := tx.QueryRow(ctx, `SELECT app_scope_widen()::text`).Scan(&previous); err != nil {
		return fmt.Errorf("check origin subnets: widen scope: %w", err)
	}
	var conflict bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subnet WHERE organization_id = $1 AND origin_asn_id = $2
		AND client_id IS DISTINCT FROM $3::uuid)`, orgID, id, clientID).Scan(&conflict)
	if _, restoreErr := tx.Exec(ctx, `SELECT app_scope_restore($1::text[])`, previous); restoreErr != nil && err == nil {
		err = restoreErr
	}
	if err != nil {
		return fmt.Errorf("check origin subnets: %w", err)
	}
	if conflict {
		return errASNOriginClient
	}
	return nil
}

// checkSubnetOriginASN applies checkOriginASN to an update of subnet id: to
// a new origin AS and to the origin AS of a subnet that moves to another
// client, against the client the subnet has after the update.
func checkSubnetOriginASN(ctx context.Context, tx pgx.Tx, orgID, id string, req *UpdateSubnetRequest) error {
	if req.ClientID == nil && (req.OriginASNID == nil || strings.TrimSpace(*req.OriginASNID) == "") {
		return nil
	}
	var clientID, asnID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), COALESCE(origin_asn_id::text, '') FROM subnet
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&clientID, &asnID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read subnet: %w", err)
	}
	if req.ClientID != nil {
		clientID = *req.ClientID
	}
	if req.OriginASNID != nil {
		asnID = *req.OriginASNID
	}
	return checkOriginASN(ctx, tx, orgID, asnID, clientID)
}

// UpdateASN changes a visible AS number and writes asn.changed (update).
// A principal restricted in the client or site scope updates no org-wide
// row: ErrNotFound. A move to a client while a subnet of another client
// (or an org-wide one) has the AS number as origin AS is 422 client_id.
func (r *PGRepository) UpdateASN(ctx context.Context, orgID, id string, req *UpdateASNRequest) (*ASN, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var number any
	if req.Number != nil {
		number = req.number
	}
	var a *ASN
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkASNClient(ctx, tx, orgID, req.ClientID); err != nil {
			return err
		}
		if req.ClientID != nil && *req.ClientID != "" {
			if err := checkASNOriginSubnets(ctx, tx, orgID, id, *req.ClientID); err != nil {
				return err
			}
		}
		var err error
		a, err = scanASN(tx.QueryRow(ctx, `UPDATE asn SET
				number = COALESCE($3::bigint, number),
				name = COALESCE($4, name),
				kind = COALESCE($5, kind),
				client_id = CASE WHEN $6::text IS NULL THEN client_id ELSE NULLIF($6, '')::uuid END,
				rir = CASE WHEN $7::text IS NULL THEN rir ELSE NULLIF($7, '') END,
				description = CASE WHEN $8::text IS NULL THEN description ELSE NULLIF($8, '') END
			WHERE organization_id = $1 AND id = $2 RETURNING `+asnCols,
			orgID, id, number, req.Name, req.Kind, req.ClientID, req.RIR, req.Description))
		if err != nil {
			return asnError(err)
		}
		return r.emitASNChanged(ctx, tx, a, "update")
	})
	return a, err
}

// DeleteASN removes a visible AS number and writes asn.changed (delete).
func (r *PGRepository) DeleteASN(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		a, err := scanASN(tx.QueryRow(ctx, "DELETE FROM asn WHERE organization_id = $1 AND id = $2 RETURNING "+asnCols, orgID, id))
		if err != nil {
			return asnError(err)
		}
		return r.emitASNChanged(ctx, tx, a, "delete")
	})
}

// ListASNRanges lists the visible AS number ranges by start.
func (r *PGRepository) ListASNRanges(ctx context.Context, orgID string, page api.PaginationParams) ([]ASNRange, int, error) {
	out := []ASNRange{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM asn_range WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+asnRangeCols+` FROM asn_range WHERE organization_id = $1
			ORDER BY range_start, id LIMIT $2 OFFSET $3`, orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rg, err := scanASNRange(rows)
			if err != nil {
				return err
			}
			out = append(out, *rg)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetASNRange returns a visible AS number range.
func (r *PGRepository) GetASNRange(ctx context.Context, orgID, id string) (*ASNRange, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var rg *ASNRange
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rg, err = scanASNRange(tx.QueryRow(ctx, "SELECT "+asnRangeCols+" FROM asn_range WHERE organization_id = $1 AND id = $2", orgID, id))
		return err
	})
	return rg, err
}

// CreateASNRange stores a range; an overlap with another range of the
// organization is 409 start (also with a range outside the caller's
// scope).
func (r *PGRepository) CreateASNRange(ctx context.Context, rg *ASNRange) error {
	return database.WithRequestTenant(ctx, r.pool, rg.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkASNClient(ctx, tx, rg.OrganizationID, rg.ClientID); err != nil {
			return err
		}
		created, err := scanASNRange(tx.QueryRow(ctx, `INSERT INTO asn_range (organization_id, client_id, name, range_start, range_end, purpose)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+asnRangeCols,
			rg.OrganizationID, emptyToNil(rg.ClientID), rg.Name, rg.Start, rg.End, nilIfEmpty(rg.Purpose)))
		if err != nil {
			return asnError(err)
		}
		*rg = *created
		return nil
	})
}

// UpdateASNRange changes a visible range; start above end is 422 end.
func (r *PGRepository) UpdateASNRange(ctx context.Context, orgID, id string, req *UpdateASNRangeRequest) (*ASNRange, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var rg *ASNRange
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkASNClient(ctx, tx, orgID, req.ClientID); err != nil {
			return err
		}
		var err error
		rg, err = scanASNRange(tx.QueryRow(ctx, `UPDATE asn_range SET
				name = COALESCE($3, name),
				range_start = COALESCE($4::bigint, range_start),
				range_end = COALESCE($5::bigint, range_end),
				client_id = CASE WHEN $6::text IS NULL THEN client_id ELSE NULLIF($6, '')::uuid END,
				purpose = CASE WHEN $7::text IS NULL THEN purpose ELSE NULLIF($7, '') END
			WHERE organization_id = $1 AND id = $2 RETURNING `+asnRangeCols,
			orgID, id, req.Name, req.start, req.end, req.ClientID, req.Purpose))
		return asnError(err)
	})
	return rg, err
}

// DeleteASNRange removes a visible range.
func (r *PGRepository) DeleteASNRange(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return r.deleteByID(ctx, orgID, "asn_range", "true", id)
}
