package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/invopop/gobl"
	"github.com/invopop/gobl/head"
	goblnet "github.com/invopop/gobl/net"
	"github.com/invopop/gobl/org"

	"github.com/invopop/gobl.verify/internal/domain/models"
	"github.com/invopop/gobl.verify/internal/domain/provider"
	"github.com/invopop/gobl.verify/internal/interfaces/web"
)

// The countersignature is the product. Its shape is what every
// receiver on the network relies on, so it is asserted field by field.
func TestApprovalProducesTheRightCountersignature(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	before := f.record()

	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	sent := f.endorsements()
	require.Len(t, sent, 1)

	env := sent[0]
	assert.Equal(t, before.EnvelopeUUID, env.Head.UUID, "the envelope identity must not change")
	require.NotNil(t, env.Head.Digest)
	assert.Equal(t, before.EnvelopeDigest,
		string(env.Head.Digest.Algorithm)+":"+env.Head.Digest.Value,
		"we countersign exactly the bytes we checked")

	var ours int
	for _, sig := range env.Signatures {
		p, err := head.SignedPayload(sig)
		require.NoError(t, err)
		if p.Iss != string(f.verifier.Address()) {
			continue
		}
		ours++
		assert.Equal(t, string(f.subAddr), p.Aud, "the countersignature is bound to the subject")
		require.NotZero(t, p.ExpiresAt, "an endorsement must expire")
		assert.Greater(t, time.Until(time.Unix(p.ExpiresAt, 0)), 300*24*time.Hour,
			"a verifier endorsement is deliberately long-lived")
		assert.Empty(t, p.Verifier,
			"naming the verifier is the authority's job, not ours — a self-referential claim proves nothing")
	}
	assert.Equal(t, 1, ours, "exactly one signature of our own")

	assert.Equal(t, models.StatusVerified, f.record().Status)
}

// Re-endorsing supersedes our own earlier signature and leaves
// everyone else's alone. A signature is a statement about the
// document; removing another party's is not ours to do.
func TestReEndorsementSupersedesOnlyOurOwnSignature(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	edited := f.newParty(func(p *org.Party) {
		p.Websites = []*org.Website{{URL: "https://acme.example"}}
	})
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(edited)).StatusCode)

	// An endorsement replaces its predecessor rather than accumulating,
	// so there is one — over the newer envelope.
	sent := f.endorsements()
	require.Len(t, sent, 1)

	var ours, theirs int
	for _, sig := range sent[0].Signatures {
		p, err := head.SignedPayload(sig)
		require.NoError(t, err)
		if p.Iss == string(f.verifier.Address()) {
			ours++
		} else {
			theirs++
		}
	}
	assert.Equal(t, 1, ours, "our earlier signature is superseded, not accumulated")
	assert.GreaterOrEqual(t, theirs, 2, "the subject's and the authority's signatures survive")
}

