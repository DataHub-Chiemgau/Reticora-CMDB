// Phase-1 acceptance audit, group T4-collector-load – REGRESSION test (must
// pass).
//
// WP-157 (REC-09, LCY-05), WP-159 (TOP-02). High test gap "Scheduler
// registration and the operator switch (on and off)": the operator switch
// RETICORA_OFFLINE_DETECTOR_ENABLED is on by default, "false"/"0" turn it
// off, an unparsable value is refused by Validate (the server does not start
// with it) and keeps the default; the detector's job is ci.offline_detector
// every 5 minutes and the edge cleanup relationship.cleanup every hour.
// Since WP-447 (F-289) the switch is the one of the worker offline_detector
// (RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED) and the former name its
// deprecated alias; whether the switch adds or omits the job is covered by
// TestOfflineDetectorRunsUnderItsWorkerSwitch in cmd/server.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestAuditOfflineDetectorSwitchAndSchedules' ./internal/lifecycle/
package lifecycle_test

import (
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

func TestAuditOfflineDetectorSwitchAndSchedules(t *testing.T) {
	cases := []struct {
		value   string
		want    bool
		invalid bool
	}{
		{"", true, false},
		{"true", true, false},
		{"1", true, false},
		{"false", false, false},
		{"0", false, false},
		{"maybe", true, true},
	}
	for _, tc := range cases {
		t.Run("RETICORA_OFFLINE_DETECTOR_ENABLED="+tc.value, func(t *testing.T) {
			t.Setenv("RETICORA_OFFLINE_DETECTOR_ENABLED", tc.value)
			t.Setenv(config.WorkerEnvKey(config.WorkerOfflineDetector), "")
			cfg := config.Load()
			if got := cfg.WorkerEnabled(config.WorkerOfflineDetector); got != tc.want {
				t.Errorf("offline detector enabled = %v, want %v", got, tc.want)
			}
			err := cfg.Validate(true)
			refused := err != nil && strings.Contains(err.Error(), "RETICORA_OFFLINE_DETECTOR_ENABLED has an invalid value")
			if refused != tc.invalid {
				t.Errorf("Validate = %v, want refusal of the value: %v", err, tc.invalid)
			}
		})
	}

	if s := lifecycle.NewOfflineDetector(nil).Schedule(); lifecycle.OfflineDetectorKind != "ci.offline_detector" || s.Kind != lifecycle.OfflineDetectorKind ||
		s.Spec != "@every 5m" || s.Run == nil {
		t.Errorf("offline detector schedule = %q %q run=%v, want ci.offline_detector @every 5m", s.Kind, s.Spec, s.Run != nil)
	}
	if s := relationship.NewCleanup(nil).Schedule(); relationship.CleanupKind != "relationship.cleanup" || s.Kind != relationship.CleanupKind ||
		s.Spec != "@every 1h" || s.Run == nil {
		t.Errorf("edge cleanup schedule = %q %q run=%v, want relationship.cleanup @every 1h", s.Kind, s.Spec, s.Run != nil)
	}
}
