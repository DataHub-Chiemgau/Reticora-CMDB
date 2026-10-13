package collectorcmd

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/update"
)

// Version is the collector's release version, set by the release build
// (-ldflags "-X github.com/DataHub-Chiemgau/Reticora-CMDB/collector/collectorcmd.Version=v1.2.3").
var Version = "dev"

// updateArtifact is the binary name of the release manifests.
const updateArtifact = "reticora-collector"

// newUpdateChecker builds the signed auto-updater (COL-06): releases of
// the subscribed channel from RETICORA_UPDATE_URL, installed only after
// the cosign signature verified and the new binary started with "version".
func newUpdateChecker(cfg *collectorConfig) *update.Checker {
	return &update.Checker{
		Source:    &update.HTTPSource{BaseURL: cfg.UpdateURL, Channel: cfg.UpdateChannel, Artifact: updateArtifact},
		Verifier:  update.CosignVerifier{},
		Channel:   cfg.UpdateChannel,
		ProbeArgs: []string{"version"},
	}
}

// updateChannelFromEnv returns the configured channel; an unknown channel
// disables updates (fail closed) and is logged.
func updateChannelFromEnv(value string) (string, bool) {
	ch := strings.ToLower(strings.TrimSpace(value))
	if ch == "" {
		return update.ChannelStable, true
	}
	if !update.ValidChannel(ch) {
		slog.Error("invalid RETICORA_UPDATE_CHANNEL (stable or beta); auto-updates are disabled", "channel", value)
		return ch, false
	}
	return ch, true
}

// startupUpdate counts this start against a pending update and returns the
// function that confirms it after the first successful heartbeat. After
// too many unconfirmed starts the previous binary is restored and
// update.ErrRolledBack returned: the process must exit so the service
// manager starts the restored binary.
func startupUpdate(execPath string) (confirm func(), err error) {
	pending, err := update.CheckPending(execPath, update.MaxStartAttempts)
	if err != nil {
		if errors.Is(err, update.ErrRolledBack) {
			slog.Error("collector update rolled back after failed starts", "version", pending, "running", Version)
		}
		return func() {}, err
	}
	if pending == "" {
		return func() {}, nil
	}
	slog.Info("collector update awaiting confirmation", "version", pending)
	var once sync.Once
	return func() {
		once.Do(func() {
			if cerr := update.Confirm(execPath); cerr != nil {
				slog.Error("confirm collector update", "version", pending, "error", cerr)
				return
			}
			slog.Info("collector update confirmed", "version", pending)
		})
	}, nil
}

// runUpdateLoop checks for a newer release once confirmed is closed (the
// running binary works) and then every cfg.UpdateInterval. After an
// update was installed it reports the version on restart; the collector
// then exits and the service manager starts the new binary.
func runUpdateLoop(ctx context.Context, interval time.Duration, u update.Updater, confirmed <-chan struct{}, restart chan<- string) {
	select {
	case <-ctx.Done():
		return
	case <-confirmed:
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if version := checkAndApply(ctx, u); version != "" {
			select {
			case restart <- version:
			case <-ctx.Done():
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// checkAndApply installs a newer release and returns its version ("" when
// none was installed).
func checkAndApply(ctx context.Context, u update.Updater) string {
	rel, err := u.Check(ctx, Version)
	if err != nil {
		slog.Warn("collector update check failed", "error", err)
		return ""
	}
	if rel == nil {
		return ""
	}
	slog.Info("installing collector update", "from", Version, "to", rel.Version, "channel", rel.Channel)
	if err = u.Apply(ctx, *rel); err != nil {
		slog.Error("collector update refused", "version", rel.Version, "error", err)
		return ""
	}
	return rel.Version
}

// executablePath is the running binary (a test seam).
var executablePath = os.Executable
