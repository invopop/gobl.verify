package web

import (
	"encoding/json"
	"log/slog"
	"net/http"

	goblnet "github.com/invopop/gobl/net"

	"github.com/invopop/gobl.verify/internal/domain"
	"github.com/invopop/gobl.verify/internal/domain/models"
)

// VerificationPath is where a subject collects the endorsement this
// service issued about it.
//
// This is the one route that is not in the GOBL Net spec, and it is
// what lets a verifier avoid being a full network participant. In the
// original choreography the verifier pushed its countersignature to
// the registration authority, which meant every verifier needed an
// outbound sender, request tokens for someone else's inbox, and retry
// machinery for a push that failed. Here the subject collects its own
// endorsement and registers it itself — the authority auto-verifies on
// any valid countersignature from a trusted verifier regardless of who
// delivered it, so nothing on that side had to change.
const VerificationPath = goblnet.WellKnownPath + "/verification"

// handleCollect serves the caller its own endorsement.
//
// Authentication is the ordinary GOBL Net request token, so a subject
// can only ever collect its own: the address that signed the token is
// the address whose case is looked up. There is no other key to hold
// and nothing to leak.
//
// The statuses are what a waiting caller needs to tell the three cases
// apart without being told anything about the checks themselves:
//
//	200  endorsed — the countersigned envelope
//	204  still in progress — ask again later
//	410  settled without an endorsement — stop asking
func handleCollect(s *domain.Setup, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject := requesterFrom(r.Context())
		if subject == "" {
			writeError(w, domain.ErrUnauthorized.WithMessage("request token required"))
			return
		}

		rec, err := s.Verifications().Find(r.Context(), subject)
		if err != nil {
			// No case at all reads the same as one still running: a
			// caller polls either way, and whether this service has
			// ever heard of an address is not something to publish.
			w.WriteHeader(http.StatusNoContent)
			return
		}

		switch rec.Status {
		case models.StatusVerified:
			if rec.Countersigned == nil {
				log.Error("collect.missing_endorsement", "address", string(subject))
				writeError(w, domain.ErrInternal.WithMessage("the endorsement could not be read"))
				return
			}
			data, err := json.Marshal(rec.Countersigned)
			if err != nil {
				writeError(w, domain.ErrInternal.WithCause(err))
				return
			}
			log.Info("collect.served", "address", string(subject), "envelope", rec.EnvelopeUUID.String())
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, http.StatusOK, data)

		case models.StatusDeclined, models.StatusMismatch, models.StatusExpired:
			// Terminal without an endorsement. Say so, so a caller
			// stops waiting — but not why, which is between this
			// service and its operators.
			log.Info("collect.settled_unverified", "address", string(subject), "status", string(rec.Status))
			http.Error(w, "this verification completed without an endorsement", http.StatusGone)

		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}
