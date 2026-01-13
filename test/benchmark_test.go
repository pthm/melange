package test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/test/authz"
	"github.com/pthm/melange/test/testutil"
)

// BenchmarkScale defines the data magnitude for a benchmark run.
type BenchmarkScale struct {
	Name          string
	Users         int
	Orgs          int
	ReposPerOrg   int
	MembersPerOrg int
	PRsPerRepo    int
}

// Production-scale benchmark configurations.
var benchmarkScales = []BenchmarkScale{
	{Name: "1K", Users: 100, Orgs: 5, ReposPerOrg: 10, MembersPerOrg: 20, PRsPerRepo: 10},
	{Name: "10K", Users: 500, Orgs: 10, ReposPerOrg: 50, MembersPerOrg: 50, PRsPerRepo: 20},
	{Name: "100K", Users: 2000, Orgs: 20, ReposPerOrg: 100, MembersPerOrg: 100, PRsPerRepo: 50},
	{Name: "1M", Users: 10000, Orgs: 50, ReposPerOrg: 200, MembersPerOrg: 200, PRsPerRepo: 100},
}

// benchmarkData holds references to created test data for benchmarks.
type benchmarkData struct {
	db         *sql.DB
	checker    *melange.Checker
	users      []int64
	orgs       []int64
	repos      []int64 // all repos across all orgs
	prs        []int64 // all PRs across all repos
	tupleCount int
}

// setupBenchmarkData creates test data at the specified scale.
func setupBenchmarkData(b *testing.B, scale BenchmarkScale) *benchmarkData {
	b.Helper()

	db := testutil.DB(b)
	ctx := context.Background()
	fixtures := testutil.NewFixtures(ctx, db)

	// Create users
	users, err := fixtures.CreateUsers(scale.Users)
	if err != nil {
		b.Fatalf("create users: %v", err)
	}

	// Create organizations
	orgs, err := fixtures.CreateOrganizations(scale.Orgs)
	if err != nil {
		b.Fatalf("create orgs: %v", err)
	}

	allRepos := make([]int64, 0, scale.Orgs*scale.ReposPerOrg)
	allPRs := make([]int64, 0, scale.Orgs*scale.ReposPerOrg*scale.PRsPerRepo)

	// For each org, add members and create repos
	for i, orgID := range orgs {
		// Distribute users across orgs as members
		// Each org gets MembersPerOrg users starting from different offsets
		startIdx := (i * scale.MembersPerOrg) % len(users)
		memberIDs := make([]int64, 0, scale.MembersPerOrg)
		for j := 0; j < scale.MembersPerOrg && j < len(users); j++ {
			idx := (startIdx + j) % len(users)
			memberIDs = append(memberIDs, users[idx])
		}

		// Add first user as owner, rest as members
		if len(memberIDs) > 0 {
			if err := fixtures.AddOrganizationMembers(orgID, memberIDs[:1], "owner"); err != nil {
				b.Fatalf("add org owner: %v", err)
			}
			if len(memberIDs) > 1 {
				if err := fixtures.AddOrganizationMembers(orgID, memberIDs[1:], "member"); err != nil {
					b.Fatalf("add org members: %v", err)
				}
			}
		}

		// Create repos for this org
		repos, err := fixtures.CreateRepositories(orgID, scale.ReposPerOrg)
		if err != nil {
			b.Fatalf("create repos: %v", err)
		}
		allRepos = append(allRepos, repos...)

		// Create PRs in each repo (use org members as authors)
		for _, repoID := range repos {
			prs, err := fixtures.CreatePullRequests(repoID, memberIDs, scale.PRsPerRepo)
			if err != nil {
				b.Fatalf("create PRs: %v", err)
			}
			allPRs = append(allPRs, prs...)
		}
	}

	// Get tuple count for reporting
	tupleCount, err := fixtures.TupleCount()
	if err != nil {
		b.Fatalf("get tuple count: %v", err)
	}

	return &benchmarkData{
		db:         db,
		checker:    melange.NewChecker(db),
		users:      users,
		orgs:       orgs,
		repos:      allRepos,
		prs:        allPRs,
		tupleCount: tupleCount,
	}
}

