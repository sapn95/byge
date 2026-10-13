// The local groups page: the groups kept in byge's own database, which a grant
// on any board can name. Only the bootstrap owner may manage them; everyone
// else — anonymous, service token, or an ordinary signed-in caller — is refused
// by requireGroupAdmin (ErrForbidden → 403 through s.fail), so no handler here
// checks identity.

package web

import (
	"errors"
	"net/http"
	"strings"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/service"
	"byge/internal/store"
)

// groups renders every local group and the form that makes another.
func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	s.renderGroups(w, r, http.StatusOK, "")
}

// renderGroups draws the groups page, with an optional message. A write refused
// for a bad field comes back through here so the reason is shown beside the
// form.
func (s *Server) renderGroups(w http.ResponseWriter, r *http.Request, status int, message string) {
	list, err := s.svc.Groups(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := groupsPage{
		Title: "Local groups",
		User:  identity.FromContext(r.Context()),
		Error: message,
	}
	for _, g := range list {
		p.Groups = append(p.Groups, groupView{
			Group:   g,
			Members: len(g.Members),
			Lines:   strings.Join(g.Members, "\n"),
		})
	}
	p.Boards = s.navBoards(r.Context())
	s.render(w, s.pages["groups"], "layout", status, p)
}

// groupsFailure puts a rejected group change back on the page with the reason.
func (s *Server) groupsFailure(w http.ResponseWriter, r *http.Request, err error) {
	var ve *service.ValidationError
	switch {
	case errors.As(err, &ve):
		s.renderGroups(w, r, http.StatusBadRequest, ve.Message)
	case errors.Is(err, store.ErrConflict):
		s.renderGroups(w, r, http.StatusConflict, "a group with that name already exists")
	case errors.Is(err, store.ErrNotFound):
		s.renderGroups(w, r, http.StatusNotFound,
			"that group is not here any more; it may have just been removed")
	default:
		// 403 from requireGroupAdmin, 500 from a store that will not answer.
		s.fail(w, r, err)
	}
}

// members splits the textarea into one address per line. The service trims,
// lowercases, deduplicates and length-checks them, so this only has to break
// the field into lines.
func members(r *http.Request) []string {
	return strings.Split(r.FormValue("members"), "\n")
}

// createGroup makes a group, then redirects to the groups page.
func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	if _, err := s.svc.CreateGroup(r.Context(), r.FormValue("name"), members(r)); err != nil {
		s.groupsFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/groups", http.StatusSeeOther)
}

// updateGroup renames a group and replaces its membership, then redirects.
func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.UpdateGroup(r.Context(), model.ID(r.PathValue("id")), r.FormValue("name"), members(r)); err != nil {
		s.groupsFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/groups", http.StatusSeeOther)
}

// deleteGroup removes a group and every grant made to it, then redirects.
func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteGroup(r.Context(), model.ID(r.PathValue("id"))); err != nil {
		s.groupsFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/groups", http.StatusSeeOther)
}
