// Package testutil provides shared test utilities for Melange integration tests.
package testutil

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// BulkFixtures provides factory functions for creating test data using PostgreSQL COPY FROM.
// This is 10-100x faster than batch INSERTs for large datasets.
type BulkFixtures struct {
	db  *sql.DB
	ctx context.Context
}

// NewBulkFixtures creates a new BulkFixtures instance for bulk data loading via COPY FROM.
func NewBulkFixtures(ctx context.Context, db *sql.DB) *BulkFixtures {
	return &BulkFixtures{db: db, ctx: ctx}
}

// copyFrom executes a COPY FROM operation using the pgx driver.
// data should be a tab-delimited text stream (one row per line).
func (bf *BulkFixtures) copyFrom(table string, columns []string, data io.Reader) error {
	// Get a connection from the pool
	conn, err := bf.db.Conn(bf.ctx)
	if err != nil {
		return fmt.Errorf("get connection: %w", err)
	}
	defer conn.Close()

	// Access the underlying pgx connection through stdlib wrapper
	var pgxConn *pgx.Conn
	err = conn.Raw(func(driverConn any) error {
		// First try to unwrap stdlib.Conn
		if stdlibConn, ok := driverConn.(*stdlib.Conn); ok {
			pgxConn = stdlibConn.Conn()
			return nil
		}
		// Fall back to direct pgx.Conn (for compatibility)
		if directConn, ok := driverConn.(*pgx.Conn); ok {
			pgxConn = directConn
			return nil
		}
		return fmt.Errorf("not a pgx connection (got %T)", driverConn)
	})
	if err != nil {
		return fmt.Errorf("access pgx connection: %w", err)
	}

	// Build COPY FROM query
	query := fmt.Sprintf("COPY %s (%s) FROM STDIN WITH (FORMAT text, DELIMITER E'\\t')",
		table, joinColumns(columns))

	// Execute COPY FROM
	_, err = pgxConn.PgConn().CopyFrom(bf.ctx, data, query)
	if err != nil {
		return fmt.Errorf("COPY FROM: %w", err)
	}

	return nil
}

// joinColumns joins column names with commas.
func joinColumns(cols []string) string {
	if len(cols) == 0 {
		return ""
	}
	result := cols[0]
	for i := 1; i < len(cols); i++ {
		result += ", " + cols[i]
	}
	return result
}

// CreateUsers creates n users using COPY FROM for bulk loading.
// Falls back to batch INSERT if COPY fails.
func (bf *BulkFixtures) CreateUsers(n int) ([]int64, error) {
	if n == 0 {
		return nil, nil
	}

	// Try COPY FROM first
	ids, err := bf.createUsersCopy(n)
	if err == nil {
		return ids, nil
	}

	// Log warning and fallback to batch INSERT
	log.Printf("COPY FROM failed for users (%v), falling back to batch INSERT", err)
	f := NewFixtures(bf.ctx, bf.db)
	return f.CreateUsers(n)
}

// createUsersCopy creates users using COPY FROM.
func (bf *BulkFixtures) createUsersCopy(n int) ([]int64, error) {
	// Decide between in-memory and temp file based on size
	if n > 10_000_000 {
		return bf.createUsersFromFile(n)
	}
	return bf.createUsersInMemory(n)
}

// createUsersInMemory generates TSV data in memory and uses COPY FROM.
func (bf *BulkFixtures) createUsersInMemory(n int) ([]int64, error) {
	// Generate TSV data
	var buf bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&buf, "bench_user_%d\n", i)
	}

	// COPY FROM
	if err := bf.copyFrom("users", []string{"username"}, &buf); err != nil {
		return nil, err
	}

	// Fetch IDs
	return bf.fetchUserIDs(n)
}

