package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Forms as trigger and step of workflows (WFL-01, WFL-02, WP-1102):
//
//   - The trigger {"type": "form_submitted", "form_id": "<uuid>", "filter":
//     <MET-45>} starts a pending run of every active matching definition in
//     the transaction of the submission (POST /forms/{id}/submissions,
//     form.SubmissionHook, the IGA pattern); the run executes over the job
//     queue after the commit. Its context holds form.<key> (the values),
//     submission_id, form_id, form_version_id and ci_id; initiated_by is the
//     submitter.
//   - The action
//
//     {"type": "request_form", "form_id": "<uuid>",
//     "recipient": {"type": "user" | "team" | "contact", "id": "<uuid>"},
//     "prefill": {"serial_number": "${ci.attributes.serial}"},
//     "deadline": "72h", "on_timeout": "cancel" | "continue"}
//
//     sets step and run waiting_input and notifies the recipient with a
//     link to the form and the run. A submission naming the run (run_id)
//     completes the step with {submission_id}, puts the values into the
//     context under form.<key> and queues the run again. Only the
//     recipient submits: the user, a member of the team or, for a contact,
//     a user with form:write in client and site of the run.
//   - waiting_input does not block the serial key of the run (only running
//     runs do, WP-1089); a run in it can be cancelled (WP-1092), and
//     workflow.approval_sweep ends expired steps after on_timeout.

const (
	// requestFormActionType is the type of the action.
	requestFormActionType = "request_form"
	// TriggerFormSubmitted is the trigger type and the run trigger of a
	// form submission.
	TriggerFormSubmitted = "form_submitted"
)

// Behaviour of an expired request_form step.
const (
	formTimeoutCancel   = "cancel"
	formTimeoutContinue = "continue"
)

// formDeadlineExpiredText is the error of a request_form step whose
// deadline expired with on_timeout cancel.
const formDeadlineExpiredText = "form deadline expired"

// Recipient kinds of request_form.
const (
	formRecipientUser    = "user"
	formRecipientTeam    = "team"
	formRecipientContact = "contact"
)

// errFormRequest is the fixed text of a stored request_form action that is
// invalid (a definition stored without the write validation).
var errFormRequest = errors.New("invalid request_form action")

// formRequestSpec is a parsed request_form action.
type formRequestSpec struct {
	FormID        string
	RecipientKind string
	RecipientID   string
	Prefill       map[string]any
	Deadline      time.Duration
	OnTimeout     string
}

// parseFormRequest reads a request_form action; a refused field is an
// approvalSpecError (field, message).
func parseFormRequest(action JSONMap) (*formRequestSpec, error) {
	s := &formRequestSpec{OnTimeout: formTimeoutCancel}
	if s.FormID = str(action, "form_id", ""); !api.IsUUID(s.FormID) {
		return nil, &approvalSpecError{"form_id", "is required and must be a UUID"}
	}
	recipient, ok := action["recipient"].(map[string]any)
	if !ok {
		if jm, isJSON := action["recipient"].(JSONMap); isJSON {
			recipient, ok = jm, true
		}
	}
	if !ok {
		return nil, &approvalSpecError{"recipient", `is required: {"type": "user" | "team" | "contact", "id": "<uuid>"}`}
	}
	s.RecipientKind, s.RecipientID = str(recipient, "type", ""), str(recipient, "id", "")
	switch s.RecipientKind {
	case formRecipientUser, formRecipientTeam, formRecipientContact:
	default:
		return nil, &approvalSpecError{"recipient.type", "must be user, team or contact"}
	}
	if !api.IsUUID(s.RecipientID) {
		return nil, &approvalSpecError{"recipient.id", "must be a UUID"}
	}
	if raw, has := action["prefill"]; has && raw != nil {
		switch p := raw.(type) {
		case map[string]any:
			s.Prefill = p
		case JSONMap:
			s.Prefill = p
		default:
			return nil, &approvalSpecError{"prefill", "must be an object"}
		}
	}
	var err error
	if s.Deadline, err = parseApprovalDuration(action["deadline"], "deadline"); err != nil {
		return nil, err
	}
	if s.Deadline > maxApprovalDeadline {
		return nil, &approvalSpecError{"deadline", "must be at most 30 days"}
	}
	if raw, has := action["on_timeout"]; has && raw != nil {
		v, _ := raw.(string)
		if v != formTimeoutCancel && v != formTimeoutContinue {
			return nil, &approvalSpecError{"on_timeout", "must be cancel or continue"}
		}
		s.OnTimeout = v
	}
	return s, nil
}

