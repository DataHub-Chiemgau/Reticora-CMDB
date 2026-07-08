package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultNATSStream     = "RETICORA_EVENTS"
	defaultNATSPort       = "4222"
	maxBufferedNATSEvents = 1000
	natsConnectTimeout    = 2 * time.Second
)

// Conn abstracts the publish/close behavior of a NATS connection.
type Conn interface {
	Publish(subject string, data []byte) error
	Close()
}

// NATSPublisher publishes events to NATS JetStream.
// Stream: RETICORA_EVENTS, Subjects: events.<org_id>.<event_type>
type NATSPublisher struct {
	url       string
	stream    string
	connected bool

	mu     sync.Mutex
	conn   Conn
	buffer []bufferedEvent
}

type bufferedEvent struct {
	subject string
	data    []byte
}

// Envelope wraps an event for transport on NATS.
type Envelope struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload"`
}

// NewNATSPublisher creates a publisher connected to the given NATS URL.
// The stream RETICORA_EVENTS is created/ensured on first publish.
func NewNATSPublisher(natsURL string) (*NATSPublisher, error) {
	if natsURL == "" {
		return nil, fmt.Errorf("events: NATS URL is required")
	}

	p := &NATSPublisher{
		url:    natsURL,
		stream: defaultNATSStream,
		buffer: make([]bufferedEvent, 0, maxBufferedNATSEvents),
	}

	slog.Info("NATS publisher initialized", "url", natsURL, "stream", p.stream)
	return p, nil
}

// SetConn injects a concrete NATS connection implementation.
func (p *NATSPublisher) SetConn(conn Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil && p.conn != conn {
		p.conn.Close()
	}

	p.conn = conn
	p.connected = false
}

// Publish sends an event to NATS JetStream.
// Subject format: events.<org_id>.<event_type>
func (p *NATSPublisher) Publish(ctx context.Context, event Event) error {
	subject := event.Subject
	if subject == "" {
		subject = fmt.Sprintf("events.%s.%s", event.OrgID, event.Type)
	}

	envelope := Envelope{
		OrgID:     event.OrgID,
		Type:      event.Type,
		Timestamp: time.Now().UTC(),
		Payload:   event.Payload,
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("events: marshal envelope for subject %q: %w", subject, err)
	}

	if err := p.ensureConnected(ctx); err != nil {
		return err
	}

	slog.Debug("event serialized for publish",
		"subject", subject,
		"stream", p.stream,
		"payload", string(data),
	)

	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()

	if conn == nil {
		p.bufferEvent(subject, data)
		return nil
	}

	if err := conn.Publish(subject, data); err != nil {
		p.mu.Lock()
		p.connected = false
		p.mu.Unlock()
		return fmt.Errorf("events: publish to subject %q: %w", subject, err)
	}

	return nil
}

func (p *NATSPublisher) ensureConnected(ctx context.Context) error {
	p.mu.Lock()
	if p.connected {
		p.mu.Unlock()
		return nil
	}

	conn := p.conn
	p.mu.Unlock()

	if err := p.verifyReachability(ctx); err != nil {
		if conn != nil {
			return err
		}

		slog.Warn("NATS reachability check failed; buffering event locally",
			"url", p.url,
			"error", err,
		)
	}

	p.mu.Lock()
	p.connected = true
	p.mu.Unlock()
	return nil
}

func (p *NATSPublisher) verifyReachability(ctx context.Context) error {
	address, err := natsAddress(p.url)
	if err != nil {
		return fmt.Errorf("events: parse NATS URL %q: %w", p.url, err)
	}

	dialCtx := ctx
	if dialCtx == nil {
		dialCtx = context.Background()
	}

	var cancel context.CancelFunc
	if _, ok := dialCtx.Deadline(); !ok {
		dialCtx, cancel = context.WithTimeout(dialCtx, natsConnectTimeout)
		defer cancel()
	}

	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
	if err != nil {
		return fmt.Errorf("events: dial NATS %s: %w", address, err)
	}

	if err := conn.Close(); err != nil {
		slog.Debug("NATS dial verification close failed", "url", p.url, "error", err)
	}

	slog.Debug("NATS reachability verified", "url", p.url, "stream", p.stream)
	return nil
}

func natsAddress(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("empty NATS URL")
	}

	if !strings.Contains(rawURL, "://") {
		if host, port, err := net.SplitHostPort(rawURL); err == nil {
			return net.JoinHostPort(host, port), nil
		}
		return net.JoinHostPort(rawURL, defaultNATSPort), nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	if _, _, err := net.SplitHostPort(parsed.Host); err == nil {
		return parsed.Host, nil
	}

	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("missing host")
	}
	port := parsed.Port()
	if port == "" {
		port = defaultNATSPort
	}
	return net.JoinHostPort(host, port), nil
}

func (p *NATSPublisher) bufferEvent(subject string, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.buffer) >= maxBufferedNATSEvents {
		p.buffer = append(p.buffer[1:], bufferedEvent{subject: subject, data: append([]byte(nil), data...)})
		slog.Warn("NATS buffer full; dropped oldest event",
			"subject", subject,
			"buffer_size", len(p.buffer),
		)
		return
	}

	p.buffer = append(p.buffer, bufferedEvent{subject: subject, data: append([]byte(nil), data...)})
	slog.Debug("event buffered locally",
		"subject", subject,
		"buffer_size", len(p.buffer),
	)
}

// Close disconnects from NATS.
func (p *NATSPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil {
		p.conn.Close()
	}
	p.connected = false

	slog.Info("NATS publisher closed", "stream", p.stream, "buffered_events", len(p.buffer))
	return nil
}
