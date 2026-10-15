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

// grantPlace is where a refused write is drawn on the access page.
type grantPlace int

const (
	atPage grantPlace = iota // the top alert
	atAdd                    // the add form of the posted kind
	atRow                    // the grant row of the posted principal
)

// grantFailure is a refused write, so the page draws the reason where it was
// asked and keeps what was posted.
type grantFailure struct {
	Place   grantPlace
	Kind    model.PrincipalKind
	ID      string // as posted, untrimmed
	Role    string // as posted
	Field   string // ValidationError.Field
	Message string
}

// access renders the board's grants, each with the form that changes it, and
// one add form per principal kind. The board is resolved with Board, whose
// RoleRead gate is not the owner check; the owner refusal comes from
// BoardGrants calling require(RoleOwner).
func (s *Server) access(w http.ResponseWriter, r *http.Request) {
	s.renderAccess(w, r, http.StatusOK, grantFailure{})
}

// renderAccess draws the access page. Every write on this page that is refused
// for a bad field comes back through here with what it posted, so the reason
// lands beside the field that asked and the typed value is still in it.
func (s *Server) renderAccess(w http.ResponseWriter, r *http.Request, status int, f grantFailure) {
	ctx := r.Context()
	b, err := s.svc.Board(ctx, r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	grants, err := s.svc.BoardGrants(ctx, b.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hints, err := s.svc.BoardGrantHints(ctx, b.ID, s.knownUsers(), s.knownGroups())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	groups, err := s.svc.Groups(ctx)
	groupAdmin := err == nil
	if err != nil {
		// A board owner who is not a group admin cannot see the group list, so
		// they get a text field for the group id rather than the page being
		// refused. Any other error is a real one.
		if !errors.Is(err, service.ErrForbidden) {
			s.fail(w, r, err)
			return
		}
		groups = nil
	}

	p := accessPage{
		Title:         b.Name + " · access",
		User:          identity.FromContext(ctx),
		BoardSlug:     b.Slug,
		Roles:         grantRoles,
		Groups:        groups,
		GroupAdmin:    groupAdmin,
		ProxyIdentity: hints.ProxyIdentity,
		ServiceGroup:  s.serviceGroup,
		AddUser:       addForm{Role: model.RoleRead},
		AddGroup:      addForm{Role: model.RoleRead},
		AddIDP:        addForm{Role: model.RoleRead},
	}

	names := make(map[model.ID]string, len(groups))
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	owners := 0
	for _, g := range grants {
		if g.Role == model.RoleOwner {
			owners++
		}
	}
	for _, g := range grants {
		v := grantView{
			Grant: g,
			Kind:  kindLabel(g.Principal.Kind),
			Name:  g.Principal.ID,
			Sole:  g.Role == model.RoleOwner && owners == 1,
		}
		// A group missing from the list was deleted between the two reads;
		// its id is still the honest thing to print.
		if n, ok := names[model.ID(g.Principal.ID)]; ok && g.Principal.Kind == model.PrincipalGroup {
			v.Name = n
		}
		p.Grants = append(p.Grants, v)
	}

	viewerNames := make(map[string]string, len(s.viewers))
	for _, v := range s.viewers {
		viewerNames[v.Email] = v.Name
	}
	for _, u := range hints.Users {
		p.Users = append(p.Users, suggestion{Value: u, Label: viewerNames[u]})
	}
	for _, g := range hints.IDPGroups {
		label := ""
		if g == s.serviceGroup {
			label = "service caller"
		}
		p.IDPGroups = append(p.IDPGroups, suggestion{Value: g, Label: label})
	}

	s.placeFailure(&p, f)
	p.Boards = s.navBoards(ctx)
	s.render(w, s.pages["access"], "layout", status, p)
}

// placeFailure puts a refused write's reason, and what it posted, where the
// page draws it.
func (s *Server) placeFailure(p *accessPage, f grantFailure) {
	if f.Message == "" {
		return
	}
	switch f.Place {
	case atAdd:
		var form *addForm
		switch f.Kind {
		case model.PrincipalUser:
			form = &p.AddUser
		case model.PrincipalGroup:
			form = &p.AddGroup
		case model.PrincipalIDP:
			form = &p.AddIDP
		default:
			p.Error = f.Message
			return
		}
		form.Value = f.ID
		if role := model.Role(f.Role); role.Valid() {
			form.Role = role
		}
		form.Error = f.Message
		form.BadID = f.Field == "id"
		form.BadRole = f.Field == "role"
	case atRow:
		// The row keeps the stored role, which is what is true; only the
		// reason is added. A row deleted in the meantime has nowhere to show
		// it, so it goes to the top.
		if want, err := principalOf(f.Kind, f.ID); err == nil {
			for i := range p.Grants {
				if p.Grants[i].Grant.Principal == want {
					p.Grants[i].Error = f.Message
					return
				}
			}
		}
		p.Error = f.Message
	default:
		p.Error = f.Message
	}
}

// knownUsers is the addresses configuration names: the AUTH_VIEWERS roster and
// the AVATARS keys. Display only; the service filters them.
func (s *Server) knownUsers() []string {
	var out []string
	for _, v := range s.viewers {
		out = append(out, v.Email)
	}
	if s.avatars != nil {
		for addr := range s.avatars.logins {
			out = append(out, addr)
		}
	}
	return out
}

// knownGroups is the configured service group, when there is one.
func (s *Server) knownGroups() []string {
	if s.serviceGroup == "" {
		return nil
	}
	return []string{s.serviceGroup}
}

// accessFailure puts a rejected grant change back on the page with the reason,
// rather than answering with a bare status the form has nowhere to show. place
// says which form asked.
func (s *Server) accessFailure(w http.ResponseWriter, r *http.Request, err error, place grantPlace) {
	f := grantFailure{
		Place: place,
		Kind:  model.PrincipalKind(r.FormValue("kind")),
		ID:    r.FormValue("id"),
		Role:  r.FormValue("role"),
	}
	var ve *service.ValidationError
	switch {
	case errors.As(err, &ve):
		f.Field, f.Message = ve.Field, ve.Message
		if ve.Field == "kind" {
			// No form exists for a kind that is not one of the three.
			f.Place = atPage
		}
		s.renderAccess(w, r, http.StatusBadRequest, f)
	case errors.Is(err, store.ErrConflict):
		// Not expected on this page — a grant replaces rather than conflicts —
		// but handled for symmetry with settings so a store that reports it
		// re-renders rather than falling to a 500.
		f.Place, f.Message = atPage, "that grant already exists"
		s.renderAccess(w, r, http.StatusConflict, f)
	case errors.Is(err, store.ErrNotFound):
		f.Place, f.Message = atPage, "something this form named is not here any more; it may have just changed"
		s.renderAccess(w, r, http.StatusNotFound, f)
	default:
		// 403 from the owner check, 500 from a store that will not answer:
		// s.fail owns those.
		s.fail(w, r, err)
	}
}

// principalOf builds a model.Principal from a kind and an id. An unknown kind
// is a 400 the service never sees; the known kinds route through the model
// constructors, which lowercase and trim.
func principalOf(kind model.PrincipalKind, id string) (model.Principal, error) {
	switch kind {
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

// principalFromForm reads the kind and id fields into a model.Principal.
func principalFromForm(r *http.Request) (model.Principal, error) {
	return principalOf(model.PrincipalKind(r.FormValue("kind")), r.FormValue("id"))
}

// setGrant adds or replaces a grant. The add forms and the grant rows both post
// here; a row says so with from=row, which only decides where a refusal is
// drawn and never what is written.
func (s *Server) setGrant(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	place := atAdd
	if r.FormValue("from") == "row" {
		place = atRow
	}
	p, err := principalFromForm(r)
	if err != nil {
		s.accessFailure(w, r, err, place)
		return
	}
	// The id goes to the service as it was posted. SetBoardGrant checks it is
	// storable text before it lowercases, and lowercasing first would turn a
	// byte that is not UTF-8 into a replacement character it then accepts.
	p.ID = r.FormValue("id")
	if err := s.svc.SetBoardGrant(r.Context(), b.ID, p, model.Role(r.FormValue("role"))); err != nil {
		s.accessFailure(w, r, err, place)
		return
	}
	s.afterGrantWrite(w, r, b)
}

// deleteGrant removes a grant.
func (s *Server) deleteGrant(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := principalFromForm(r)
	if err != nil {
		s.accessFailure(w, r, err, atRow)
		return
	}
	if err := s.svc.RemoveBoardGrant(r.Context(), b.ID, p); err != nil {
		s.accessFailure(w, r, err, atRow)
		return
	}
	s.afterGrantWrite(w, r, b)
}

// afterGrantWrite sends the caller where they can still go: the access page
// while they own the board, the board while they can read it, the board list
// otherwise. An owner who demotes or removes themselves would otherwise land
// on a 403.
func (s *Server) afterGrantWrite(w http.ResponseWriter, r *http.Request, b *model.Board) {
	to := "/b/" + b.Slug + "/access"
	role, err := s.svc.RoleOn(r.Context(), identity.FromContext(r.Context()), b.ID)
	switch {
	case err != nil:
		// Keep the access page; the GET that follows reports the store error
		// through s.fail, which logs it.
	case !role.Allows(model.RoleRead):
		to = "/boards"
	case !role.Allows(model.RoleOwner):
		to = "/b/" + b.Slug
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
