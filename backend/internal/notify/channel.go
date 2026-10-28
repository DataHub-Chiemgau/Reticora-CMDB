// Package notify is the notification layer of section 41: channels with a
// provider abstraction (NTF-01), e-mail over SMTP (TEC-13), in-app
// notifications and webhooks. A channel's provider configuration is stored
// encrypted.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// Channel kinds (NTF-01); sms and pager get their providers with NTF-02.
const (
	KindEmail   = "email"
	KindInApp   = "inapp"
	KindWebhook = "webhook"
	KindSMS     = "sms"
	KindPager   = "pager"
)

// Providers.
const (
	// ProviderCentralSMTP sends over the installation's SMTP (SaaS) with
	// the organization as sender name.
	ProviderCentralSMTP = "central_smtp"
	// ProviderSMTP sends over the organization's own SMTP (dedicated,
	// on-premises, air-gapped). Its server is held to the egress policy
	// (SEC-08): a relay in the internal network needs
	// RETICORA_EGRESS_ALLOW_PRIVATE=true.
	ProviderSMTP    = "smtp"
	ProviderInApp   = "inapp"
	ProviderWebhook = "webhook"
)

// providerKinds maps each provider to its channel kind.
var providerKinds = map[string]string{
	ProviderCentralSMTP: KindEmail, ProviderSMTP: KindEmail, ProviderInApp: KindInApp, ProviderWebhook: KindWebhook,
}

