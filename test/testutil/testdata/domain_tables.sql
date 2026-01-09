-- Domain tables for melange integration tests.
-- These represent a GitHub-like application data model.

-- Users table
CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(255) NOT NULL UNIQUE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Organizations table
CREATE TABLE IF NOT EXISTS organizations (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Organization members
CREATE TABLE IF NOT EXISTS organization_members (
    organization_id BIGINT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role VARCHAR(50) NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'billing_manager')),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    PRIMARY KEY (organization_id, user_id)
);

-- Teams table
CREATE TABLE IF NOT EXISTS teams (
    id BIGSERIAL PRIMARY KEY,
    organization_id BIGINT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE (organization_id, name)
);

-- Team members
CREATE TABLE IF NOT EXISTS team_members (
    team_id BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role VARCHAR(50) NOT NULL CHECK (role IN ('maintainer', 'member')),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    PRIMARY KEY (team_id, user_id)
);

-- Repositories table
CREATE TABLE IF NOT EXISTS repositories (
    id BIGSERIAL PRIMARY KEY,
    organization_id BIGINT REFERENCES organizations(id) ON DELETE CASCADE,
    owner_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    is_public BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    CHECK (organization_id IS NOT NULL OR owner_id IS NOT NULL)
);

-- Repository collaborators
CREATE TABLE IF NOT EXISTS repository_collaborators (
    repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role VARCHAR(50) NOT NULL CHECK (role IN ('owner', 'admin', 'maintainer', 'writer', 'reader')),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    PRIMARY KEY (repository_id, user_id)
);

-- Issues table
CREATE TABLE IF NOT EXISTS issues (
    id BIGSERIAL PRIMARY KEY,
    repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    author_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    assignee_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    title VARCHAR(255) NOT NULL,
    body TEXT,
    state VARCHAR(50) NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Pull Requests table
CREATE TABLE IF NOT EXISTS pull_requests (
    id BIGSERIAL PRIMARY KEY,
    repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    author_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title VARCHAR(255) NOT NULL,
    body TEXT,
    state VARCHAR(50) NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed', 'merged')),
    source_branch VARCHAR(255) NOT NULL,
    target_branch VARCHAR(255) NOT NULL DEFAULT 'main',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Pull Request reviewers
CREATE TABLE IF NOT EXISTS pull_request_reviewers (
    pull_request_id BIGINT NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    PRIMARY KEY (pull_request_id, user_id)
);

-- Repository bans (for exclusion tests)
-- user_id is NULL when banned_all is true (wildcard ban)
CREATE TABLE IF NOT EXISTS repository_bans (
    id BIGSERIAL PRIMARY KEY,
    repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    banned_all BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (repository_id, user_id)
);

-- Create indexes for common queries
CREATE INDEX IF NOT EXISTS idx_org_members_user ON organization_members(user_id);
CREATE INDEX IF NOT EXISTS idx_team_members_user ON team_members(user_id);
CREATE INDEX IF NOT EXISTS idx_repo_collaborators_user ON repository_collaborators(user_id);
CREATE INDEX IF NOT EXISTS idx_repos_org ON repositories(organization_id);
CREATE INDEX IF NOT EXISTS idx_issues_repo ON issues(repository_id);
CREATE INDEX IF NOT EXISTS idx_prs_repo ON pull_requests(repository_id);
CREATE INDEX IF NOT EXISTS idx_repo_bans_repo ON repository_bans(repository_id);

-- Expression indexes for melange_tuples view performance.
-- The view converts integer IDs to text (id::TEXT), which prevents PostgreSQL from
-- using standard integer indexes. These expression indexes enable efficient lookups
-- through the UNION ALL view by indexing the text-converted columns.
-- See: docs/content/docs/tuples-view.md#expression-indexes-for-text-id-conversion

-- Organizations: ID lookup
CREATE INDEX IF NOT EXISTS idx_org_id_text ON organizations ((id::TEXT));

-- Organization members: object and subject lookups
CREATE INDEX IF NOT EXISTS idx_org_members_obj_text ON organization_members ((organization_id::TEXT), (user_id::TEXT));
CREATE INDEX IF NOT EXISTS idx_org_members_subj_text ON organization_members ((user_id::TEXT), (organization_id::TEXT));

-- Teams: ID and org relationship lookups
CREATE INDEX IF NOT EXISTS idx_teams_id_text ON teams ((id::TEXT));
CREATE INDEX IF NOT EXISTS idx_teams_org_text ON teams ((id::TEXT), (organization_id::TEXT));

-- Team members: object and subject lookups
CREATE INDEX IF NOT EXISTS idx_team_members_obj_text ON team_members ((team_id::TEXT), (user_id::TEXT));
CREATE INDEX IF NOT EXISTS idx_team_members_subj_text ON team_members ((user_id::TEXT), (team_id::TEXT));

-- Repositories: ID and org relationship lookups
CREATE INDEX IF NOT EXISTS idx_repos_id_text ON repositories ((id::TEXT));
CREATE INDEX IF NOT EXISTS idx_repos_org_text ON repositories ((id::TEXT), (organization_id::TEXT));
CREATE INDEX IF NOT EXISTS idx_repos_owner_text ON repositories ((id::TEXT), (owner_id::TEXT));

-- Repository collaborators: object and subject lookups
CREATE INDEX IF NOT EXISTS idx_repo_collabs_obj_text ON repository_collaborators ((repository_id::TEXT), (user_id::TEXT));
CREATE INDEX IF NOT EXISTS idx_repo_collabs_subj_text ON repository_collaborators ((user_id::TEXT), (repository_id::TEXT));

-- Issues: ID, repo relationship, and author/assignee lookups
CREATE INDEX IF NOT EXISTS idx_issues_id_text ON issues ((id::TEXT));
CREATE INDEX IF NOT EXISTS idx_issues_repo_text ON issues ((id::TEXT), (repository_id::TEXT));
CREATE INDEX IF NOT EXISTS idx_issues_author_text ON issues ((id::TEXT), (author_id::TEXT));
CREATE INDEX IF NOT EXISTS idx_issues_assignee_text ON issues ((id::TEXT), (assignee_id::TEXT));

-- Pull requests: ID, repo relationship, and author lookups
CREATE INDEX IF NOT EXISTS idx_prs_id_text ON pull_requests ((id::TEXT));
CREATE INDEX IF NOT EXISTS idx_prs_repo_text ON pull_requests ((id::TEXT), (repository_id::TEXT));
CREATE INDEX IF NOT EXISTS idx_prs_author_text ON pull_requests ((id::TEXT), (author_id::TEXT));

-- Pull request reviewers: object and subject lookups
CREATE INDEX IF NOT EXISTS idx_pr_reviewers_obj_text ON pull_request_reviewers ((pull_request_id::TEXT), (user_id::TEXT));
CREATE INDEX IF NOT EXISTS idx_pr_reviewers_subj_text ON pull_request_reviewers ((user_id::TEXT), (pull_request_id::TEXT));

-- Repository bans: lookup for exclusion checks (includes wildcard support)
CREATE INDEX IF NOT EXISTS idx_repo_bans_text ON repository_bans ((repository_id::TEXT), (user_id::TEXT));
