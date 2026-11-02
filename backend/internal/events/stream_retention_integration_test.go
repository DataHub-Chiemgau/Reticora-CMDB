package events_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// TestPublisherSetupStreamRetention covers EVT-01 and NFR-06 (WP-548,
// F-321): Setup gives the stream RETICORA_EVENTS the configured retention
// (RETICORA_EVENTS_MAX_AGE, RETICORA_EVENTS_MAX_BYTES, see cmd/server),
// also when the stream already exists with another one; the outbox keeps
// published data as long as the stream (KeepPublished follows MaxAge).
// Without settings the publisher keeps 30 days and 10 GB.
//
// It runs only against NATS with JetStream (TEST_NATS_URL). The stream is
// shared with the other tests: it is set back to the defaults at the end.
func TestPublisherSetupStreamRetention(t *testing.T) {
	natsURL := os.Getenv("TEST_NATS_URL")
	if natsURL == "" {
		t.Skip("TEST_NATS_URL not set; skipping NATS integration test")
	}
	bg := context.Background()
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	// Closed after the cleanup below has restored the defaults.
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	streamConfig := func() jetstream.StreamConfig {
		t.Helper()
		stream, serr := js.Stream(bg, events.StreamName)
		if serr != nil {
			t.Fatal(serr)
		}
		info, serr := stream.Info(bg)
		if serr != nil {
			t.Fatal(serr)
		}
		return info.Config
	}

	defaults := events.NewPublisher(nil, js)
	if defaults.MaxAge != 30*24*time.Hour || defaults.MaxBytes != 10<<30 || defaults.KeepPublished() != 30*24*time.Hour {
		t.Errorf("default retention: %v, %d bytes, outbox %v; want 30 days, 10 GB, 30 days",
			defaults.MaxAge, defaults.MaxBytes, defaults.KeepPublished())
	}
	if err = defaults.Setup(bg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := events.NewPublisher(nil, js).Setup(context.Background()); cerr != nil {
			t.Errorf("restore the default stream retention: %v", cerr)
		}
	})
	if c := streamConfig(); c.MaxAge != 30*24*time.Hour || c.MaxBytes != 10<<30 {
		t.Errorf("stream with the defaults: max age %v, max bytes %d", c.MaxAge, c.MaxBytes)
	}

	// The existing stream is updated to the configured retention.
	configured := events.NewPublisher(nil, js)
	configured.MaxAge, configured.MaxBytes = 45*24*time.Hour, 5<<30
	if err = configured.Setup(bg); err != nil {
		t.Fatal(err)
	}
	c := streamConfig()
	if c.MaxAge != 45*24*time.Hour || c.MaxBytes != 5<<30 {
		t.Errorf("stream with 45 days and 5 GB configured: max age %v, max bytes %d", c.MaxAge, c.MaxBytes)
	}
	if c.Subjects[0] != events.SubjectPrefix+">" || c.Storage != jetstream.FileStorage || c.Duplicates != 10*time.Minute {
		t.Errorf("stream settings besides the retention changed: %+v", c)
	}
	if keep := configured.KeepPublished(); keep != 45*24*time.Hour {
		t.Errorf("outbox keeps published data %v, want the stream's 45 days", keep)
	}
}
