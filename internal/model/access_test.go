package model

import "testing"

// The three roles are ordered, and every refusal in the application will come
// down to Allows, so this walks the whole 3x3 and pins the unknown role as
// well: a value read out of a row that is not one of the three has to allow
// nothing, not everything.
func TestRoleAllows(t *testing.T) {
	roles := []Role{RoleRead, RoleWrite, RoleOwner}
	want := map[Role]map[Role]bool{
		RoleRead:  {RoleRead: true, RoleWrite: false, RoleOwner: false},
		RoleWrite: {RoleRead: true, RoleWrite: true, RoleOwner: false},
		RoleOwner: {RoleRead: true, RoleWrite: true, RoleOwner: true},
	}
	for _, held := range roles {
		for _, need := range roles {
			if got := held.Allows(need); got != want[held][need] {
				t.Errorf("Role(%q).Allows(%q) = %v, want %v", held, need, got, want[held][need])
			}
		}
	}
	for _, need := range roles {
		if Role("admin").Allows(need) {
			t.Errorf("an unknown role allows %q", need)
		}
		if need.Allows(Role("admin")) {
			t.Errorf("%q allows an unknown requirement", need)
		}
	}
	if Role("").Allows(Role("")) {
		t.Error("the empty role allows the empty requirement")
	}
}

func TestRoleValidAndMax(t *testing.T) {
	for _, r := range []Role{RoleRead, RoleWrite, RoleOwner} {
		if !r.Valid() {
			t.Errorf("%q is not valid", r)
		}
	}
	for _, r := range []Role{"", "admin", "READ", "Read"} {
		if Role(r).Valid() {
			t.Errorf("%q is valid", r)
		}
	}
	// Max is how several principals become one role, so the argument order
	// must not matter and an unknown role must never win.
	cases := []struct{ a, b, want Role }{
		{RoleRead, RoleWrite, RoleWrite},
		{RoleWrite, RoleRead, RoleWrite},
		{RoleOwner, RoleRead, RoleOwner},
		{RoleRead, RoleOwner, RoleOwner},
		{RoleWrite, RoleWrite, RoleWrite},
		{"", RoleRead, RoleRead},
		{RoleRead, "", RoleRead},
		{"", "", ""},
		{"admin", RoleRead, RoleRead},
	}
	for _, c := range cases {
		if got := c.a.Max(c.b); got != c.want {
			t.Errorf("Role(%q).Max(%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// A grant that stops matching because the provider changed the case it sends
// is a lockout, so the constructors lower and trim and the database stores
// what they produce.
func TestPrincipalConstructorsNormalise(t *testing.T) {
	if p := UserPrincipal("  Ada@Example.COM "); p != (Principal{PrincipalUser, "ada@example.com"}) {
		t.Errorf("UserPrincipal = %#v", p)
	}
	if p := IDPPrincipal(" Platform-Team "); p != (Principal{PrincipalIDP, "platform-team"}) {
		t.Errorf("IDPPrincipal = %#v", p)
	}
	// A group's ID is generated here, not typed by a person, so it is used as
	// it is: lowering it would break the lookup.
	if p := GroupPrincipal(ID("GrpABC")); p != (Principal{PrincipalGroup, "GrpABC"}) {
		t.Errorf("GroupPrincipal = %#v", p)
	}
}

func TestPrincipalValid(t *testing.T) {
	valid := []Principal{
		{PrincipalUser, "ada@example.com"},
		{PrincipalGroup, "grp1"},
		{PrincipalIDP, "platform"},
	}
	for _, p := range valid {
		if !p.Valid() {
			t.Errorf("%#v is not valid", p)
		}
	}
	invalid := []Principal{
		{PrincipalUser, ""},
		{PrincipalGroup, ""},
		{PrincipalIDP, ""},
		{"", "ada@example.com"},
		{"User", "ada@example.com"},
		{"everyone", "x"},
	}
	for _, p := range invalid {
		if p.Valid() {
			t.Errorf("%#v is valid", p)
		}
	}
	if got := (Principal{PrincipalUser, "ada@example.com"}).String(); got != "user:ada@example.com" {
		t.Errorf("String = %q", got)
	}
}