// validateFormWrite checks a definition that is written (POST and PATCH
// /api/v1/workflows): the trigger form_submitted names its form, every
// request_form action is valid; a refusal is a 422 on its field.
func validateFormWrite(trigger JSONMap, actions []JSONMap) error {
	if str(trigger, "type", "") == TriggerFormSubmitted && !api.IsUUID(str(trigger, "form_id", "")) {
		return &api.FieldError{Field: "trigger.form_id", Message: "is required and must be a UUID", Status: http.StatusUnprocessableEntity}
	}
	for i, action := range actions {
		if typ, _ := action["type"].(string); typ != requestFormActionType {
			continue
		}
		if _, err := parseFormRequest(action); err != nil {
			var se *approvalSpecError
			if errors.As(err, &se) {
				return &api.FieldError{Field: fmt.Sprintf("actions[%d].%s", i, se.field), Message: se.message, Status: http.StatusUnprocessableEntity}
			}
			return err
		}
	}
	return nil
}

// formStore keeps the form steps; the PostgreSQL repository implements it
// (forms need the database).
type formStore interface {
	// requestInput sets step and run waiting_input with the step's output
	// and deadline.
	requestInput(ctx context.Context, orgID string, run *Run, stepID string, output JSONMap, due *time.Time) (*Run, error)
	// lockInput locks the run and its step waiting for formID in the
	// transaction of ctx; ErrRunNotWaiting when there is none.
	lockInput(ctx context.Context, orgID, runID, formID string) (*formInput, error)
	// isTeamMember reports whether userID is a member of teamID.
	isTeamMember(ctx context.Context, orgID, teamID, userID string) (bool, error)
	// completeInput completes the waiting step of runID with the
	// submission and queues the run again (pending, enqueues raised), the
	// values in the context under form.
	completeInput(ctx context.Context, orgID, runID, formID string, sub *form.Submission) (*Run, error)
	// formDefinitions are the active definitions with the trigger
	// form_submitted of formID.
	formDefinitions(ctx context.Context, orgID, formID string) ([]Definition, error)
	// dueInputs are the waiting_input steps whose deadline is reached.
	dueInputs(ctx context.Context, orgID string, now time.Time, limit int) ([]dueInput, error)
	// expireInput ends an expired step after onTimeout under the row locks
	// (FOR UPDATE SKIP LOCKED); done is false when it changed meanwhile.
	expireInput(ctx context.Context, orgID string, d *dueInput, now time.Time, onTimeout string) (*Run, bool, error)
}

// formInput is a locked waiting_input step.
type formInput struct {
	StepID    string
	StepIndex int
	Input     JSONMap
	Output    JSONMap
	ClientID  string
	SiteID    string
}

// dueInput is an expired waiting_input step the sweep reads.
type dueInput struct {
	StepID string
	RunID  string
	Input  JSONMap
	DueAt  time.Time
}

func (e *Executor) formStore() (formStore, error) {
	store, ok := e.repo.(formStore)
	if !ok {
		return nil, fmt.Errorf("workflow repository keeps no form requests")
	}
	return store, nil
}

// requestInput moves step of run into waiting_input (AK2): the output names
// form, recipient, the rendered prefill and the deadline; after the commit
// the recipient is notified.
func (e *Executor) requestInput(ctx context.Context, orgID string, run *Run, step *Step, action JSONMap) (*Run, error) {
	spec, err := parseFormRequest(action)
	if err != nil {
		return nil, errFormRequest
	}
	store, err := e.formStore()
	if err != nil {
		return nil, err
	}
	prefill := map[string]any{}
	if spec.Prefill != nil {
		prefill, _ = renderPayload(spec.Prefill, EvaluationValues(run.Context)).(map[string]any)
	}
	output := JSONMap{"form_id": spec.FormID, "recipient": map[string]any{"type": spec.RecipientKind, "id": spec.RecipientID},
		"prefill": prefill, "on_timeout": spec.OnTimeout}
	var due *time.Time
	if spec.Deadline > 0 {
		at := time.Now().UTC().Add(spec.Deadline)
		due = &at
		output["due_at"] = at.Format(time.RFC3339)
	}
	waiting, err := store.requestInput(ctx, orgID, run, step.ID, output, due)
	if err != nil {
		return nil, err
	}
	e.notifyFormRecipient(ctx, orgID, waiting, step.StepIndex, spec)
	return waiting, nil
}

