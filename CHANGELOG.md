# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **Providers now declare what they need.** `Provider.Requires()` returns a
  `Requirements`, whose `RegistryFacts` field says whether the provider looks
  the company up in a company registry. The domain reads it at the inbox and
  again after a decision.

  Before this, every submission had to publish a legal name, a registry
  country and a tax or legal identity, whatever the configured provider did
  with them — so the email reference check refused parties it could have
  verified perfectly well, and refused the very parties the sandbox service
  it replaces accepts today. Those requirements belong to a registry lookup,
  not to verification as such.

  It also fixes something quieter. The match check compares what the party
  claims against what the provider resolved, and that is only evidence when
  the provider resolved it independently. A provider that consults no
  registry can only echo the subject's own declaration back, so running the
  check compared the party's claims with the party's claims — a comparison
  that cannot fail — and filed the result as a passed match. It is now
  skipped, and such a case carries no match report at all. The envelope-swap
  guard is unaffected: the facts are still re-derived from the envelope about
  to be signed, so a party that changed under a running check is still never
  countersigned.

- Renamed from `gobl.kyb` to **`gobl.verify`**, and reframed from a service
  Invopop operates into the reference implementation a network member runs.
  "KYB" overstated it: a verifier is whoever vouches, and what they check is
  theirs to define and publish.

- **The endorsement goes back through the inbox.** When the checks pass, the
  verifier countersigns the envelope and delivers it to the subject's own
  inbox — the same ordinary `POST /.well-known/gobl/inbox` the subject used
  to reach it. The subject then registers it with the authority itself.

  This needs no change to the authority. `acceptedVerifier` scans an
  envelope's signatures for one issued by a trusted verifier and has no
  notion of who delivered it, so a subject re-registering with a
  countersignature aboard auto-verifies exactly as before.

  An earlier draft of this had the subject *collect* the endorsement from a
  new `GET /.well-known/gobl/verification` endpoint. That was a mistake: it
  invented a transport for a document the protocol already knows how to
  move. A party envelope delivered to a party's inbox is the most ordinary
  thing on the network, every subject already has an inbox, and a verifier
  that publishes no route of its own has no protocol surface to get wrong.
  A verifier that wants to expose progress to its own customers can, but
  that is its product, not the specification's business.

  `delivered` and `delivery_failed` do not come back as statuses. Delivery
  is tracked beside the verification status, not inside it: "did we
  endorse?" and "did they receive it?" have different owners, and conflating
  them made a verified case whose push failed read as unverified. A failed
  send is retried by the reconciler, a refusal (`ErrInboxRejected`) is
  final, and when retries run out the case keeps its endorsement and alerts
  an operator. The subject's own recovery is to re-submit, which re-endorses
  and re-delivers for free.

  `redeliver` becomes `reissue`: the recovery path for an endorsement that
  lapsed, not a delivery retry — those happen on their own.

- **The reconciler's CouchDB view and its in-memory twin now share one
  predicate.** They had drifted: the view's allowlist still named `approved`
  and `delivery_failed`, which no longer exist, and omitted `error`, so
  errored cases were never swept in production while the tests swept them
  fine. Both now derive from `Verification.NeedsReconcile`, and the view is
  written as a terminal-status denylist so a new status cannot be silently
  dropped from the queue again.

### Removed

- **The Didit adapter.** An unused vendor integration in a reference
  implementation is worse than none: it reads as "this is how it is done"
  while nobody exercises it, and we found three bugs in that one during the
  single week we did. The vendor knowledge belongs where the vendor is
  actually used. What was transferable is now in the README, as prose, where
  it cannot rot.

### Added

- **An email provider** as the reference check: confirm control of the
  party's published address, record a declaration. It is deliberately the
  whole of one implementation — read it end to end and you have seen every
  moving part a verifier needs.

  It reports its company data as applicant-sourced, because that is what it
  is. Claiming otherwise would have the match check compare the party
  against itself and call that corroboration.

- `ConfirmingProvider` and `CallbackProvider`, so a provider can contribute
  a page or a callback route without the transport layer knowing anything
  about any particular vendor.

- `Advance`, the entry point for a provider that reports progress by being
  asked rather than by calling back.
