package web_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/invopop/gobl/head"
	goblnet "github.com/invopop/gobl/net"
	"github.com/invopop/gobl/org"

	"github.com/invopop/gobl.verify/internal/domain/models"
	"github.com/invopop/gobl.verify/internal/domain/provider"
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

	// Each endorsement is handed over as it is issued, so the newer
	// one is a second delivery. What must not accumulate is our
	// signature *inside* it.
	require.Len(t, f.endorsements(), 2)

	var ours, theirs int
	for _, sig := range f.lastEndorsement().Signatures {
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

// The endorsement goes back the way it came: a party envelope
// delivered to the subject's inbox, which is the one route every
// participant already has. The subject registers it with the
// authority itself.
func TestEndorsementIsReturnedToTheSubjectsInbox(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	sent := f.endorsements()
	require.Len(t, sent, 1, "the endorsement goes back the way it came")

	var ours int
	for _, sig := range sent[0].Signatures {
		p, err := head.SignedPayload(sig)
		require.NoError(t, err)
		if p.Iss == string(f.verifier.Address()) {
			ours++
			assert.Equal(t, string(f.subAddr), p.Aud)
		}
	}
	assert.Equal(t, 1, ours, "the delivered envelope carries our countersignature")

	rec := f.record()
	assert.Equal(t, models.StatusVerified, rec.Status)
	require.NotNil(t, rec.Delivery.At)
	assert.Equal(t, 1, rec.Delivery.Attempts, "one attempt, no retry needed")
	assert.False(t, rec.Delivery.Pending())
}

// A case that settles without an endorsement has nothing to hand
// back, and must not hand back anything that leaks why.
func TestNothingIsDeliveredWhenThereIsNoEndorsement(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany(func(c *provider.CompanyRecord) {
		c.RegistrationNumber = "B98765431"
		c.TaxCode = "ESB98765431"
	})).StatusCode)

	require.Equal(t, models.StatusMismatch, f.record().Status)
	assert.Empty(t, f.endorsements())
	assert.Nil(t, f.signed())
}

