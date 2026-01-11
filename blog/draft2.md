## The Authorization Goldilocks Problem: Why I Built Melange

Over the holidays, I finally had some time to breathe and dive into a few "itch-to-scratch" side projects. It’s that familiar, exciting phase: a fresh repo, a blank README, and the rapid-fire implementation of core features. But like clockwork, I hit the same wall I’ve hit in every project for the last decade.

I had to deal with **Authorization.**

### Authentication: The Solved Problem

We often lump "Auth" into one bucket, but they are two very different beasts.

**Authentication (Authn)** is about identity: *Who are you?* In 2026, this is essentially a solved problem. Whether you use a managed provider or a solid library, the path is well-trodden. You set up a login form, handle a session, maybe add 2FA, and you’re done.

The interesting thing about Authn is that it doesn’t really grow with your app. Whether you have ten users or ten million, the "login" logic remains largely the same. It’s a contained unit of work. Most "unfinished" apps have perfectly functioning authentication; it's often the last thing left standing in a dead repo.

### Authorization: The Growing Burden

**Authorization (Authz)** is the opposite. It’s about permissions: *What are you allowed to do?*

In the beginning, Authz is deceptively simple. You have a few tables. Let’s look at a "simple" GitHub-style model:

* **Users** belong to **Orgs**.
* **Repos** belong to **Orgs**.
* **Members** of an Org can **contribute** to its Repos.

In your database, that looks like five tables: `users`, `orgs`, `repos`, and the join tables `org_users` and `org_repos`.

Now, imagine an API route to update a repository: `PUT /repos/:id`. You have an authenticated user ID () and a repository ID (). You need to ask: *Can User X modify Repository Y?*

To answer that, you have to verify the chain of ownership:

1. Which Org owns this Repo?
2. Is the User a member of that Org?

In SQL, it starts out manageable:

```sql
SELECT 1 FROM repos r
JOIN org_repos or ON r.id = or.repo_id
JOIN org_users ou ON or.org_id = ou.org_id
WHERE r.id = 'Y' AND ou.user_id = 'X';

```

### Where the Pain Starts

The "simple" model never stays simple. Real-world requirements start creeping in, and the questions become exponentially harder to answer with raw SQL joins.

**The "Downstream" Problem:**
You add **Issues**. Issues belong to Repos. Now, to check if a user can edit a comment on an issue, you’re joining `issues` -> `repos` -> `org_repos` -> `org_users`.

**The "Inheritance" Problem:**
What if an Org has **Teams**? And Teams can be nested? Now you aren't just checking if a user is in an Org; you're checking if they are in a Team, or a Sub-Team, that has a specific role on a Repo.

Suddenly, you are writing more code to *authorize* the request than you are to perform the actual business logic. Your queries look like this:

```sql
-- Checking permission via nested teams and direct grants
SELECT 1 FROM permissions p
LEFT JOIN team_members tm ON p.accessor_id = tm.team_id
WHERE p.resource_id = 'repo_123' 
  AND (p.user_id = 'user_abc' OR tm.user_id = 'user_abc')
  AND p.capability = 'write';

```

This is **Relationship-Based Access Control (ReBAC)**, and doing it manually in SQL is a recipe for security holes and performance bottlenecks.

### Zanzibar: The Light (and the Tunnel)

Google solved this with **Zanzibar**, a global system that treats permissions as a graph. It ignores your database tables and looks at "tuples"—simple strings that define relationships:
`user:alice is member of org:acme`
`repo:engine is child of org:acme`

Systems like **OpenFGA** or **SpiceDB** allow you to model this beautifully:

```fga
type organization
  relations
    define member: [user]

type repository
  relations
    define parent_org: [organization]
    define writer: member from parent_org

```

But there’s a catch. If you use an external Zanzibar service, you have to keep it in sync. Every time you `INSERT` into your `org_users` table, you also have to make an API call to your Authz service to create a tuple. If that API call fails or your database rolls back, your permissions are now out of sync. For a small project, this is a massive operational tax.

### The Rover Breakthrough: Pure Postgres ReBAC

I was searching for a middle ground when I found a post by the team at **Rover**. They had a "Eureka" moment: *Why not use the Zanzibar logic, but derive the tuples from our existing database tables using Views?*

They wrote a recursive PL/pgSQL function to traverse these relationships. It was brilliant. It offered the flexibility of Zanzibar without the burden of an external service. It was "Always-In-Sync" by design.

### Melange: Refined ReBAC

I loved the Rover approach, but as I pushed it further, I hit the limits of a generalized recursive function. I wanted full support for the **OpenFGA 1.1 spec**—including wildcards (e.g., "public" access) and complex usersets.

I got "nerd-sniped." I spent days implementing the spec in Postgres, but I realized that a single "one-size-fits-all" function was too slow for complex models.

That’s why I built **Melange**.

Instead of one generic function, Melange is a **compiler**. It reads your OpenFGA schema and generates **specialized, highly optimized PostgreSQL functions** tailored to your specific model.

For example, it generates a specific `check_repository_can_write(user_id, repo_id)` function. Because the function is generated *for your schema*, it knows the shortest path through the relationship graph.

* **Zero Synchronization:** It uses your existing tables via Views.
* **Atomic:** If your DB transaction succeeds, your permissions are updated instantly.
* **Postgres-Native:** No new infrastructure. Just a few functions in your existing schema.

### The Pragmatic Path

Melange is designed to carry you through the "Growth" phase of your project. It gives you the power of Google-scale relationship modeling with the "Boring Technology" reliability of a local Postgres table.

It strikes that perfect balance: sophisticated enough to handle nested teams and complex inheritance, but simple enough that you don't need a DevOps team to manage it.

---

**How does this feel for the technical depth? Would you like to see a specific example of the SQL Melange generates compared to the manual JOIN approach?**