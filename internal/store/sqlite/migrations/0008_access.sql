-- Who may read a board, who may write to it, and who decides that.
--
-- Until now identity answered "whose name goes on this card" and never "may
-- this caller open this board": AUTH_REQUIRED refused a caller with no
-- identity and served every board to everyone it let in. These three tables
-- are the whole of the answer, and nothing enforces them yet; this migration
-- only makes somewhere to put the grants.
--
-- A grant names a board, a principal and a role, and there is at most one per
-- pair, which is what the primary key says. Three kinds of principal:
--
--   user   one person, by the address the identity provider hands over
--   group  a group kept here, with its members in group_members
--   idp    a group name that arrives in the request, from an Access claim or
--          a proxy header, whose membership this database never sees
--
-- The third kind is why a grant stores the kind beside the id rather than
-- deriving it: a local group's id and a provider's group name live in the same
-- column and would otherwise be indistinguishable.
--
-- Addresses and provider group names are stored lowercased, because an
-- identity provider may change the case it sends between one sign-in and the
-- next and a grant that stops matching is a lockout.
CREATE TABLE groups (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

-- Two groups whose names differ only in case are the same group to a reader,
-- and telling them apart in a picker is not possible.
CREATE UNIQUE INDEX groups_name_key ON groups (lower(name));

CREATE TABLE group_members (
    group_id TEXT NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    email    TEXT NOT NULL CHECK (email = lower(email)),
    PRIMARY KEY (group_id, email)
);

-- Answering "which boards may this person see" walks the grants of every
-- principal they are, so the lookup is by principal and not by board.
CREATE INDEX group_members_email_idx ON group_members (email);

CREATE TABLE board_grants (
    board_id       TEXT NOT NULL REFERENCES boards (id) ON DELETE CASCADE,
    principal_kind TEXT NOT NULL CHECK (principal_kind IN ('user', 'group', 'idp')),
    principal_id   TEXT NOT NULL CHECK (principal_id <> ''),
    role           TEXT NOT NULL CHECK (role IN ('read', 'write', 'owner')),
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    PRIMARY KEY (board_id, principal_kind, principal_id)
);

CREATE INDEX board_grants_principal_idx ON board_grants (principal_kind, principal_id);
