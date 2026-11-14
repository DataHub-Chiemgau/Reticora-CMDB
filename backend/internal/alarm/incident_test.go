package alarm_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
)

// The incident model on the MemoryStore (ALR-03, ALR-09, WP-1027): merge,
// the sticky manual root, the append-only log and the cause fields. The
// PostgreSQL cases and the parity of both stores are in
// incident_integration_test.go.

// incidentEnv is a store with one organization's context.
type incidentEnv struct {
	store alarm.Store
	ctx   context.Context
	orgID string
	ci    func(name string) string
}

// openAlarms raises n alarms of distinct series on the store.
func openAlarms(t *testing.T, e *incidentEnv, prefix string, n int) []string {
	t.Helper()
	var ids []string
	for i := range n {
		ci := e.ci(fmt.Sprintf("%s-%d", prefix, i))
		a, _, err := e.store.UpsertOpen(e.ctx, e.orgID, &alarm.UpsertInput{SourceKind: alarm.SourceCheck, SourceID: ruleID,
			SeriesKey: fmt.Sprintf("%s-%d", prefix, i), CIID: &ci, Severity: alarm.SeverityCritical, State: alarm.StateOpen,
			Summary: "down"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	return ids
}

// newIncident opens an incident with root ids[0] and symptoms ids[1:].
func newIncident(t *testing.T, e *incidentEnv, ids []string) *alarm.Incident {
	t.Helper()
	inc, created, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{CauseClass: alarm.CauseTopology,
		Confidence: 80, Reasons: []string{"parent down", "children unreachable"}})
	if err != nil || !created {
		t.Fatalf("open incident: %+v, %v", inc, err)
	}
	if _, err = e.store.AttachAlarms(e.ctx, e.orgID, inc.ID, ids[:1], alarm.RoleRoot); err != nil {
		t.Fatal(err)
	}
	if len(ids) > 1 {
		if _, err = e.store.AttachAlarms(e.ctx, e.orgID, inc.ID, ids[1:], alarm.RoleSymptom); err != nil {
			t.Fatal(err)
		}
	}
	inc, err = e.store.GetIncident(e.ctx, e.orgID, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return inc
}

func memIncidentEnv() *incidentEnv {
	e := memoryEnv()
	return &incidentEnv{store: e.store, ctx: e.ctx, orgID: e.orgID, ci: e.ci}
}

// alarmsOf returns the alarms of an incident.
func alarmsOf(t *testing.T, e *incidentEnv, incidentID string) []alarm.Alarm {
	t.Helper()
	page, err := e.store.List(e.ctx, e.orgID, &alarm.ListFilter{IncidentID: incidentID, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func reasonsOf(t *testing.T, e *incidentEnv, incidentID string) []string {
	t.Helper()
	trs, err := e.store.IncidentTransitions(e.ctx, e.orgID, incidentID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tr := range trs {
		out = append(out, tr.Reason)
	}
	return out
}

func checkMergeMovesAlarms(t *testing.T, e *incidentEnv) {
	t.Helper()
	keepAlarms, absorbAlarms := openAlarms(t, e, "keep", 3), openAlarms(t, e, "absorb", 3)
	keep, absorb := newIncident(t, e, keepAlarms), newIncident(t, e, absorbAlarms)
	merged, err := e.store.MergeIncidents(e.ctx, e.orgID, keep.ID, absorb.ID, absorbAlarms[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := alarmsOf(t, e, keep.ID); len(got) != 6 {
		t.Errorf("%d alarms at keep after the merge, want 6", len(got))
	}
	if got := alarmsOf(t, e, absorb.ID); len(got) != 0 {
		t.Errorf("%d alarms left at absorb, want none", len(got))
	}
	gone, err := e.store.GetIncident(e.ctx, e.orgID, absorb.ID)
	if err != nil || gone.State != alarm.IncidentResolved || gone.ResolvedAt == nil {
		t.Errorf("absorb after the merge: %+v, %v; want resolved", gone, err)
	}
	if r := reasonsOf(t, e, absorb.ID); r[len(r)-1] != alarm.IncidentReasonMergedInto {
		t.Errorf("absorb reasons %v, want merged_into last", r)
	}
	// absorb keeps no root alarm: its former root belongs to keep now and
	// stands in the details of merged_into.
	if gone != nil && gone.RootAlarmID != nil {
		t.Errorf("absorb root %s after the merge, want none", *gone.RootAlarmID)
	}
	if trs, _ := e.store.IncidentTransitions(e.ctx, e.orgID, absorb.ID); trs[len(trs)-1].Details["root_alarm_id"] != absorbAlarms[0] {
		t.Errorf("merged_into details %v, want the former root %s", trs[len(trs)-1].Details, absorbAlarms[0])
	}
	// Without manual root the caller's root wins; every other alarm is a
	// symptom.
	if merged.RootAlarmID == nil || *merged.RootAlarmID != absorbAlarms[0] {
		t.Errorf("root after the merge %v, want %s", merged.RootAlarmID, absorbAlarms[0])
	}
	for _, a := range alarmsOf(t, e, keep.ID) {
		symptom := a.SuppressedReason != nil && *a.SuppressedReason == alarm.SuppressedSymptom
		if symptom == (a.ID == absorbAlarms[0]) {
			t.Errorf("alarm %s: suppressed %v (root %v)", a.ID, a.SuppressedReason, a.ID == absorbAlarms[0])
		}
	}
	// A resolved incident takes no further merge.
	if _, err := e.store.MergeIncidents(e.ctx, e.orgID, keep.ID, absorb.ID, ""); !errors.Is(err, alarm.ErrIncidentResolved) {
		t.Errorf("merge of a resolved incident: %v, want ErrIncidentResolved", err)
	}
}

func TestIncidentMergeMovesAlarms(t *testing.T) {
	checkMergeMovesAlarms(t, memIncidentEnv())
}

// checkAlarmStaysInItsIncident: AttachAlarms, SetRoot and the root of a
// merge refuse an alarm of another incident with ErrAlarmInOtherIncident;
// the other incident keeps its alarm, its root and its log.
func checkAlarmStaysInItsIncident(t *testing.T, e *incidentEnv) {
	t.Helper()
	ownIDs, otherIDs := openAlarms(t, e, "own", 2), openAlarms(t, e, "other", 2)
	own, other := newIncident(t, e, ownIDs), newIncident(t, e, otherIDs)
	third := newIncident(t, e, openAlarms(t, e, "third", 1))
	before := reasonsOf(t, e, other.ID)
	if _, err := e.store.SetRoot(e.ctx, e.orgID, own.ID, otherIDs[0], true); !errors.Is(err, alarm.ErrAlarmInOtherIncident) {
		t.Errorf("set root to the root of another incident: %v, want ErrAlarmInOtherIncident", err)
	}
	if _, err := e.store.AttachAlarms(e.ctx, e.orgID, own.ID, otherIDs[1:], alarm.RoleSymptom); !errors.Is(err, alarm.ErrAlarmInOtherIncident) {
		t.Errorf("attach a symptom of another incident: %v, want ErrAlarmInOtherIncident", err)
	}
	if _, err := e.store.MergeIncidents(e.ctx, e.orgID, own.ID, third.ID, otherIDs[0]); !errors.Is(err, alarm.ErrAlarmInOtherIncident) {
		t.Errorf("merge with the root of another incident: %v, want ErrAlarmInOtherIncident", err)
	}
	got, err := e.store.GetIncident(e.ctx, e.orgID, other.ID)
	if err != nil || got.RootAlarmID == nil || *got.RootAlarmID != otherIDs[0] {
		t.Errorf("other incident after the refused changes: %+v, %v; want root %s", got, err, otherIDs[0])
	}
	if n := len(alarmsOf(t, e, other.ID)); n != 2 {
		t.Errorf("%d alarms at the other incident, want 2", n)
	}
	if after := reasonsOf(t, e, other.ID); !slices.Equal(after, before) {
		t.Errorf("log of the other incident %v, want unchanged %v", after, before)
	}
	if got, _ := e.store.GetIncident(e.ctx, e.orgID, third.ID); got == nil || got.State != alarm.IncidentOpen {
		t.Errorf("third incident after the refused merge: %+v, want open", got)
	}
	// Re-attaching an alarm of the same incident stays allowed.
	if _, err := e.store.SetRoot(e.ctx, e.orgID, own.ID, ownIDs[1], true); err != nil {
		t.Errorf("set root to an own symptom: %v", err)
	}
}

func TestIncidentAttachRefusesAlarmOfOtherIncident(t *testing.T) {
	checkAlarmStaysInItsIncident(t, memIncidentEnv())
}

func checkManualRootSticky(t *testing.T, e *incidentEnv) {
	t.Helper()
	ids := openAlarms(t, e, "manual", 3)
	inc := newIncident(t, e, ids)
	inc, err := e.store.SetRoot(e.ctx, e.orgID, inc.ID, ids[1], true)
	if err != nil || !inc.ManualRoot || *inc.RootAlarmID != ids[1] {
		t.Fatalf("manual root: %+v, %v", inc, err)
	}
	before := len(reasonsOf(t, e, inc.ID))
	if _, err = e.store.SetRoot(e.ctx, e.orgID, inc.ID, ids[2], false); !errors.Is(err, alarm.ErrManualRoot) {
		t.Errorf("automatic root after a manual one: %v, want ErrManualRoot", err)
	}
	if _, err = e.store.AttachAlarms(e.ctx, e.orgID, inc.ID, ids[2:], alarm.RoleRoot); !errors.Is(err, alarm.ErrManualRoot) {
		t.Errorf("automatic root attach after a manual one: %v, want ErrManualRoot", err)
	}
	if after := len(reasonsOf(t, e, inc.ID)); after != before {
		t.Errorf("refused root changes wrote %d transitions", after-before)
	}
	other := newIncident(t, e, openAlarms(t, e, "manual-other", 2))
	merged, err := e.store.MergeIncidents(e.ctx, e.orgID, inc.ID, other.ID, *other.RootAlarmID)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.ManualRoot || *merged.RootAlarmID != ids[1] {
		t.Errorf("merge into a manual root: root %v, manual %v; want %s kept", merged.RootAlarmID, merged.ManualRoot, ids[1])
	}
	// A manual change of a manual root is allowed.
	if again, err := e.store.SetRoot(e.ctx, e.orgID, inc.ID, ids[0], true); err != nil || *again.RootAlarmID != ids[0] {
		t.Errorf("manual root change: %+v, %v", again, err)
	}
}

func TestIncidentManualRootSticky(t *testing.T) {
	checkManualRootSticky(t, memIncidentEnv())
}

// TestIncidentTransitionsAppendOnly: every change appends to the log of
// the incident, earlier entries stay unchanged, and a copy handed out
// does not change the log.
func TestIncidentTransitionsAppendOnly(t *testing.T) {
	e := memIncidentEnv()
	ids := openAlarms(t, e, "log", 2)
	inc := newIncident(t, e, ids)
	first, err := e.store.IncidentTransitions(e.ctx, e.orgID, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{alarm.IncidentReasonOpened, alarm.IncidentReasonRootChanged, alarm.IncidentReasonAlarmsAttached}; !slices.Equal(reasonsOf(t, e, inc.ID), want) {
		t.Fatalf("reasons %v, want %v", reasonsOf(t, e, inc.ID), want)
	}
	first[0].Reason = "rewritten"
	actor := "00000000-0000-4000-8000-0000000000f1"
	if _, _, err = e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{ID: inc.ID, CauseClass: alarm.CauseCollectorUnreachable,
		Confidence: 55, Reasons: []string{"collector silent"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.store.TransitionIncident(e.ctx, e.orgID, inc.ID, alarm.IncidentAcknowledged, &actor); err != nil {
		t.Fatal(err)
	}
	if _, err = e.store.TransitionIncident(e.ctx, e.orgID, inc.ID, alarm.IncidentResolved, nil); err != nil {
		t.Fatal(err)
	}
	all, _ := e.store.IncidentTransitions(e.ctx, e.orgID, inc.ID)
	want := []string{alarm.IncidentReasonOpened, alarm.IncidentReasonRootChanged, alarm.IncidentReasonAlarmsAttached,
		alarm.IncidentReasonUpdated, alarm.IncidentReasonAcknowledged, alarm.IncidentReasonResolved}
	if got := reasonsOf(t, e, inc.ID); !slices.Equal(got, want) {
		t.Errorf("reasons %v, want %v", got, want)
	}
	for i := range 3 {
		if all[i].ID != first[i].ID || (i == 0 && all[i].Reason != alarm.IncidentReasonOpened) {
			t.Errorf("entry %d changed: %+v", i, all[i])
		}
	}
	if all[4].ActorID == nil || *all[4].ActorID != actor {
		t.Errorf("ack entry without actor: %+v", all[4])
	}
	// The cause fields of ALR-09.
	got, _ := e.store.GetIncident(e.ctx, e.orgID, inc.ID)
	if got.CauseClass != alarm.CauseCollectorUnreachable || got.Confidence != 55 || !slices.Equal(got.Reasons, []string{"collector silent"}) {
		t.Errorf("cause %s, confidence %d, reasons %v", got.CauseClass, got.Confidence, got.Reasons)
	}
	// A resolved incident takes no further change, and invalid input none.
	if _, _, err = e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{ID: inc.ID}); !errors.Is(err, alarm.ErrIncidentResolved) {
		t.Errorf("update of a resolved incident: %v", err)
	}
	for _, bad := range []alarm.IncidentInput{{Confidence: 101}, {Confidence: -1}, {CauseClass: "gremlins"}} {
		if _, _, err = e.store.UpsertIncident(e.ctx, e.orgID, &bad); err == nil {
			t.Errorf("incident %+v accepted", bad)
		}
	}
	if _, err = e.store.TransitionIncident(e.ctx, e.orgID, inc.ID, alarm.IncidentOpen, nil); !errors.Is(err, alarm.ErrInvalidTransition) {
		t.Errorf("reopen of a resolved incident: %v, want ErrInvalidTransition", err)
	}
}
