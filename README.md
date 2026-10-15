# gobl.verify

A reference implementation of a **GOBL Net verifier** — the participant that
establishes which real-world organization is behind an address and
countersigns their party envelope to say so. On a live network that means
running a KYC/KYB process.

> ⚠️ **EXPERIMENTAL** — GOBL Net is under active development and the wire
> protocol may change without notice.

GOBL Net splits trust in two. A **registration authority** confirms that a
party controls its address — which proves only that whoever registered the
domain also published the keys there. A **verifier**, which the spec requires
to be a different participant, establishes *who that is*: the organization
behind the address, checked against a company registry and whatever else its
policy demands. Receivers want the second before they will accept what an
address sends, so without a verifier an identity can receive but never send.

**It is not a certificate authority.** Web PKI already binds a key to a
domain, and that is the layer underneath this one. A verifier binds an address
to a legal identity, which is a different question answered with different
evidence — a registry record and a director's documents, not a domain
validation.

This repository is what a verifier looks like. It ships one working check —
confirming control of the party's published email address — and the seam to
replace it with a real one. **It is not a service Invopop operates**; it is
the code a network member runs to become a verifier.

## What a verifier actually has to do

Less than you would expect, and that is the point.

1. **Receive** a registered party envelope on the standard inbox.
2. **Check** whatever it is you check.
3. **Countersign** that exact envelope — same `uuid`, same digest — with
   `iss` = your address, `aud` = the subject, and an expiry.
4. **Send it back to the subject's inbox.**

There is no fourth party to notify. The endorsement goes back the way it
came — a party envelope delivered to a party's inbox, the same ordinary
`POST /.well-known/gobl/inbox` the subject used to reach you. The subject
registers it with the authority itself, and the authority auto-verifies on
any valid countersignature from an address on its trusted list, regardless
of who delivered it.

So the whole protocol surface is endpoints that already exist. A verifier
publishes no route of its own, and a subject that can be on the network at
all can already receive its result. If you want to expose a status or
progress API to your own customers, that is yours to design — it is not
part of being a verifier.

## The claim you are making

An endorsement is a public statement others rely on, and GOBL Net does not
grade it. `Endorsement.Verified()` is a boolean, and a receiver requiring a
verified sender is satisfied by **any** verifier on the authority's list. It
can inspect which one, but nothing obliges it to.

Two things follow, and they are the whole reason to read this section.

**Your address is the trust signal.** It is all a receiver sees. Name it for
what you are vouching for — and if what you check changes, that is a change
to what the name means, not a settings tweak.

**A weak verifier is worse than no verifier.** It does not produce a weaker
claim; it produces the *same* claim backed by less. The shipped email
provider confirms somebody can read a mailbox, which is no evidence at all
about who a company is, so it belongs on a sandbox network and nowhere else.

## The seam

Everything vendor-specific lives behind one interface in
`internal/domain/provider`:

```go
type Provider interface {
    Name() Name
    Requires() Requirements
    CreateSession(ctx context.Context, req SessionRequest) (*Session, error)
    Decision(ctx context.Context, sessionID string) (*Decision, error)
    ParseEvent(raw []byte, header http.Header, now time.Time) (*Event, error)
}
```

A new verifier is a package implementing it and a case in `buildProvider`.
Nothing else changes. Two optional interfaces let a provider contribute
routes: `ConfirmingProvider` if it needs a page of its own (as the email one
does), `CallbackProvider` if its vendor pushes updates to a URL.

`Requires()` is the one method worth thinking about before you write any of
the others. Its single field, `RegistryFacts`, says whether your provider
looks the company up in a company registry, and the domain reads it twice:

- **At the inbox.** A registry lookup needs a legal name, a registry country
  and a tax or legal identity, so a party publishing none of them is refused
  before a session is opened — discovering it afterwards means discovering it
  after the check has been paid for. A provider that consults no registry
  asks for none of that, and demanding a tax code it will never read only
  turns away subjects it could have served.
- **After a decision.** The match check below is only meaningful against a
  record the provider resolved *independently*. A provider that consults no
  registry can only hand back what the subject declared, so the domain skips
  the check rather than comparing the party's claims with the party's claims
  and filing the result as corroboration.

The envelope-swap guard runs either way: the facts are always re-derived from
the envelope about to be signed, so a party document that changed under a
running check is never countersigned.

### Writing one

The hard part is not calling the vendor. It is this:

**A provider tells you about a company it was asked to look up — not about
the party you were asked to endorse.** Nothing in a hosted verification flow
forces those to be the same. Search fields stay editable, and an analyst may
change them mid-review. So an approval is evidence that *some* real company
was checked, and establishing that it was *this* one is yours to do. That is
what `match.go` is for, and it is the piece worth reading before you write
an adapter: country and identifiers compared strictly, legal name with
tolerance, and a name-only miss held for a human rather than declined —
because auto-declining on a fuzzy name burns a customer's money on a check
that actually succeeded.

**Where the data came from matters more than what it says.** Registry-
disclosed people are evidence about a company; people the applicant typed in
are evidence about nobody. Providers mix both, sometimes in the same array.
`PersonSource` carries that distinction and `sourceOf`-style mapping must
fail closed: anything not positively identifiable as registry data is
applicant data.

**Three things that cost us a week, in case your vendor does the same:**

- Status vocabularies in documentation and in responses are not always the
  same strings. Ours documented `NOT_STARTED` and returned `Not Started`;
  matching only the documented spelling mapped every live status to unknown.
  Pin your mapping against a captured response, not against the docs.