// Channel is one notification channel of an organization.
type Channel struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	Provider       string         `json:"provider"`
	Config         map[string]any `json:"config,omitempty"`
	Enabled        bool           `json:"enabled"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// ErrValidation marks an invalid channel.
var ErrValidation = errors.New("invalid notification channel")

// Validate checks name, kind, provider and the provider configuration.
func (c *Channel) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	kind, ok := providerKinds[c.Provider]
	if !ok {
		return fmt.Errorf("%w: unknown provider %q", ErrValidation, c.Provider)
	}
	if c.Kind == "" {
		c.Kind = kind
	}
	if c.Kind != kind {
		return fmt.Errorf("%w: provider %s belongs to kind %s", ErrValidation, c.Provider, kind)
	}
	switch c.Provider {
	case ProviderSMTP:
		cfg, err := SMTPConfigFrom(c.Config)
		if err != nil {
			return err
		}
		return cfg.Validate()
	case ProviderCentralSMTP:
		if reply, _ := c.Config["reply_to"].(string); reply != "" {
			if _, err := parseAddress(reply); err != nil {
				return fmt.Errorf("%w: reply_to: %v", ErrValidation, err)
			}
		}
	case ProviderWebhook:
		if u, _ := c.Config["url"].(string); !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return fmt.Errorf("%w: webhook url is required", ErrValidation)
		}
	}
	return nil
}

// CheckEgress checks the destination of the channel against the egress
// policy of the installation when it is stored (SEC-08): the server of an
// organization's own SMTP channel (SMTPConfig.CheckEgress) and the url of a
// webhook channel (egress.Options.ValidateURL, 400 on the field url,
// F-204); the central server is the operator's configuration. The address
// a name resolves to is checked again on every connection.
func (c *Channel) CheckEgress(opts egress.Options) error {
	switch c.Provider {
	case ProviderSMTP:
		cfg, err := SMTPConfigFrom(c.Config)
		if err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		return cfg.CheckEgress(opts)
	case ProviderWebhook:
		u, _ := c.Config["url"].(string)
		if u == "" {
			// Validate refuses the channel without url.
			return nil
		}
		if err := opts.ValidateURL(u); err != nil {
			// Metadata endpoints stay blocked with the setting as well.
			if (egress.Options{AllowPrivate: true}).ValidateURL(u) == nil {
				err = fmt.Errorf("%w; a webhook in the internal network needs %s", err, allowPrivate)
			}
			return &DestinationError{Field: "url", Err: err, Status: http.StatusBadRequest}
		}
	}
	return nil
}

// Recipient is one addressee of a message.
type Recipient struct {
	UserID string `json:"user_id,omitempty"`
	Email  string `json:"email,omitempty"`
	Name   string `json:"name,omitempty"`
}

// Message is what a channel delivers.
type Message struct {
	Category string         `json:"category"`
	Subject  string         `json:"subject"`
	Body     string         `json:"body"`
	To       []Recipient    `json:"to"`
	Data     map[string]any `json:"data,omitempty"`
}

// Delivery is the result of a send.
type Delivery struct {
	ChannelID  string    `json:"channel_id"`
	Provider   string    `json:"provider"`
	ProviderID string    `json:"provider_id,omitempty"`
	Recipients int       `json:"recipients"`
	SentAt     time.Time `json:"sent_at"`
}

// Provider delivers messages of one provider (NTF-01: Send(message) →
// delivery).
type Provider interface {
	Send(ctx context.Context, ch *Channel, msg *Message) (*Delivery, error)
}

// ErrDisabled is returned for a send over a disabled channel.
var ErrDisabled = errors.New("notification channel is disabled")

// Sender routes a message to the provider of its channel.
type Sender struct {
	providers map[string]Provider
}

// NewSender creates a sender with the given providers by name.
func NewSender(providers map[string]Provider) *Sender {
	return &Sender{providers: providers}
}

// Send delivers msg over ch.
func (s *Sender) Send(ctx context.Context, ch *Channel, msg *Message) (*Delivery, error) {
	if !ch.Enabled {
		return nil, ErrDisabled
	}
	p, ok := s.providers[ch.Provider]
	if !ok {
		return nil, fmt.Errorf("notification provider %s is not available", ch.Provider)
	}
	if len(msg.To) == 0 {
		return nil, fmt.Errorf("notification without recipients")
	}
	d, err := p.Send(ctx, ch, msg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ch.Provider, err)
	}
	d.ChannelID, d.Provider = ch.ID, ch.Provider
	return d, nil
}

// Sealer encrypts channel configurations: each one under a data key of its
// own, wrapped with the master key.
type Sealer struct {
	encryptor *crypto.EnvelopeEncryptor
}

// NewSealer creates a sealer on the master key encryptor.
func NewSealer(encryptor *crypto.EnvelopeEncryptor) *Sealer { return &Sealer{encryptor: encryptor} }

// Seal encrypts cfg; an empty configuration is stored as nothing.
func (s *Sealer) Seal(cfg map[string]any) (ciphertext, wrappedDEK []byte, err error) {
	if len(cfg) == 0 {
		return nil, nil, nil
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("encode channel config: %w", err)
	}
	dek, err := crypto.GenerateDEK()
	if err != nil {
		return nil, nil, err
	}
	if ciphertext, err = crypto.Encrypt(dek, plain); err != nil {
		return nil, nil, err
	}
	if wrappedDEK, err = s.encryptor.WrapDEK(dek); err != nil {
		return nil, nil, err
	}
	return ciphertext, wrappedDEK, nil
}

// Open decrypts a sealed configuration.
func (s *Sealer) Open(ciphertext, wrappedDEK []byte) (map[string]any, error) {
	cfg := map[string]any{}
	if len(ciphertext) == 0 {
		return cfg, nil
	}
	dek, err := s.encryptor.UnwrapDEK(wrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("unwrap channel key: %w", err)
	}
	plain, err := crypto.Decrypt(dek, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt channel config: %w", err)
	}
	if err = json.Unmarshal(plain, &cfg); err != nil {
		return nil, fmt.Errorf("decode channel config: %w", err)
	}
	return cfg, nil
}

// Repository stores channels; configurations are returned decrypted.
type Repository interface {
	Create(ctx context.Context, ch *Channel) error
	Get(ctx context.Context, orgID, id string) (*Channel, error)
	List(ctx context.Context, orgID string) ([]Channel, error)
	Update(ctx context.Context, ch *Channel) error
	Delete(ctx context.Context, orgID, id string) error
}

// ErrNotFound is returned for an unknown channel.
var ErrNotFound = errors.New("notification channel not found")

// ErrConflict is returned for a taken channel name.
var ErrConflict = errors.New("notification channel name is taken")

// MemoryRepository keeps channels in memory (tests, --no-db).
type MemoryRepository struct {
	mu       sync.Mutex
	channels map[string]*Channel
	seq      int
}

// NewMemoryRepository creates an empty memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{channels: map[string]*Channel{}}
}

// Create implements Repository.
func (r *MemoryRepository) Create(_ context.Context, ch *Channel) error {
	if err := ch.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.channels {
		if c.OrganizationID == ch.OrganizationID && c.Name == ch.Name {
			return ErrConflict
		}
	}
	r.seq++
	ch.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
	ch.CreatedAt = time.Now().UTC()
	ch.UpdatedAt = ch.CreatedAt
	stored := *ch
	r.channels[ch.ID] = &stored
	return nil
}

// Get implements Repository.
func (r *MemoryRepository) Get(_ context.Context, orgID, id string) (*Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.channels[id]
	if !ok || c.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	out := *c
	return &out, nil
}

// List implements Repository.
func (r *MemoryRepository) List(_ context.Context, orgID string) ([]Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Channel{}
	for _, c := range r.channels {
		if c.OrganizationID == orgID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Update implements Repository.
func (r *MemoryRepository) Update(_ context.Context, ch *Channel) error {
	if err := ch.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.channels[ch.ID]
	if !ok || old.OrganizationID != ch.OrganizationID {
		return ErrNotFound
	}
	ch.CreatedAt, ch.UpdatedAt = old.CreatedAt, time.Now().UTC()
	stored := *ch
	r.channels[ch.ID] = &stored
	return nil
}

// Delete implements Repository.
func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.channels[id]
	if !ok || c.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.channels, id)
	return nil
}
