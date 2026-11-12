package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// its clients. The allocation from a range and the origin AS of a subnet
// follow in WP-1067.

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
	errASNNumberTaken  = &api.ConflictError{Field: "number", Message: "the AS number already exists in the organization"}
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
		created, err := scanASN(tx.QueryRow(ctx, `INSERT INTO asn (organization_id, client_id, number, name, kind, rir, description)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+asnCols,
			a.OrganizationID, emptyToNil(a.ClientID), a.Number, a.Name, a.Kind, a.RIR, nilIfEmpty(a.Description)))
		if err != nil {
			return asnError(err)
		}
		*a = *created
		return r.emitASNChanged(ctx, tx, a, "create")
	})
}

// UpdateASN changes a visible AS number and writes asn.changed (update).
// A principal restricted in the client or site scope updates no org-wide
// row: ErrNotFound.
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
