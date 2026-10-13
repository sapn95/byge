// Local groups: sets of addresses kept in this database, as opposed to the
// group names an identity provider sends. A grant on any board can name one,
// so managing them is a board-crossing power and is gated in one place,
// requireGroupAdmin.

package service

import (
	"context"
	"strings"

	"byge/internal/model"
)

// requireGroupAdmin decides who may manage the groups kept in this database.
// Local groups are global and a grant can point at one, so managing them is a
// board-crossing power; this is the one place that power is gated. Only the
// bootstrap owner may: everyone else — anonymous, a service token, or an
// ordinary signed-in caller — is refused. With AUTH_BOOTSTRAP_OWNER unset
// isBootstrapOwner is false for everyone, so nobody can manage groups, which is
// the secure default for an instance that has not been told who its owner is.
func (k *Byge) requireGroupAdmin(ctx context.Context) error {
	if !k.isBootstrapOwner(ctx) {
		return ErrForbidden
	}
	return nil
}

// Groups lists every local group with its members, by name.
func (k *Byge) Groups(ctx context.Context) ([]model.Group, error) {
	if err := k.requireGroupAdmin(ctx); err != nil {
		return nil, err
	}
	return k.store.ListGroups(ctx)
}

// CreateGroup makes a group from a name and a list of member addresses. Members
// are lowercased and trimmed the one way the rest of the system compares an
// address, blanks are dropped and duplicates removed, so a group holds each
// member once in the form a grant match expects.
func (k *Byge) CreateGroup(ctx context.Context, name string, members []string) (*model.Group, error) {
	if err := k.requireGroupAdmin(ctx); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if err := checkText("name", name, MaxName, true); err != nil {
		return nil, err
	}
	normal, err := normalMembers(members)
	if err != nil {
		return nil, err
	}
	now := k.now()
	g := &model.Group{ID: k.newID(), Name: name, Members: normal, CreatedAt: now, UpdatedAt: now}
	if err := k.store.CreateGroup(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

// UpdateGroup renames a group and replaces its whole membership.
func (k *Byge) UpdateGroup(ctx context.Context, id model.ID, name string, members []string) error {
	if err := k.requireGroupAdmin(ctx); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if err := checkText("name", name, MaxName, true); err != nil {
		return err
	}
	normal, err := normalMembers(members)
	if err != nil {
		return err
	}
	return k.store.UpdateGroup(ctx, &model.Group{ID: id, Name: name, Members: normal, UpdatedAt: k.now()})
}

// DeleteGroup removes a group, its membership and every grant made to it.
func (k *Byge) DeleteGroup(ctx context.Context, id model.ID) error {
	if err := k.requireGroupAdmin(ctx); err != nil {
		return err
	}
	return k.store.DeleteGroup(ctx, id)
}

// normalMembers turns a list of addresses from a form into the list a group is
// stored with: each lowercased and trimmed through model.UserPrincipal, blanks
// dropped, duplicates removed, and each length-checked. An empty list is valid;
// a group with no members simply matches nobody.
func normalMembers(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, m := range in {
		addr := model.UserPrincipal(m).ID
		if addr == "" || seen[addr] {
			continue
		}
		if err := checkText("members", addr, MaxAssignee, false); err != nil {
			return nil, err
		}
		seen[addr] = true
		out = append(out, addr)
	}
	return out, nil
}
