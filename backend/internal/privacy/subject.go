package privacy

// Rights of a single person (SEC-05 [P3], WP-1158): the report of
// everything the organization keeps about a person (Art. 15 DSGVO) and its
// erasure by crypto-shredding or pseudonymization (Art. 17), over every
// surrogate reference of the history.
//
// A person is a user (user_id), a contact (contact_id) or a vault subject
// of kind person or contact (subject_id, e.g. the holder of an ADMP
// connection). Its match values are e-mail, name and phone of app_user or of
// its decrypted vault subject, lower case and trimmed. Personal values of
// the history are value subjects without a blind index: the jobs decrypt
// them in batches of 1,000 and compare them with the match values; a value
// anchored to the person's subject (parent_id) belongs to it anyway.
//
// The report job names every place of a found value or of the user: every
// jsonb column of the tenant tables with a surrogate reference
// ({"$pii": "<id>"}) or the id of the user, the user columns of the audit,
// the CI history, stock movements, notification deliveries, tickets and
// assignments, every column with a foreign key on pii_subject (read from
// pg_constraint, so later tables need no code change) and the deliveries to
// the person's address. Its result holds the person's values unsealed
// (E-165) and is seen with user:manage only (jobs API, WithReadKind).
//
// The erasure job (mode shred) deletes the found value subjects in one
// transaction per batch and then the person's subject (its anchored
// subjects by cascade), anonymizes a user (AnonymizeUserTx, sessions
// revoked), empties the addresses of the deliveries to the person and the
// results of earlier reports about it: pii.RevealTx resolves every reference
// to null afterwards. Mode pseudonymize replaces the data of the found
// subjects with person-<8 hex digits of SHA-256 of the surrogate> and keeps
// the surrogates, so the history stays connected. Both jobs write one audit
// entry with the id or surrogate, the mode and the counts, never a value.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// Job kinds of the rights of a person.
const (
	SubjectReportJobKind = "privacy.subject_report"
	SubjectEraseJobKind  = "privacy.subject_erase"
)

// Modes of the erasure of a person.
const (
	ModeShred        = "shred"
	ModePseudonymize = "pseudonymize"
)

// Kinds of a person.
const (
	PersonUser    = "user"
	PersonContact = "contact"
	PersonSubject = "subject"
)

// subjectBatch is the number of value subjects decrypted, deleted or
// pseudonymized per transaction.
const subjectBatch = 1000

// SubjectRequest names the person of a report or an erasure: exactly one of
// user_id, contact_id and subject_id; Mode is the mode of an erasure.
type SubjectRequest struct {
	UserID    string `json:"user_id,omitempty"`
	ContactID string `json:"contact_id,omitempty"`
	SubjectID string `json:"subject_id,omitempty"`
	Mode      string `json:"mode,omitempty"`
	// Confirmed are value subjects the report listed as candidates (a
	// match on the name alone) that the erasure includes; any other
	// candidate stays, since the name may be another person's.
	Confirmed []string `json:"confirmed_surrogates,omitempty"`
}

// maxConfirmed bounds the confirmed candidates of one erasure.
const maxConfirmed = 10000

// SubjectPerson is the person of a report with its unsealed data.
type SubjectPerson struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// SubjectID is the vault subject of the person; empty for a user and a
	// contact without one.
	SubjectID string   `json:"subject_id,omitempty"`
	Data      pii.Data `json:"data"`
}

// SubjectFinding is one place that refers to the person: the table, the id
// of the row (its ctid for a table without id), the field (a column, or the
// column and the path within its JSON), the surrogate of a value subject
// and its unsealed value.
type SubjectFinding struct {
	Table     string `json:"table"`
	RowID     string `json:"row_id"`
	Field     string `json:"field"`
	Surrogate string `json:"surrogate,omitempty"`
	Value     string `json:"value,omitempty"`
	// Candidate marks a value subject found by the name alone: it may be
	// another person's, an erasure leaves it unless confirmed.
	Candidate bool `json:"candidate,omitempty"`
}

// SubjectReport is the result of the job privacy.subject_report.
type SubjectReport struct {
	Person SubjectPerson `json:"person"`
	// ValueSubjects counts the value subjects of the history that hold a
	// value of the person.
	ValueSubjects int `json:"value_subjects"`
	// Candidates counts those of them found by the name alone.
	Candidates int              `json:"candidates"`
	Findings   []SubjectFinding `json:"findings"`
	// Counts is the number of findings per table.
	Counts     map[string]int `json:"counts"`
	ReportedAt time.Time      `json:"reported_at"`
}

// SubjectErasure is the result of the job privacy.subject_erase: counts
// only, never a value.
type SubjectErasure struct {
	Mode       string `json:"mode"`
	PersonKind string `json:"person_kind"`
	PersonID   string `json:"person_id"`
	SubjectID  string `json:"subject_id,omitempty"`
	// ValueSubjects counts the value subjects deleted or pseudonymized,
	// Subjects the subjects of the person itself (with its anchored ones).
	ValueSubjects int64 `json:"value_subjects"`
	// CandidatesKept counts the value subjects found by the name alone
	// and not confirmed: they stay.
	CandidatesKept int64 `json:"candidates_kept"`
	Subjects       int64 `json:"subjects"`
	Deliveries     int64 `json:"deliveries"`
	Reports        int64 `json:"reports"`
	UserAnonymized bool  `json:"user_anonymized"`
}

// SubjectJobs runs a job in this process; jobs.Service implements it.
type SubjectJobs interface {
	RunDetached(ctx context.Context, orgID, kind string, params any, createdBy string,
		run func(ctx context.Context) (any, error)) (*jobs.Job, error)
}

// SubjectQueue submits a job to the job queue on NATS; jobs.Queue
// implements it.
type SubjectQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// UserAnonymizer anonymizes the account of a user; user.PGRepository
// implements it.
type UserAnonymizer interface {
	AnonymizeUserTx(ctx context.Context, orgID, id string, onDeactivate user.DeactivationHook) (*user.User, error)
}

