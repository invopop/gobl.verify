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

- **The verifier no longer delivers anything.** It countersigns and stores
  the result; the subject collects it from
  `GET /.well-known/gobl/verification` and registers it with the authority
  itself.

  This needed no change to the authority. `acceptedVerifier` scans an
  envelope's signatures for one issued by a trusted verifier and has no
  notion of who delivered it, so a subject re-registering with a
  countersignature aboard auto-verifies exactly as before. What it removes
  is most of a verifier: the outbound sender and its SSRF guard, request
  tokens minted for someone else's inbox, the delivery retry states, and the
  reconciler path that chased them. A verifier is now a keypair, a public
  key endpoint and a signing step — which is what makes it realistic for
  somebody other than us to run one.

  `delivered` and `delivery_failed` collapse into a terminal `verified`, and
  `redeliver` becomes `reissue`: not a delivery retry, but the recovery path
  for an endorsement that lapsed before anyone collected it.

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