// createUsersFromFile generates TSV data to a temp file and uses COPY FROM.
func (bf *BulkFixtures) createUsersFromFile(n int) ([]int64, error) {
	// Create temp file
	f, err := os.CreateTemp("", "melange-bench-users-*.tsv")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	// Write TSV data with buffering
	w := bufio.NewWriter(f)
	for i := 0; i < n; i++ {
		fmt.Fprintf(w, "bench_user_%d\n", i)
	}
	if err := w.Flush(); err != nil {
		return nil, fmt.Errorf("flush temp file: %w", err)
	}

	// Rewind for reading
	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek temp file: %w", err)
	}

	// COPY FROM
	if err := bf.copyFrom("users", []string{"username"}, f); err != nil {
		return nil, err
	}

	// Fetch IDs
	return bf.fetchUserIDs(n)
}

// fetchUserIDs fetches the most recent n user IDs.
func (bf *BulkFixtures) fetchUserIDs(n int) ([]int64, error) {
	rows, err := bf.db.QueryContext(bf.ctx,
		"SELECT id FROM users ORDER BY id DESC LIMIT $1", n)
	if err != nil {
		return nil, fmt.Errorf("fetch user IDs: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, n)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to get ascending order
	for i := 0; i < len(ids)/2; i++ {
		ids[i], ids[len(ids)-1-i] = ids[len(ids)-1-i], ids[i]
	}

	return ids, nil
}

// CreateOrganizations creates n organizations using COPY FROM.
// Falls back to batch INSERT if COPY fails.
func (bf *BulkFixtures) CreateOrganizations(n int) ([]int64, error) {
	if n == 0 {
		return nil, nil
	}

	// Try COPY FROM first
	ids, err := bf.createOrganizationsCopy(n)
	if err == nil {
		return ids, nil
	}

	// Log warning and fallback
	log.Printf("COPY FROM failed for organizations (%v), falling back to batch INSERT", err)
	f := NewFixtures(bf.ctx, bf.db)
	return f.CreateOrganizations(n)
}

// createOrganizationsCopy creates organizations using COPY FROM.
func (bf *BulkFixtures) createOrganizationsCopy(n int) ([]int64, error) {
	if n > 10_000_000 {
		return bf.createOrganizationsFromFile(n)
	}
	return bf.createOrganizationsInMemory(n)
}

// createOrganizationsInMemory generates TSV data in memory.
func (bf *BulkFixtures) createOrganizationsInMemory(n int) ([]int64, error) {
	var buf bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&buf, "bench_org_%d\n", i)
	}

	if err := bf.copyFrom("organizations", []string{"name"}, &buf); err != nil {
		return nil, err
	}

	return bf.fetchOrganizationIDs(n)
}

// createOrganizationsFromFile generates TSV data to a temp file.
func (bf *BulkFixtures) createOrganizationsFromFile(n int) ([]int64, error) {
	f, err := os.CreateTemp("", "melange-bench-orgs-*.tsv")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	w := bufio.NewWriter(f)
	for i := 0; i < n; i++ {
		fmt.Fprintf(w, "bench_org_%d\n", i)
	}
	if err := w.Flush(); err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}

	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}

	if err := bf.copyFrom("organizations", []string{"name"}, f); err != nil {
		return nil, err
	}

	return bf.fetchOrganizationIDs(n)
}

// fetchOrganizationIDs fetches the most recent n organization IDs.
func (bf *BulkFixtures) fetchOrganizationIDs(n int) ([]int64, error) {
	rows, err := bf.db.QueryContext(bf.ctx,
		"SELECT id FROM organizations ORDER BY id DESC LIMIT $1", n)
	if err != nil {
		return nil, fmt.Errorf("fetch org IDs: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, n)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to ascending order
	for i := 0; i < len(ids)/2; i++ {
		ids[i], ids[len(ids)-1-i] = ids[len(ids)-1-i], ids[i]
	}

	return ids, nil
}

// CreateRepositories creates n repositories under an organization using COPY FROM.
func (bf *BulkFixtures) CreateRepositories(orgID int64, n int) ([]int64, error) {
	if n == 0 {
		return nil, nil
	}

	// Try COPY FROM first
	ids, err := bf.createRepositoriesCopy(orgID, n)
	if err == nil {
		return ids, nil
	}

	// Fallback
	log.Printf("COPY FROM failed for repositories (%v), falling back to batch INSERT", err)
	f := NewFixtures(bf.ctx, bf.db)
	return f.CreateRepositories(orgID, n)
}

// createRepositoriesCopy creates repositories using COPY FROM.
func (bf *BulkFixtures) createRepositoriesCopy(orgID int64, n int) ([]int64, error) {
	if n > 10_000_000 {
		return bf.createRepositoriesFromFile(orgID, n)
	}
	return bf.createRepositoriesInMemory(orgID, n)
}

// createRepositoriesInMemory generates TSV data in memory.
func (bf *BulkFixtures) createRepositoriesInMemory(orgID int64, n int) ([]int64, error) {
	var buf bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&buf, "%d\tbench_repo_%d\n", orgID, i)
	}

	if err := bf.copyFrom("repositories", []string{"organization_id", "name"}, &buf); err != nil {
		return nil, err
	}

	return bf.fetchRepositoryIDs(n)
}

