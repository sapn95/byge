package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/store"
	"byge/internal/store/memory"
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

// A grant to a local group that does exist is allowed and written. The group
// is created through CreateGroup, which stage 5 restricts to the bootstrap
// owner, so this runs on a service whose bootstrap owner is the owner@example.com
// caller ownerCtx carries.
func TestSetBoardGrantKnownGroup(t *testing.T) {
	n := 0
	k := New(memory.New(),
		WithClock(func() time.Time { return fixed }),
		WithIDs(func() model.ID { n++; return model.ID(fmt.Sprintf("id%03d", n)) }),
		WithBootstrapOwner("owner@example.com"))
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

// proxySvc is a service whose grant rules are the proxy-mode ones, with the
// same clock, ids and bootstrap owner the other grant tests use.
func proxySvc(t *testing.T) *Byge {
	t.Helper()
	n := 0
	return New(memory.New(),
		WithClock(func() time.Time { return fixed }),
		WithIDs(func() model.ID { n++; return model.ID(fmt.Sprintf("id%03d", n)) }),
		WithBootstrapOwner("owner@example.com"),
		WithProxyIdentity(true))
}

// grantMessage is the message of a validation error on field, "" otherwise.
func grantMessage(err error, field string) string {
	var ve *ValidationError
	if errors.As(err, &ve) && ve.Field == field {
		return ve.Message
	}
	return ""
}

// In access mode a user grant is an address: anything else matches nobody,
// which is how "sapm" became a grant. A refused one writes nothing.
func TestSetBoardGrantUserMustBeAnAddress(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")

	for _, id := range []string{
		"sapm",
		"Ada <ada@example.com>",
		`"a b"@example.com`,
		"ada lovelace@example.com",
		"a@b@example.com",
		"@example.com",
		"ada@",
		"",
		"   ",
		"ada\xff@example.com",
		strings.Repeat("a", 309) + "@example.com",
	} {
		// Built by hand, as the handler passes it: the service normalises.
		p := model.Principal{Kind: model.PrincipalUser, ID: id}
		err := k.SetBoardGrant(ownerCtx(), b.ID, p, model.RoleRead)
		if !isValidation(err, "id") {
			t.Errorf("user %q = %v, want an id ValidationError", id, err)
		}
	}
	if grants, _ := k.store.ListGrants(context.Background(), b.ID); len(grants) != 1 {
		t.Fatalf("a refused grant was written: %+v", grants)
	}
	if msg := grantMessage(k.SetBoardGrant(ownerCtx(), b.ID, model.UserPrincipal("sapm"), model.RoleRead), "id"); msg != msgNotAnAddress {
		t.Errorf("sapm message = %q, want %q", msg, msgNotAnAddress)
	}
	if msg := grantMessage(k.SetBoardGrant(ownerCtx(), b.ID, model.UserPrincipal(""), model.RoleRead), "id"); msg != "enter an address" {
		t.Errorf("empty message = %q, want enter an address", msg)
	}
	// Invalid UTF-8 is refused as such, before lowercasing turns it into a
	// valid replacement character the address rules would let through.
	bad := model.Principal{Kind: model.PrincipalUser, ID: "\xffda@example.com"}
	if msg := grantMessage(k.SetBoardGrant(ownerCtx(), b.ID, bad, model.RoleRead), "id"); msg != "must be text" {
		t.Errorf("invalid UTF-8 message = %q, want must be text", msg)
	}

	for id, stored := range map[string]string{
		" Ada@Example.com ":   "ada@example.com",
		"émile@example.com":   "émile@example.com",
		"ada+tag@example.com": "ada+tag@example.com",
	} {
		p := model.Principal{Kind: model.PrincipalUser, ID: id}
		if err := k.SetBoardGrant(ownerCtx(), b.ID, p, model.RoleRead); err != nil {
			t.Errorf("user %q = %v, want nil", id, err)
			continue
		}
		if role, _ := k.RoleOn(context.Background(), identity.User{Email: stored}, b.ID); role != model.RoleRead {
			t.Errorf("user %q was not stored as %q", id, stored)
		}
	}
}

// Behind a proxy the header may carry a login rather than an address, so any
// token is a user; a space in one is still refused.
func TestSetBoardGrantLoginsInProxyMode(t *testing.T) {
	k := proxySvc(t)
	b := mustMakeBoard(t, k, "Board")

	if err := k.SetBoardGrant(ownerCtx(), b.ID, model.UserPrincipal("u236858"), model.RoleRead); err != nil {
		t.Fatalf("login u236858 = %v, want nil", err)
	}
	for id, want := range map[string]string{
		"u 236858": "a login has no spaces in it",
		"u\t1":     "a login has no spaces in it",
		"":         "enter an address or login",
	} {
		msg := grantMessage(k.SetBoardGrant(ownerCtx(), b.ID, model.UserPrincipal(id), model.RoleRead), "id")
		if msg != want {
			t.Errorf("login %q message = %q, want %q", id, msg, want)
		}
	}
}

// An idp group name is one line of text. The signed claim is a JSON list, so
// in access mode it may hold a comma and inner spaces; a proxy sends one
// comma-separated header, so there a comma is refused.
func TestSetBoardGrantIDPName(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	for _, name := range []string{"Platform Engineers", "Engineering, Zurich"} {
		if err := k.SetBoardGrant(ownerCtx(), b.ID, model.IDPPrincipal(name), model.RoleRead); err != nil {
			t.Errorf("idp %q = %v, want nil", name, err)
		}
	}
	grants, _ := k.store.ListGrants(context.Background(), b.ID)
	got := map[model.Principal]bool{}
	for _, g := range grants {
		got[g.Principal] = true
	}
	for _, id := range []string{"platform engineers", "engineering, zurich"} {
		if !got[model.Principal{Kind: model.PrincipalIDP, ID: id}] {
			t.Errorf("idp:%s was not stored: %+v", id, grants)
		}
	}
	if msg := grantMessage(k.SetBoardGrant(ownerCtx(), b.ID, model.IDPPrincipal("a\nb"), model.RoleRead), "id"); msg != "a group name is one line of text" {
		t.Errorf("a\\nb message = %q", msg)
	}
	if msg := grantMessage(k.SetBoardGrant(ownerCtx(), b.ID, model.IDPPrincipal(" "), model.RoleRead), "id"); msg != "enter a group name" {
		t.Errorf("empty message = %q", msg)
	}

	p := proxySvc(t)
	pb := mustMakeBoard(t, p, "Board")
	msg := grantMessage(p.SetBoardGrant(ownerCtx(), pb.ID, model.IDPPrincipal("platform, ops"), model.RoleRead), "id")
	if !strings.Contains(msg, "cannot contain a comma") {
		t.Errorf("proxy-mode comma message = %q", msg)
	}
}

// A kind that is none of the three is refused on the kind field and writes
// nothing.
func TestSetBoardGrantUnknownKind(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	err := k.SetBoardGrant(ownerCtx(), b.ID, model.Principal{Kind: "wizard", ID: "x"}, model.RoleRead)
	if !isValidation(err, "kind") {
		t.Fatalf("unknown kind = %v, want a kind ValidationError", err)
	}
	if grants, _ := k.store.ListGrants(context.Background(), b.ID); len(grants) != 1 {
		t.Errorf("a grant was written for an unknown kind: %+v", grants)
	}
}

// A principal built by hand is stored in the form grants are matched in, the
// same as one built through the model constructors.
func TestSetBoardGrantRenormalises(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	p := model.Principal{Kind: model.PrincipalUser, ID: "Ada@Example.com"}
	if err := k.SetBoardGrant(ownerCtx(), b.ID, p, model.RoleRead); err != nil {
		t.Fatal(err)
	}
	grants, _ := k.store.ListGrants(context.Background(), b.ID)
	for _, g := range grants {
		if g.Principal == model.UserPrincipal("ada@example.com") {
			return
		}
	}
	t.Errorf("Ada@Example.com was not stored as user:ada@example.com: %+v", grants)
}

// A second grant to the same principal changes its role; it does not add a row.
func TestSetBoardGrantTwiceUpdatesTheRole(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	ada := model.UserPrincipal("ada@example.com")
	for _, role := range []model.Role{model.RoleRead, model.RoleWrite} {
		if err := k.SetBoardGrant(ownerCtx(), b.ID, ada, role); err != nil {
			t.Fatal(err)
		}
	}
	grants, _ := k.store.ListGrants(context.Background(), b.ID)
	n := 0
	for _, g := range grants {
		if g.Principal == ada {
			n++
			if g.Role != model.RoleWrite {
				t.Errorf("role = %q, want write", g.Role)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d grants for ada, want 1", n)
	}
}

// On a board with no owner at all, a grant that leaves it ownerless says to
// claim it, rather than to "make someone else owner" when nobody is.
func TestOwnerlessBoardPointsToClaim(t *testing.T) {
	k := New(memory.New(), WithClock(func() time.Time { return fixed }),
		WithBootstrapOwner("owner@example.com"))
	boards, err := k.EnsureDefaultBoard(context.Background())
	if err != nil || len(boards) == 0 {
		t.Fatalf("EnsureDefaultBoard = %v, %v", boards, err)
	}
	b := boards[0]
	err = k.SetBoardGrant(ownerCtx(), b.ID, model.UserPrincipal("ada@example.com"), model.RoleRead)
	if msg := grantMessage(err, "role"); !strings.Contains(msg, "claim it") {
		t.Fatalf("SetBoardGrant on an ownerless board = %v, want a role error that says claim it", err)
	}
	// Adding an owner is how it gets one, and that still works.
	if err := k.SetBoardGrant(ownerCtx(), b.ID, model.UserPrincipal("ada@example.com"), model.RoleOwner); err != nil {
		t.Errorf("adding an owner to an ownerless board = %v, want nil", err)
	}
}

// A grant stored before the address rule existed can still be removed, because
// removal checks no format.
func TestRemoveBoardGrantRemovesALegacyInvalidGrant(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	legacy := model.Principal{Kind: model.PrincipalUser, ID: "sapm"}
	if err := k.store.SetGrant(context.Background(), &model.Grant{BoardID: b.ID, Principal: legacy, Role: model.RoleWrite}); err != nil {
		t.Fatal(err)
	}
	if err := k.RemoveBoardGrant(ownerCtx(), b.ID, legacy); err != nil {
		t.Fatalf("RemoveBoardGrant(user:sapm) = %v, want nil", err)
	}
	grants, _ := k.store.ListGrants(context.Background(), b.ID)
	for _, g := range grants {
		if g.Principal == legacy {
			t.Error("the legacy grant is still there")
		}
	}
}

// hintsScenario builds the same world on k: a board the owner owns, a second
// board they own with grants on it, a third they only write on, assignees on
// the first, and one principal granted on both owned boards.
func hintsScenario(t *testing.T, k *Byge) *model.Board {
	t.Helper()
	b := mustMakeBoard(t, k, "Board")
	other := mustMakeBoard(t, k, "Other")
	grant(t, k, other.ID, model.UserPrincipal("carol@example.com"), model.RoleRead)
	grant(t, k, other.ID, model.IDPPrincipal("Platform"), model.RoleRead)
	grant(t, k, other.ID, model.Principal{Kind: model.PrincipalUser, ID: "sapm"}, model.RoleRead)
	grant(t, k, other.ID, model.UserPrincipal("reader@example.com"), model.RoleRead)
	grant(t, k, b.ID, model.UserPrincipal("reader@example.com"), model.RoleRead)

	stranger := identity.NewContext(context.Background(), identity.User{Email: "x@example.com"})
	third, err := k.CreateBoard(stranger, "Third", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	grant(t, k, third.ID, model.UserPrincipal("owner@example.com"), model.RoleWrite)
	grant(t, k, third.ID, model.UserPrincipal("dave@example.com"), model.RoleRead)

	for _, who := range []string{"bob@example.com", "Ada"} {
		if _, err := k.CreateCard(ownerCtx(), b.ID, b.Columns[0].ID, CardInput{Title: "c", Assignee: who}); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func TestBoardGrantHints(t *testing.T) {
	asOwner := identity.NewContext(context.Background(),
		identity.User{Email: "owner@example.com", Groups: []string{"Ops"}})
	users := []string{"grace@example.com", "not an address"}
	groups := []string{"aemtli", "a,b"}

	t.Run("owner only", func(t *testing.T) {
		k := newSvc(t)
		b := mustMakeBoard(t, k, "Board")
		reader := grantedCtx(t, k, b.ID, "reader@example.com", model.RoleRead)
		stranger := identity.NewContext(context.Background(), identity.User{Email: "stranger@example.com"})
		for name, ctx := range map[string]context.Context{"reader": reader, "stranger": stranger} {
			if _, err := k.BoardGrantHints(ctx, b.ID, nil, nil); err != ErrForbidden {
				t.Errorf("%s BoardGrantHints = %v, want ErrForbidden", name, err)
			}
		}
	})

	t.Run("access mode", func(t *testing.T) {
		k := newSvc(t)
		b := hintsScenario(t, k)
		h, err := k.BoardGrantHints(asOwner, b.ID, users, groups)
		if err != nil {
			t.Fatal(err)
		}
		wantUsers := []string{"bob@example.com", "carol@example.com", "grace@example.com"}
		wantIDP := []string{"a,b", "aemtli", "ops", "platform"}
		if !slices.Equal(h.Users, wantUsers) {
			t.Errorf("Users = %q, want %q", h.Users, wantUsers)
		}
		if !slices.Equal(h.IDPGroups, wantIDP) {
			t.Errorf("IDPGroups = %q, want %q", h.IDPGroups, wantIDP)
		}
		if h.ProxyIdentity {
			t.Error("ProxyIdentity = true on the default service")
		}
	})

	t.Run("proxy mode", func(t *testing.T) {
		k := proxySvc(t)
		b := hintsScenario(t, k)
		h, err := k.BoardGrantHints(asOwner, b.ID, users, groups)
		if err != nil {
			t.Fatal(err)
		}
		// A login is a user here, so the assignee "Ada" and the legacy sapm
		// grant are suggestions; a comma is not a group name.
		wantUsers := []string{"ada", "bob@example.com", "carol@example.com", "grace@example.com", "sapm"}
		wantIDP := []string{"aemtli", "ops", "platform"}
		if !slices.Equal(h.Users, wantUsers) {
			t.Errorf("Users = %q, want %q", h.Users, wantUsers)
		}
		if !slices.Equal(h.IDPGroups, wantIDP) {
			t.Errorf("IDPGroups = %q, want %q", h.IDPGroups, wantIDP)
		}
		if !h.ProxyIdentity {
			t.Error("ProxyIdentity = false on a proxy-mode service")
		}
	})

	t.Run("granted principals are left out", func(t *testing.T) {
		k := newSvc(t)
		b := hintsScenario(t, k)
		grant(t, k, b.ID, model.IDPPrincipal("aemtli"), model.RoleWrite)
		h, err := k.BoardGrantHints(asOwner, b.ID, []string{"owner@example.com"}, groups)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(h.IDPGroups, "aemtli") || slices.Contains(h.Users, "owner@example.com") {
			t.Errorf("a principal granted here was suggested: %+v", h)
		}
	})
}

func TestKeepsAnOwner(t *testing.T) {
	ada, bob := model.UserPrincipal("ada@example.com"), model.UserPrincipal("bob@example.com")
	owner := func(p model.Principal) model.Grant { return model.Grant{Principal: p, Role: model.RoleOwner} }
	reader := func(p model.Principal) model.Grant { return model.Grant{Principal: p, Role: model.RoleRead} }
	for _, tc := range []struct {
		name   string
		grants []model.Grant
		p      model.Principal
		role   model.Role
		want   string // "" for allowed
	}{
		{"demote the only owner", []model.Grant{owner(ada)}, ada, model.RoleRead, msgLastOwner},
		{"remove the only owner", []model.Grant{owner(ada)}, ada, "", msgLastOwner},
		{"demote one of two owners", []model.Grant{owner(ada), owner(bob)}, ada, model.RoleWrite, ""},
		{"remove a reader", []model.Grant{owner(ada), reader(bob)}, bob, "", ""},
		{"owner stays owner", []model.Grant{owner(ada)}, ada, model.RoleOwner, ""},
		{"reader on an ownerless board", []model.Grant{reader(bob)}, ada, model.RoleRead, msgNoOwnerYet},
		{"owner on an ownerless board", nil, ada, model.RoleOwner, ""},
	} {
		got := grantMessage(keepsAnOwner(tc.grants, tc.p, tc.role), "role")
		if got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