// SessionRevoker ends the sessions of a user (user.SessionRevoker).
type SessionRevoker interface {
	RevokeUser(ctx context.Context, userID string, at time.Time) error
}

// SubjectService serves the report and the erasure of a person.
type SubjectService struct {
	pool     *pgxpool.Pool
	users    UserAnonymizer
	sessions SessionRevoker
	jobs     SubjectJobs
	queue    SubjectQueue
	now      func() time.Time
}

// NewSubjectService creates the service on the application pool; users
// anonymizes the account of an erased user. Without a pool the routes
// answer 503.
func NewSubjectService(pool *pgxpool.Pool, users UserAnonymizer) *SubjectService {
	return &SubjectService{pool: pool, users: users, now: func() time.Time { return time.Now().UTC() }}
}

// WithSessionRevoker makes the erasure of a user end its sessions.
func (s *SubjectService) WithSessionRevoker(sessions SessionRevoker) *SubjectService {
	s.sessions = sessions
	return s
}

// WithJobs runs the jobs in this process.
func (s *SubjectService) WithJobs(svc SubjectJobs) *SubjectService {
	s.jobs = svc
	return s
}

// WithQueue runs the jobs on the job queue.
func (s *SubjectService) WithQueue(q SubjectQueue) *SubjectService {
	s.queue = q
	return s
}

// Errors of the rights of a person. Their texts reach the client or the job;
// causes go to the log only (API-03).
var (
	errPersonNotFound = errors.New("the person was not found")
	errPersonErased   = errors.New("the person was erased while the report ran")
	errSubjectFailed  = errors.New("the job of the person could not run")
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validate checks the request: exactly one person as a UUID and, for an
// erasure, a known mode (422 with the field).
func (req *SubjectRequest) validate(erase bool) error {
	set := 0
	for _, f := range []struct{ name, value string }{{"user_id", req.UserID}, {"contact_id", req.ContactID}, {"subject_id", req.SubjectID}} {
		if f.value == "" {
			continue
		}
		set++
		if !uuidRE.MatchString(f.value) {
			return &api.FieldError{Field: f.name, Message: f.name + " must be a UUID", Status: http.StatusUnprocessableEntity}
		}
	}
	if set != 1 {
		return &api.FieldError{Field: "user_id", Message: "name exactly one of user_id, contact_id and subject_id",
			Status: http.StatusUnprocessableEntity}
	}
	if erase && req.Mode != ModeShred && req.Mode != ModePseudonymize {
		return &api.FieldError{Field: "mode", Message: "mode must be shred or pseudonymize", Status: http.StatusUnprocessableEntity}
	}
	if !erase && req.Mode != "" {
		return &api.FieldError{Field: "mode", Message: "the report has no mode", Status: http.StatusUnprocessableEntity}
	}
	if !erase && len(req.Confirmed) > 0 {
		return &api.FieldError{Field: "confirmed_surrogates", Message: "the report confirms no candidates",
			Status: http.StatusUnprocessableEntity}
	}
	if len(req.Confirmed) > maxConfirmed {
		return &api.FieldError{Field: "confirmed_surrogates", Message: "at most 10000 confirmed surrogates",
			Status: http.StatusUnprocessableEntity}
	}
	for _, id := range req.Confirmed {
		if !uuidRE.MatchString(id) {
			return &api.FieldError{Field: "confirmed_surrogates", Message: "confirmed_surrogates must be UUIDs",
				Status: http.StatusUnprocessableEntity}
		}
	}
	return nil
}

// subjectJobResponse is the answer of the two routes.
type subjectJobResponse struct {
	JobID  string      `json:"job_id"`
	Status jobs.Status `json:"status"`
	Kind   string      `json:"kind"`
}

// Report handles POST /api/v1/privacy/subjects/report: the job
// privacy.subject_report (202).
func (s *SubjectService) Report(w http.ResponseWriter, r *http.Request) {
	s.submit(w, r, SubjectReportJobKind)
}

// Erase handles POST /api/v1/privacy/subjects/erase: the job
// privacy.subject_erase (202).
func (s *SubjectService) Erase(w http.ResponseWriter, r *http.Request) {
	s.submit(w, r, SubjectEraseJobKind)
}

func (s *SubjectService) submit(w http.ResponseWriter, r *http.Request, kind string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req SubjectRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "the request body is no valid subject request")
		return
	}
	erase := kind == SubjectEraseJobKind
	if err := req.validate(erase); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if erase && req.UserID != "" && strings.EqualFold(req.UserID, t.UserID) {
		api.WriteFieldError(w, &api.FieldError{Field: "user_id", Message: "you cannot erase your own account",
			Status: http.StatusUnprocessableEntity})
		return
	}
	if s == nil || s.pool == nil || (s.jobs == nil && s.queue == nil) {
		api.WriteUnavailable(w, "the rights of a person need PostgreSQL and the job service")
		return
	}
	// A person of another organization or outside the scope is not found
	// (404) before a job exists.
	err := database.WithRequestTenant(r.Context(), s.pool, t.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, perr := s.personTx(ctx, tx, t.OrganizationID, &req, false)
		return perr
	})
	if errors.Is(err, errPersonNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", errPersonNotFound.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "privacy subject: find the person", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	params := SubjectRequest{UserID: strings.ToLower(req.UserID), ContactID: strings.ToLower(req.ContactID),
		SubjectID: strings.ToLower(req.SubjectID), Mode: req.Mode}
	for _, id := range req.Confirmed {
		params.Confirmed = append(params.Confirmed, strings.ToLower(id))
	}
	var job *jobs.Job
	switch {
	case s.queue != nil && erase:
		job, _, err = s.queue.Submit(r.Context(), t.OrganizationID, SubjectEraseJobKind, params, jobs.EnqueueOptions{CreatedBy: t.UserID})
	case s.queue != nil:
		job, _, err = s.queue.Submit(r.Context(), t.OrganizationID, SubjectReportJobKind, params, jobs.EnqueueOptions{CreatedBy: t.UserID})
	default:
		orgID, createdBy := t.OrganizationID, t.UserID
		job, err = s.jobs.RunDetached(r.Context(), orgID, kind, params, createdBy,
			func(ctx context.Context) (any, error) { return s.run(ctx, kind, orgID, createdBy, &params) })
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "privacy subject: submit the job", "kind", kind, "error", err)
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, subjectJobResponse{JobID: job.ID, Status: job.Status, Kind: job.Kind})
}