// FormLink is the path of the frontend that opens the form a run requests.
func FormLink(formID, runID string) string {
	return "/forms?form_id=" + url.QueryEscape(formID) + "&run_id=" + url.QueryEscape(runID)
}

// notifyFormRecipient notifies the recipient of a form request in the
// category workflow (AK2, WP-1095); failures are logged, the step waits.
func (e *Executor) notifyFormRecipient(ctx context.Context, orgID string, run *Run, stepIndex int, spec *formRequestSpec) {
	if e.notifier == nil || e.recipients == nil {
		slog.WarnContext(ctx, "workflow: form request not notified, notifications unavailable", "run_id", run.ID)
		return
	}
	audience := &notify.Audience{}
	switch spec.RecipientKind {
	case formRecipientUser:
		audience.DirectUserIDs = []string{spec.RecipientID}
	case formRecipientTeam, formRecipientContact:
		// Team members and the contact are bounded by the client and site
		// of the run: a contact of another client is never addressed.
		client, site := "", ""
		if store, ok := e.repo.(approvalStore); ok {
			var serr error
			if client, site, serr = store.runScope(ctx, orgID, run.ID); serr != nil {
				slog.WarnContext(ctx, "workflow: form request not notified, scope of the run unknown", "run_id", run.ID, "error", serr)
				return
			}
		}
		audience.ClientID, audience.SiteID = client, site
		if spec.RecipientKind == formRecipientTeam {
			audience.Permission, audience.TeamIDs = identity.PermFormWrite, []string{spec.RecipientID}
		} else {
			audience.ContactIDs = []string{spec.RecipientID}
		}
	}
	recipients, err := e.recipients.Resolve(ctx, orgID, audience)
	if err != nil || len(recipients) == 0 {
		slog.WarnContext(ctx, "workflow: form request without recipient", "run_id", run.ID, "step_index", stepIndex, "error", err)
		return
	}
	formName, workflowName := "", ""
	if e.forms != nil {
		if def, ferr := e.forms.GetDefinition(ctx, orgID, spec.FormID); ferr == nil {
			formName = def.Name
		}
	}
	if def, derr := e.repo.GetDefinition(ctx, orgID, run.WorkflowID); derr == nil {
		workflowName = def.Name
	}
	var brand *notify.Branding
	if source, ok := e.recipients.(notify.BrandingSource); ok {
		if brand, err = source.Branding(ctx, orgID); err != nil {
			slog.WarnContext(ctx, "workflow: branding of the form request", "error", err)
		}
	}
	orgLocale := ""
	if brand != nil {
		orgLocale = brand.Locale
	}
	orgTemplate := notify.TemplateLocale("", orgLocale)
	link := FormLink(spec.FormID, run.ID)
	texts := map[string]notify.Text{
		notify.LocaleDE: notify.Render(notify.WorkflowNotification, notify.LocaleDE, map[string]any{
			"subject": oneLine("Formular angefordert: " + formName), "workflow_name": workflowName, "run_id": run.ID,
			"body": "Bitte füllen Sie das Formular " + formName + " aus: " + link}),
		notify.LocaleEN: notify.Render(notify.WorkflowNotification, notify.LocaleEN, map[string]any{
			"subject": oneLine("Form requested: " + formName), "workflow_name": workflowName, "run_id": run.ID,
			"body": "Please fill in the form " + formName + ": " + link}),
	}
	if _, err := e.notifier.Notify(ctx, orgID, &notify.Request{Category: notify.CategoryWorkflow, Personal: true,
		Subject: texts[orgTemplate].Subject, Body: texts[orgTemplate].Body, Texts: texts, Branding: brand,
		Data:     map[string]any{"run_id": run.ID, "workflow_id": run.WorkflowID, "step_index": stepIndex, "form_id": spec.FormID, "link": link},
		DedupKey: "workflow:" + run.ID + ":" + strconv.Itoa(stepIndex) + ":form", Recipients: recipients}); err != nil {
		slog.WarnContext(ctx, "workflow: form request not notified", "run_id", run.ID, "error", err)
	}
}

