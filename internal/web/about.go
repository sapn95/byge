package web

import (
	"net/http"

	"byge/internal/identity"
)

// aboutPage is what /about draws: what this board is, whose work it is built
// on, and under what licence.
type aboutPage struct {
	Title string
	User  identity.User
	// BoardSlug is what layout.html reads for the body attribute. There is no
	// board here, and an empty one leaves the attribute off; offlinePage
	// carries it for the same reason.
	BoardSlug string
	// Upstream is the project this one was forked from.
	Upstream string
	// Vendored and Modules are the things somebody else wrote.
	Vendored []credit
	Modules  []credit
}

// about serves the attribution page.
//
// A board that is somebody's fork, published under somebody else's licence,
// and shipping three other people's files has somewhere it says so or it does
// not say so at all. The readme is where a reader of the repository looks; a
// page in the application is where a reader of the application looks, and
// those are not the same person.
//
// It reads no board and no identity, so it answers whatever the storage is
// doing, exactly like /offline. It is not open to everybody: it sits behind
// the identity middleware like every other page, and AUTH_REQUIRED refuses it
// to a caller it cannot name. Only /healthz and /readyz are exempt from that,
// and this is not one of them.
func (s *Server) about(w http.ResponseWriter, r *http.Request) {
	s.render(w, s.pages["about"], "layout", http.StatusOK, aboutPage{
		Title:    "About",
		User:     identity.FromContext(r.Context()),
		Upstream: "https://github.com/NicolasHaas/golang-kanban",
		Vendored: vendoredCredits(),
		Modules:  goCredits(),
	})
}