// A provider approval is evidence that some company was verified, not
// that it was this one. Nothing is signed until we have established
// that ourselves.
func TestMismatchIsNeverCountersigned(t *testing.T) {
	tests := []struct {
		name    string
		company provider.CompanyRecord
	}{
		{
			name: "a different registration number",
			company: verifiedCompany(func(c *provider.CompanyRecord) {
				c.RegistrationNumber = "B98765431"
				c.TaxCode = "ESB98765431"
			}),
		},
		{
			name: "a different country",
			company: verifiedCompany(func(c *provider.CompanyRecord) {
				c.Country = "PT"
			}),
		},
		{
			name: "a dissolved company",
			company: verifiedCompany(func(c *provider.CompanyRecord) {
				inactive := false
				c.Status = "Dissolved"
				c.Active = &inactive
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
			require.Equal(t, http.StatusOK, f.approve(tt.company).StatusCode)

			rec := f.record()
			assert.Equal(t, models.StatusMismatch, rec.Status)
			assert.Empty(t, f.endorsements(), "nothing is endorsed")
			require.NotNil(t, rec.Match)
			assert.False(t, rec.Match.Passed)
			assert.False(t, rec.Match.Reviewable, "a hard failure is not an operator's to release")
			assert.NotEmpty(t, rec.Match.Reasons)
		})
	}
}

// The evidence for a signature has to outlive the process that made
// it — including on the cases where nothing was signed.
func TestTheMatchReportIsAlwaysRecorded(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	rec := f.record()
	require.NotNil(t, rec.Match)
	assert.True(t, rec.Match.Passed)
	assert.NotEmpty(t, rec.Match.MatchedOn)
	assert.Equal(t, "Acme Widgets SL", rec.Match.Observed.LegalName)
	require.NotNil(t, rec.Decision)
	assert.NotEmpty(t, rec.Decision.DocID)

	stored, err := f.store.GetDecision(context.Background(), rec.Provider.ID)
	require.NoError(t, err, "the raw provider verdict is kept as evidence")
	assert.NotEmpty(t, stored.Raw)
	assert.NotEqual(t, rec.ID, stored.ID,
		"it lives in its own document: it carries personal data and needs its own retention clock")
}

func TestDeclineIsNeverCountersigned(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)

	rec := f.record()
	f.provider.SetDecision(rec.Provider.ID, provider.Decision{Status: provider.StatusDeclined, Reason: "sanctioned"})
	require.Equal(t, http.StatusOK, f.callback(rec, provider.StatusDeclined).StatusCode)

	assert.Equal(t, models.StatusDeclined, f.record().Status)
	assert.Empty(t, f.endorsements())
}

// Callbacks are retried, so the same event will arrive more than once.
func TestDuplicateCallbackIsANoOp(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)

	rec := f.record()
	f.provider.SetDecision(rec.Provider.ID, provider.Decision{
		Status:  provider.StatusApproved,
		Company: verifiedCompany(),
	})
	ev := provider.Event{
		ID:        "evt-duplicate",
		Reference: rec.Provider.Reference,
		SessionID: rec.Provider.ID,
		Status:    provider.StatusApproved,
		At:        time.Now().UTC(),
	}
	require.Equal(t, http.StatusOK, f.callbackWith(ev).StatusCode)
	require.Equal(t, http.StatusOK, f.callbackWith(ev).StatusCode)

	assert.Len(t, f.endorsements(), 1, "a retried callback must not re-endorse")
}

// Retries arrive out of order. Applying an older status would walk the
// case backwards into a state the provider has already left.
func TestStaleCallbackIsDiscarded(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)

	rec := f.record()
	now := time.Now().UTC()
	require.Equal(t, http.StatusOK, f.callbackWith(provider.Event{
		ID: "evt-review", Reference: rec.Provider.Reference, SessionID: rec.Provider.ID,
		Status: provider.StatusReview, At: now,
	}).StatusCode)
	require.Equal(t, models.StatusInReview, f.record().Status)

	require.Equal(t, http.StatusOK, f.callbackWith(provider.Event{
		ID: "evt-earlier", Reference: rec.Provider.Reference, SessionID: rec.Provider.ID,
		Status: provider.StatusPending, At: now.Add(-time.Minute),
	}).StatusCode)
	assert.Equal(t, models.StatusInReview, f.record().Status)
}

// We do not withdraw a signature we have published, and we do not
// approve after a decline without a human deciding to.
func TestTerminalCaseIsNotDisturbedByALaterCallback(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)
	require.Equal(t, models.StatusVerified, f.record().Status)

	rec := f.record()
	require.Equal(t, http.StatusOK, f.callbackWith(provider.Event{
		ID: "evt-contradiction", Reference: rec.Provider.Reference, SessionID: rec.Provider.ID,
		Status: provider.StatusDeclined, At: time.Now().UTC().Add(time.Hour),
	}).StatusCode)

	assert.Equal(t, models.StatusVerified, f.record().Status)
}

// A late callback for an attempt that has since been superseded is
// evidence about a company we are no longer looking at.
func TestCallbackForASupersededAttemptIsIgnored(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	first := f.record()

	changed := f.newParty(func(p *org.Party) { p.Name = "Acme Holdings SL" })
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(changed)).StatusCode)
	require.Equal(t, 2, f.record().Attempt)

	f.provider.SetDecision(first.Provider.ID, provider.Decision{
		Status: provider.StatusApproved, Company: verifiedCompany(),
	})
	require.Equal(t, http.StatusOK, f.callbackWith(provider.Event{
		ID: "evt-old", Reference: first.Provider.Reference, SessionID: first.Provider.ID,
		Status: provider.StatusApproved, At: time.Now().UTC(),
	}).StatusCode)

	assert.Empty(t, f.endorsements())
	assert.Equal(t, models.StatusSessionOpen, f.record().Status)
}