// An unreachable inbox is a fact about the subject's host, not a
// reason to undo a check that has already been paid for. The
// endorsement is signed, kept, and re-offered by the reconciler.
func TestDeliveryIsRetriedWhenTheInboxIsDown(t *testing.T) {
	f := newFixture(t)
	f.fetcher.failPosts(goblnet.ErrUnavailable)

	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	rec := f.record()
	assert.Equal(t, models.StatusVerified, rec.Status,
		"a failed delivery does not change the outcome of the checks")
	assert.NotNil(t, rec.Countersigned, "the endorsement is signed and kept")
	assert.True(t, rec.Delivery.Pending())
	assert.Equal(t, 1, rec.Delivery.Attempts)
	require.NotNil(t, rec.NextPollAt, "a retry is scheduled")
	assert.Empty(t, f.endorsements())

	// The inbox comes back; the reconciler hands it over, unchanged.
	f.fetcher.failPosts(nil)
	f.due(rec)
	n, err := f.setup.Verifications().Reconcile(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	sent := f.endorsements()
	require.Len(t, sent, 1)
	assert.Equal(t, f.signed().Head.Digest.Value, sent[0].Head.Digest.Value,
		"the retry delivers the endorsement that was already signed, not a new one")

	rec = f.record()
	require.NotNil(t, rec.Delivery.At)
	assert.Equal(t, 2, rec.Delivery.Attempts)
	assert.Nil(t, rec.NextPollAt, "nothing left to do")
}

// Retrying forever against an inbox that will never take it is just a
// slower way of never delivering. We stop, tell the operators, and
// keep the endorsement.
func TestDeliveryIsAbandonedEventually(t *testing.T) {
	f := newFixture(t)
	f.fetcher.failPosts(goblnet.ErrUnavailable)

	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	for i := 0; i < 20 && f.record().Delivery.Pending(); i++ {
		f.due(f.record())
		_, err := f.setup.Verifications().Reconcile(context.Background(), 10)
		require.NoError(t, err)
	}

	rec := f.record()
	assert.True(t, rec.Delivery.Abandoned)
	assert.Equal(t, models.StatusVerified, rec.Status, "the verification still happened")
	assert.NotNil(t, rec.Countersigned, "and the endorsement is still held")
	assert.Nil(t, rec.NextPollAt, "but we have stopped knocking")

	assert.NotEmpty(t, f.opsAlerts(), "the operators are told")
}

// An inbox that refuses the envelope has decided, not failed.
// Re-sending an identical document produces an identical refusal.
func TestARefusedDeliveryIsNotRetried(t *testing.T) {
	f := newFixture(t)
	f.fetcher.failPosts(goblnet.ErrInboxRejected)

	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	rec := f.record()
	assert.True(t, rec.Delivery.Abandoned, "a refusal is final")
	assert.Equal(t, 1, rec.Delivery.Attempts, "and is not tried again")
	assert.Nil(t, rec.NextPollAt)
}

// The subject's own recovery for an endorsement it never received is
// the flow it already uses: send the party again. A verified case
// re-endorses for free, so this costs nothing and needs no second
// endpoint to exist.
func TestResubmissionRedeliversAnUndeliveredEndorsement(t *testing.T) {
	f := newFixture(t)
	f.fetcher.failPosts(goblnet.ErrUnavailable)

	env := f.registeredEnvelope(f.newParty())
	require.Equal(t, http.StatusAccepted, f.submit(env).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)
	require.True(t, f.record().Delivery.Pending())
	require.Empty(t, f.endorsements())

	before := f.provider.Sessions()
	f.fetcher.failPosts(nil)
	require.Equal(t, http.StatusAccepted, f.submit(env).StatusCode)

	assert.Equal(t, before, f.provider.Sessions(), "re-delivery buys no new check")
	require.Len(t, f.endorsements(), 1)
	assert.NotNil(t, f.record().Delivery.At)
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

// A provider that consults no registry hands back the subject's own
// declaration. Running the match check on that would compare the
// party's claims with the party's claims — a comparison that cannot
// fail — and recording it as a passed match would dress a
// self-assertion up as corroboration. So it is skipped, and the case
// carries no match report at all.
func TestRegistrylessApprovalIsNotRecordedAsAMatch(t *testing.T) {
	f := newFixture(t)
	f.provider.Requirements = provider.Requirements{}
	require.Equal(t, http.StatusAccepted,
		f.submit(f.registeredEnvelope(f.newParty(func(p *org.Party) { p.TaxID = nil }))).StatusCode)

	// What the email provider returns: the subject echoed back.
	require.Equal(t, http.StatusOK, f.approve(provider.CompanyRecord{
		LegalName: "Acme Widgets SL",
	}).StatusCode)

	rec := f.record()
	assert.Equal(t, models.StatusVerified, rec.Status)
	assert.Nil(t, rec.Match, "an echo is not a match, and must not be filed as one")
	assert.Len(t, f.endorsements(), 1)
}

// The registry-less path would otherwise have no comparison left at
// all, so the one check that does not depend on the provider has to
// keep working: the facts are re-derived from the envelope about to be
// signed, and a case whose party document no longer describes what was
// checked is never countersigned.
func TestRegistrylessEndorsementStillRefusesAChangedParty(t *testing.T) {
	f := newFixture(t)
	f.provider.Requirements = provider.Requirements{}
	require.Equal(t, http.StatusAccepted,
		f.submit(f.registeredEnvelope(f.newParty(func(p *org.Party) { p.TaxID = nil }))).StatusCode)

	// Swap the stored envelope for a different company, as a process
	// that got between the open session and the decision would.
	rec := f.record()
	swapped := f.registeredEnvelope(f.newParty(func(p *org.Party) {
		p.Name = "Totally Different Holdings"
		p.TaxID = nil
	}))
	rec.SetEnvelope(swapped)
	require.NoError(t, f.store.Put(context.Background(), rec))

	require.Equal(t, http.StatusOK, f.approve(provider.CompanyRecord{
		LegalName: "Acme Widgets SL",
	}).StatusCode)

	assert.Equal(t, models.StatusMismatch, f.record().Status)
	assert.Empty(t, f.endorsements(), "a party that changed under the check is never countersigned")
}

// The premise of returning the endorsement through the inbox is that
// an ordinary GOBL Net inbox will accept it. That is worth checking
// against the library's own verification rather than assuming: this
// stands in for the subject's inbox and verifies what the verifier
// actually put on the wire.
func TestTheDeliveryIsOneARealInboxWouldAccept(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusAccepted, f.submit(f.registeredEnvelope(f.newParty())).StatusCode)
	require.Equal(t, http.StatusOK, f.approve(verifiedCompany()).StatusCode)

	// Addressed to the subject's inbox, not anywhere else.
	hdr := f.fetcher.lastPostHeader()
	require.NotNil(t, hdr)

	// The request token checks out as the subject's inbox would check
	// it: audience us, issued by the verifier, within its freshness
	// window, against the verifier's published key.
	inbox := goblnet.NewClient(goblnet.WithFetcher(f.fetcher))
	requester, err := inbox.VerifyAuthorization(
		context.Background(), hdr.Get("Authorization"), f.subAddr)
	require.NoError(t, err, "the subject's inbox must be able to verify our request token")
	assert.Equal(t, f.verifier.Address(), requester,
		"the delivery is authenticated as the verifier")

	// And the envelope it carried verifies as a party endorsed by us.
	env := f.lastEndorsement()
	require.NoError(t, env.Validate())
	subject, err := inbox.VerifyParty(context.Background(), env)
	require.NoError(t, err, "what arrives is a valid party envelope")
	assert.Equal(t, f.subAddr, subject)
}