// FormHook implements form.SubmissionHook with the executor (AK1, AK3).
type FormHook struct {
	exec *Executor
	// grants reads the grants of a submitter for a contact recipient
	// (form:write in the run's scope).
	grants notify.GrantSource
}

// NewFormHook returns the hook of the form routes; grants reads the role
// grants of a user (permission repository).
func NewFormHook(exec *Executor, grants notify.GrantSource) *FormHook {
	return &FormHook{exec: exec, grants: grants}
}

var _ form.SubmissionHook = (*FormHook)(nil)

// PrepareResume implements form.SubmissionHook (AK3, AK4).
func (h *FormHook) PrepareResume(ctx context.Context, orgID, runID, formID, userID string) (form.JSONMap, error) {
	store, err := h.exec.formStore()
	if err != nil {
		return nil, form.ErrRunNotWaiting
	}
	in, err := store.lockInput(ctx, orgID, runID, formID)
	if err != nil {
		return nil, err
	}
	recipient, _ := in.Output["recipient"].(map[string]any)
	kind, id := str(recipient, "type", ""), str(recipient, "id", "")
	allowed := false
	switch kind {
	case formRecipientUser:
		allowed = userID != "" && userID == id
	case formRecipientTeam:
		if userID != "" {
			if allowed, err = store.isTeamMember(ctx, orgID, id, userID); err != nil {
				return nil, err
			}
		}
	case formRecipientContact:
		if h.grants != nil && userID != "" {
			grants, gerr := h.grants.AccessGrants(ctx, orgID, userID)
			if gerr != nil {
				return nil, gerr
			}
			allowed = permission.Covers(grants, identity.PermFormWrite, in.ClientID, in.SiteID)
		}
	}
	if !allowed {
		return nil, form.ErrNotFormRecipient
	}
	prefill := form.JSONMap{}
	if p, ok := in.Output["prefill"].(map[string]any); ok {
		prefill = p
	}
	return prefill, nil
}

// Submitted implements form.SubmissionHook (AK1, AK3): the runs of the
// form_submitted definitions of the form are started, the waiting step of
// sub.RunID is completed; after executes them once committed.
func (h *FormHook) Submitted(ctx context.Context, sub *form.Submission) (func(ctx context.Context), error) {
	e := h.exec
	store, err := e.formStore()
	if err != nil {
		if sub.RunID != "" {
			return nil, form.ErrRunNotWaiting
		}
		return nil, nil
	}
	defs, err := store.formDefinitions(ctx, sub.OrganizationID, sub.FormID)
	if err != nil {
		return nil, fmt.Errorf("read the form workflows: %w", err)
	}
	type started struct {
		def *Definition
		run *Run
	}
	var runs []started
	for i := range defs {
		def := &defs[i]
		payload := JSONMap{"form": map[string]any(sub.Values), "submission_id": sub.ID, "form_id": sub.FormID,
			"form_version_id": sub.FormVersionID}
		if sub.CIID != "" {
			payload["ci_id"] = sub.CIID
		}
		hold, ferr := filterHolds(def.Trigger, payload)
		if ferr != nil {
			slog.WarnContext(ctx, "workflow trigger: invalid stored filter", "workflow_id", def.ID)
			continue
		}
		if !hold {
			continue
		}
		run, serr := e.Start(ctx, sub.OrganizationID, def, TriggerFormSubmitted, payload)
		if serr != nil {
			return nil, fmt.Errorf("start the run of %s: %w", def.ID, serr)
		}
		runs = append(runs, started{def: def, run: run})
	}
	var resumed *Run
	if sub.RunID != "" {
		if resumed, err = store.completeInput(ctx, sub.OrganizationID, sub.RunID, sub.FormID, sub); err != nil {
			return nil, err
		}
	}
	if len(runs) == 0 && resumed == nil {
		return nil, nil
	}
	return func(ctx context.Context) {
		for _, s := range runs {
			if _, xerr := e.Execute(ctx, sub.OrganizationID, s.def, s.run); xerr != nil {
				slog.WarnContext(ctx, "workflow: run of a form submission failed", "run_id", s.run.ID, "error", xerr)
			}
		}
		if resumed != nil {
			e.continueInput(ctx, sub.OrganizationID, resumed)
		}
	}, nil
}

