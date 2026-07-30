package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Delivery captures one webhook delivery attempt.
type Delivery struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	SubscriptionID string    `json:"subscription_id"`
	Event          string    `json:"event"`
	URL            string    `json:"url"`
	Attempt        int       `json:"attempt"`
	StatusCode     int       `json:"status_code,omitempty"`
	Success        bool      `json:"success"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type deliveryJob struct {
	deliveryID string
	orgID      string
	event      string
	payload    []byte
	sub        Subscription
}

// Dispatcher performs background webhook deliveries.
type Dispatcher struct {
	repo       Repository
	client     *http.Client
	queue      chan deliveryJob
	wg         sync.WaitGroup
	seq        uint64
	mu         sync.RWMutex
	deliveries []Delivery
}

// NewDispatcher creates a dispatcher with a background worker.
func NewDispatcher(repo Repository, client *http.Client) *Dispatcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	d := &Dispatcher{
		repo:   repo,
		client: client,
		queue:  make(chan deliveryJob, 128),
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for job := range d.queue {
			d.deliver(job)
		}
	}()
	return d
}

// Dispatch queues webhook delivery jobs for matching subscriptions.
func (d *Dispatcher) Dispatch(orgID, event string, payload any) {
	if d == nil || d.repo == nil {
		return
	}

	subs, err := d.repo.ListByEvent(orgID, event)
	if err != nil {
		slog.Error("list webhook subscriptions failed", "error", err, "organization_id", orgID, "event", event)
		return
	}
	if len(subs) == 0 {
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("marshal webhook payload failed", "error", err, "organization_id", orgID, "event", event)
		return
	}

	for _, sub := range subs {
		job := deliveryJob{
			deliveryID: d.nextID(),
			orgID:      orgID,
			event:      event,
			payload:    body,
			sub:        sub,
		}
		select {
		case d.queue <- job:
		default:
			go func(j deliveryJob) { d.queue <- j }(job)
		}
	}
}

// DeliverOnce performs a single synchronous delivery attempt against the given
// subscription and returns the recorded attempt. It is used by the webhook test
// endpoint, where the caller wants immediate feedback instead of a queued retry
// sequence.
func (d *Dispatcher) DeliverOnce(sub Subscription, event string, payload any) (Delivery, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Delivery{}, fmt.Errorf("marshal webhook payload: %w", err)
	}

	job := deliveryJob{
		deliveryID: d.nextID(),
		orgID:      sub.OrganizationID,
		event:      event,
		payload:    body,
		sub:        sub,
	}

	statusCode, sendErr := d.send(job)
	d.recordAttempt(job, 1, statusCode, sendErr)

	deliveries := d.Deliveries()
	return deliveries[len(deliveries)-1], nil
}

// Deliveries returns a snapshot of recorded delivery attempts.
func (d *Dispatcher) Deliveries() []Delivery {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Delivery, len(d.deliveries))
	copy(out, d.deliveries)
	return out
}

// Shutdown waits for the background worker to finish processing queued jobs.
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	if d == nil {
		return nil
	}
	close(d.queue)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.wg.Wait()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (d *Dispatcher) deliver(job deliveryJob) {
	backoffs := []time.Duration{0, time.Second, 2 * time.Second}
	for attempt := 1; attempt <= 3; attempt++ {
		if backoffs[attempt-1] > 0 {
			time.Sleep(backoffs[attempt-1])
		}

		statusCode, err := d.send(job)
		d.recordAttempt(job, attempt, statusCode, err)
		if err == nil && statusCode >= 200 && statusCode < 300 {
			return
		}
	}
}

func (d *Dispatcher) send(job deliveryJob) (int, error) {
	req, err := http.NewRequest(http.MethodPost, job.sub.URL, bytes.NewReader(job.payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Event", job.event)
	req.Header.Set("X-Webhook-ID", job.deliveryID)
	req.Header.Set("X-Webhook-Signature", signPayload(job.sub.Secret, job.payload))
	for key, value := range job.sub.Headers {
		req.Header.Set(key, value)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("unexpected status %d", resp.StatusCode)
}

func (d *Dispatcher) recordAttempt(job deliveryJob, attempt, statusCode int, err error) {
	delivery := Delivery{
		ID:             job.deliveryID,
		OrganizationID: job.orgID,
		SubscriptionID: job.sub.ID,
		Event:          job.event,
		URL:            job.sub.URL,
		Attempt:        attempt,
		StatusCode:     statusCode,
		Success:        err == nil && statusCode >= 200 && statusCode < 300,
		CreatedAt:      time.Now().UTC(),
	}
	if err != nil {
		delivery.Error = err.Error()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.deliveries = append(d.deliveries, delivery)
}

func (d *Dispatcher) nextID() string {
	seq := atomic.AddUint64(&d.seq, 1)
	return fmt.Sprintf("wh_%020d", seq)
}

func signPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
