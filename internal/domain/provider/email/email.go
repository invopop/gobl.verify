// Package email implements the simplest verifier there is: it confirms
// that someone can read the email address the party publishes, and
// records a declaration that they are entitled to act for it.
//
// It is the reference implementation of the Provider seam, and it is
// deliberately the whole of one — read it end to end and you have seen
// every moving part a verifier needs. A real verifier substitutes a
// registry lookup, a document check or a KYB vendor for the single
// confirmation step here; everything around it stays the same.
//
// What it attests is worth stating plainly, because the GOBL Net
// endorsement does not carry a grade: a receiver that requires a
// verified sender treats this exactly as it treats a full KYB check.
// Control of a mailbox is not evidence of who a company is, so this
// provider belongs on a sandbox network and nowhere else.
package email

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/invopop/gobl.verify/internal/domain/provider"
)

// Name identifies this provider.
const Name provider.Name = "email"

// Config holds the provider's settings.
type Config struct {
	// PublicBaseURL is the base of the confirmation links handed to
	// the subject.
	PublicBaseURL string
	// ConfirmPath is the route serving the confirmation page.
	ConfirmPath string
}

// Provider confirms control of a published email address.
//
// Sessions live in memory only as a lookup from confirmation token to
// session id; the durable state is the verification case the domain
// already persists, so a restart loses nothing a subject cannot
// recover by following their link again.
type Provider struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*session
}

// session is one outstanding confirmation.
type session struct {
	id        string
	token     string
	reference string
	subject   provider.Subject
	confirmed bool
	at        time.Time
}

// New builds the provider.
func New(cfg Config) (*Provider, error) {
	if cfg.PublicBaseURL == "" {
		return nil, fmt.Errorf("email: public base url is required")
	}
	if cfg.ConfirmPath == "" {
		cfg.ConfirmPath = "/confirm"
	}
	return &Provider{cfg: cfg, sessions: make(map[string]*session)}, nil
}

// Name identifies the implementation.
func (p *Provider) Name() provider.Name { return Name }

// Requires reports that this provider consults no registry.
//
// It therefore asks nothing of the party beyond the published email
// address the inbox already requires: a sandbox party that has not
// been given a tax identity can still prove it reads its own mail,
// which is the whole of what this check attests. The decision it
// returns echoes the subject's own declaration, so the domain skips
// the match check rather than compare that echo with itself.
func (p *Provider) Requires() provider.Requirements {
	return provider.Requirements{}
}

// CreateSession mints a confirmation link.
//
// Nothing is spent here, which is the one way this provider differs in
// kind from a commercial one. An implementation that bills per check
// must treat this call as non-idempotent — a timeout may still have
// created and charged a session — and must never retry it
// automatically.
func (p *Provider) CreateSession(_ context.Context, req provider.SessionRequest) (*provider.Session, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	s := &session{
		id:        "email-" + token[:12],
		token:     token,
		reference: req.Reference,
		subject:   req.Subject,
	}

	p.mu.Lock()
	p.sessions[token] = s
	p.mu.Unlock()

	return &provider.Session{
		ID:  s.id,
		URL: strings.TrimSuffix(p.cfg.PublicBaseURL, "/") + p.cfg.ConfirmPath + "/" + token,
	}, nil
}

// Decision reports whether the link has been followed.
//
// The company record it returns is the subject's own declared detail,
// marked as applicant-sourced, because that is exactly what it is:
// this provider consults no registry and confirms nothing about the
// company. Reporting it as registry data would be a lie the match
// check would then believe.
func (p *Provider) Decision(_ context.Context, sessionID string) (*provider.Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, s := range p.sessions {
		if s.id != sessionID {
			continue
		}
		dec := &provider.Decision{
			SessionID: sessionID,
			Status:    provider.StatusPending,
		}
		if s.confirmed {
			dec.Status = provider.StatusApproved
			dec.DecidedAt = s.at
			dec.Company = provider.CompanyRecord{
				LegalName:          s.subject.LegalName,
				Country:            s.subject.Country,
				RegistrationNumber: s.subject.RegistrationNumber,
				TaxCode:            s.subject.TaxCode,
			}
			dec.KeyPeople = []provider.Person{{
				Source: provider.PersonSourceApplicant,
			}}
		}
		return dec, nil
	}
	return nil, fmt.Errorf("%w: %s", provider.ErrNotFound, sessionID)
}

// ParseEvent is unused: this provider has no callbacks to verify. A
// vendor implementation does its signature checking here, so that the
// HTTP layer never learns how any particular vendor signs.
func (p *Provider) ParseEvent(_ []byte, _ http.Header, _ time.Time) (*provider.Event, error) {
	return nil, fmt.Errorf("email: this provider sends no callbacks")
}

// Confirm records that the link behind token was followed, and
// reports the session's reference so the caller can drive the case
// forward.
//
// It is idempotent: following the link again re-reports the same
// reference without moving the confirmation time, so a subject who
// reloads the page — or retries after the endorsement step failed —
// does not restart anything.
func (p *Provider) Confirm(token string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[token]
	if !ok {
		return "", false
	}
	if !s.confirmed {
		s.confirmed = true
		s.at = time.Now().UTC()
	}
	return s.reference, true
}

// Pending reports whether a token is outstanding, for the confirmation
// page to render before anything is accepted.
func (p *Provider) Pending(token string) (*provider.Subject, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[token]
	if !ok {
		return nil, false
	}
	subject := s.subject
	return &subject, true
}

// newToken mints an unguessable confirmation token.
func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("email: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