// continueInput executes a run whose form step was completed: over the job
// queue like an approved run, in the request without one.
func (e *Executor) continueInput(ctx context.Context, orgID string, run *Run) {
	if e.queue != nil {
		e.requeue(ctx, orgID, run)
		return
	}
	if _, err := e.resume(ctx, orgID, run, attempt{}); err != nil {
		slog.WarnContext(ctx, "workflow: continued run failed", "run_id", run.ID, "error", err)
	}
}

// sweepInputs ends the expired waiting_input steps of the organization
// after their on_timeout (AK5): cancel sets run and step cancelled,
// continue completes the step without values and queues the run.
func (s *ApprovalSweep) sweepInputs(ctx context.Context, orgID string, now time.Time, res *ApprovalSweepResult) error {
	store, ok := s.exec.repo.(formStore)
	if !ok {
		return nil
	}
	due, err := store.dueInputs(ctx, orgID, now, approvalSweepBatch)
	if err != nil {
		return err
	}
	var errs []error
	for i := range due {
		onTimeout := formTimeoutCancel
		if spec, perr := parseFormRequest(due[i].Input); perr == nil {
			onTimeout = spec.OnTimeout
		}
		run, done, xerr := store.expireInput(ctx, orgID, &due[i], now, onTimeout)
		if xerr != nil {
			errs = append(errs, fmt.Errorf("form step %s: %w", due[i].StepID, xerr))
			continue
		}
		if !done {
			continue
		}
		res.InputsExpired++
		if onTimeout == formTimeoutContinue {
			s.exec.continueInput(ctx, orgID, run)
		}
	}
	return errors.Join(errs...)
}

// ─── PostgreSQL ───────────────────────────────────────────────────────────────

// requestInput implements formStore.
func (r *PGRepository) requestInput(ctx context.Context, orgID string, run *Run, stepID string, output JSONMap, due *time.Time) (*Run, error) {
	return r.changeRun(ctx, orgID, run.ID, func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error) {
		if endedStatus(prev) {
			return nil, ErrRunEnded
		}
		tag, err := tx.Exec(ctx, `UPDATE workflow_step SET status='waiting_input', output=$3, error='', due_at=$4, reminded_at=NULL, updated_at=now()
			WHERE organization_id=$1 AND id=$2`, orgID, stepID, output, due)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, fmt.Errorf("form step not found")
		}
		return scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='waiting_input', updated_at=now()
			WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, run.ID))
	})
}

// lockInput implements formStore: run and step are locked FOR UPDATE in
// the transaction of ctx (the submission's) in the caller's scope, so a
// run of another organization or outside the scope is not found.
func (r *PGRepository) lockInput(ctx context.Context, orgID, runID, formID string) (*formInput, error) {
	var in *formInput
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM workflow_run WHERE organization_id=$1 AND id::text=$2 FOR UPDATE`, orgID, runID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return form.ErrRunNotWaiting
		}
		if err != nil {
			return err
		}
		if status != StatusWaitingInput {
			return form.ErrRunNotWaiting
		}
		var f formInput
		var inRaw, outRaw []byte
		err = tx.QueryRow(ctx, `SELECT id::text, step_index, input, output, COALESCE(client_id::text, ''), COALESCE(site_id::text, '')
			FROM workflow_step WHERE organization_id=$1 AND run_id::text=$2 AND status='waiting_input' AND input->>'form_id' = $3
			FOR UPDATE`, orgID, runID, formID).Scan(&f.StepID, &f.StepIndex, &inRaw, &outRaw, &f.ClientID, &f.SiteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return form.ErrRunNotWaiting
		}
		if err != nil {
			return err
		}
		f.Input, f.Output = asMap(inRaw), asMap(outRaw)
		in = &f
		return nil
	})
	return in, err
}

// isTeamMember implements formStore.
func (r *PGRepository) isTeamMember(ctx context.Context, orgID, teamID, userID string) (bool, error) {
	var member bool
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_member WHERE organization_id=$1 AND team_id::text=$2 AND user_id::text=$3)`,
			orgID, teamID, userID).Scan(&member)
	})
	return member, err
}