// RunReportJob runs a job privacy.subject_report of the job queue in the
// tenant and scope of its creator.
func (s *SubjectService) RunReportJob(ctx context.Context, job *jobs.Job) (any, error) {
	return s.runJob(ctx, job, SubjectReportJobKind)
}

// RunEraseJob runs a job privacy.subject_erase of the job queue in the
// tenant and scope of its creator.
func (s *SubjectService) RunEraseJob(ctx context.Context, job *jobs.Job) (any, error) {
	return s.runJob(ctx, job, SubjectEraseJobKind)
}

func (s *SubjectService) runJob(ctx context.Context, job *jobs.Job, kind string) (any, error) {
	var p SubjectRequest
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, errSubjectFailed
	}
	return s.run(jobs.CreatorContext(ctx, job), kind, job.OrganizationID, job.CreatedBy, &p)
}

// run runs a report or an erasure; a failure reaches the job as
// errSubjectFailed, its cause the log.
func (s *SubjectService) run(ctx context.Context, kind, orgID, createdBy string, req *SubjectRequest) (any, error) {
	if s.pool == nil {
		return nil, errSubjectFailed
	}
	var out any
	var err error
	if kind == SubjectReportJobKind {
		out, err = s.RunReport(ctx, orgID, createdBy, req)
	} else {
		out, err = s.RunErase(ctx, orgID, createdBy, req)
	}
	if errors.Is(err, errPersonNotFound) || errors.Is(err, errPersonErased) {
		return nil, err
	}
	if err != nil {
		slog.ErrorContext(ctx, "privacy subject job", "kind", kind, "error", err)
		return nil, errSubjectFailed
	}
	return out, nil
}

// person is a person with its match values.
type person struct {
	SubjectPerson
	// values are the normalized match values, emails those of the e-mail
	// fields; unique are the values of the fields that name one person
	// (all but a name: e-mail, phone), a value only in values is a name.
	values map[string]bool
	unique map[string]bool
	emails []string
}

// nameField reports whether the field of a person holds a name, which two
// persons may share.
func nameField(key string) bool { return strings.Contains(strings.ToLower(key), "name") }

// normalize is the form of a match value: lower case, trimmed.
func normalize(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// personTx reads the person of the request in the organization; forUpdate
// locks the row of a user or contact.
func (s *SubjectService) personTx(ctx context.Context, tx pgx.Tx, orgID string, req *SubjectRequest, forUpdate bool) (*person, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	p := &person{values: map[string]bool{}, unique: map[string]bool{}}
	vault := pii.Default()
	resolve := func(id string) (pii.Data, string, error) {
		if vault == nil {
			var kind string
			err := tx.QueryRow(ctx, `SELECT kind FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2`, orgID, id).Scan(&kind)
			return pii.Data{}, kind, err
		}
		subjects, err := vault.ResolveTx(ctx, tx, orgID, []string{id})
		if err != nil {
			return nil, "", err
		}
		if subjects[id] == nil {
			return nil, "", pgx.ErrNoRows
		}
		return subjects[id].Data, subjects[id].Kind, nil
	}
	var err error
	switch {
	case req.UserID != "":
		var email, name string
		err = tx.QueryRow(ctx, `SELECT email, COALESCE(display_name, '') FROM app_user WHERE organization_id = $1 AND id = $2`+lock,
			orgID, req.UserID).Scan(&email, &name)
		p.Kind, p.ID, p.Data = PersonUser, req.UserID, pii.Data{"email": email, "name": name}
	case req.ContactID != "":
		var name, email, phone, subject string
		err = tx.QueryRow(ctx, `SELECT COALESCE(display_name, ''), COALESCE(email, ''), COALESCE(phone, ''),
				COALESCE(pii_subject_id::text, '') FROM contact WHERE organization_id = $1 AND id = $2`+lock,
			orgID, req.ContactID).Scan(&name, &email, &phone, &subject)
		p.Kind, p.ID, p.SubjectID = PersonContact, req.ContactID, subject
		p.Data = pii.Data{"name": name, "email": email, "phone": phone}
		if err == nil && subject != "" {
			var data pii.Data
			if data, _, err = resolve(subject); errors.Is(err, pgx.ErrNoRows) {
				// The subject is gone (shredded): only the columns are left.
				err = nil
			} else if err == nil && vault != nil {
				p.Data = data
			}
		}
	default:
		var kind string
		p.Kind, p.ID, p.SubjectID = PersonSubject, req.SubjectID, req.SubjectID
		p.Data, kind, err = resolve(req.SubjectID)
		if err == nil && kind != pii.KindPerson && kind != pii.KindContact {
			err = pgx.ErrNoRows
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errPersonNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read the person: %w", err)
	}
	for k, v := range p.Data {
		if v = normalize(v); v == "" {
			continue
		}
		p.values[v] = true
		if !nameField(k) {
			p.unique[v] = true
		}
		if strings.Contains(strings.ToLower(k), "email") {
			p.emails = append(p.emails, v)
		}
	}
	sort.Strings(p.emails)
	return p, nil
}

// tenantTx runs fn in a transaction of the organization.
func (s *SubjectService) tenantTx(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return database.WithRequestTenant(ctx, s.pool, orgID, fn)
}

// matchValues decrypts the value subjects of the organization in batches
// and returns those of the person by surrogate with their values: a value
// equal to a match value, or anchored to the person's subject. candidates
// are those found by a name alone (not anchored, no e-mail or phone): a
// name may be another person's (two contacts "Max Müller", a functional
// name), so the report marks them and the erasure leaves them unless
// confirmed.
func (s *SubjectService) matchValues(ctx context.Context, orgID string, p *person) (matched map[string]string, candidates map[string]bool, err error) {
	matched, candidates = map[string]string{}, map[string]bool{}
	vault := pii.Default()
	if vault == nil {
		return matched, candidates, nil
	}
	after := ""
	for {
		var batch []pii.ValueSubject
		err := s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			batch, err = vault.ValueSubjectsTx(ctx, tx, orgID, after, subjectBatch)
			return err
		})
		if err != nil {
			return nil, nil, err
		}
		for _, v := range batch {
			anchored := p.SubjectID != "" && v.ParentID == p.SubjectID
			value := normalize(v.Value)
			if anchored || p.values[value] {
				matched[v.ID] = v.Value
				if !anchored && !p.unique[value] {
					candidates[v.ID] = true
				}
			}
		}
		if len(batch) < subjectBatch {
			return matched, candidates, nil
		}
		after = batch[len(batch)-1].ID
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
	}
}

