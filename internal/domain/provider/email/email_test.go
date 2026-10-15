package email_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/invopop/gobl.verify/internal/domain/provider"
	"github.com/invopop/gobl.verify/internal/domain/provider/email"
)

func newProvider(t *testing.T) *email.Provider {
	t.Helper()
	p, err := email.New(email.Config{PublicBaseURL: "https://verify.example"})
	require.NoError(t, err)
	return p
}

func subject() provider.Subject {
	return provider.Subject{
		LegalName:          "Acme Widgets SL",
		Country:            "ES",
		RegistrationNumber: "B12345674",
		TaxCode:            "B12345674",
	}
}

func TestSessionIsPendingUntilConfirmed(t *testing.T) {
	p := newProvider(t)

	sess, err := p.CreateSession(context.Background(), provider.SessionRequest{
		Reference: "acme.example#1",
		Subject:   subject(),
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(sess.URL, "https://verify.example/confirm/"))

	dec, err := p.Decision(context.Background(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, provider.StatusPending, dec.Status)

	token := strings.TrimPrefix(sess.URL, "https://verify.example/confirm/")
	ref, ok := p.Confirm(token)
	require.True(t, ok)
	assert.Equal(t, "acme.example#1", ref, "the reference is what drives the case forward")

	dec, err = p.Decision(context.Background(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, provider.StatusApproved, dec.Status)
}

// The company record this provider returns is the subject's own
// declared detail, and it says so. Reporting it as registry data would
// be a lie the match check would then believe — it would compare the
// party against itself and call that corroboration.
func TestDecisionReportsItsCompanyDataAsApplicantSourced(t *testing.T) {
	p := newProvider(t)
	sess, err := p.CreateSession(context.Background(), provider.SessionRequest{
		Reference: "acme.example#1",
		Subject:   subject(),
	})
	require.NoError(t, err)

	token := strings.TrimPrefix(sess.URL, "https://verify.example/confirm/")
	_, ok := p.Confirm(token)
	require.True(t, ok)

	dec, err := p.Decision(context.Background(), sess.ID)
	require.NoError(t, err)

	require.Len(t, dec.KeyPeople, 1)
	assert.Equal(t, provider.PersonSourceApplicant, dec.KeyPeople[0].Source)

	var registrySourced int
	for _, person := range dec.KeyPeople {
		if person.Source == provider.PersonSourceRegistry {
			registrySourced++
		}
	}
	assert.Zero(t, registrySourced, "this provider consults no registry, so nothing it returns is registry data")
}

func TestConfirmIsIdempotent(t *testing.T) {
	p := newProvider(t)
	sess, err := p.CreateSession(context.Background(), provider.SessionRequest{
		Reference: "acme.example#1",
		Subject:   subject(),
	})
	require.NoError(t, err)
	token := strings.TrimPrefix(sess.URL, "https://verify.example/confirm/")

	first, ok := p.Confirm(token)
	require.True(t, ok)
	second, ok := p.Confirm(token)
	require.True(t, ok)
	assert.Equal(t, first, second)

	dec, err := p.Decision(context.Background(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, provider.StatusApproved, dec.Status)
}

func TestUnknownTokenAndSession(t *testing.T) {
	p := newProvider(t)

	_, ok := p.Confirm("nope")
	assert.False(t, ok)

	_, ok = p.Pending("nope")
	assert.False(t, ok)

	_, err := p.Decision(context.Background(), "no-such-session")
	assert.ErrorIs(t, err, provider.ErrNotFound)
}

// This provider has no callbacks, so the route is never registered for
// it and the method exists only to satisfy the interface.
func TestParseEventIsNotSupported(t *testing.T) {
	p := newProvider(t)
	_, err := p.ParseEvent(nil, nil, provider.Decision{}.DecidedAt)
	assert.Error(t, err)
}

// The requirements are part of this provider's contract, not an
// implementation detail: they are what lets a party with no tax
// identity be verified, and what stops the domain filing this
// provider's echo of the subject as a passed match.
func TestRequiresNothingFromARegistry(t *testing.T) {
	p, err := email.New(email.Config{PublicBaseURL: "https://verify.example"})
	require.NoError(t, err)
	assert.False(t, p.Requires().RegistryFacts,
		"this provider consults no registry and must not ask the party for registry facts")
}
