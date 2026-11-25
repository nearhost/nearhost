package documents

import (
	"net/http"

	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/report/htmlview"
)

// missingPage says the file at path on server isn't there any more. The
// caller has set the isolation headers.
func missingPage(w http.ResponseWriter, r *http.Request, server, path string) {
	m := report.Errorf(report.DocumentsMissing, "This file isn't there any more").
		Because("%s on %s isn't there any more. It may have been moved, renamed or deleted.", path, server)
	htmlview.WriteHTML(w, r, m, http.StatusNotFound, "")
}

// changingPage says the file at path on server, or a folder above it, kept
// changing while Nearhost checked it, so nothing was read.
func changingPage(w http.ResponseWriter, r *http.Request, server, path string) {
	w.Header().Set("Retry-After", "1")
	m := report.Errorf(report.DocumentsChanging, "This file's folder keeps changing").
		Because("%s on %s or a folder above it changed each time Nearhost checked it, so Nearhost read nothing.", path, server).
		Then("Something is writing there. Reload this page once it has finished.")
	htmlview.WriteHTML(w, r, m, http.StatusServiceUnavailable, "")
}

// notConnectedPage says Nearhost isn't connected to server, and how to
// connect it.
func notConnectedPage(w http.ResponseWriter, r *http.Request, server string) {
	m := report.Errorf(report.DocumentsNotConnected, "Nearhost isn't connected to %s", server).
		Then("To read this file, select " + server + " in Nearhost and press c to connect it, then reload this page.")
	htmlview.WriteHTML(w, r, m, http.StatusServiceUnavailable, "")
}

// refuse answers r with a plain refusal as a message: a page for a
// browser, text for anything else.
func refuse(w http.ResponseWriter, r *http.Request, code report.Code, title string, status int) {
	htmlview.Write(w, r, report.Errorf(code, "%s", title), status, "")
}
