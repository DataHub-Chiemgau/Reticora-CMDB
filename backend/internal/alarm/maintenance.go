package alarm

import (
	"context"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Suppression in active maintenance windows (ALR-11, MGT-04, WP-1033,
// E-204 option 2). Raise asks the MaintenanceChecker whether the CI of the
// alarm belongs to an active window: a window CI or a CI the window
// certainly affects (topology impact) opens the alarm with
// suppressed_reason maintenance and details.maintenance_window_id; it is
// recorded, but not notified (the notification skips a suppressed alarm,
// WP-987). A CI affected only over a heuristic path gets
// details.maintenance_heuristic = true and is notified as usual. At the end
// of the window (system job maintenance.boundaries) ReleaseMaintenance
// drops the suppression of the alarms still open; with the setting
// notify_after_maintenance (default true) an alarm.opened with
// after_maintenance = true notifies them then.

// Details keys of the suppression.
const (
	DetailsMaintenanceWindow    = "maintenance_window_id"
	DetailsMaintenanceHeuristic = "maintenance_heuristic"
)

// MaintenanceHit is the active window a CI belongs to: WindowID empty is
// none; Heuristic marks a CI reached only over a heuristic path.
type MaintenanceHit struct {
	WindowID  string
	Heuristic bool
}

// MaintenanceChecker is the port to the active maintenance windows
// (maintenance.Active).
type MaintenanceChecker interface {
	MaintenanceOf(ctx context.Context, orgID, ciID string, at time.Time) (MaintenanceHit, error)
}

// WithMaintenance makes Raise suppress the alarms of CIs in active
// maintenance windows.
func (s *Service) WithMaintenance(c MaintenanceChecker) *Service {
	s.maintenance = c
	return s
}

// maintenanceOf applies the active window of the CI of in to its details
// and reports whether the alarm is suppressed. Without checker or CI
// nothing changes. The window must certainly hold the CI at the
// observation (at) and still now: an observation delivered late, after the
// window ended (a buffered collector), is not suppressed, its window was
// already released.
func (s *Service) maintenanceOf(ctx context.Context, orgID string, in *RaiseInput, at time.Time) (bool, error) {
	if s.maintenance == nil || in.CIID == nil || *in.CIID == "" {
		return false, nil
	}
	hit, err := s.maintenance.MaintenanceOf(ctx, orgID, *in.CIID, at)
	if err != nil {
		return false, fmt.Errorf("maintenance windows: %w", err)
	}
	if hit.WindowID != "" && !hit.Heuristic {
		if now := s.clock(); now.Sub(at) > time.Second {
			if hit, err = s.maintenance.MaintenanceOf(ctx, orgID, *in.CIID, now); err != nil {
				return false, fmt.Errorf("maintenance windows: %w", err)
			}
		}
	}
	if hit.WindowID == "" {
		return false, nil
	}
	if hit.Heuristic {
		in.Details[DetailsMaintenanceHeuristic] = true
		return false, nil
	}
	in.Details[DetailsMaintenanceWindow] = hit.WindowID
	return true, nil
}

// keepMaintenanceWindow keeps the window of a suppressed alarm in the
// details that replace its own (an escalation or the opening of a pending
// alarm whose observation carries no window): the release finds the alarm
// by it.
func keepMaintenanceWindow(a *Alarm, u *StateUpdate) {
	if deref(u.SuppressedReason) != SuppressedMaintenance || u.Details == nil {
		return
	}
	if _, ok := u.Details[DetailsMaintenanceWindow]; ok {
		return
	}
	if w, ok := a.Details[DetailsMaintenanceWindow]; ok {
		d := make(map[string]any, len(u.Details)+1)
		for k, v := range u.Details {
			d[k] = v
		}
		d[DetailsMaintenanceWindow] = w
		u.Details = d
	}
}

// ReleaseMaintenance ends the suppression of the alarms of the window that
// are still open (pending, open or acknowledged with suppressed_reason
// maintenance and details.maintenance_window_id of the window). With the
// setting notify_after_maintenance an alarm.opened with after_maintenance =
// true notifies each opened one; without, the suppression only goes. An
// alarm whose CI another active window still certainly holds (overlapping
// windows) stays suppressed and moves to that window, without an event. It
// returns the number of alarms released.
func (s *Service) ReleaseMaintenance(ctx context.Context, orgID, windowID string) (int, error) {
	return s.releaseMaintenance(ctx, orgID, func(a *Alarm) bool {
		return fmt.Sprint(a.Details[DetailsMaintenanceWindow]) == windowID
	})
}

// ReleaseStaleMaintenance releases every alarm that is suppressed for
// maintenance although no active window certainly holds its CI any more:
// a window cancelled or deleted while active, an observation raised during
// a window and stored after its release, an alarm without window in its
// details. The job maintenance.boundaries runs it every minute (WP-1033
// AK4). It returns the number of alarms released.
func (s *Service) ReleaseStaleMaintenance(ctx context.Context, orgID string) (int, error) {
	return s.releaseMaintenance(ctx, orgID, func(*Alarm) bool { return true })
}

// releaseMaintenance decides the suppressed alarms that match at now (see
// ReleaseMaintenance).
func (s *Service) releaseMaintenance(ctx context.Context, orgID string, match func(*Alarm) bool) (int, error) {
	released := 0
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		released = 0
		settings, err := s.store.Settings(ctx, orgID)
		if err != nil {
			return err
		}
		suppressed := true
		f := &ListFilter{States: []string{StatePending, StateOpen, StateAcknowledged}, Suppressed: &suppressed, Limit: 500}
		var ids []string
		for {
			page, err := s.store.List(ctx, orgID, f)
			if err != nil {
				return err
			}
			for i := range page.Items {
				a := &page.Items[i]
				if deref(a.SuppressedReason) == SuppressedMaintenance && match(a) {
					ids = append(ids, a.ID)
				}
			}
			if page.Next == nil {
				break
			}
			f.After = page.Next
		}
		now := s.clock()
		for _, id := range ids {
			a, err := s.store.LockForUpdate(ctx, orgID, id)
			if err != nil {
				return err
			}
			if deref(a.SuppressedReason) != SuppressedMaintenance || a.State == StateResolved {
				continue
			}
			// Another active window still holds the CI: keep it there.
			if s.maintenance != nil && a.CIID != nil && *a.CIID != "" {
				hit, herr := s.maintenance.MaintenanceOf(ctx, orgID, *a.CIID, now)
				if herr != nil {
					return fmt.Errorf("maintenance windows: %w", herr)
				}
				if hit.WindowID != "" && !hit.Heuristic {
					if fmt.Sprint(a.Details[DetailsMaintenanceWindow]) != hit.WindowID {
						u := stateOf(a)
						u.Details = make(map[string]any, len(a.Details)+1)
						for k, v := range a.Details {
							u.Details[k] = v
						}
						u.Details[DetailsMaintenanceWindow] = hit.WindowID
						if _, uerr := s.store.UpdateState(ctx, orgID, id, u); uerr != nil {
							return uerr
						}
					}
					continue
				}
			}
			u := stateOf(a)
			u.SuppressedReason = nil
			updated, err := s.store.UpdateState(ctx, orgID, id, u)
			if err != nil {
				return err
			}
			released++
			if !settings.NotifyAfterMaintenance || updated.State == StatePending || s.sink == nil {
				continue
			}
			data := eventData(updated)
			data.AfterMaintenance = true
			if err := s.sink.Append(ctx, &events.Event{OrgID: orgID, Type: events.AlarmOpened, EntityID: updated.ID, Data: data}); err != nil {
				return err
			}
		}
		return nil
	})
	return released, err
}
