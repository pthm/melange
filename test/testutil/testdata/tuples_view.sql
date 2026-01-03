-- melange_tuples view for integration tests.
-- This view maps domain tables to FGA tuples for permission evaluation.

CREATE OR REPLACE VIEW melange_tuples AS

-- Organization memberships
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'organization' AS object_type,
    organization_id::text AS object_id
FROM organization_members

UNION ALL

-- Team memberships
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'team' AS object_type,
    team_id::text AS object_id
FROM team_members

UNION ALL

-- Team -> Organization relationship
SELECT
    'organization' AS subject_type,
    organization_id::text AS subject_id,
    'org' AS relation,
    'team' AS object_type,
    id::text AS object_id
FROM teams

UNION ALL

-- Repository -> Organization relationship
SELECT
    'organization' AS subject_type,
    organization_id::text AS subject_id,
    'org' AS relation,
    'repository' AS object_type,
    id::text AS object_id
FROM repositories
WHERE organization_id IS NOT NULL

UNION ALL

-- Repository collaborators
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'repository' AS object_type,
    repository_id::text AS object_id
FROM repository_collaborators

UNION ALL

-- Issue -> Repository relationship
SELECT
    'repository' AS subject_type,
    repository_id::text AS subject_id,
    'repo' AS relation,
    'issue' AS object_type,
    id::text AS object_id
FROM issues

UNION ALL

-- Issue authors
SELECT
    'user' AS subject_type,
    author_id::text AS subject_id,
    'author' AS relation,
    'issue' AS object_type,
    id::text AS object_id
FROM issues

UNION ALL

-- Issue assignees
SELECT
    'user' AS subject_type,
    assignee_id::text AS subject_id,
    'assignee' AS relation,
    'issue' AS object_type,
    id::text AS object_id
FROM issues
WHERE assignee_id IS NOT NULL

UNION ALL

-- Pull Request -> Repository relationship
SELECT
    'repository' AS subject_type,
    repository_id::text AS subject_id,
    'repo' AS relation,
    'pull_request' AS object_type,
    id::text AS object_id
FROM pull_requests

UNION ALL

-- Pull Request authors
SELECT
    'user' AS subject_type,
    author_id::text AS subject_id,
    'author' AS relation,
    'pull_request' AS object_type,
    id::text AS object_id
FROM pull_requests

UNION ALL

-- Pull Request reviewers
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'reviewer' AS relation,
    'pull_request' AS object_type,
    pull_request_id::text AS object_id
FROM pull_request_reviewers

UNION ALL

-- Repository authors (direct, from owner_id)
SELECT
    'user' AS subject_type,
    owner_id::text AS subject_id,
    'author' AS relation,
    'repository' AS object_type,
    id::text AS object_id
FROM repositories
WHERE owner_id IS NOT NULL

UNION ALL

-- Repository bans (wildcard or specific user)
SELECT
    'user' AS subject_type,
    CASE
        WHEN banned_all THEN '*'
        ELSE user_id::text
    END AS subject_id,
    'banned' AS relation,
    'repository' AS object_type,
    repository_id::text AS object_id
FROM repository_bans;
