// Package testutil provides shared test utilities for Melange integration tests.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
)

// Fixtures provides factory functions for creating test data in bulk.
// All functions use batch inserts for efficiency at scale.
type Fixtures struct {
	db  *sql.DB
	ctx context.Context
}

// NewFixtures creates a new Fixtures instance for bulk data insertion.
func NewFixtures(ctx context.Context, db *sql.DB) *Fixtures {
	return &Fixtures{db: db, ctx: ctx}
}

// CreateUsers creates n users and returns their IDs.
func (f *Fixtures) CreateUsers(n int) ([]int64, error) {
	if n == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, n)

	// Use batch inserts for efficiency (1000 rows per batch)
	batchSize := 1000
	for i := 0; i < n; i += batchSize {
		end := i + batchSize
		if end > n {
			end = n
		}

		batchIDs, err := f.insertUsersBatch(i, end)
		if err != nil {
			return nil, fmt.Errorf("insert users batch %d-%d: %w", i, end, err)
		}
		ids = append(ids, batchIDs...)
	}

	return ids, nil
}

func (f *Fixtures) insertUsersBatch(start, end int) ([]int64, error) {
	count := end - start
	ids := make([]int64, 0, count)

	// Build multi-row INSERT
	query := "INSERT INTO users (username) VALUES "
	args := make([]any, 0, count)

	for i := start; i < end; i++ {
		if i > start {
			query += ", "
		}
		query += fmt.Sprintf("($%d)", i-start+1)
		args = append(args, fmt.Sprintf("bench_user_%d", i))
	}
	query += " RETURNING id"

	rows, err := f.db.QueryContext(f.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateOrganization creates a single organization and returns its ID.
func (f *Fixtures) CreateOrganization(name string) (int64, error) {
	var id int64
	err := f.db.QueryRowContext(f.ctx,
		`INSERT INTO organizations (name) VALUES ($1) RETURNING id`,
		name,
	).Scan(&id)
	return id, err
}

// CreateOrganizations creates n organizations and returns their IDs.
func (f *Fixtures) CreateOrganizations(n int) ([]int64, error) {
	if n == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, n)
	batchSize := 1000

	for i := 0; i < n; i += batchSize {
		end := i + batchSize
		if end > n {
			end = n
		}

		batchIDs, err := f.insertOrgsBatch(i, end)
		if err != nil {
			return nil, fmt.Errorf("insert orgs batch %d-%d: %w", i, end, err)
		}
		ids = append(ids, batchIDs...)
	}

	return ids, nil
}

func (f *Fixtures) insertOrgsBatch(start, end int) ([]int64, error) {
	count := end - start
	ids := make([]int64, 0, count)

	query := "INSERT INTO organizations (name) VALUES "
	args := make([]any, 0, count)

	for i := start; i < end; i++ {
		if i > start {
			query += ", "
		}
		query += fmt.Sprintf("($%d)", i-start+1)
		args = append(args, fmt.Sprintf("bench_org_%d", i))
	}
	query += " RETURNING id"

	rows, err := f.db.QueryContext(f.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AddOrganizationMembers adds users to an organization with the specified role.
// role must be one of: owner, admin, member, billing_manager
func (f *Fixtures) AddOrganizationMembers(orgID int64, userIDs []int64, role string) error {
	if len(userIDs) == 0 {
		return nil
	}

	batchSize := 1000
	for i := 0; i < len(userIDs); i += batchSize {
		end := i + batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}

		if err := f.insertOrgMembersBatch(orgID, userIDs[i:end], role); err != nil {
			return fmt.Errorf("insert org members batch %d-%d: %w", i, end, err)
		}
	}
	return nil
}

func (f *Fixtures) insertOrgMembersBatch(orgID int64, userIDs []int64, role string) error {
	query := "INSERT INTO organization_members (organization_id, user_id, role) VALUES "
	args := make([]any, 0, len(userIDs)*3)
	argIdx := 1

	for i, userID := range userIDs {
		if i > 0 {
			query += ", "
		}
		query += fmt.Sprintf("($%d, $%d, $%d)", argIdx, argIdx+1, argIdx+2)
		args = append(args, orgID, userID, role)
		argIdx += 3
	}

	_, err := f.db.ExecContext(f.ctx, query, args...)
	return err
}

// CreateRepositories creates n repositories under an organization and returns their IDs.
func (f *Fixtures) CreateRepositories(orgID int64, n int) ([]int64, error) {
	if n == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, n)
	batchSize := 1000

	for i := 0; i < n; i += batchSize {
		end := i + batchSize
		if end > n {
			end = n
		}

		batchIDs, err := f.insertReposBatch(orgID, i, end)
		if err != nil {
			return nil, fmt.Errorf("insert repos batch %d-%d: %w", i, end, err)
		}
		ids = append(ids, batchIDs...)
	}

	return ids, nil
}

func (f *Fixtures) insertReposBatch(orgID int64, start, end int) ([]int64, error) {
	count := end - start
	ids := make([]int64, 0, count)

	query := "INSERT INTO repositories (organization_id, name) VALUES "
	args := make([]any, 0, count*2)
	argIdx := 1

	for i := start; i < end; i++ {
		if i > start {
			query += ", "
		}
		query += fmt.Sprintf("($%d, $%d)", argIdx, argIdx+1)
		args = append(args, orgID, fmt.Sprintf("bench_repo_%d", i))
		argIdx += 2
	}
	query += " RETURNING id"

	rows, err := f.db.QueryContext(f.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AddRepositoryCollaborators adds users as collaborators to a repository.
// role must be one of: owner, admin, maintainer, writer, reader
func (f *Fixtures) AddRepositoryCollaborators(repoID int64, userIDs []int64, role string) error {
	if len(userIDs) == 0 {
		return nil
	}

	batchSize := 1000
	for i := 0; i < len(userIDs); i += batchSize {
		end := i + batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}

		if err := f.insertRepoCollabsBatch(repoID, userIDs[i:end], role); err != nil {
			return fmt.Errorf("insert repo collabs batch %d-%d: %w", i, end, err)
		}
	}
	return nil
}

func (f *Fixtures) insertRepoCollabsBatch(repoID int64, userIDs []int64, role string) error {
	query := "INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES "
	args := make([]any, 0, len(userIDs)*3)
	argIdx := 1

	for i, userID := range userIDs {
		if i > 0 {
			query += ", "
		}
		query += fmt.Sprintf("($%d, $%d, $%d)", argIdx, argIdx+1, argIdx+2)
		args = append(args, repoID, userID, role)
		argIdx += 3
	}

	_, err := f.db.ExecContext(f.ctx, query, args...)
	return err
}

// CreatePullRequests creates pull requests in the given repository.
// Cycles through authorIDs for each PR.
func (f *Fixtures) CreatePullRequests(repoID int64, authorIDs []int64, n int) ([]int64, error) {
	if n == 0 || len(authorIDs) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, n)
	batchSize := 1000

	for i := 0; i < n; i += batchSize {
		end := i + batchSize
		if end > n {
			end = n
		}

		batchIDs, err := f.insertPRsBatch(repoID, authorIDs, i, end)
		if err != nil {
			return nil, fmt.Errorf("insert PRs batch %d-%d: %w", i, end, err)
		}
		ids = append(ids, batchIDs...)
	}

	return ids, nil
}

func (f *Fixtures) insertPRsBatch(repoID int64, authorIDs []int64, start, end int) ([]int64, error) {
	count := end - start
	ids := make([]int64, 0, count)

	query := "INSERT INTO pull_requests (repository_id, author_id, title, source_branch) VALUES "
	args := make([]any, 0, count*4)
	argIdx := 1

	for i := start; i < end; i++ {
		if i > start {
			query += ", "
		}
		query += fmt.Sprintf("($%d, $%d, $%d, $%d)", argIdx, argIdx+1, argIdx+2, argIdx+3)
		authorID := authorIDs[i%len(authorIDs)]
		args = append(args, repoID, authorID, fmt.Sprintf("PR %d", i), fmt.Sprintf("feature-%d", i))
		argIdx += 4
	}
	query += " RETURNING id"

	rows, err := f.db.QueryContext(f.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateIssues creates issues in the given repository.
// Cycles through authorIDs for each issue.
func (f *Fixtures) CreateIssues(repoID int64, authorIDs []int64, n int) ([]int64, error) {
	if n == 0 || len(authorIDs) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, n)
	batchSize := 1000

	for i := 0; i < n; i += batchSize {
		end := i + batchSize
		if end > n {
			end = n
		}

		batchIDs, err := f.insertIssuesBatch(repoID, authorIDs, i, end)
		if err != nil {
			return nil, fmt.Errorf("insert issues batch %d-%d: %w", i, end, err)
		}
		ids = append(ids, batchIDs...)
	}

	return ids, nil
}

func (f *Fixtures) insertIssuesBatch(repoID int64, authorIDs []int64, start, end int) ([]int64, error) {
	count := end - start
	ids := make([]int64, 0, count)

	query := "INSERT INTO issues (repository_id, author_id, title) VALUES "
	args := make([]any, 0, count*3)
	argIdx := 1

	for i := start; i < end; i++ {
		if i > start {
			query += ", "
		}
		query += fmt.Sprintf("($%d, $%d, $%d)", argIdx, argIdx+1, argIdx+2)
		authorID := authorIDs[i%len(authorIDs)]
		args = append(args, repoID, authorID, fmt.Sprintf("Issue %d", i))
		argIdx += 3
	}
	query += " RETURNING id"

	rows, err := f.db.QueryContext(f.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateTeams creates n teams in an organization and returns their IDs.
func (f *Fixtures) CreateTeams(orgID int64, n int) ([]int64, error) {
	if n == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, n)
	batchSize := 1000

	for i := 0; i < n; i += batchSize {
		end := i + batchSize
		if end > n {
			end = n
		}

		batchIDs, err := f.insertTeamsBatch(orgID, i, end)
		if err != nil {
			return nil, fmt.Errorf("insert teams batch %d-%d: %w", i, end, err)
		}
		ids = append(ids, batchIDs...)
	}

	return ids, nil
}

func (f *Fixtures) insertTeamsBatch(orgID int64, start, end int) ([]int64, error) {
	count := end - start
	ids := make([]int64, 0, count)

	query := "INSERT INTO teams (organization_id, name) VALUES "
	args := make([]any, 0, count*2)
	argIdx := 1

	for i := start; i < end; i++ {
		if i > start {
			query += ", "
		}
		query += fmt.Sprintf("($%d, $%d)", argIdx, argIdx+1)
		args = append(args, orgID, fmt.Sprintf("bench_team_%d", i))
		argIdx += 2
	}
	query += " RETURNING id"

	rows, err := f.db.QueryContext(f.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AddTeamMembers adds users to a team with the specified role.
// role must be one of: maintainer, member
func (f *Fixtures) AddTeamMembers(teamID int64, userIDs []int64, role string) error {
	if len(userIDs) == 0 {
		return nil
	}

	batchSize := 1000
	for i := 0; i < len(userIDs); i += batchSize {
		end := i + batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}

		if err := f.insertTeamMembersBatch(teamID, userIDs[i:end], role); err != nil {
			return fmt.Errorf("insert team members batch %d-%d: %w", i, end, err)
		}
	}
	return nil
}

func (f *Fixtures) insertTeamMembersBatch(teamID int64, userIDs []int64, role string) error {
	query := "INSERT INTO team_members (team_id, user_id, role) VALUES "
	args := make([]any, 0, len(userIDs)*3)
	argIdx := 1

	for i, userID := range userIDs {
		if i > 0 {
			query += ", "
		}
		query += fmt.Sprintf("($%d, $%d, $%d)", argIdx, argIdx+1, argIdx+2)
		args = append(args, teamID, userID, role)
		argIdx += 3
	}

	_, err := f.db.ExecContext(f.ctx, query, args...)
	return err
}

// TupleCount returns the current count of tuples in the melange_tuples view.
func (f *Fixtures) TupleCount() (int, error) {
	var count int
	err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM melange_tuples").Scan(&count)
	return count, err
}