// createRepositoriesFromFile generates TSV data to a temp file.
func (bf *BulkFixtures) createRepositoriesFromFile(orgID int64, n int) ([]int64, error) {
	f, err := os.CreateTemp("", "melange-bench-repos-*.tsv")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	w := bufio.NewWriter(f)
	for i := 0; i < n; i++ {
		fmt.Fprintf(w, "%d\tbench_repo_%d\n", orgID, i)
	}
	if err := w.Flush(); err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}

	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}

	if err := bf.copyFrom("repositories", []string{"organization_id", "name"}, f); err != nil {
		return nil, err
	}

	return bf.fetchRepositoryIDs(n)
}

// fetchRepositoryIDs fetches the most recent n repository IDs.
func (bf *BulkFixtures) fetchRepositoryIDs(n int) ([]int64, error) {
	rows, err := bf.db.QueryContext(bf.ctx,
		"SELECT id FROM repositories ORDER BY id DESC LIMIT $1", n)
	if err != nil {
		return nil, fmt.Errorf("fetch repo IDs: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, n)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to ascending order
	for i := 0; i < len(ids)/2; i++ {
		ids[i], ids[len(ids)-1-i] = ids[len(ids)-1-i], ids[i]
	}

	return ids, nil
}

// AddOrganizationMembers adds users to an organization with the specified role using COPY FROM.
func (bf *BulkFixtures) AddOrganizationMembers(orgID int64, userIDs []int64, role string) error {
	if len(userIDs) == 0 {
		return nil
	}

	// Try COPY FROM first
	err := bf.addOrganizationMembersCopy(orgID, userIDs, role)
	if err == nil {
		return nil
	}

	// Fallback
	log.Printf("COPY FROM failed for org members (%v), falling back to batch INSERT", err)
	f := NewFixtures(bf.ctx, bf.db)
	return f.AddOrganizationMembers(orgID, userIDs, role)
}

// addOrganizationMembersCopy adds organization members using COPY FROM.
func (bf *BulkFixtures) addOrganizationMembersCopy(orgID int64, userIDs []int64, role string) error {
	if len(userIDs) > 10_000_000 {
		return bf.addOrganizationMembersFromFile(orgID, userIDs, role)
	}
	return bf.addOrganizationMembersInMemory(orgID, userIDs, role)
}

// addOrganizationMembersInMemory generates TSV data in memory.
func (bf *BulkFixtures) addOrganizationMembersInMemory(orgID int64, userIDs []int64, role string) error {
	var buf bytes.Buffer
	for _, userID := range userIDs {
		fmt.Fprintf(&buf, "%d\t%d\t%s\n", orgID, userID, role)
	}

	return bf.copyFrom("organization_members", []string{"organization_id", "user_id", "role"}, &buf)
}

// addOrganizationMembersFromFile generates TSV data to a temp file.
func (bf *BulkFixtures) addOrganizationMembersFromFile(orgID int64, userIDs []int64, role string) error {
	f, err := os.CreateTemp("", "melange-bench-org-members-*.tsv")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, userID := range userIDs {
		fmt.Fprintf(w, "%d\t%d\t%s\n", orgID, userID, role)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek: %w", err)
	}

	return bf.copyFrom("organization_members", []string{"organization_id", "user_id", "role"}, f)
}

// CreatePullRequests creates pull requests in the given repository using COPY FROM.
// Cycles through authorIDs for each PR.
func (bf *BulkFixtures) CreatePullRequests(repoID int64, authorIDs []int64, n int) ([]int64, error) {
	if n == 0 || len(authorIDs) == 0 {
		return nil, nil
	}

	// Try COPY FROM first
	ids, err := bf.createPullRequestsCopy(repoID, authorIDs, n)
	if err == nil {
		return ids, nil
	}

	// Fallback
	log.Printf("COPY FROM failed for pull requests (%v), falling back to batch INSERT", err)
	f := NewFixtures(bf.ctx, bf.db)
	return f.CreatePullRequests(repoID, authorIDs, n)
}

// createPullRequestsCopy creates pull requests using COPY FROM.
func (bf *BulkFixtures) createPullRequestsCopy(repoID int64, authorIDs []int64, n int) ([]int64, error) {
	if n > 10_000_000 {
		return bf.createPullRequestsFromFile(repoID, authorIDs, n)
	}
	return bf.createPullRequestsInMemory(repoID, authorIDs, n)
}

// createPullRequestsInMemory generates TSV data in memory.
func (bf *BulkFixtures) createPullRequestsInMemory(repoID int64, authorIDs []int64, n int) ([]int64, error) {
	var buf bytes.Buffer
	for i := 0; i < n; i++ {
		authorID := authorIDs[i%len(authorIDs)]
		fmt.Fprintf(&buf, "%d\t%d\tPR %d\tfeature-%d\n", repoID, authorID, i, i)
	}

	if err := bf.copyFrom("pull_requests",
		[]string{"repository_id", "author_id", "title", "source_branch"}, &buf); err != nil {
		return nil, err
	}

	return bf.fetchPullRequestIDs(n)
}

// createPullRequestsFromFile generates TSV data to a temp file.
func (bf *BulkFixtures) createPullRequestsFromFile(repoID int64, authorIDs []int64, n int) ([]int64, error) {
	f, err := os.CreateTemp("", "melange-bench-prs-*.tsv")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	w := bufio.NewWriter(f)
	for i := 0; i < n; i++ {
		authorID := authorIDs[i%len(authorIDs)]
		fmt.Fprintf(w, "%d\t%d\tPR %d\tfeature-%d\n", repoID, authorID, i, i)
	}
	if err := w.Flush(); err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}

	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}

	if err := bf.copyFrom("pull_requests",
		[]string{"repository_id", "author_id", "title", "source_branch"}, f); err != nil {
		return nil, err
	}

	return bf.fetchPullRequestIDs(n)
}

// fetchPullRequestIDs fetches the most recent n pull request IDs.
func (bf *BulkFixtures) fetchPullRequestIDs(n int) ([]int64, error) {
	rows, err := bf.db.QueryContext(bf.ctx,
		"SELECT id FROM pull_requests ORDER BY id DESC LIMIT $1", n)
	if err != nil {
		return nil, fmt.Errorf("fetch PR IDs: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, n)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to ascending order
	for i := 0; i < len(ids)/2; i++ {
		ids[i], ids[len(ids)-1-i] = ids[len(ids)-1-i], ids[i]
	}

	return ids, nil
}

// TupleCount returns the current count of tuples in the melange_tuples view.
func (bf *BulkFixtures) TupleCount() (int, error) {
	var count int
	err := bf.db.QueryRowContext(bf.ctx, "SELECT COUNT(*) FROM melange_tuples").Scan(&count)
	return count, err
}

// hasEnoughMemory checks if there's enough available memory for in-memory operations.
func hasEnoughMemory(requiredBytes int) bool {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Require at least 2x the needed bytes to be safe
	availableBytes := m.Sys - m.Alloc
	return availableBytes > uint64(requiredBytes*2)
}