// userColumns are the columns that name a user as actor, assignee or
// recipient: the audit, the CI history, stock movements, deliveries,
// tickets and assignments.
var userColumns = []struct{ table, column string }{
	{"audit_log", "actor_id"},
	{"ci_change", "actor_id"},
	{"ci_change", "actor_user_id"},
	{"stock_movement", "actor_id"},
	{"stock_movement", "assignee_user_id"},
	{"stock_movement", "signer_user_id"},
	{"notification_delivery", "recipient_user_id"},
	{"ticket", "reporter_id"},
	{"ticket", "assignee_id"},
	{"assignment_projection", "assigned_to"},
	{"assignment_projection", "assigned_by"},
}

// scanExcluded are tables whose JSON the report does not search: the jobs,
// whose parameters and results are the requests and reports about persons
// themselves (an erasure empties the reports).
var scanExcluded = map[string]bool{"job": true}

// jsonColumn is a jsonb column of a tenant table and whether the table has
// a column id.
type jsonColumn struct {
	table, column string
	hasID         bool
}

// jsonColumns lists the jsonb columns of the tenant tables the role may
// read.
func jsonColumns(ctx context.Context, tx pgx.Tx) ([]jsonColumn, error) {
	rows, err := tx.Query(ctx, `SELECT c.relname::text, a.attname::text,
			EXISTS (SELECT 1 FROM pg_attribute i WHERE i.attrelid = c.oid AND i.attname = 'id' AND NOT i.attisdropped)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped AND a.atttypid = 'jsonb'::regtype
		WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
		  AND EXISTS (SELECT 1 FROM pg_attribute o WHERE o.attrelid = c.oid AND o.attname = 'organization_id' AND NOT o.attisdropped)
		  AND has_table_privilege(c.oid, 'SELECT')
		ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list json columns: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (jsonColumn, error) {
		var c jsonColumn
		scanErr := r.Scan(&c.table, &c.column, &c.hasID)
		return c, scanErr
	})
}

// subjectFKColumns lists the columns with a foreign key on pii_subject
// (besides its own parent_id): contact.pii_subject_id, the holder and
// address of an ADMP connection and every later one.
func subjectFKColumns(ctx context.Context, tx pgx.Tx) ([]jsonColumn, error) {
	rows, err := tx.Query(ctx, `SELECT c.relname::text, a.attname::text,
			EXISTS (SELECT 1 FROM pg_attribute i WHERE i.attrelid = c.oid AND i.attname = 'id' AND NOT i.attisdropped)
		FROM pg_constraint k
		JOIN pg_class c ON c.oid = k.conrelid
		JOIN pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = k.conkey[1]
		WHERE k.contype = 'f' AND k.confrelid = 'pii_subject'::regclass AND k.conrelid <> 'pii_subject'::regclass
		  AND has_table_privilege(c.oid, 'SELECT')
		ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list pii_subject references: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (jsonColumn, error) {
		var c jsonColumn
		scanErr := r.Scan(&c.table, &c.column, &c.hasID)
		return c, scanErr
	})
}

// rowIDExpr is the row id of a finding: the id, else the ctid.
func rowIDExpr(hasID bool) string {
	if hasID {
		return "id::text"
	}
	return "ctid::text"
}

// delivery is a notification delivery to the person.
type delivery struct {
	id, userID, email, name, key string
	recipients                   []byte
}

