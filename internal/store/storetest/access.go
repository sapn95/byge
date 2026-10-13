package storetest

import (
	"context"
	"sort"
	"testing"

	"byge/internal/model"
	"byge/internal/store"
)

// mustGroup makes a group with the members given and fails the test if the
// store refuses it.
func mustGroup(t *testing.T, s store.Store, name string, members ...string) *model.Group {
	t.Helper()
	g := &model.Group{ID: newID("grp"), Name: name, Members: members, CreatedAt: now(), UpdatedAt: now()}
	if err := s.CreateGroup(context.Background(), g); err != nil {
		t.Fatalf("CreateGroup(%q): %v", name, err)
	}
	return g
}

// mustGrant gives a principal a role on a board.
func mustGrant(t *testing.T, s store.Store, board model.ID, p model.Principal, role model.Role) {
	t.Helper()
	g := &model.Grant{BoardID: board, Principal: p, Role: role, CreatedAt: now(), UpdatedAt: now()}
	if err := s.SetGrant(context.Background(), g); err != nil {
		t.Fatalf("SetGrant(%s, %s): %v", p, role, err)
	}
}

// testGroups covers the membership rules every backend has to share: the
// normalising, the name uniqueness, and the replace-the-whole-set update.
func testGroups(t *testing.T, s store.Store) {
	ctx := context.Background()

	// The same address typed with different case and spacing is one member,
	// and the store answers lowercased and sorted whatever order it was given.
	g := mustGroup(t, s, "Platform", " Bea@Example.com ", "ada@example.com", "ADA@EXAMPLE.COM", "   ")
	got, err := s.GetGroup(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if !equalStrings(got.Members, []string{"ada@example.com", "bea@example.com"}) {
		t.Errorf("members = %q", got.Members)
	}
	if got.Name != "Platform" {
		t.Errorf("name = %q, want Platform", got.Name)
	}

	// Two groups a reader cannot tell apart are one group.
	dup := &model.Group{ID: newID("grp"), Name: "platform", CreatedAt: now(), UpdatedAt: now()}
	wantErr(t, "CreateGroup with a name differing only in case", s.CreateGroup(ctx, dup), store.ErrConflict)

	// An update replaces the whole membership rather than adding to it.
	got.Name = "Platform Team"
	got.Members = []string{"cem@example.com"}
	got.UpdatedAt = now()
	if err := s.UpdateGroup(ctx, got); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	after, err := s.GetGroup(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetGroup after update: %v", err)
	}
	if !equalStrings(after.Members, []string{"cem@example.com"}) {
		t.Errorf("members after update = %q, want only cem", after.Members)
	}
	if after.Name != "Platform Team" {
		t.Errorf("name after update = %q", after.Name)
	}

	// A group that is gone is gone, and so is the one that never was.
	other := mustGroup(t, s, "Readers")
	list, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(list) != 2 || list[0].Name != "Platform Team" || list[1].Name != "Readers" {
		t.Errorf("ListGroups = %v, want Platform Team then Readers", names(list))
	}
	if err := s.DeleteGroup(ctx, other.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	wantErr(t, "GetGroup after delete", errOf(s.GetGroup(ctx, other.ID)), store.ErrNotFound)
	wantErr(t, "DeleteGroup twice", s.DeleteGroup(ctx, other.ID), store.ErrNotFound)
	wantErr(t, "UpdateGroup of a group that is gone", s.UpdateGroup(ctx, other), store.ErrNotFound)
	wantErr(t, "CreateGroup with no id", s.CreateGroup(ctx, &model.Group{Name: "x"}), store.ErrInvalid)
}

// testGrants covers what a grant is: at most one role per principal per
// board, replaced rather than added, and gone when the board or the group is.
func testGrants(t *testing.T, s store.Store) {
	ctx := context.Background()
	b := mustBoard(t, s, "grants", "To Do")
	ada := model.UserPrincipal("Ada@Example.com")

	// Granting twice replaces, and the address is matched without case.
	mustGrant(t, s, b.ID, ada, model.RoleRead)
	mustGrant(t, s, b.ID, model.UserPrincipal("ADA@EXAMPLE.COM"), model.RoleWrite)
	grants, err := s.ListGrants(ctx, b.ID)
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].Role != model.RoleWrite || grants[0].Principal != ada {
		t.Fatalf("ListGrants = %+v, want one write grant for %s", grants, ada)
	}
	// The grant was replaced, not re-made: when the principal first got a
	// role on this board is worth keeping.
	if !grants[0].CreatedAt.Before(grants[0].UpdatedAt) && !grants[0].CreatedAt.Equal(grants[0].UpdatedAt) {
		t.Errorf("created_at %v is after updated_at %v", grants[0].CreatedAt, grants[0].UpdatedAt)
	}

	// Taking off a grant that is not there is not an error.
	if err := s.DeleteGrant(ctx, b.ID, model.UserPrincipal("nobody@example.com")); err != nil {
		t.Errorf("DeleteGrant for a principal with no grant: %v", err)
	}
	if err := s.DeleteGrant(ctx, b.ID, ada); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if grants, err = s.ListGrants(ctx, b.ID); err != nil || len(grants) != 0 {
		t.Fatalf("ListGrants after delete = %+v, %v", grants, err)
	}

	// Neither an unknown role nor an unknown principal kind is storable: a
	// row the application cannot read is a row that allows nothing or, worse,
	// is read as something.
	bad := []model.Grant{
		{BoardID: b.ID, Principal: ada, Role: "admin"},
		{BoardID: b.ID, Principal: ada, Role: ""},
		{BoardID: b.ID, Principal: model.Principal{Kind: "everyone", ID: "x"}, Role: model.RoleRead},
		{BoardID: b.ID, Principal: model.Principal{Kind: model.PrincipalUser, ID: ""}, Role: model.RoleRead},
	}
	for _, g := range bad {
		wantErr(t, "SetGrant "+string(g.Principal.Kind)+"/"+string(g.Role), s.SetGrant(ctx, &g), store.ErrInvalid)
	}

	// A board that is not there takes no grants and lists none.
	missing := &model.Grant{BoardID: newID("brd"), Principal: ada, Role: model.RoleRead, CreatedAt: now(), UpdatedAt: now()}
	wantErr(t, "SetGrant on a board that is gone", s.SetGrant(ctx, missing), store.ErrNotFound)
	wantErr(t, "ListGrants of a board that is gone", errOf2(s.ListGrants(ctx, missing.BoardID)), store.ErrNotFound)
	wantErr(t, "DeleteGrant on a board that is gone", s.DeleteGrant(ctx, missing.BoardID, ada), store.ErrNotFound)

	// A grant to a group that does not exist would be a role nobody can hold
	// and nobody can see.
	wantErr(t, "SetGrant to a group that is gone",
		s.SetGrant(ctx, &model.Grant{BoardID: b.ID, Principal: model.GroupPrincipal(newID("grp")), Role: model.RoleRead, CreatedAt: now(), UpdatedAt: now()}),
		store.ErrNotFound)

	// Deleting the group takes its grants with it, and so does deleting the
	// board.
	grp := mustGroup(t, s, "Writers", "bea@example.com")
	mustGrant(t, s, b.ID, model.GroupPrincipal(grp.ID), model.RoleWrite)
	mustGrant(t, s, b.ID, ada, model.RoleRead)
	if err := s.DeleteGroup(ctx, grp.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if grants, err = s.ListGrants(ctx, b.ID); err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].Principal != ada {
		t.Errorf("ListGrants after the group went = %+v, want only %s", grants, ada)
	}
	if err := s.DeleteBoard(ctx, b.ID); err != nil {
		t.Fatalf("DeleteBoard: %v", err)
	}
	roles, err := s.BoardRoles(ctx, []model.Principal{ada})
	if err != nil {
		t.Fatalf("BoardRoles: %v", err)
	}
	if _, ok := roles[b.ID]; ok {
		t.Errorf("a deleted board still grants %q", roles[b.ID])
	}
}

// testBoardRoles is the read the board list and the search both make: every
// board these principals reach, already reduced to one role each.
func testBoardRoles(t *testing.T, s store.Store) {
	ctx := context.Background()
	one := mustBoard(t, s, "one", "To Do")
	two := mustBoard(t, s, "two", "To Do")
	three := mustBoard(t, s, "three", "To Do")

	ada := model.UserPrincipal("ada@example.com")
	grp := mustGroup(t, s, "Platform", "ada@example.com")
	idp := model.IDPPrincipal("platform-team")

	// Two principals reaching the same board keep the stronger role, in
	// either direction, so the answer cannot depend on row order.
	mustGrant(t, s, one.ID, ada, model.RoleRead)
	mustGrant(t, s, one.ID, model.GroupPrincipal(grp.ID), model.RoleOwner)
	mustGrant(t, s, two.ID, model.GroupPrincipal(grp.ID), model.RoleRead)
	mustGrant(t, s, two.ID, ada, model.RoleWrite)
	// A board nobody in the set holds anything on is absent, not "".
	mustGrant(t, s, three.ID, model.UserPrincipal("bea@example.com"), model.RoleOwner)
	mustGrant(t, s, three.ID, idp, model.RoleRead)

	roles, err := s.BoardRoles(ctx, []model.Principal{ada, model.GroupPrincipal(grp.ID)})
	if err != nil {
		t.Fatalf("BoardRoles: %v", err)
	}
	if len(roles) != 2 {
		t.Fatalf("BoardRoles = %v, want two boards", roles)
	}
	if roles[one.ID] != model.RoleOwner {
		t.Errorf("board one = %q, want owner", roles[one.ID])
	}
	if roles[two.ID] != model.RoleWrite {
		t.Errorf("board two = %q, want write", roles[two.ID])
	}
	if _, ok := roles[three.ID]; ok {
		t.Errorf("board three is present as %q", roles[three.ID])
	}

	// A group name that arrives with the request reaches its board without
	// this database knowing who is in it.
	roles, err = s.BoardRoles(ctx, []model.Principal{idp})
	if err != nil {
		t.Fatalf("BoardRoles(idp): %v", err)
	}
	if len(roles) != 1 || roles[three.ID] != model.RoleRead {
		t.Errorf("BoardRoles(idp) = %v, want read on board three", roles)
	}

	// No principals is no boards. An empty set that meant "everything" would
	// hand every board to a caller the provider could not name.
	for _, in := range [][]model.Principal{nil, {}, {{Kind: model.PrincipalUser, ID: ""}}, {{Kind: "everyone", ID: "x"}}} {
		roles, err = s.BoardRoles(ctx, in)
		if err != nil {
			t.Fatalf("BoardRoles(%v): %v", in, err)
		}
		if len(roles) != 0 {
			t.Errorf("BoardRoles(%v) = %v, want none", in, roles)
		}
	}

	// The pairs are matched as pairs: a kind from one principal must not meet
	// an id from another.
	roles, err = s.BoardRoles(ctx, []model.Principal{
		{Kind: model.PrincipalUser, ID: "platform-team"},
		{Kind: model.PrincipalIDP, ID: "ada@example.com"},
	})
	if err != nil {
		t.Fatalf("BoardRoles(crossed): %v", err)
	}
	if len(roles) != 0 {
		t.Errorf("BoardRoles with the kinds and ids crossed = %v, want none", roles)
	}
}

// testGroupsOf is the membership lookup the request path makes: one address
// in, the groups it belongs to out.
func testGroupsOf(t *testing.T, s store.Store) {
	ctx := context.Background()
	platform := mustGroup(t, s, "Platform", "ada@example.com", "bea@example.com")
	readers := mustGroup(t, s, "Readers", "ada@example.com")
	mustGroup(t, s, "Nobody")

	got, err := s.GroupsOf(ctx, "ADA@Example.com ")
	if err != nil {
		t.Fatalf("GroupsOf: %v", err)
	}
	want := []model.ID{platform.ID, readers.ID}
	sortIDs(want)
	if !equalIDs(got, want) {
		t.Errorf("GroupsOf(ada) = %v, want %v", got, want)
	}

	// An address in nothing, and no address at all, are both none. An empty
	// address matching every row would make an unidentified caller a member
	// of everything.
	for _, email := range []string{"cem@example.com", "", "   "} {
		got, err = s.GroupsOf(ctx, email)
		if err != nil {
			t.Fatalf("GroupsOf(%q): %v", email, err)
		}
		if len(got) != 0 {
			t.Errorf("GroupsOf(%q) = %v, want none", email, got)
		}
	}

	// Membership follows the group, so taking somebody out takes the lookup
	// with it.
	platform.Members = []string{"bea@example.com"}
	platform.UpdatedAt = now()
	if err := s.UpdateGroup(ctx, platform); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if got, err = s.GroupsOf(ctx, "ada@example.com"); err != nil {
		t.Fatalf("GroupsOf after update: %v", err)
	}
	if !equalIDs(got, []model.ID{readers.ID}) {
		t.Errorf("GroupsOf(ada) after removal = %v, want only Readers", got)
	}
}

// testBoardOf covers the two lookups that place a column or a label on its
// board: the resolution, ErrNotFound for an unknown id, and that a second
// board's column and label resolve to the second board, not the first. That
// last part is the mutant guard: a lookup that returns "the only board" or
// ignores its id passes the first board's assertions and fails here.
func testBoardOf(t *testing.T, s store.Store) {
	ctx := context.Background()

	a := mustBoard(t, s, "board-a", "To Do")
	colA := a.Columns[0].ID
	labelA := &model.Label{ID: newID("l"), BoardID: a.ID, Name: "bug", Color: "#f00"}
	if err := s.CreateLabel(ctx, labelA); err != nil {
		t.Fatalf("CreateLabel(a): %v", err)
	}
	mustCard(t, s, a, colA, "a1")

	if got, err := s.BoardOfColumn(ctx, colA); err != nil || got != a.ID {
		t.Fatalf("BoardOfColumn(a) = %q, %v; want %q", got, err, a.ID)
	}
	if got, err := s.BoardOfLabel(ctx, labelA.ID); err != nil || got != a.ID {
		t.Fatalf("BoardOfLabel(a) = %q, %v; want %q", got, err, a.ID)
	}

	// An unknown id of each kind is ErrNotFound, so the service can map a
	// missing entity to a 404 rather than reading a board's role for it.
	wantErr(t, "BoardOfColumn unknown", errOfID(s.BoardOfColumn(ctx, newID("c"))), store.ErrNotFound)
	wantErr(t, "BoardOfLabel unknown", errOfID(s.BoardOfLabel(ctx, newID("l"))), store.ErrNotFound)

	// A second board's column and label must resolve to the second board.
	b := mustBoard(t, s, "board-b", "To Do")
	colB := b.Columns[0].ID
	labelB := &model.Label{ID: newID("l"), BoardID: b.ID, Name: "idea", Color: "#00f"}
	if err := s.CreateLabel(ctx, labelB); err != nil {
		t.Fatalf("CreateLabel(b): %v", err)
	}

	if got, err := s.BoardOfColumn(ctx, colB); err != nil || got != b.ID {
		t.Fatalf("BoardOfColumn(b) = %q, %v; want %q, not %q", got, err, b.ID, a.ID)
	}
	if got, err := s.BoardOfLabel(ctx, labelB.ID); err != nil || got != b.ID {
		t.Fatalf("BoardOfLabel(b) = %q, %v; want %q, not %q", got, err, b.ID, a.ID)
	}
}

// errOfID drops the id a BoardOf* call returns beside its error, so a
// not-found can be asserted in one line the way errOf does for a group.
func errOfID(_ model.ID, err error) error { return err }

// sortIDs and equalIDs compare an ID list without pulling in a dependency.
func sortIDs(in []model.ID) {
	sort.Slice(in, func(i, j int) bool { return in[i] < in[j] })
}

func equalIDs(a, b []model.ID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// names lists the group names, for a failure message.
func names(gs []model.Group) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.Name
	}
	return out
}

// errOf and errOf2 drop the value a call returns beside its error, so a
// not-found can be asserted in one line.
func errOf(_ *model.Group, err error) error { return err }

func errOf2(_ []model.Grant, err error) error { return err }
