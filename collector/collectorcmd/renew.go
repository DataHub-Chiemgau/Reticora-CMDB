package collectorcmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/enrollment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// Certificate renewal (COL-04, F-026): every NATS session checks the
// collector certificate at its start and then every 24 h. From 60 % of its
// lifetime the collector renews it over the session
// (collector.<org>.<id>.certificate.renew, enrollment.CollectorClient.Renew),
// stores certificate, key and CA bundle atomically in the keystore (0600)
// and runNATS starts a new session with the new pair. A failure is logged
// with its cause and retried after 1 h; the current certificate stays.
const (
	renewCheckInterval  = 24 * time.Hour
	renewRetryInterval  = time.Hour
	renewRequestTimeout = 30 * time.Second
)

// renewCertificate renews the certificate in the keystore at path when it
// is due (now nil is the system clock); renewed reports whether the
// keystore holds a renewed certificate.
func renewCertificate(ctx context.Context, path string, request enrollment.RequestFunc, now func() time.Time) (renewed bool, err error) {
	creds, err := (&keystore.FileStore{Path: path}).Load(ctx)
	if err != nil {
		return false, err
	}
	renewed, err = (&enrollment.CollectorClient{Request: request, Now: now}).Renew(ctx, creds)
	if err != nil || !renewed {
		return false, err
	}
	// The server issued the certificate: a shutdown does not discard it.
	if err = saveCredentials(context.WithoutCancel(ctx), path, creds); err != nil {
		return false, fmt.Errorf("store the renewed certificate: %w", err)
	}
	if cert, cerr := leafCertificate(creds.ClientCertificatePEM); cerr == nil {
		slog.Info("collector certificate renewed", "serial", cert.SerialNumber.Text(16), "not_after", cert.NotAfter)
	}
	return true, nil
}

// saveCredentials replaces the keystore at path atomically: the
// credentials go to a new file (0600) in the same directory, which is
// synced and renamed over the keystore. A crash leaves the former or the
// new credentials, never a mix.
func saveCredentials(ctx context.Context, path string, creds *keystore.Credentials) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // after the rename there is nothing to remove
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = (&keystore.FileStore{Path: name}).Save(ctx, *creds); err != nil {
		return err
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// runRenewal checks at once and then every 24 h, after a failure again
// after 1 h, until a renewal is stored (true: the caller restarts the
// session with it) or ctx ends (false).
func runRenewal(ctx context.Context, renew func(context.Context) (bool, error), after func(time.Duration) <-chan time.Time) bool {
	for {
		wait := renewCheckInterval
		renewed, err := renew(ctx)
		switch {
		case renewed:
			return true
		case ctx.Err() != nil:
			return false
		case err != nil:
			slog.Warn("collector certificate not renewed; the current certificate stays",
				"error", err, "retry_in", renewRetryInterval.String())
			wait = renewRetryInterval
		}
		select {
		case <-ctx.Done():
			return false
		case <-after(wait):
		}
	}
}

// sessionRequest sends renewal requests over the session's connection.
func sessionRequest(nc *nats.Conn) enrollment.RequestFunc {
	return func(ctx context.Context, subject string, data []byte) ([]byte, error) {
		rctx, cancel := context.WithTimeout(ctx, renewRequestTimeout)
		defer cancel()
		msg, err := nc.RequestWithContext(rctx, subject, data)
		if err != nil {
			return nil, err
		}
		return msg.Data, nil
	}
}