// A callback that cannot be routed is still acknowledged: a 4xx would
// spend the provider's two retries on something no retry can fix.
func TestUnroutableCallbackIsAcknowledged(t *testing.T) {
	f := newFixture(t)
	resp := f.callbackWith(provider.Event{
		ID: "evt-orphan", Reference: "nobody", SessionID: "sess-x",
		Status: provider.StatusApproved, At: time.Now().UTC(),
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// Analyst review can last days and produces no callback in the
// meantime, so the case gets its own state and its own pacing.
func TestReviewIsItsOwnState(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)

	rec := f.record()
	require.Equal(t, http.StatusOK, f.callback(rec, provider.StatusReview).StatusCode)

	after := f.record()
	assert.Equal(t, models.StatusInReview, after.Status)
	require.NotNil(t, after.ReviewStartedAt)
	require.NotNil(t, after.NextPollAt)
	assert.Greater(t, time.Until(*after.NextPollAt), time.Hour,
		"there is no point polling an analyst every few minutes")

	// The subject is told to sit tight — otherwise an impatient
	// customer starts again and buys a second check.
	msgs := f.mail.records()
	require.GreaterOrEqual(t, len(msgs), 2)
	assert.Contains(t, msgs[len(msgs)-1].Text, "nothing further for you to do")
}

// The provider retries a callback twice and then drops it forever. The
// reconciler is what stops a paid-for verification being lost with it.
func TestReconcilerRecoversADroppedCallback(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)

	rec := f.record()
	f.provider.SetDecision(rec.Provider.ID, provider.Decision{
		Status: provider.StatusApproved, Company: verifiedCompany(),
	})
	// No callback ever arrives. Time passes until the case is due a
	// poll.
	require.Empty(t, f.endorsements())
	f.due(rec)

	n, err := f.setup.Verifications().Reconcile(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	assert.Equal(t, models.StatusVerified, f.record().Status)
	assert.Len(t, f.endorsements(), 1)
}

// Collection is the contract that replaced outbound delivery, and it
// is what lets a verifier stay a signing service: the subject fetches
// its own endorsement and registers it with the authority itself.
func TestSubjectCollectsItsOwnEndorsement(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	resp := f.collect()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	env := new(gobl.Envelope)
	require.NoError(t, json.Unmarshal([]byte(readBody(t, resp)), env))

	var ours int
	for _, sig := range env.Signatures {
		p, err := head.SignedPayload(sig)
		require.NoError(t, err)
		if p.Iss == string(f.verifier.Address()) {
			ours++
			assert.Equal(t, string(f.subAddr), p.Aud)
		}
	}
	assert.Equal(t, 1, ours, "the collected envelope carries our countersignature")
}

// The three answers a waiting caller needs to tell apart, without
// being told anything about the checks themselves.
func TestCollectionDistinguishesWaitingFromSettled(t *testing.T) {
	t.Run("still running is 204, so the caller keeps waiting", func(t *testing.T) {
		f := newFixture(t)
		require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
		assert.Equal(t, http.StatusNoContent, f.collect().StatusCode)
	})

	t.Run("never heard of is also 204, and says no more than that", func(t *testing.T) {
		f := newFixture(t)
		assert.Equal(t, http.StatusNoContent, f.collect().StatusCode)
	})

	t.Run("settled without an endorsement is 410, so the caller stops", func(t *testing.T) {
		f := newFixture(t)
		require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
		require.Equal(t, http.StatusOK, f.approve(verifiedCompany(func(c *provider.CompanyRecord) {
			c.RegistrationNumber = "B98765431"
			c.TaxCode = "ESB98765431"
		})).StatusCode)
		require.Equal(t, models.StatusMismatch, f.record().Status)

		resp := f.collect()
		assert.Equal(t, http.StatusGone, resp.StatusCode)
		assert.NotContains(t, readBody(t, resp), "B98765431",
			"a caller is told it ended, not what the registry said")
	})
}

// The request token is the only credential, and the address that
// signed it is the address whose case is read — so a subject can only
// ever collect its own.
func TestCollectionRequiresARequestToken(t *testing.T) {
	f := newFixture(t)
	resp := f.do(http.MethodGet, web.VerificationPath, nil, "", "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// An operator may release a name mismatch, and only a name mismatch.
func TestOperatorCanReleaseANameMismatchButNothingElse(t *testing.T) {
	t.Run("a name mismatch", func(t *testing.T) {
		f := newFixture(t)
		require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
		require.Equal(t, http.StatusOK, f.approve(verifiedCompany(func(c *provider.CompanyRecord) {
			c.LegalName = "Something Quite Different SL"
		})).StatusCode)
		require.Equal(t, models.StatusMismatch, f.record().Status)

		rec, err := f.setup.Verifications().Approve(context.Background(), f.subAddr, "sam", "confirmed against the registry by hand")
		require.NoError(t, err)
		assert.Equal(t, models.StatusVerified, rec.Status)
		assert.Len(t, f.endorsements(), 1)
		require.Len(t, rec.OperatorActions, 1)
		assert.Equal(t, "sam", rec.OperatorActions[0].Actor)
		assert.NotEmpty(t, rec.OperatorActions[0].Reason)
	})

	t.Run("an identifier mismatch", func(t *testing.T) {
		f := newFixture(t)
		require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
		require.Equal(t, http.StatusOK, f.approve(verifiedCompany(func(c *provider.CompanyRecord) {
			c.RegistrationNumber = "B98765431"
			c.TaxCode = "ESB98765431"
		})).StatusCode)

		_, err := f.setup.Verifications().Approve(context.Background(), f.subAddr, "sam", "looks fine to me")
		require.Error(t, err, "no operator may sign off on a different company")
		assert.Empty(t, f.endorsements())
	})
}

// The status page is authenticated by its unguessable token, and shows
// the customer nothing about what the registry returned.
func TestStatusPageDoesNotLeakRegistryData(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany(func(c *provider.CompanyRecord) {
		c.LegalName = "Secret Registry Name SL"
		c.RegistrationNumber = "B98765431"
		c.TaxCode = "ESB98765431"
	})).StatusCode)

	rec := f.record()
	resp := f.do(http.MethodGet, "/status/"+rec.Token, nil, "", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	assert.NotContains(t, body, "Secret Registry Name")
	assert.NotContains(t, body, "B98765431")
	assert.Contains(t, body, "not able to complete")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestStatusPageRejectsAnUnknownToken(t *testing.T) {
	f := newFixture(t)
	resp := f.do(http.MethodGet, "/status/nope", nil, "", "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// Key discovery stays open so verifying a request token can never
// recurse into needing one.
func TestKeyEndpointsAreOpen(t *testing.T) {
	f := newFixture(t)
	kid := f.verifier.PrivateKey.ID()

	resp := f.do(http.MethodGet, goblnet.KeysPath+"/"+kid, nil, "", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp = f.do(http.MethodGet, goblnet.JWKSPath, nil, "", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHealthz(t *testing.T) {
	f := newFixture(t)
	resp := f.do(http.MethodGet, "/healthz", nil, "", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// The endorsement is pushed to the subject's inbox as a courtesy so
// its platform learns at once, but collection is the contract. A push
// that fails must leave the case verified and collectable, because
// treating it as a failure is what would drag outbound retries, a
// failure state and a reconciler back into a verifier.
func TestInboxNotificationIsBestEffort(t *testing.T) {
	f := newFixture(t)

	// The fixture's fetcher serves keys but refuses posts, so the
	// notification cannot succeed.
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	assert.Equal(t, models.StatusVerified, f.record().Status,
		"a failed notification does not change the outcome")
	require.Len(t, f.endorsements(), 1)
	assert.Equal(t, http.StatusOK, f.collect().StatusCode,
		"the endorsement is still collectable")
}