- If callbacks are signed over a *canonical* form of the payload rather than
  the bytes on the wire, reproducing it is exacting: preserve original number
  text, leave `<`, `>` and `&` unescaped, keep non-ASCII literal. Each of
  those passes every test written against your own encoder and fails against
  real traffic.
- Sandbox and live payloads are shaped identically. If the vendor reports
  which environment a decision came from, check it — otherwise a credential
  mix-up turns mocked checks into real endorsements and nothing downstream
  can tell.

## Endpoints

| Endpoint | Auth | Purpose |
|---|---|---|
| `POST /.well-known/gobl/inbox` | request token | receive registered envelopes |
| `GET /.well-known/gobl/who` | request token | this verifier's own signed party |
| `GET /.well-known/gobl/keys/<kid>` | open | published signing keys |
| `GET /.well-known/jwks.json` | open | keys as an RFC 7517 set |
| `GET/POST /confirm/<token>` | link token | the email provider's declaration page |
| `GET /status/<token>` | link token | progress, for the subject |
| `GET /healthz` | open | readiness/liveness |

Every GOBL Net route here is one the spec already defines; the rest are this
implementation's own, and none of them is part of the protocol. Key
discovery stays open so that verifying a request token never recurses into
needing one.

### Delivering the endorsement

The endorsement is sent to the subject's inbox as soon as it is signed, and
a send that fails is retried by the reconciler. The verification itself is
never undone by a delivery problem: an unreachable inbox is a fact about the
subject's host, and by then the check has been paid for and the
countersignature exists.

Two failures are told apart, because `gobl/net` draws the line for us. A
transport failure or a `5xx` is transient and retried. `ErrInboxRejected` —
a `4xx` other than `429` — means the inbox *decided*, and re-sending an
identical envelope only produces the same refusal more slowly, so it is
abandoned at once and an operator is told.

When retries run out the case keeps its endorsement and stays `verified`.
Delivery is tracked beside that status rather than inside it: "did we
endorse?" and "did they receive it?" are different questions, and folding
the second into the first makes a verified case whose push failed read as
though the verification never happened. The subject's own recovery is the
flow it already uses — re-submit, and a verified case re-endorses and
re-delivers for free.

### Why refusals say little

`403` means this verifier will not act on the envelope — not registered with
our authority, a jurisdiction we do not cover, a previous decline still in
cooldown, a case held for an operator. Nothing the subject edits changes the
answer. `422` means the document is missing something verification needs —
re-registering a corrected party fixes it. `401` is authentication, and
`503` means a check could not be *performed*, not that it failed.

Messages to the subject never repeat what an external source returned, even
when that is exactly what went wrong. Whoever holds the link controls the
party's published mailbox, which is not the same as being entitled to a
third party's registry record — and naming the failing comparison tells
anyone probing the service what to adjust.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `CONFIG_DIR` | — | identity dir (`private.jwk`, `party.json`, `keys/`) |
| `COUCHDB_*` | scheme `http`, port `5984` | store connection |
| `COUCHDB_DATABASE` | `gobl_verify` | database name |
| `AUTHORITY` | `lookup.gobl.org` | the authority whose countersignature admits an envelope |
| `PUBLIC_BASE_URL` | `https://<domain>` | base of the emailed links; must be https |
| `PROVIDER` | `email` | `email` or `mock` |
| `SUPPORTED_COUNTRIES` | *(all)* | ISO codes; refused at the inbox, before any check |
| `MATCH_NAME_THRESHOLD` | `0.90` | legal-name similarity floor |
| `MATCH_REQUIRE_ACTIVE` | `true` | never endorse a company a registry has struck off |
| `ENDORSEMENT_TTL` | `8760h` | lifetime stamped on the countersignature |
| `KYB_RESULT_TTL` | `8760h` | how long a completed check re-endorses unchanged facts for free |
| `SESSION_TTL` | `336h` | when an abandoned session is swept |
| `MAX_SESSIONS_PER_ADDRESS` / `MAX_SESSIONS_WINDOW` | `3` / `720h` | cost ceiling per address |
| `RESYNC_INTERVAL` | `15m` | reconciler tick; `0` disables |
| `OPS_ALERT_EMAIL` | — | mismatch and error notices |
| `SMTP_*`, `EMAIL_FROM` | port `587` | transactional email |
| `HTTP_PORT` / `PORT` | `8080` | listen port |

## Running locally

```sh
docker compose up -d
go run ./cmd/gobl.verify init verify.example.org --name "Example Verifier"
go run ./cmd/gobl.verify serve \
  --config-dir ~/.config/gobl.verify/verify.example.org \
  --couchdb http://admin:pass@localhost:5984
```

Without `SMTP_HOST` the emails — links included — go to the log, which is
all a local loop needs. `go test -race ./...` needs nothing external.

## Commands

`init` · `serve` · `status <address>` · `resync` · `reissue <address>` ·
`resend <address>` · `approve <address> --reason` · `reject <address>
--reason` · `version`

Every mutating command records who ran it and why. For a service whose
product is trustworthiness, that is the only question anyone asks later.

## Known limitations

- **The shipped provider proves nothing about a company.** It confirms
  mailbox control and records a declaration. Sandbox networks only.
- **Analyst review, if your provider has it, is an operational commitment.**
  Cases escalate to a human on the vendor's side, with no API to hurry it.
- **Registry status vocabularies vary.** An unrecognised status is treated as
  *unknown* rather than guessed at, so an unusual wording will not block a
  dissolved company on that axis alone.

## License

Released under the Apache 2.0 LICENSE, Copyright 2026 Invopop S.L.
