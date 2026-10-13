package web

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/invopop/gobl.verify/internal/domain"
	"github.com/invopop/gobl.verify/internal/domain/provider"
)

// ConfirmingProvider is a provider that asks the subject to confirm
// something on a page this service serves, rather than on the vendor's
// own site.
//
// It is the extension point for a provider with a user interface. A
// hosted-flow vendor returns its own URL from CreateSession and needs
// none of this; the email provider has nowhere else to send anyone, so
// it contributes these two routes.
type ConfirmingProvider interface {
	// Pending reports the subject behind an outstanding token.
	Pending(token string) (*provider.Subject, bool)
	// Confirm records acceptance and returns the session's reference.
	Confirm(token string) (reference string, ok bool)
}

// confirmPage is the whole user interface: one page, no assets, so the
// emailed link works in any browser without a second request.
var confirmPage = template.Must(template.New("confirm").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 0; background: #f5f5f4; color: #1c1917; }
  main { max-width: 34rem; margin: 4rem auto; padding: 2rem; background: #fff;
         border-radius: 0.5rem; box-shadow: 0 1px 3px rgba(0,0,0,.1); }
  h1 { font-size: 1.25rem; margin-top: 0; }
  code { background: #f5f5f4; padding: 0.1rem 0.3rem; border-radius: 0.25rem; }
  label { display: flex; gap: 0.6rem; align-items: flex-start; margin: 1.5rem 0; }
  button { background: #1c1917; color: #fff; border: 0; border-radius: 0.375rem;
           padding: 0.6rem 1.2rem; font-size: 1rem; cursor: pointer; }
  .error { color: #b91c1c; }
  .note { color: #57534e; font-size: 0.875rem; }
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
{{if .Message}}<p>{{.Message}}</p>{{end}}
{{if .ShowForm}}
<p>Confirm the verification of <code>{{.Address}}</code>.</p>
<form method="post">
  <label>
    <input type="checkbox" name="accept" value="on">
    <span>I confirm that I am entitled to act for this organisation.</span>
  </label>
  <button type="submit">Confirm</button>
</form>
{{end}}
<p class="note">This service confirms control of the published email
address and records the declaration above. It performs no company
registry checks.</p>
</main>
</body>
</html>
`))

// confirmView is what the page renders.
type confirmView struct {
	Title    string
	Message  string
	Error    string
	Address  string
	ShowForm bool
}

// handleConfirmPage renders the declaration.
func handleConfirmPage(s *domain.Setup, _ *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cp, ok := s.Provider().(ConfirmingProvider)
		if !ok {
			http.NotFound(w, r)
			return
		}
		subject, found := cp.Pending(r.PathValue("token"))
		if !found {
			renderConfirm(w, http.StatusNotFound, confirmView{
				Title:   "Link not found",
				Message: "This link is not valid, or the verification has already been completed.",
			})
			return
		}
		renderConfirm(w, http.StatusOK, confirmView{
			Title:    "Confirm verification",
			Address:  subject.LegalName,
			ShowForm: true,
		})
	}
}

// handleConfirmSubmit records the declaration and drives the case on.
func handleConfirmSubmit(s *domain.Setup, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cp, ok := s.Provider().(ConfirmingProvider)
		if !ok {
			http.NotFound(w, r)
			return
		}
		token := r.PathValue("token")

		if err := r.ParseForm(); err != nil || r.PostForm.Get("accept") != "on" {
			subject, found := cp.Pending(token)
			if !found {
				renderConfirm(w, http.StatusNotFound, confirmView{Title: "Link not found"})
				return
			}
			renderConfirm(w, http.StatusUnprocessableEntity, confirmView{
				Title:    "Confirm verification",
				Error:    "Tick the box to confirm.",
				Address:  subject.LegalName,
				ShowForm: true,
			})
			return
		}

		reference, found := cp.Confirm(token)
		if !found {
			renderConfirm(w, http.StatusNotFound, confirmView{
				Title:   "Link not found",
				Message: "This link is not valid, or the verification has already been completed.",
			})
			return
		}

		// The declaration is recorded either way; only the step that
		// turns it into an endorsement can fail, and repeating the
		// link retries it.
		if err := s.Verifications().Advance(r.Context(), reference); err != nil {
			log.Warn("confirm.advance_failed", "reference", reference, "error", err.Error())
			status := http.StatusServiceUnavailable
			if errors.Is(err, domain.ErrNotFound) {
				status = http.StatusNotFound
			}
			renderConfirm(w, status, confirmView{
				Title:   "Confirmation saved",
				Message: "Your confirmation has been recorded, but the result could not be issued just yet. Open this link again in a few minutes.",
			})
			return
		}

		renderConfirm(w, http.StatusOK, confirmView{
			Title:   "Verification confirmed",
			Message: "Thank you. Your platform will collect the result and publish it; nothing further is needed from you.",
		})
	}
}

// renderConfirm writes the page. The URL is the credential, so it is
// never cached and never leaks as a referrer.
func renderConfirm(w http.ResponseWriter, status int, view confirmView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = confirmPage.Execute(w, view)
}
