// The board access page: who may read, write and own this board. Owner only.
// The page and its two writes resolve the board, then call a grant service
// method whose first act is require(ctx, boardID, RoleOwner); a reader or a
// stranger is refused there and turned into a 403 by s.fail, so no handler
// here checks a role itself. This is the handoff's "do not invent a parallel
// auth check in the handler".

package web

import (
	"errors"
	"net/http"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/service"
	"byge/internal/store"
)

// access renders the board's grants and the form that adds one. The board is
// resolved with Board, whose RoleRead gate is not the owner check; the owner
// refusal comes from BoardGrants calling require(RoleOwner).
func (s *Server) access(w http.ResponseWriter, r *http.Request) {
	s.renderAccess(w, r, http.StatusOK, "")
}

// renderAccess draws the access page, with an optional message. Every write on
// this page that is refused for a bad field comes back through here, so the
// reason lands on the page that asked.
func (s *Server) renderAccess(w http.ResponseWriter, r *http.Request, status int, message string) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	grants, err := s.svc.BoardGrants(r.Context(), b.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	groups, err := s.svc.Groups(r.Context())
	if err != nil {
		// A board owner who is not a group admin cannot see the group list, so
		// the datalist is simply empty rather than the page being refused. Any
		// other error is a real one.
		if !errors.Is(err, service.ErrForbidden) {
			s.fail(w, r, err)
			return
		}
		groups = nil
	}
	p := accessPage{
		Title:     b.Name + " · access",
		User:      identity.FromContext(r.Context()),
		BoardSlug: b.Slug,
		Roles:     grantRoles,
		Kinds:     grantKinds,
		Groups:    groups,
		Error:     message,
	}
	for _, g := range grants {
		p.Grants = append(p.Grants, grantView{Grant: g, Kind: kindLabel(g.Principal.Kind)})
	}
	p.Boards = s.navBoards(r.Context())
	s.render(w, s.pages["access"], "layout", status, p)
}

// accessFailure puts a rejected grant change back on the page with the reason,
// rather than answering with a bare status the form has nowhere to show.
func (s *Server) accessFailure(w http.ResponseWriter, r *http.Request, err error) {
	var ve *service.ValidationError
	switch {
	case errors.As(err, &ve):
		s.renderAccess(w, r, http.StatusBadRequest, ve.Message)
	case errors.Is(err, store.ErrConflict):
		// Not expected on this page — a grant replaces rather than conflicts —
		// but handled for symmetry with settings so a store that reports it
		// re-renders rather than falling to a 500.
		s.renderAccess(w, r, http.StatusConflict, "that grant already exists")
	case errors.Is(err, store.ErrNotFound):
		s.renderAccess(w, r, http.StatusNotFound,
			"something this form named is not here any more; it may have just changed")
	default:
		// 403 from the owner check, 500 from a store that will not answer:
		// s.fail owns those.
		s.fail(w, r, err)
	}
}

// principalFromForm reads the kind and id fields into a model.Principal. An
// unknown kind is a 400 the service never sees; the known kinds route through
// the model constructors, which lowercase and trim.
func principalFromForm(r *http.Request) (model.Principal, error) {
	id := r.FormValue("id")
	switch model.PrincipalKind(r.FormValue("kind")) {
	case model.PrincipalUser:
		return model.UserPrincipal(id), nil
	case model.PrincipalGroup:
		return model.GroupPrincipal(model.ID(id)), nil
	case model.PrincipalIDP:
		return model.IDPPrincipal(id), nil
	default:
		return model.Principal{}, &service.ValidationError{Field: "kind", Message: "unknown kind"}
	}
}

// setGrant adds or replaces a grant, then redirects back to the access page.
func (s *Server) setGrant(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := principalFromForm(r)
	if err != nil {
		s.accessFailure(w, r, err)
		return
	}
	if err := s.svc.SetBoardGrant(r.Context(), b.ID, p, model.Role(r.FormValue("role"))); err != nil {
		s.accessFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/b/"+b.Slug+"/access", http.StatusSeeOther)
}

// deleteGrant removes a grant, then redirects back to the access page.
func (s *Server) deleteGrant(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := principalFromForm(r)
	if err != nil {
		s.accessFailure(w, r, err)
		return
	}
	if err := s.svc.RemoveBoardGrant(r.Context(), b.ID, p); err != nil {
		s.accessFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/b/"+b.Slug+"/access", http.StatusSeeOther)
}
