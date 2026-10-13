package service

import (
	"context"
	"errors"
	"testing"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/store"
)

// requireGroupAdmin is any signed-in person: an anonymous caller and a service
// token are refused, a person is let through. Exercised through Groups and
// CreateGroup, the two readable from a test without a store write.
func TestGroupAdminIsAnySignedInPerson(t *testing.T) {
	k := newSvc(t)

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"anonymous", context.Background()},
		{"service token", identity.NewContext(context.Background(), identity.User{Service: "ci"})},
	} {
		if _, err := k.Groups(tc.ctx); err != ErrForbidden {
			t.Errorf("%s Groups = %v, want ErrForbidden", tc.name, err)
		}
		if _, err := k.CreateGroup(tc.ctx, "x", nil); err != ErrForbidden {
			t.Errorf("%s CreateGroup = %v, want ErrForbidden", tc.name, err)
		}
	}

	// A person is let through.
	person := identity.NewContext(context.Background(), identity.User{Email: "ada@example.com"})
	if _, err := k.Groups(person); err != nil {
		t.Errorf("person Groups = %v, want nil", err)
	}
}

// CreateGroup lowercases, trims and deduplicates members, so a group holds each
// address once in the form a grant match expects.
func TestCreateGroupNormalisesMembers(t *testing.T) {
	k := newSvc(t)
	ctx := identity.NewContext(context.Background(), identity.User{Email: "ada@example.com"})

	g, err := k.CreateGroup(ctx, "  Platform  ", []string{"Ada@Example.com", "  bob@example.com  ", "ada@example.com", ""})
	if err != nil {
		t.Fatalf("CreateGroup = %v, want nil", err)
	}
	if g.Name != "Platform" {
		t.Errorf("name = %q, want trimmed", g.Name)
	}
	want := []string{"ada@example.com", "bob@example.com"}
	if len(g.Members) != len(want) {
		t.Fatalf("members = %v, want %v", g.Members, want)
	}
	seen := map[string]bool{}
	for _, m := range g.Members {
		if seen[m] {
			t.Errorf("member %q appears twice", m)
		}
		seen[m] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("member %q missing from %v", w, g.Members)
		}
	}
}

// An empty name is refused; a duplicate name is a store conflict.
func TestCreateGroupNameRules(t *testing.T) {
	k := newSvc(t)
	ctx := identity.NewContext(context.Background(), identity.User{Email: "ada@example.com"})

	if _, err := k.CreateGroup(ctx, "   ", nil); !isValidation(err, "name") {
		t.Errorf("empty name = %v, want a name ValidationError", err)
	}
	if _, err := k.CreateGroup(ctx, "Platform", nil); err != nil {
		t.Fatalf("first create = %v, want nil", err)
	}
	_, err := k.CreateGroup(ctx, "Platform", nil)
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate name = %v, want store.ErrConflict", err)
	}
}

// Groups lists what was created, so the page has something to draw.
func TestGroupsListsWhatWasCreated(t *testing.T) {
	k := newSvc(t)
	ctx := identity.NewContext(context.Background(), identity.User{Email: "ada@example.com"})
	if _, err := k.CreateGroup(ctx, "Platform", nil); err != nil {
		t.Fatal(err)
	}
	list, err := k.Groups(ctx)
	if err != nil {
		t.Fatalf("Groups = %v, want nil", err)
	}
	if len(list) != 1 || list[0].Name != "Platform" {
		t.Errorf("Groups = %+v, want one named Platform", list)
	}
}

// UpdateGroup and DeleteGroup are guarded the same way. An anonymous caller is
// refused both.
func TestUpdateAndDeleteGroupAreGuarded(t *testing.T) {
	k := newSvc(t)
	ctx := identity.NewContext(context.Background(), identity.User{Email: "ada@example.com"})
	g, err := k.CreateGroup(ctx, "Platform", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.UpdateGroup(context.Background(), g.ID, "Renamed", nil); err != ErrForbidden {
		t.Errorf("anonymous UpdateGroup = %v, want ErrForbidden", err)
	}
	if err := k.DeleteGroup(context.Background(), g.ID); err != ErrForbidden {
		t.Errorf("anonymous DeleteGroup = %v, want ErrForbidden", err)
	}
	// The person may, which proves the refusal is identity and not a bad id.
	if err := k.UpdateGroup(ctx, g.ID, "Renamed", []string{"x@example.com"}); err != nil {
		t.Errorf("person UpdateGroup = %v, want nil", err)
	}
	if err := k.DeleteGroup(ctx, g.ID); err != nil {
		t.Errorf("person DeleteGroup = %v, want nil", err)
	}
}

// UpdateGroup on an id that does not exist is the store's ErrNotFound, which a
// handler turns into a 404.
func TestUpdateUnknownGroupIsNotFound(t *testing.T) {
	k := newSvc(t)
	ctx := identity.NewContext(context.Background(), identity.User{Email: "ada@example.com"})
	err := k.UpdateGroup(ctx, model.ID("nope"), "Renamed", nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateGroup unknown id = %v, want store.ErrNotFound", err)
	}
}
