package service

import (
	"context"
	"errors"
	"testing"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/store"
)

// The board keeps an owner: the last owner grant cannot be demoted or removed,
// because a board with no owner is visible to nobody and fixable only in the
// database.
func TestLastOwnerCannotBeRemovedOrDemoted(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	owner := model.UserPrincipal("owner@example.com")

	// Demoting the only owner to read is refused, and the grant is untouched.
	err := k.SetBoardGrant(ownerCtx(), b.ID, owner, model.RoleRead)
	if !isValidation(err, "role") {
		t.Fatalf("SetBoardGrant demoting last owner = %v, want a role ValidationError", err)
	}
	grants, _ := k.store.ListGrants(context.Background(), b.ID)
	if len(grants) != 1 || grants[0].Role != model.RoleOwner {
		t.Fatalf("the last owner grant was changed: %+v", grants)
	}

	// Removing the only owner is refused the same way.
	err = k.RemoveBoardGrant(ownerCtx(), b.ID, owner)
	if !isValidation(err, "role") {
		t.Fatalf("RemoveBoardGrant of last owner = %v, want a role ValidationError", err)
	}
	grants, _ = k.store.ListGrants(context.Background(), b.ID)
	if len(grants) != 1 {
		t.Fatalf("the last owner grant was removed: %+v", grants)
	}
}

// A non-last owner may be demoted, self-demotion included, as long as another
// owner remains.
func TestANonLastOwnerCanBeDemoted(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	owner := model.UserPrincipal("owner@example.com")
	second := model.UserPrincipal("second@example.com")

	// A second owner, then the first demotes themselves. Another owner remains,
	// so it is allowed.
	if err := k.SetBoardGrant(ownerCtx(), b.ID, second, model.RoleOwner); err != nil {
		t.Fatalf("granting a second owner: %v", err)
	}
	if err := k.SetBoardGrant(ownerCtx(), b.ID, owner, model.RoleWrite); err != nil {
		t.Fatalf("self-demotion with another owner present = %v, want nil", err)
	}
	role, err := k.RoleOn(context.Background(), identity.User{Email: "owner@example.com"}, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if role != model.RoleWrite {
		t.Errorf("the demotion did not take: role = %q, want write", role)
	}

	// And the remaining owner may remove the now-non-owner grant.
	if err := k.RemoveBoardGrant(identity.NewContext(context.Background(),
		identity.User{Email: "second@example.com"}), b.ID, owner); err != nil {
		t.Fatalf("removing a non-owner grant = %v, want nil", err)
	}
}

// A grant to a local group that does not exist is a validation error on the id
// field, not the store's ErrNotFound, so the add form re-renders at 400 rather
// than reading as "the board is gone".
func TestSetBoardGrantUnknownGroupIsValidationError(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")

	err := k.SetBoardGrant(ownerCtx(), b.ID, model.GroupPrincipal("nope"), model.RoleRead)
	if !isValidation(err, "id") {
		t.Fatalf("grant to an unknown group = %v, want an id ValidationError", err)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Error("the store's ErrNotFound leaked out instead of a ValidationError")
	}
	grants, _ := k.store.ListGrants(context.Background(), b.ID)
	if len(grants) != 1 {
		t.Errorf("a grant was written for a group that does not exist: %+v", grants)
	}
}

// A grant to a local group that does exist is allowed and written.
func TestSetBoardGrantKnownGroup(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	g, err := k.CreateGroup(ownerCtx(), "Platform", []string{"ada@example.com"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := k.SetBoardGrant(ownerCtx(), b.ID, model.GroupPrincipal(g.ID), model.RoleWrite); err != nil {
		t.Fatalf("grant to a known group = %v, want nil", err)
	}
	grants, _ := k.store.ListGrants(context.Background(), b.ID)
	if len(grants) != 2 {
		t.Fatalf("the group grant was not written: %+v", grants)
	}
}

// Every grant method is owner-only. A reader, a writer, a stranger and an
// anonymous caller are all refused.
func TestGrantMethodsAreOwnerOnly(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	reader := grantedCtx(t, k, b.ID, "reader@example.com", model.RoleRead)
	writer := grantedCtx(t, k, b.ID, "writer@example.com", model.RoleWrite)
	stranger := identity.NewContext(context.Background(), identity.User{Email: "stranger@example.com"})
	anon := context.Background()

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"reader", reader},
		{"writer", writer},
		{"stranger", stranger},
		{"anonymous", anon},
	} {
		if _, err := k.BoardGrants(tc.ctx, b.ID); err != ErrForbidden {
			t.Errorf("%s BoardGrants = %v, want ErrForbidden", tc.name, err)
		}
		if err := k.SetBoardGrant(tc.ctx, b.ID, model.UserPrincipal("x@example.com"), model.RoleRead); err != ErrForbidden {
			t.Errorf("%s SetBoardGrant = %v, want ErrForbidden", tc.name, err)
		}
		if err := k.RemoveBoardGrant(tc.ctx, b.ID, model.UserPrincipal("owner@example.com")); err != ErrForbidden {
			t.Errorf("%s RemoveBoardGrant = %v, want ErrForbidden", tc.name, err)
		}
	}
}