// completeInput implements formStore (AK3): the step succeeds with the
// submission, the run is pending with its enqueues raised and the values
// under form in its context.
func (r *PGRepository) completeInput(ctx context.Context, orgID, runID, formID string, sub *form.Submission) (*Run, error) {
	return r.changeRun(ctx, orgID, runID, func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error) {
		if prev != StatusWaitingInput {
			return nil, form.ErrRunNotWaiting
		}
		tag, err := tx.Exec(ctx, `UPDATE workflow_step SET status='succeeded', output = output || jsonb_build_object('submission_id', $3::text),
			error='', updated_at=now()
			WHERE organization_id=$1 AND run_id::text=$2 AND status='waiting_input' AND input->>'form_id' = $4`, orgID, runID, sub.ID, formID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, form.ErrRunNotWaiting
		}
		values := map[string]any(sub.Values)
		if values == nil {
			values = map[string]any{}
		}
		return scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='pending', enqueues=enqueues+1,
			context = jsonb_set(context, '{form}', COALESCE(CASE WHEN jsonb_typeof(context->'form') = 'object' THEN context->'form' END, '{}'::jsonb) || $3::jsonb),
			updated_at=now() WHERE organization_id=$1 AND id::text=$2 RETURNING `+runCols, orgID, runID, values))
	})
}

// formDefinitions implements formStore.
func (r *PGRepository) formDefinitions(ctx context.Context, orgID, formID string) ([]Definition, error) {
	out := []Definition{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+defCols+` FROM workflow_def WHERE organization_id=$1 AND active AND trigger->>'type' = $2
			AND trigger->>'form_id' = $3 ORDER BY id`, orgID, TriggerFormSubmitted, formID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDef(rows)
			if err != nil {
				return err
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, err
}

// dueInputs implements formStore.
func (r *PGRepository) dueInputs(ctx context.Context, orgID string, now time.Time, limit int) ([]dueInput, error) {
	var out []dueInput
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.id::text, s.run_id::text, s.input, s.due_at FROM workflow_step s
			JOIN workflow_run r ON r.id = s.run_id AND r.organization_id = s.organization_id
			WHERE s.organization_id = $1 AND s.status = 'waiting_input' AND r.status = 'waiting_input'
			  AND s.due_at IS NOT NULL AND s.due_at <= $2
			ORDER BY s.due_at, s.id LIMIT $3`, orgID, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d dueInput
			var in []byte
			if err := rows.Scan(&d.StepID, &d.RunID, &in, &d.DueAt); err != nil {
				return err
			}
			d.Input = asMap(in)
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// expireInput implements formStore: run and step are locked with FOR
// UPDATE SKIP LOCKED (a submission in progress wins) and checked again.
func (r *PGRepository) expireInput(ctx context.Context, orgID string, d *dueInput, now time.Time, onTimeout string) (*Run, bool, error) {
	var run *Run
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var prev string
		err := tx.QueryRow(ctx, `SELECT status FROM workflow_run WHERE organization_id=$1 AND id=$2 FOR UPDATE SKIP LOCKED`, orgID, d.RunID).Scan(&prev)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if prev != StatusWaitingInput {
			return nil
		}
		var stepID string
		err = tx.QueryRow(ctx, `SELECT id::text FROM workflow_step WHERE organization_id=$1 AND id=$2 AND status='waiting_input'
			AND due_at IS NOT NULL AND due_at <= $3 FOR UPDATE SKIP LOCKED`, orgID, d.StepID, now).Scan(&stepID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if onTimeout == formTimeoutContinue {
			if _, err = tx.Exec(ctx, `UPDATE workflow_step SET status='succeeded', output = output || '{"timed_out": true}'::jsonb, error='', updated_at=now()
				WHERE organization_id=$1 AND id=$2`, orgID, stepID); err != nil {
				return err
			}
			run, err = scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='pending', enqueues=enqueues+1, updated_at=now()
				WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, d.RunID))
		} else {
			if _, err = tx.Exec(ctx, `UPDATE workflow_step SET status='cancelled', error=$3, updated_at=now() WHERE organization_id=$1 AND id=$2`,
				orgID, stepID, formDeadlineExpiredText); err != nil {
				return err
			}
			run, err = scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='cancelled', finished_at=now(), next_attempt_at=NULL, updated_at=now()
				WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, d.RunID))
		}
		if err != nil {
			return err
		}
		if run.Steps, err = r.listStepsTx(ctx, tx, orgID, d.RunID); err != nil {
			return err
		}
		return recordRunTransition(ctx, tx, prev, run)
	})
	if err != nil {
		return nil, false, err
	}
	return run, run != nil, nil
}