// BenchmarkCheck benchmarks the Check function across different scales.
func BenchmarkCheck(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	for _, scale := range benchmarkScales {
		b.Run(scale.Name, func(b *testing.B) {
			data := setupBenchmarkData(b, scale)
			b.Logf("Setup complete: %d tuples", data.tupleCount)

			ctx := context.Background()

			b.Run("DirectMembership", func(b *testing.B) {
				// Check org membership (direct tuple lookup)
				user := authz.User(data.users[0])
				org := authz.Organization(data.orgs[0])

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, err := data.checker.Check(ctx, user, authz.RelCanRead, org)
					if err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("InheritedPermission", func(b *testing.B) {
				// Check repo permission inherited from org membership
				user := authz.User(data.users[0])
				repo := authz.Repository(data.repos[0])

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, err := data.checker.Check(ctx, user, authz.RelCanRead, repo)
					if err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("ExclusionPattern", func(b *testing.B) {
				if len(data.prs) == 0 {
					b.Skip("no PRs created")
				}

				// Check can_review on PR (uses "but not author" exclusion)
				// Use a user who is NOT the author of the first PR
				user := authz.User(data.users[len(data.users)-1])
				pr := authz.PullRequest(data.prs[0])

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, err := data.checker.Check(ctx, user, authz.RelCanReview, pr)
					if err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("DeniedPermission", func(b *testing.B) {
				// Check permission that should be denied (user not in org)
				// Create a user ID that doesn't exist in the data
				user := authz.User(999999999)
				org := authz.Organization(data.orgs[0])

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, err := data.checker.Check(ctx, user, authz.RelCanRead, org)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkListObjects benchmarks the ListObjects function across different scales.
func BenchmarkListObjects(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	pageSizes := []int{10, 50, 100, 500}

	for _, scale := range benchmarkScales {
		b.Run(scale.Name, func(b *testing.B) {
			data := setupBenchmarkData(b, scale)
			b.Logf("Setup complete: %d tuples, %d repos", data.tupleCount, len(data.repos))

			ctx := context.Background()

			for _, pageSize := range pageSizes {
				b.Run(fmt.Sprintf("ListAccessibleRepos_Page%d", pageSize), func(b *testing.B) {
					// List first page of repos a user can read
					user := authz.User(data.users[0])

					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ids, cursor, err := data.checker.ListObjects(ctx, user, authz.RelCanRead, authz.TypeRepository, melange.PageOptions{Limit: pageSize})
						if err != nil {
							b.Fatal(err)
						}
						_ = ids
						_ = cursor
					}
				})
			}

			for _, pageSize := range pageSizes {
				b.Run(fmt.Sprintf("ListAccessibleOrgs_Page%d", pageSize), func(b *testing.B) {
					// List first page of orgs a user can read
					user := authz.User(data.users[0])

					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ids, cursor, err := data.checker.ListObjects(ctx, user, authz.RelCanRead, authz.TypeOrganization, melange.PageOptions{Limit: pageSize})
						if err != nil {
							b.Fatal(err)
						}
						_ = ids
						_ = cursor
					}
				})
			}

			for _, pageSize := range pageSizes {
				b.Run(fmt.Sprintf("ListAccessiblePRs_Page%d", pageSize), func(b *testing.B) {
					if len(data.prs) == 0 {
						b.Skip("no PRs created")
					}

					// List first page of PRs a user can read
					user := authz.User(data.users[0])

					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ids, cursor, err := data.checker.ListObjects(ctx, user, authz.RelCanRead, authz.TypePullRequest, melange.PageOptions{Limit: pageSize})
						if err != nil {
							b.Fatal(err)
						}
						_ = ids
						_ = cursor
					}
				})
			}

			// Benchmark pagination: fetching all results by walking through pages
			b.Run("ListAccessibleRepos_PaginateAll", func(b *testing.B) {
				user := authz.User(data.users[0])

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var allIDs []string
					var cursor *string
					for {
						ids, next, err := data.checker.ListObjects(ctx, user, authz.RelCanRead, authz.TypeRepository, melange.PageOptions{Limit: 100, After: cursor})
						if err != nil {
							b.Fatal(err)
						}
						allIDs = append(allIDs, ids...)
						if next == nil {
							break
						}
						cursor = next
					}
					_ = allIDs
				}
			})
		})
	}
}

// BenchmarkListSubjects benchmarks the ListSubjects function across different scales.
func BenchmarkListSubjects(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	pageSizes := []int{10, 50, 100, 500}

	for _, scale := range benchmarkScales {
		b.Run(scale.Name, func(b *testing.B) {
			data := setupBenchmarkData(b, scale)
			b.Logf("Setup complete: %d tuples, %d users", data.tupleCount, len(data.users))

			ctx := context.Background()

			for _, pageSize := range pageSizes {
				b.Run(fmt.Sprintf("ListOrgMembers_Page%d", pageSize), func(b *testing.B) {
					// List first page of users who can read an org
					org := authz.Organization(data.orgs[0])

					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ids, cursor, err := data.checker.ListSubjects(ctx, org, authz.RelCanRead, authz.TypeUser, melange.PageOptions{Limit: pageSize})
						if err != nil {
							b.Fatal(err)
						}
						_ = ids
						_ = cursor
					}
				})
			}

			for _, pageSize := range pageSizes {
				b.Run(fmt.Sprintf("ListRepoReaders_Page%d", pageSize), func(b *testing.B) {
					// List first page of users who can read a repo
					repo := authz.Repository(data.repos[0])

					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ids, cursor, err := data.checker.ListSubjects(ctx, repo, authz.RelCanRead, authz.TypeUser, melange.PageOptions{Limit: pageSize})
						if err != nil {
							b.Fatal(err)
						}
						_ = ids
						_ = cursor
					}
				})
			}

			for _, pageSize := range pageSizes {
				b.Run(fmt.Sprintf("ListRepoWriters_Page%d", pageSize), func(b *testing.B) {
					// List first page of users who can write to a repo (usually smaller set)
					repo := authz.Repository(data.repos[0])

					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ids, cursor, err := data.checker.ListSubjects(ctx, repo, authz.RelCanWrite, authz.TypeUser, melange.PageOptions{Limit: pageSize})
						if err != nil {
							b.Fatal(err)
						}
						_ = ids
						_ = cursor
					}
				})
			}

			// Benchmark pagination: fetching all results by walking through pages
			b.Run("ListRepoReaders_PaginateAll", func(b *testing.B) {
				repo := authz.Repository(data.repos[0])

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var allIDs []string
					var cursor *string
					for {
						ids, next, err := data.checker.ListSubjects(ctx, repo, authz.RelCanRead, authz.TypeUser, melange.PageOptions{Limit: 100, After: cursor})
						if err != nil {
							b.Fatal(err)
						}
						allIDs = append(allIDs, ids...)
						if next == nil {
							break
						}
						cursor = next
					}
					_ = allIDs
				}
			})
		})
	}
}

// BenchmarkCheckParallel benchmarks Check under parallel load.
func BenchmarkCheckParallel(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	// Use medium scale for parallel tests
	scale := BenchmarkScale{
		Name:          "10K-Parallel",
		Users:         500,
		Orgs:          10,
		ReposPerOrg:   50,
		MembersPerOrg: 50,
		PRsPerRepo:    20,
	}

	data := setupBenchmarkData(b, scale)
	b.Logf("Setup complete: %d tuples", data.tupleCount)

	ctx := context.Background()

	b.Run("ParallelDirectCheck", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				// Cycle through users and orgs
				user := authz.User(data.users[i%len(data.users)])
				org := authz.Organization(data.orgs[i%len(data.orgs)])

				_, err := data.checker.Check(ctx, user, authz.RelCanRead, org)
				if err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	})

	b.Run("ParallelInheritedCheck", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				user := authz.User(data.users[i%len(data.users)])
				repo := authz.Repository(data.repos[i%len(data.repos)])

				_, err := data.checker.Check(ctx, user, authz.RelCanRead, repo)
				if err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	})
}

// BenchmarkCheckWithCache benchmarks Check with caching enabled.
func BenchmarkCheckWithCache(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	// Use medium scale
	scale := BenchmarkScale{
		Name:          "10K-Cached",
		Users:         500,
		Orgs:          10,
		ReposPerOrg:   50,
		MembersPerOrg: 50,
		PRsPerRepo:    20,
	}

	data := setupBenchmarkData(b, scale)
	b.Logf("Setup complete: %d tuples", data.tupleCount)

	ctx := context.Background()

	b.Run("WithoutCache", func(b *testing.B) {
		checker := melange.NewChecker(data.db)
		user := authz.User(data.users[0])
		repo := authz.Repository(data.repos[0])

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := checker.Check(ctx, user, authz.RelCanRead, repo)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("WithCache", func(b *testing.B) {
		cache := melange.NewCache()
		checker := melange.NewChecker(data.db, melange.WithCache(cache))
		user := authz.User(data.users[0])
		repo := authz.Repository(data.repos[0])

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := checker.Check(ctx, user, authz.RelCanRead, repo)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("WithCacheColdStart", func(b *testing.B) {
		// Each iteration gets a fresh cache (cold start scenario)
		user := authz.User(data.users[0])
		repo := authz.Repository(data.repos[0])

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			cache := melange.NewCache()
			checker := melange.NewChecker(data.db, melange.WithCache(cache))
			_, err := checker.Check(ctx, user, authz.RelCanRead, repo)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkTupleCount reports the actual tuple counts at each scale.
// This is useful for verifying the benchmark setup.
func BenchmarkTupleCount(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	for _, scale := range benchmarkScales {
		b.Run(scale.Name, func(b *testing.B) {
			data := setupBenchmarkData(b, scale)

			b.ReportMetric(float64(data.tupleCount), "tuples")
			b.ReportMetric(float64(len(data.users)), "users")
			b.ReportMetric(float64(len(data.orgs)), "orgs")
			b.ReportMetric(float64(len(data.repos)), "repos")
			b.ReportMetric(float64(len(data.prs)), "prs")

			// Run a single check just to have something to measure
			ctx := context.Background()
			user := authz.User(data.users[0])
			org := authz.Organization(data.orgs[0])

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = data.checker.Check(ctx, user, authz.RelCanRead, org)
			}
		})
	}
}

// printScale logs the scale configuration for debugging.
func printScale(b *testing.B, scale BenchmarkScale) {
	expectedTuples := scale.Orgs*scale.MembersPerOrg + // org members
		scale.Orgs*scale.ReposPerOrg + // repo->org relationships
		scale.Orgs*scale.ReposPerOrg*scale.PRsPerRepo*2 // PRs (repo rel + author)

	b.Logf("Scale %s: ~%d expected tuples (users=%d, orgs=%d, repos=%d, prs=%d)",
		scale.Name, expectedTuples,
		scale.Users, scale.Orgs,
		scale.Orgs*scale.ReposPerOrg,
		scale.Orgs*scale.ReposPerOrg*scale.PRsPerRepo)
}

// BenchmarkScaleVerification verifies that the scales produce expected tuple counts.
func BenchmarkScaleVerification(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	for _, scale := range benchmarkScales {
		b.Run(scale.Name, func(b *testing.B) {
			printScale(b, scale)

			data := setupBenchmarkData(b, scale)

			b.Logf("Actual: tuples=%d, users=%d, orgs=%d, repos=%d, prs=%d",
				data.tupleCount,
				len(data.users),
				len(data.orgs),
				len(data.repos),
				len(data.prs))

			// Verify we got what we expected
			if len(data.users) != scale.Users {
				b.Errorf("expected %d users, got %d", scale.Users, len(data.users))
			}
			if len(data.orgs) != scale.Orgs {
				b.Errorf("expected %d orgs, got %d", scale.Orgs, len(data.orgs))
			}
			expectedRepos := scale.Orgs * scale.ReposPerOrg
			if len(data.repos) != expectedRepos {
				b.Errorf("expected %d repos, got %d", expectedRepos, len(data.repos))
			}
			expectedPRs := scale.Orgs * scale.ReposPerOrg * scale.PRsPerRepo
			if len(data.prs) != expectedPRs {
				b.Errorf("expected %d PRs, got %d", expectedPRs, len(data.prs))
			}

			// Dummy benchmark loop
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = fmt.Sprintf("%d", data.tupleCount)
			}
		})
	}
}
