package service

import (
	"context"
	"errors"

	"byge/internal/identity"
	"byge/internal/model"
)

// ErrForbidden is a caller who was identified and may not do this. It is not
// "who are you": that is identity's refusal and it happens before any of
// this. A handler turns it into 403 and never into 401, because there is no
// scheme the caller could satisfy by asking again.
var ErrForbidden = errors.New("not allowed on this board")

// Principals is everything a caller is, for matching against grants: the
// address itself, every group kept here that the address belongs to, and
// every group name the identity provider sent.
//
// A caller with no address is no principals, even when the provider sent
// groups: a group name arriving beside no identity is a header with nobody
// behind it. A service token is no principals either, for the same reason it
// is not a person, so a token reaches a board only through a grant to the
// address its deployment gives it, which it has not got. That is deliberate:
// a machine credential that could read every board the moment grants exist
// would be the hole this change is closing.
func (k *Byge) Principals(ctx context.Context, u identity.User) ([]model.Principal, error) {
	if !u.Person() {
		return nil, nil
	}
	out := []model.Principal{model.UserPrincipal(u.Email)}
	for _, g := range u.Groups {
		if p := model.IDPPrincipal(g); p.Valid() {
			out = append(out, p)
		}
	}
	ids, err := k.store.GroupsOf(ctx, u.Email)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out = append(out, model.GroupPrincipal(id))
	}
	return out, nil
}

// RoleOn is what this caller may do with this board, "" for nothing at all.
func (k *Byge) RoleOn(ctx context.Context, u identity.User, boardID model.ID) (model.Role, error) {
	ps, err := k.Principals(ctx, u)
	if err != nil {
		return "", err
	}
	if len(ps) == 0 {
		return "", nil
	}
	roles, err := k.store.BoardRoles(ctx, ps)
	if err != nil {
		return "", err
	}
	return roles[boardID], nil
}

// VisibleBoards is every board this caller holds any role on, with the role.
// The board list and the search both read it, so that neither has to ask per
// board and neither can forget to filter.
func (k *Byge) VisibleBoards(ctx context.Context, u identity.User) (map[model.ID]model.Role, error) {
	ps, err := k.Principals(ctx, u)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return map[model.ID]model.Role{}, nil
	}
	return k.store.BoardRoles(ctx, ps)
}

// require refuses unless the caller in ctx holds at least need on this board.
//
// The caller comes out of the context rather than being passed in, because
// every one of these methods already takes a context and threading a user
// through thirty-six signatures would be thirty-six chances to pass the wrong
// one. A context with no user is a caller with no identity, which holds
// nothing.
func (k *Byge) require(ctx context.Context, boardID model.ID, need model.Role) error {
	role, err := k.RoleOn(ctx, identity.FromContext(ctx), boardID)
	if err != nil {
		return err
	}
	if !role.Allows(need) {
		return ErrForbidden
	}
	return nil
}