// references finds every place that refers to the person or one of its
// value subjects.
func (s *SubjectService) references(ctx context.Context, orgID string, p *person, matched map[string]string) ([]SubjectFinding, error) {
	var out []SubjectFinding
	userID := ""
	if p.Kind == PersonUser {
		userID = p.ID
	}
	var columns, fks []jsonColumn
	err := s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if columns, err = jsonColumns(ctx, tx); err != nil {
			return err
		}
		fks, err = subjectFKColumns(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}

	// Surrogate references and the user id in JSON.
	if len(matched) > 0 || userID != "" {
		for _, c := range columns {
			if scanExcluded[c.table] {
				continue
			}
			found, ferr := s.jsonFindings(ctx, orgID, c, userID, matched)
			if ferr != nil {
				return nil, ferr
			}
			out = append(out, found...)
		}
	}

	err = s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The user as actor, assignee or recipient, and the account.
		if userID != "" {
			out = append(out, SubjectFinding{Table: "app_user", RowID: userID, Field: "email"},
				SubjectFinding{Table: "app_user", RowID: userID, Field: "display_name"})
			for _, c := range userColumns {
				ids, qerr := columnRows(ctx, tx, orgID, c.table, c.column, true, []string{userID})
				if qerr != nil {
					return qerr
				}
				for _, id := range ids {
					out = append(out, SubjectFinding{Table: c.table, RowID: id[0], Field: c.column})
				}
			}
		}
		// Columns with a foreign key on pii_subject: the person's subject
		// and its values.
		refs := make([]string, 0, len(matched)+1)
		if p.SubjectID != "" {
			refs = append(refs, p.SubjectID)
		}
		for id := range matched {
			refs = append(refs, id)
		}
		if len(refs) > 0 {
			for _, c := range fks {
				ids, qerr := columnRows(ctx, tx, orgID, c.table, c.column, c.hasID, refs)
				if qerr != nil {
					return qerr
				}
				for _, id := range ids {
					out = append(out, SubjectFinding{Table: c.table, RowID: id[0], Field: c.column, Surrogate: id[1]})
				}
			}
		}
		// The subjects anchored to the person (an ADMP address).
		if p.SubjectID != "" {
			out = append(out, SubjectFinding{Table: "pii_subject", RowID: p.SubjectID, Field: "encrypted_payload", Surrogate: p.SubjectID})
			rows, qerr := tx.Query(ctx, `SELECT surrogate_id::text FROM pii_subject
				WHERE organization_id = $1 AND parent_id = $2 AND kind <> $3 ORDER BY 1`, orgID, p.SubjectID, pii.KindValue)
			if qerr != nil {
				return fmt.Errorf("read anchored subjects: %w", qerr)
			}
			children, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
			if qerr != nil {
				return fmt.Errorf("read anchored subjects: %w", qerr)
			}
			for _, id := range children {
				out = append(out, SubjectFinding{Table: "pii_subject", RowID: id, Field: "parent_id", Surrogate: id})
			}
		}
		// Other contacts with the address of the person.
		found, qerr := sameAddressContacts(ctx, tx, orgID, p)
		if qerr != nil {
			return qerr
		}
		out = append(out, found...)
		// Deliveries to the person.
		deliveries, qerr := personDeliveries(ctx, tx, orgID, userID, p.emails, false)
		if qerr != nil {
			return qerr
		}
		for _, d := range deliveries {
			out = append(out, d.findings(userID, p.emails)...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// columnRows returns id and value of the rows of the organization whose
// column holds one of the ids.
func columnRows(ctx context.Context, tx pgx.Tx, orgID, table, column string, hasID bool, ids []string) ([][2]string, error) {
	q := fmt.Sprintf(`SELECT %s, %s::text FROM %s WHERE organization_id = $1 AND %s = ANY($2::uuid[]) ORDER BY 1`,
		rowIDExpr(hasID), pgx.Identifier{column}.Sanitize(), pgx.Identifier{table}.Sanitize(), pgx.Identifier{column}.Sanitize())
	rows, err := tx.Query(ctx, q, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("read %s.%s: %w", table, column, err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var v [2]string
		scanErr := r.Scan(&v[0], &v[1])
		return v, scanErr
	})
}

// jsonFindings searches one jsonb column for the surrogates of the matched
// values and for the user id, in a transaction of its own.
func (s *SubjectService) jsonFindings(ctx context.Context, orgID string, c jsonColumn, userID string, matched map[string]string) ([]SubjectFinding, error) {
	col := pgx.Identifier{c.column}.Sanitize()
	cond := []string{}
	args := []any{orgID}
	if len(matched) > 0 {
		cond = append(cond, col+`::text LIKE '%"$pii": "%'`)
	}
	if userID != "" {
		args = append(args, "%"+userID+"%")
		cond = append(cond, col+`::text LIKE $2`)
	}
	q := fmt.Sprintf(`SELECT %s, %s FROM %s WHERE organization_id = $1 AND (%s)`, rowIDExpr(c.hasID), col,
		pgx.Identifier{c.table}.Sanitize(), strings.Join(cond, " OR "))
	var out []SubjectFinding
	err := s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("search %s.%s: %w", c.table, c.column, err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var raw []byte
			if err = rows.Scan(&id, &raw); err != nil {
				return fmt.Errorf("search %s.%s: %w", c.table, c.column, err)
			}
			var doc any
			if json.Unmarshal(raw, &doc) != nil {
				continue
			}
			walkJSON(doc, c.column, func(path string, v any) {
				if ref, ok := pii.RefID(v); ok {
					if value, hit := matched[ref]; hit && ref != "" {
						out = append(out, SubjectFinding{Table: c.table, RowID: id, Field: path, Surrogate: ref, Value: value})
					}
					return
				}
				if str, ok := v.(string); ok && userID != "" && strings.EqualFold(str, userID) {
					out = append(out, SubjectFinding{Table: c.table, RowID: id, Field: path})
				}
			})
		}
		return rows.Err()
	})
	return out, err
}

// walkJSON calls fn for every reference and every scalar of v with its path;
// a reference is not descended into.
func walkJSON(v any, path string, fn func(path string, v any)) {
	if _, ok := pii.RefID(v); ok {
		fn(path, v)
		return
	}
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkJSON(t[k], path+"."+k, fn)
		}
	case []any:
		for i, child := range t {
			walkJSON(child, path+"["+strconv.Itoa(i)+"]", fn)
		}
	default:
		fn(path, v)
	}
}

