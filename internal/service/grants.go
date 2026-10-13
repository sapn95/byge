// Board grants: who may read, write and own a board. Every method here is
// owner-only, through require(ctx, boardID, RoleOwner) as its first act, so a
// reader or a stranger is refused in the service and not in a handler. This is
// the stage-3 rule applied to the one page that hands out roles.

package service

import (
	"context"
	"errors"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/store"
)

// BoardGrants lists a board's grants, each a principal and its role. Owner
// only: the page that lists who may do what is itself one of the things only
// an owner may see.
func (k *Byge) BoardGrants(ctx context.Context, boardID model.ID) ([]model.Grant, error) {
	if err := k.require(ctx, boardID, model.RoleOwner); err != nil {
		return nil, err
	}
	return k.store.ListGrants(ctx, boardID)
}

// SetBoardGrant writes one principal's role on a board, replacing whatever that
// principal held. Owner only.
//
// A grant to a local group names a group this database keeps, so the group has
// to exist. The store refuses an unknown group with ErrNotFound, which a
// handler would turn into a 404 that reads as "the board is gone"; this checks
// the group first and turns the miss into a validation error, so the add form
// re-renders at 400 with the reason beside the field.
//
// The last owner cannot be demoted away: a board that exists must have an
// owner, or it is visible to nobody and fixable only in the database.
func (k *Byge) SetBoardGrant(ctx context.Context, boardID model.ID, p model.Principal, role model.Role) error {
	if err := k.require(ctx, boardID, model.RoleOwner); err != nil {
		return err
	}
	if !p.Valid() {
		return invalid("id", "not a valid principal")
	}
	if !role.Valid() {
		return invalid("role", "must be read, write or owner")
	}
	if p.Kind == model.PrincipalGroup {
		if _, err := k.store.GetGroup(ctx, model.ID(p.ID)); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return invalid("id", "no such local group")
			}
			return err
		}
	}
	// The post-change owner set: this principal takes the new role, everybody
	// else keeps theirs. If that leaves nobody holding owner, the change is the
	// one that strands the board, so it is refused. Demoting a non-last owner,
	// self-demotion included, is allowed.
	grants, err := k.store.ListGrants(ctx, boardID)
	if err != nil {
		return err
	}
	owners := 0
	for _, g := range grants {
		if g.Principal == p {
			continue // replaced by role below
		}
		if g.Role == model.RoleOwner {
			owners++
		}
	}
	if role == model.RoleOwner {
		owners++
	}
	if owners == 0 {
		return invalid("role", "a board must keep an owner")
	}
	return k.store.SetGrant(ctx, &model.Grant{BoardID: boardID, Principal: p, Role: role})
}

// ClaimBoard makes the bootstrap owner the owner of a board that has no owner.
// It is how an ownerless board — the default board EnsureDefaultBoard writes
// with no grant, or any board left ownerless — gets its first owner, and it is
// the only way to do so.
//
// Only the configured AUTH_BOOTSTRAP_OWNER may call it, enforced here and not in
// a handler, consistent with every other grant method. It refuses a board that
// already has an owner: claiming is for the ownerless, and a board with an owner
// is changed through SetBoardGrant by that owner, not taken over from config.
//
// The check-then-write (the ListGrants scan, then SetGrant) is not atomic:
// nothing holds a lock between the two. It is safe rather than a second
// uncontrolled owner, because every writer that reaches here sets the identical
// user:<bootstrap> → owner grant and SetGrant replaces rather than appends on
// the principal key, so the worst case is a redundant write of the same grant;
// SetBoardGrant's last-owner guard then governs the board from the first write.
func (k *Byge) ClaimBoard(ctx context.Context, boardID model.ID) error {
	if !k.isBootstrapOwner(ctx) {
		return ErrForbidden
	}
	grants, err := k.store.ListGrants(ctx, boardID)
	if err != nil {
		return err
	}
	for _, g := range grants {
		if g.Role == model.RoleOwner {
			// Already owned. The bootstrap owner is not an owner of it unless a
			// grant says so, so this is refused rather than silently adding a
			// second owner from config.
			return ErrForbidden
		}
	}
	return k.store.SetGrant(ctx, &model.Grant{
		BoardID:   boardID,
		Principal: model.UserPrincipal(identity.FromContext(ctx).Email),
		Role:      model.RoleOwner,
	})
}

// RemoveBoardGrant takes a principal off a board. Owner only, and it will not
// remove the last owner for the same reason SetBoardGrant will not demote one.
func (k *Byge) RemoveBoardGrant(ctx context.Context, boardID model.ID, p model.Principal) error {
	if err := k.require(ctx, boardID, model.RoleOwner); err != nil {
		return err
	}
	// The owner set after dropping this principal. If removing it would leave
	// no owner, refuse.
	grants, err := k.store.ListGrants(ctx, boardID)
	if err != nil {
		return err
	}
	owners := 0
	for _, g := range grants {
		if g.Principal == p {
			continue
		}
		if g.Role == model.RoleOwner {
			owners++
		}
	}
	if owners == 0 {
		return invalid("role", "a board must keep an owner")
	}
	return k.store.DeleteGrant(ctx, boardID, p)
}
