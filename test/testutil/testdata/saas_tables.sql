-- Domain tables and melange_tuples view for the SaaS scale benchmark
-- (saas_schema.fga). IDs are text, as a service using KSUIDs or UUID strings
-- stores them, so each branch is indexed on the columns it is filtered by
-- from either end: object first (check, list_subjects) and subject first
-- (list_objects).

CREATE TABLE orgs (
    id   TEXT PRIMARY KEY,
    n    INT NOT NULL UNIQUE, -- generation ordinal
    kind TEXT NOT NULL CHECK (kind IN ('production', 'testing', 'demo'))
);

CREATE TABLE users (
    id       TEXT PRIMARY KEY,
    is_staff BOOLEAN NOT NULL DEFAULT false,
    active   BOOLEAN NOT NULL DEFAULT true
);
CREATE INDEX users_staff ON users (id) WHERE is_staff;

CREATE TABLE memberships (
    org_id  TEXT NOT NULL,
    user_id TEXT NOT NULL,
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX memberships_by_user ON memberships (user_id, org_id);

CREATE TABLE user_roles (
    org_id  TEXT NOT NULL,
    role    TEXT NOT NULL,
    user_id TEXT NOT NULL,
    PRIMARY KEY (org_id, role, user_id)
);
CREATE INDEX user_roles_by_user ON user_roles (user_id, role, org_id);

CREATE TABLE partner_clients (
    client_org_id  TEXT NOT NULL,
    partner_org_id TEXT NOT NULL,
    PRIMARY KEY (client_org_id, partner_org_id)
);
CREATE INDEX partner_clients_by_partner ON partner_clients (partner_org_id, client_org_id);

CREATE TABLE partner_roles (
    partner_org_id TEXT NOT NULL,
    role           TEXT NOT NULL,
    user_id        TEXT NOT NULL,
    PRIMARY KEY (partner_org_id, role, user_id)
);
CREATE INDEX partner_roles_by_user ON partner_roles (user_id, role, partner_org_id);

CREATE TABLE contacts (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX contacts_by_org ON contacts (org_id, id);
CREATE INDEX contacts_by_org_created ON contacts (org_id, created_at);

CREATE TABLE deals (
    id       TEXT PRIMARY KEY,
    org_id   TEXT NOT NULL,
    owner_id TEXT
);
CREATE INDEX deals_by_org ON deals (org_id, id);
CREATE INDEX deals_by_owner ON deals (owner_id, id);

CREATE TABLE queues (
    id     TEXT PRIMARY KEY,
    org_id TEXT NOT NULL
);
CREATE INDEX queues_by_org ON queues (org_id, id);

CREATE TABLE tickets (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('incident', 'request')),
    org_id      TEXT NOT NULL,
    queue_id    TEXT NOT NULL,
    assignee_id TEXT
);
CREATE INDEX tickets_by_org ON tickets (org_id, id);
CREATE INDEX tickets_by_queue ON tickets (queue_id, id);
CREATE INDEX tickets_by_assignee ON tickets (assignee_id, id);

CREATE TABLE invoices (
    id        TEXT PRIMARY KEY,
    org_id    TEXT NOT NULL,
    author_id TEXT
);
CREATE INDEX invoices_by_org ON invoices (org_id, id);
CREATE INDEX invoices_by_author ON invoices (author_id, id);

CREATE TABLE employees (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL,
    manager_id TEXT
);
CREATE INDEX employees_by_org ON employees (org_id, id);
CREATE INDEX employees_by_manager ON employees (manager_id, id);

CREATE TABLE teams (
    id     TEXT PRIMARY KEY,
    org_id TEXT NOT NULL
);
CREATE INDEX teams_by_org ON teams (org_id, id);

CREATE TABLE team_members (
    team_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_by_user ON team_members (user_id, team_id);

CREATE TABLE folders (
    id        TEXT PRIMARY KEY,
    org_id    TEXT NOT NULL,
    parent_id TEXT
);
CREATE INDEX folders_by_org ON folders (org_id, id);
CREATE INDEX folders_by_parent ON folders (parent_id, id);

CREATE TABLE folder_shares (
    folder_id TEXT NOT NULL,
    user_id   TEXT,
    team_id   TEXT,
    CHECK ((user_id IS NULL) <> (team_id IS NULL))
);
CREATE INDEX folder_shares_by_folder ON folder_shares (folder_id);
CREATE INDEX folder_shares_by_user ON folder_shares (user_id, folder_id) WHERE user_id IS NOT NULL;
CREATE INDEX folder_shares_by_team ON folder_shares ((team_id || '#member'), folder_id) WHERE team_id IS NOT NULL;

CREATE TABLE documents (
    id        TEXT PRIMARY KEY,
    org_id    TEXT NOT NULL,
    folder_id TEXT NOT NULL,
    owner_id  TEXT
);
CREATE INDEX documents_by_org ON documents (org_id, id);
CREATE INDEX documents_by_folder ON documents (folder_id, id);
CREATE INDEX documents_by_owner ON documents (owner_id, id);

CREATE VIEW melange_tuples AS
-- Membership counts only for active users.
SELECT 'user' AS subject_type, m.user_id AS subject_id, 'member' AS relation,
       'organization' AS object_type, m.org_id AS object_id
FROM memberships m JOIN users u ON u.id = m.user_id AND u.active
UNION ALL
-- Org roles are stored under their relation names.
SELECT 'user', r.user_id, r.role, 'organization', r.org_id FROM user_roles r
UNION ALL
-- Support staff read every production org.
SELECT 'user', u.id, 'support_staff', 'organization', o.id
FROM users u CROSS JOIN orgs o WHERE u.is_staff AND o.kind = 'production'
UNION ALL
SELECT 'user', '*', 'is_root', 'organization', o.id FROM orgs o
WHERE NOT EXISTS (SELECT 1 FROM partner_clients pc WHERE pc.client_org_id = o.id)
UNION ALL
SELECT 'partner', pc.partner_org_id, 'partner', 'organization', pc.client_org_id FROM partner_clients pc
UNION ALL
SELECT 'organization', o.id, 'owner', 'partner', o.id FROM orgs o
UNION ALL
SELECT 'user', r.user_id, r.role, 'partner', r.partner_org_id FROM partner_roles r
UNION ALL
SELECT 'organization', c.org_id, 'org', 'contact', c.id FROM contacts c
UNION ALL
SELECT 'organization', d.org_id, 'org', 'deal', d.id FROM deals d
UNION ALL
SELECT 'user', d.owner_id, 'owner', 'deal', d.id FROM deals d WHERE d.owner_id IS NOT NULL
UNION ALL
SELECT 'organization', q.org_id, 'org', 'queue', q.id FROM queues q
UNION ALL
-- One table, two types.
SELECT 'organization', t.org_id, 'org', CASE t.kind WHEN 'incident' THEN 'incident' ELSE 'service_request' END, t.id
FROM tickets t
UNION ALL
SELECT 'queue', t.queue_id, 'queue', CASE t.kind WHEN 'incident' THEN 'incident' ELSE 'service_request' END, t.id
FROM tickets t
UNION ALL
SELECT 'user', t.assignee_id, 'assignee', CASE t.kind WHEN 'incident' THEN 'incident' ELSE 'service_request' END, t.id
FROM tickets t WHERE t.assignee_id IS NOT NULL
UNION ALL
SELECT 'organization', i.org_id, 'org', 'invoice', i.id FROM invoices i
UNION ALL
SELECT 'user', i.author_id, 'author', 'invoice', i.id FROM invoices i WHERE i.author_id IS NOT NULL
UNION ALL
SELECT 'organization', e.org_id, 'org', 'employee', e.id FROM employees e
UNION ALL
SELECT 'user', e.manager_id, 'manager', 'employee', e.id FROM employees e WHERE e.manager_id IS NOT NULL
UNION ALL
SELECT 'organization', t.org_id, 'org', 'team', t.id FROM teams t
UNION ALL
SELECT 'user', tm.user_id, 'member', 'team', tm.team_id FROM team_members tm
UNION ALL
SELECT 'organization', f.org_id, 'org', 'folder', f.id FROM folders f
UNION ALL
SELECT 'folder', f.parent_id, 'parent', 'folder', f.id FROM folders f WHERE f.parent_id IS NOT NULL
UNION ALL
SELECT 'user', s.user_id, 'viewer', 'folder', s.folder_id FROM folder_shares s WHERE s.user_id IS NOT NULL
UNION ALL
SELECT 'team', s.team_id || '#member', 'viewer', 'folder', s.folder_id FROM folder_shares s WHERE s.team_id IS NOT NULL
UNION ALL
SELECT 'organization', d.org_id, 'org', 'document', d.id FROM documents d
UNION ALL
SELECT 'folder', d.folder_id, 'folder', 'document', d.id FROM documents d
UNION ALL
SELECT 'user', d.owner_id, 'owner', 'document', d.id FROM documents d WHERE d.owner_id IS NOT NULL;
