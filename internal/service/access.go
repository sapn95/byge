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

// Principals is everything a caller is, for matching against grants.
//
// A caller with an address is that address, every local group kept here that
// holds the address, and every idp group the provider sent, in that order. A
// caller with no address carries no user principal and no local-group
// membership — both need an address — but it does carry any idp groups that
// arrived with the request. The C1 service identity is exactly that: a
// non-person with a configured idp group, grantable through the access page. A
// machine credential with no configured group (a Cloudflare service token)
// carries no group, so it is still no principals and reaches no board, which is
// the hole stages 3-5 closed.
func (k *Byge) Principals(ctx context.Context, u identity.User) ([]model.Principal, error) {
	if !u.Person() {
		// A caller with no address is not a user principal and has no local
		// group membership (both need an address). It can still carry idp
		// groups that arrived with the request — the C1 service identity does.
		// A Cloudflare service token carries none, so it stays principal-less
		// and reaches no board, which is the hole stages 3-5 closed.
		return idpPrincipals(u.Groups), nil
	}
	out := []model.Principal{model.UserPrincipal(u.Email)}
	// Order is load-bearing: TestPrincipals asserts [user, idp, group] by
	// index. idpPrincipals is appended after the user principal and before the
	// GroupsOf loop; do not move it to the end of the function.
	out = append(out, idpPrincipals(u.Groups)...)
	ids, err := k.store.GroupsOf(ctx, u.Email)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out = append(out, model.GroupPrincipal(id))
	}
	return out, nil
}

// idpPrincipals turns the group names that arrived with the request into idp
// principals, dropping any that are not valid.
func idpPrincipals(groups []string) []model.Principal {
	out := make([]model.Principal, 0, len(groups))
	for _, g := range groups {
		if p := model.IDPPrincipal(g); p.Valid() {
			out = append(out, p)
		}
	}
	return out
}

// addrIsBootstrapOwner reports whether u is the configured AUTH_BOOTSTRAP_OWNER.
// Empty config, an anonymous caller or a service token is never the bootstrap
// owner: Person() gates the token out the same way Principals does, and an empty
// k.bootstrapOwner matches nobody because the address is compared, not its
// emptiness. This is the single security-critical predicate; isBootstrapOwner is
// a thin ctx wrapper over it so ClaimBoard (ctx) and RoleOn (explicit u) decide
// against the same principal with the same normalisation.
func (k *Byge) addrIsBootstrapOwner(u identity.User) bool {
	if k.bootstrapOwner == "" || !u.Person() {
		return false
	}
	return model.UserPrincipal(u.Email).ID == k.bootstrapOwner
}

// isBootstrapOwner reports whether the caller in ctx is the bootstrap owner.
func (k *Byge) isBootstrapOwner(ctx context.Context) bool {
	return k.addrIsBootstrapOwner(identity.FromContext(ctx))
}

// boardHasOwner reports whether any grant on the board is RoleOwner. The error
// is returned, never discarded: a store that will not answer must not be read
// as "no owner", which would hand the bootstrap owner a board whose owner the
// error was hiding. A board that does not exist returns the store's
// ErrNotFound rather than (false, nil), so a missing board is an error, not an
// ownerless board the bootstrap owner may claim.
func (k *Byge) boardHasOwner(ctx context.Context, boardID model.ID) (bool, error) {
	grants, err := k.store.ListGrants(ctx, boardID)
	if err != nil {
		return false, err
	}
	for _, g := range grants {
		if g.Role == model.RoleOwner {
			return true, nil
		}
	}
	return false, nil
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
	if r := roles[boardID]; r != "" {
		return r, nil
	}
	// A board the caller holds no grant on. The bootstrap owner is treated as
	// its owner only while it has no owner of its own, which is what lets them
	// reach it to claim it; everyone else gets nothing, so an ownerless board
	// leaks to no arbitrary caller. Checked only on the miss, so it costs a
	// grant read for the bootstrap owner alone and nothing for anybody else.
	// The decision is against RoleOn's explicit u, not the ctx user, so the
	// predicate used here is the user-based addrIsBootstrapOwner.
	if k.addrIsBootstrapOwner(u) {
		owned, err := k.boardHasOwner(ctx, boardID)
		if err != nil {
			return "", err
		}
		if !owned {
			return model.RoleOwner, nil
		}
	}
	return "", nil
}

// OwnerlessBoards lists the boards with no owner grant, for the bootstrap owner
// to claim. Empty for every other caller, so an ownerless board is a thing only
// the bootstrap owner can even see exists. Not board-scoped and refuses nothing;
// it filters, like VisibleBoardList, so it is named in allowNoRequire.
//
// The body is ListBoards plus one boardHasOwner per board, written on the same
// boardHasOwner the RoleOn ownerless branch uses, so the two deciders of "is
// this board ownerless" cannot drift. A store read error from either ListBoards
// or boardHasOwner is returned, never swallowed into a shorter list, for the
// same reason boardHasOwner returns its error: a blip must not read as
// "ownerless". The N+1 is accepted — the ownerless set the Pi recovery targets
// is the single default board — and replacing it with a bespoke store query is
// forbidden without a test pinning OwnerlessBoards to the RoleOn branch.
func (k *Byge) OwnerlessBoards(ctx context.Context) ([]model.Board, error) {
	if !k.isBootstrapOwner(ctx) {
		return nil, nil
	}
	all, err := k.store.ListBoards(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.Board
	for _, b := range all {
		owned, err := k.boardHasOwner(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		if !owned {
			out = append(out, b)
		}
	}
	return out, nil
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

// VisibleBoardList is every board the caller in ctx may read, in the order the
// store lists them. It is what the board list and the switcher draw: Boards
// lists every board regardless of role, so a layer that shows the list to a
// caller filters it through this first, or it shows a caller the name of a
// board they hold nothing on.
//
// A caller with no principals sees nothing, which is an empty list and not an
// error: an anonymous reader on a board with no grants is not refused the
// front page, they are shown that there is nothing for them on it yet.
func (k *Byge) VisibleBoardList(ctx context.Context) ([]model.Board, error) {
	visible, err := k.VisibleBoards(ctx, identity.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	if len(visible) == 0 {
		return nil, nil
	}
	all, err := k.store.ListBoards(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Board, 0, len(visible))
	for _, b := range all {
		if _, ok := visible[b.ID]; ok {
			out = append(out, b)
		}
	}
	return out, nil
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