// sameAddressContacts lists the other contacts with an e-mail address of
// the person, in the columns or in their vault subject (blind index): the
// report names them, the erasure leaves them (they are persons of their
// own).
func sameAddressContacts(ctx context.Context, tx pgx.Tx, orgID string, p *person) ([]SubjectFinding, error) {
	if len(p.emails) == 0 {
		return nil, nil
	}
	tokens := []string{}
	if vault := pii.Default(); vault != nil {
		for _, e := range p.emails {
			token, err := vault.ExactTokenTx(ctx, tx, orgID, e)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token)
		}
	}
	self := ""
	if p.Kind == PersonContact {
		self = p.ID
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM contact c WHERE organization_id = $1 AND id::text <> $2
		AND (lower(btrim(email)) = ANY($3::text[])
		  OR EXISTS (SELECT 1 FROM pii_subject s WHERE s.organization_id = c.organization_id AND s.surrogate_id = c.pii_subject_id
		             AND s.surrogate_id::text <> $4 AND s.search_tokens && $5::text[]))
		ORDER BY 1`, orgID, self, p.emails, p.SubjectID, tokens)
	if err != nil {
		return nil, fmt.Errorf("read contacts with the address: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read contacts with the address: %w", err)
	}
	out := make([]SubjectFinding, 0, len(ids))
	for _, id := range ids {
		out = append(out, SubjectFinding{Table: "contact", RowID: id, Field: "email"})
	}
	return out, nil
}

// personDeliveries reads the deliveries to the user or to an address of
// the person, locked with forUpdate.
func personDeliveries(ctx context.Context, tx pgx.Tx, orgID, userID string, emails []string, forUpdate bool) ([]delivery, error) {
	if userID == "" && len(emails) == 0 {
		return nil, nil
	}
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(recipient_user_id::text, ''), COALESCE(recipient_email, ''),
			COALESCE(recipient_name, ''), recipient_key, COALESCE(recipients, 'null'::jsonb)
		FROM notification_delivery d
		WHERE organization_id = $1 AND (recipient_user_id::text = $2 OR lower(btrim(recipient_email)) = ANY($3::text[])
		  OR EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(d.recipients) = 'array' THEN d.recipients ELSE '[]'::jsonb END) e
		             WHERE e ->> 'user_id' = $2 OR lower(btrim(e ->> 'email')) = ANY($3::text[])))
		ORDER BY 1`+lock, orgID, userID, emails)
	if err != nil {
		return nil, fmt.Errorf("read deliveries: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (delivery, error) {
		var d delivery
		scanErr := r.Scan(&d.id, &d.userID, &d.email, &d.name, &d.key, &d.recipients)
		return d, scanErr
	})
}

// isPerson reports whether a recipient (user id, address) is the person.
func isPerson(userID, email, personUser string, emails []string) bool {
	if personUser != "" && strings.EqualFold(userID, personUser) {
		return true
	}
	e := normalize(email)
	for _, want := range emails {
		if e != "" && e == want {
			return true
		}
	}
	return false
}

// findings are the fields of the delivery that name the person.
func (d *delivery) findings(userID string, emails []string) []SubjectFinding {
	var out []SubjectFinding
	if isPerson(d.userID, d.email, userID, emails) {
		if d.userID != "" {
			out = append(out, SubjectFinding{Table: "notification_delivery", RowID: d.id, Field: "recipient_user_id"})
		}
		if d.email != "" {
			out = append(out, SubjectFinding{Table: "notification_delivery", RowID: d.id, Field: "recipient_email", Value: d.email})
		}
		if d.name != "" {
			out = append(out, SubjectFinding{Table: "notification_delivery", RowID: d.id, Field: "recipient_name", Value: d.name})
		}
	}
	var list []map[string]any
	if json.Unmarshal(d.recipients, &list) == nil {
		for i, r := range list {
			uid, _ := r["user_id"].(string)
			email, _ := r["email"].(string)
			if isPerson(uid, email, userID, emails) {
				out = append(out, SubjectFinding{Table: "notification_delivery", RowID: d.id,
					Field: "recipients[" + strconv.Itoa(i) + "]", Value: email})
			}
		}
	}
	return out
}

// RunReport builds the report of a person (the job privacy.subject_report).
func (s *SubjectService) RunReport(ctx context.Context, orgID, createdBy string, req *SubjectRequest) (*SubjectReport, error) {
	var p *person
	if err := s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		p, err = s.personTx(ctx, tx, orgID, req, false)
		return err
	}); err != nil {
		return nil, err
	}
	started := s.now()
	matched, candidates, err := s.matchValues(ctx, orgID, p)
	if err != nil {
		return nil, err
	}
	findings, err := s.references(ctx, orgID, p, matched)
	if err != nil {
		return nil, err
	}
	for i := range findings {
		findings[i].Candidate = findings[i].Surrogate != "" && candidates[findings[i].Surrogate]
	}
	report := &SubjectReport{Person: p.SubjectPerson, ValueSubjects: len(matched), Candidates: len(candidates),
		Findings: findings, Counts: map[string]int{}, ReportedAt: started}
	if report.Findings == nil {
		report.Findings = []SubjectFinding{}
	}
	for _, f := range findings {
		report.Counts[f.Table]++
	}
	counts := map[string]any{"value_subjects": len(matched), "candidates": len(candidates), "findings": len(findings)}
	for table, n := range report.Counts {
		counts[table] = n
	}
	err = s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// An erasure of the person that ran meanwhile leaves no report: its
		// values must not reach the job table after they were deleted.
		if erased, eerr := erasedSince(ctx, tx, orgID, p, started); eerr != nil || erased {
			if eerr == nil {
				eerr = errPersonErased
			}
			return eerr
		}
		return recordSubjectAudit(ctx, tx, orgID, createdBy, "privacy.subject_report", p, "report", counts)
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// Pseudonym is the pseudonym of a person: person- and the first 8 hex
// digits of SHA-256 of its surrogate (the user id for a user).
func Pseudonym(surrogate string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(surrogate)))
	return "person-" + hex.EncodeToString(sum[:])[:8]
}

