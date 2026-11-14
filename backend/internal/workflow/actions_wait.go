package workflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The action wait_until (WFL-04, WP-1109):
//
//	{"type": "wait_until", "at": "window.starts_at - 72h"}
//
// at is a path of the run context holding a time (RFC 3339) with an
// optional offset (+ or - a duration such as 72h, 90m or 3d). A time not
// reached yet sets the step and the run pending with next_attempt_at at
// that time and ends the attempt: the job of the run is queued for it
// (WP-1088), the next attempt executes the step again, finds the time
// reached and continues. A time in the past continues at once, so the
// action is idempotent.

const waitUntilActionType = "wait_until"

// errWaitUntil is the fixed text of an invalid or unresolvable at.
var errWaitUntil = errors.New("wait_until needs at: a context time with an optional offset such as window.starts_at - 72h")

// waitExpr is the form of at: <path> [(+|-) <duration>].
var waitExpr = regexp.MustCompile(`^\s*([A-Za-z0-9_.\-]+)\s*(?:([+-])\s*([0-9]+[a-z]+(?:[0-9]+[a-z]+)*)\s*)?$`)

// WithClock sets the clock of wait_until and of the window check of
// maintenance_affected (tests).
func (e *Executor) WithClock(now func() time.Time) *Executor {
	e.clock = now
	return e
}

func (e *Executor) now() time.Time {
	if e.clock != nil {
		return e.clock().UTC()
	}
	return time.Now().UTC()
}

// waitTime resolves at over the run context.
func waitTime(at string, runCtx JSONMap) (time.Time, error) {
	m := waitExpr.FindStringSubmatch(at)
	if m == nil {
		return time.Time{}, errWaitUntil
	}
	raw, ok := EvaluationValues(runCtx)[m[1]].(string)
	if !ok {
		return time.Time{}, errWaitUntil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, errWaitUntil
	}
	if m[2] != "" {
		d, derr := parseApprovalDuration(m[3], "at")
		if derr != nil {
			return time.Time{}, errWaitUntil
		}
		if m[2] == "-" {
			d = -d
		}
		t = t.Add(d)
	}
	return t.UTC(), nil
}

// validateWaitActions checks the wait_until actions of a definition that
// is written: at is required and of the form above (422 on actions[i].at).
func validateWaitActions(actions []JSONMap) error {
	for i, action := range actions {
		if typ, _ := action["type"].(string); typ != waitUntilActionType {
			continue
		}
		at, _ := action["at"].(string)
		if m := waitExpr.FindStringSubmatch(at); m == nil || strings.TrimSpace(at) == "" {
			return triggerFieldError(fmt.Sprintf("actions[%d].at", i), "must be a context path with an optional offset such as window.starts_at - 72h")
		} else if m[2] != "" {
			if _, err := parseApprovalDuration(m[3], "at"); err != nil {
				return triggerFieldError(fmt.Sprintf("actions[%d].at", i), "has an invalid offset")
			}
		}
	}
	return nil
}

// waitStore defers a run to a time; the PostgreSQL repository implements
// it.
type waitStore interface {
	deferRun(ctx context.Context, orgID, runID, stepID string, until time.Time, output JSONMap) (*Run, error)
}

// waitUntil executes wait_until as step of run: done reports that the time
// is reached (the step succeeds with output); otherwise the run is
// deferred and returned pending.
func (e *Executor) waitUntil(ctx context.Context, orgID string, run *Run, step *Step, action JSONMap) (output JSONMap, deferred *Run, err error) {
	at, _ := action["at"].(string)
	until, err := waitTime(at, run.Context)
	if err != nil {
		return nil, nil, err
	}
	output = JSONMap{"until": until.Format(time.RFC3339)}
	if !until.After(e.now()) {
		return output, nil, nil
	}
	store, ok := e.repo.(waitStore)
	if !ok {
		return nil, nil, fmt.Errorf("workflow repository cannot defer runs")
	}
	deferred, err = store.deferRun(ctx, orgID, run.ID, step.ID, until, output)
	if err != nil {
		return nil, nil, err
	}
	e.requeueAt(ctx, orgID, deferred, until)
	return output, deferred, nil
}

// deferRun implements waitStore: the step goes back to pending with its
// output, the run to pending with next_attempt_at until and its enqueues
// raised (the job queued for until is the one that executes it).
func (r *PGRepository) deferRun(ctx context.Context, orgID, runID, stepID string, until time.Time, output JSONMap) (*Run, error) {
	return r.changeRun(ctx, orgID, runID, func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error) {
		if endedStatus(prev) {
			return nil, ErrRunEnded
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_step SET status='pending', output=$3, error='', updated_at=now()
			WHERE organization_id=$1 AND id=$2`, orgID, stepID, output); err != nil {
			return nil, err
		}
		return scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='pending', next_attempt_at=$3, enqueues=enqueues+1, updated_at=now()
			WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, runID, until))
	})
}