// RunErase erases or pseudonymizes a person (the job
// privacy.subject_erase).
func (s *SubjectService) RunErase(ctx context.Context, orgID, createdBy string, req *SubjectRequest) (*SubjectErasure, error) {
	if req.Mode != ModeShred && req.Mode != ModePseudonymize {
		return nil, errSubjectFailed
	}
	var p *person
	if err := s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		p, err = s.personTx(ctx, tx, orgID, req, false)
		return err
	}); err != nil {
		return nil, err
	}
	matched, candidates, err := s.matchValues(ctx, orgID, p)
	if err != nil {
		return nil, err
	}
	surrogate := p.SubjectID
	if surrogate == "" {
		surrogate = p.ID
	}
	pseudonym := Pseudonym(surrogate)
	out := &SubjectErasure{Mode: req.Mode, PersonKind: p.Kind, PersonID: p.ID, SubjectID: p.SubjectID}
	vault := pii.Default()

	// The value subjects: a candidate (a match on the name alone) only
	// when the request confirms it.
	confirmed := map[string]bool{}
	for _, id := range req.Confirmed {
		confirmed[strings.ToLower(id)] = true
	}
	ids := make([]string, 0, len(matched))
	for id := range matched {
		if candidates[id] && !confirmed[strings.ToLower(id)] {
			out.CandidatesKept++
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// The audit names the erasure before its first batch commits: a later
	// failure (the person deleted meanwhile, the anonymization) leaves the
	// deleted values traced.
	err = s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return recordSubjectAudit(ctx, tx, orgID, createdBy, "privacy.subject_erase.started", p, req.Mode, map[string]any{
			"value_subjects": len(ids), "candidates_kept": out.CandidatesKept,
		})
	})
	if err != nil {
		return nil, err
	}

	// One transaction per batch.
	for start := 0; start < len(ids); start += subjectBatch {
		batch := ids[start:min(start+subjectBatch, len(ids))]
		err = s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
			if req.Mode == ModeShred {
				n, derr := pii.DeleteManyTx(ctx, tx, orgID, batch)
				out.ValueSubjects += n
				return derr
			}
			for _, id := range batch {
				if uerr := vault.UpdateTx(ctx, tx, orgID, id, pii.Data{"v": pseudonym}); uerr != nil && !errors.Is(uerr, pii.ErrNotFound) {
					return uerr
				}
				out.ValueSubjects++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// The person itself, its deliveries and earlier reports, with the audit
	// entry, in one transaction; the anonymization of a user joins it.
	err = s.tenantTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		ctx = database.ContextWithTx(ctx, tx)
		if _, perr := s.personTx(ctx, tx, orgID, req, true); perr != nil {
			return perr
		}
		if serr := s.erasePerson(ctx, tx, orgID, p, req.Mode, pseudonym, out); serr != nil {
			return serr
		}
		n, derr := eraseDeliveries(ctx, tx, orgID, p, req.Mode, pseudonym)
		if derr != nil {
			return derr
		}
		out.Deliveries = n
		if out.Reports, derr = clearReports(ctx, tx, orgID, p); derr != nil {
			return derr
		}
		return recordSubjectAudit(ctx, tx, orgID, createdBy, "privacy.subject_erase", p, req.Mode, map[string]any{
			"value_subjects": out.ValueSubjects, "candidates_kept": out.CandidatesKept, "subjects": out.Subjects, "deliveries": out.Deliveries,
			"reports": out.Reports, "user_anonymized": out.UserAnonymized,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// erasePerson shreds or pseudonymizes the subject of the person and
// anonymizes a user.
func (s *SubjectService) erasePerson(ctx context.Context, tx pgx.Tx, orgID string, p *person, mode, pseudonym string, out *SubjectErasure) error {
	vault := pii.Default()
	if p.SubjectID != "" {
		if mode == ModeShred {
			// The anchored subjects (values, an ADMP address) go by cascade.
			var anchored int64
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM pii_subject WHERE organization_id = $1 AND parent_id = $2`,
				orgID, p.SubjectID).Scan(&anchored); err != nil {
				return fmt.Errorf("count anchored subjects: %w", err)
			}
			n, err := pii.DeleteManyTx(ctx, tx, orgID, []string{p.SubjectID})
			if err != nil {
				return err
			}
			if n > 0 {
				out.Subjects += n + anchored
			}
		} else if vault != nil {
			rows, err := tx.Query(ctx, `SELECT surrogate_id::text, kind FROM pii_subject
				WHERE organization_id = $1 AND (surrogate_id = $2 OR parent_id = $2)`, orgID, p.SubjectID)
			if err != nil {
				return fmt.Errorf("read the subjects of the person: %w", err)
			}
			type subj struct{ id, kind string }
			subjects, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (subj, error) {
				var v subj
				scanErr := r.Scan(&v.id, &v.kind)
				return v, scanErr
			})
			if err != nil {
				return fmt.Errorf("read the subjects of the person: %w", err)
			}
			for _, sub := range subjects {
				data := pii.Data{"name": pseudonym}
				if sub.kind == pii.KindValue {
					data = pii.Data{"v": pseudonym}
				}
				if uerr := vault.UpdateTx(ctx, tx, orgID, sub.id, data); uerr != nil {
					return uerr
				}
				out.Subjects++
			}
		}
	}
	switch p.Kind {
	case PersonContact:
		// Plaintext columns of a contact the backfill has not moved yet.
		name := any(nil)
		if mode == ModePseudonymize && p.SubjectID == "" {
			name = pseudonym
		}
		if _, err := tx.Exec(ctx, `UPDATE contact SET display_name = $3, email = NULL, phone = NULL, updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND (display_name IS NOT NULL OR email IS NOT NULL OR phone IS NOT NULL)`,
			orgID, p.ID, name); err != nil {
			return fmt.Errorf("clear the contact: %w", err)
		}
	case PersonUser:
		if s.users == nil {
			return errors.New("no user repository to anonymize the user")
		}
		hook := func(ctx context.Context, _ pgx.Tx) error {
			if s.sessions == nil {
				return nil
			}
			return s.sessions.RevokeUser(ctx, p.ID, s.now())
		}
		if _, err := s.users.AnonymizeUserTx(ctx, orgID, p.ID, hook); err != nil {
			return fmt.Errorf("anonymize the user: %w", err)
		}
		out.UserAnonymized = true
	}
	return nil
}

// eraseDeliveries empties (shred) or pseudonymizes the address of the
// deliveries to the person; a key with the address follows.
func eraseDeliveries(ctx context.Context, tx pgx.Tx, orgID string, p *person, mode, pseudonym string) (int64, error) {
	userID := ""
	if p.Kind == PersonUser {
		userID = p.ID
	}
	deliveries, err := personDeliveries(ctx, tx, orgID, userID, p.emails, true)
	if err != nil {
		return 0, err
	}
	var email, name any
	if mode == ModePseudonymize {
		email, name = pseudonym+"@pseudonymized.invalid", pseudonym
	}
	var n int64
	for i := range deliveries {
		d := &deliveries[i]
		changed := false
		recipientEmail, recipientName, key := nullable(d.email), nullable(d.name), d.key
		if isPerson(d.userID, d.email, userID, p.emails) {
			if d.email != "" {
				recipientEmail = email
			}
			if d.name != "" {
				recipientName = name
			}
			if strings.HasPrefix(key, "mail:") {
				if mode == ModePseudonymize {
					key = "mail:" + pseudonym + "@pseudonymized.invalid"
				} else {
					key = "erased:" + d.id
				}
			}
			changed = true
		}
		recipients := d.recipients
		var list []map[string]any
		if json.Unmarshal(d.recipients, &list) == nil && list != nil {
			listChanged := false
			for _, r := range list {
				uid, _ := r["user_id"].(string)
				addr, _ := r["email"].(string)
				if !isPerson(uid, addr, userID, p.emails) {
					continue
				}
				listChanged = true
				for _, k := range []string{"email", "name"} {
					if _, has := r[k]; !has {
						continue
					}
					if mode == ModePseudonymize {
						r[k] = name
						if k == "email" {
							r[k] = email
						}
					} else {
						delete(r, k)
					}
				}
			}
			if listChanged {
				if recipients, err = json.Marshal(list); err != nil {
					return 0, err
				}
				changed = true
			}
		}
		if !changed {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE notification_delivery SET recipient_email = $3, recipient_name = $4, recipient_key = $5,
				recipients = CASE WHEN recipients IS NULL THEN NULL ELSE $6::jsonb END, updated_at = now()
			WHERE organization_id = $1 AND id = $2`, orgID, d.id, recipientEmail, recipientName, key, recipients); err != nil {
			return 0, fmt.Errorf("erase delivery: %w", err)
		}
		n++
	}
	return n, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// personIDs are the ids a job about the person names in its parameters.
func personIDs(p *person) []string {
	ids := []string{strings.ToLower(p.ID)}
	if p.SubjectID != "" {
		ids = append(ids, strings.ToLower(p.SubjectID))
	}
	return ids
}

// personParams is the condition on the parameters of a job about the
// person ($3: personIDs).
const personParams = `(params ->> 'user_id' = ANY($3::text[]) OR params ->> 'contact_id' = ANY($3::text[]) OR params ->> 'subject_id' = ANY($3::text[]))`

// clearReports empties the results of earlier reports about the person:
// they hold its values unsealed. A report still queued or running is
// cancelled: it may have decrypted the values before they were deleted,
// and a cancelled job stores no result (its completion needs the status
// running).
func clearReports(ctx context.Context, tx pgx.Tx, orgID string, p *person) (int64, error) {
	ids := personIDs(p)
	tag, err := tx.Exec(ctx, `UPDATE job SET result = '{"erased": true}'::jsonb, updated_at = now()
		WHERE organization_id = $1 AND kind = $2 AND result IS NOT NULL AND `+personParams,
		orgID, SubjectReportJobKind, ids)
	if err != nil {
		return 0, fmt.Errorf("clear earlier reports: %w", err)
	}
	cancelled, err := tx.Exec(ctx, `UPDATE job SET status = 'cancelled', error = $4, finished_at = now(), updated_at = now()
		WHERE organization_id = $1 AND kind = $2 AND status IN ('queued', 'running') AND `+personParams,
		orgID, SubjectReportJobKind, ids, errPersonErased.Error())
	if err != nil {
		return 0, fmt.Errorf("cancel open reports: %w", err)
	}
	return tag.RowsAffected() + cancelled.RowsAffected(), nil
}

// erasedSince reports whether an erasure of the person ran or succeeded
// since t: a report started before it must not store its values.
func erasedSince(ctx context.Context, tx pgx.Tx, orgID string, p *person, t time.Time) (bool, error) {
	var erased bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM job
		WHERE organization_id = $1 AND kind = $2 AND `+personParams+`
		  AND (status = 'running' OR (status = 'succeeded' AND finished_at >= $4)))`,
		orgID, SubjectEraseJobKind, personIDs(p), t).Scan(&erased)
	if err != nil {
		return false, fmt.Errorf("check erasures of the person: %w", err)
	}
	return erased, nil
}

// recordSubjectAudit writes the audit entry of a report or an erasure: the
// person by id or surrogate, the mode and the counts, never a value.
func recordSubjectAudit(ctx context.Context, tx pgx.Tx, orgID, actor, action string, p *person, mode string, counts map[string]any) error {
	actorType := "user"
	if actor == "" {
		actorType = "system"
	}
	changes := map[string]any{"person_kind": p.Kind, "person_id": p.ID, "mode": mode, "counts": counts}
	if p.SubjectID != "" {
		changes["subject_id"] = p.SubjectID
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: action, ResourceType: "privacy", ResourceID: p.ID, Changes: changes}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}
