## The Authorization Goldilocks Problem: Why I Built Melange

Over the holidays, I finally had some time to breathe and dive into a few "itch-to-scratch" side projects. A fresh repo, a blank README, and the rapid-fire implementation of core features. But like clockwork, I hit the same wall I’ve hit in every project for the last decade.

I had to deal with **Authorization.**

### Authentication

We often lump "Auth" into one bucket, for good reason they are very close cousins but they are two very different beasts.

**Authentication (Authn)** is about identity: _Who are you?_ it's usually one of the first problems you solve, I have built more authentication systems than I have built full apps or products. Most unfinished apps have a functioning authentication systems. In 2026, this is essentially a solved problem.

Whether you use a managed provider or a solid library, the path is well-trodden. You set up a login form, handle a session, maybe add 2FA, and you’re done.

The interesting thing about Authn is that it doesn’t really grow with your app. Whether you have ten users or ten million, the "login" logic remains largely the same.

### Authorization

**Authorization (Authz)** is the opposite. It’s about permissions: _What are you allowed to do?_

In the beginning, Authz is deceptively simple. Let’s look at a standard GitHub-style model:

- **Users** belong to **Orgs**.
- **Repos** belong to **Orgs**.
- **Members** of an Org can **contribute** to its Repos.

In your database, that looks like five tables: `users`, `orgs`, `repos`, and the join tables `org_users` and `org_repos`.

Imagine an API route to update a repository: `PUT /repos/:id`. You have an authenticated user ID () and a repository ID (). You need to ask: _Can User X modify Repository Y?_

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

A relatively simple query that joins through the ownership chain. It works fine for five tables. But real-world requirements never stay simple.

### Growing Pain

You add **Issues**. Issues belong to Repos. Now, to check if a user can edit an issue, you’re joining `issues` -> `repos` -> `org_repos` -> `org_users`.

What if an Org has **Teams**? And Teams can be nested? Now you aren't just checking if a user is in an Org; you're checking if they are in a Team, or a Sub-Team, that has a specific role on a Repo.

Suddenly, you are writing more code to _authorize_ the request than you are to perform the actual business logic. Your queries look like this:

```sql
-- Checking permission via nested teams and direct grants
SELECT 1 FROM permissions p
LEFT JOIN team_members tm ON p.accessor_id = tm.team_id
WHERE p.resource_id = 'repo_123'
  AND (p.user_id = 'user_abc' OR tm.user_id = 'user_abc')
  AND p.capability = 'write';

```

This is **Relationship-Based Access Control (ReBAC)**, and doing it manually in SQL or in your application code can be cumbersome and error-prone.

### The Light and the Tunnel

There are a number of solutions to this problem, and a plethora of services you can run or buy that aim to provide a way of answering these questions for you.

A large portion of these systems are based off a paper published by Google, ["Zanzibar: Google’s Consistent, Global Authorization System"](https://research.google/pubs/zanzibar-googles-consistent-global-authorization-system/).

These systems treat your permissions as a graph. They ignore your database tables and looks at "tuples", simple strings that define relationships:
`user:alice is member of org:acme`
`repo:engine is child of org:acme`

A few notable examples of these Zanzibar or Zanzibar like systems are:
- [OpenFGA](https://openfga.dev/)
- [SpiceDB](https://spicedb.com/)
- [Keto](https://www.getketo.com/)
- [Casbin](https://casbin.org/)

For the rest of this post I will be talking about **OpenFGA**

OpenFGA is an open-source implementation of Zanzibar (donated to the CNCF by Okta/Auth0).

OpenFGA provides a Domain Specific Language (DSL) to model your authorization rules. But it has a high "Entry Fee." Because OpenFGA is a separate service, you face a **Synchronization Problem**: every time you update your database, you must also update the OpenFGA tuples via an API call. If one fails, your permissions are out of sync.

### RoverApp - Pure Postgres ReBAC

I was searching for a middle ground when I found a [brilliant post by the team at Rover](https://getrover.substack.com/p/how-we-rewrote-openfga-in-pure-postgres). They loved the OpenFGA model but hated the sync overhead. Their solution was to implement OpenFGA in Postgres. **[pgfga](https://github.com/rover-app/pgfga)**.

Rover’s approach was elegant in its simplicity:

1. **The Model Table:** They created an `authz_model` table to store the OpenFGA schema directly in Postgres.
2. **Dynamic Tuples (The Secret Sauce):** Instead of a static table of tuples that you have to sync, they used **Database Views**. They mapped their existing domain tables (like `org_users`) into a view that "looks" like an OpenFGA tuple table.
3. **The Generic Check:** They wrote a recursive PL/pgSQL function—`check_permission(user, relation, object)`—that traverses these views to find a path between the user and the resource.

This was a game-changer. It meant you could use the power of OpenFGA's modeling while keeping everything "Always-In-Sync" within a single Postgres transaction.

### Melange

I took the Rover solution and ran with it, but I eventually hit a ceiling.

The Rover implementation used a generalized, recursive function. This is fine for simpler models, but as I tried to implement the more advanced parts of the **OpenFGA 1.1 spec**—specifically **wildcards** (for public access) and **Tuple-to-Userset (TTU)** patterns—the generalized logic became a bottleneck.

I got "nerd-sniped." I spent the holiday implementing the full spec, but I decided on a different architectural path. Instead of a generic interpreter, I built **Melange**.

**Melange is a compiler.** It reads your OpenFGA schema and generates specialized PostgreSQL functions tailored specifically to your relationship graph.

#### 1. Specialized Dispatching

Instead of a single, massive recursive function trying to handle every possible entity type, Melange generates specific logic for every relationship.

* It creates a **dispatcher function** that acts as the entry point.
* When you call `check_permission('user:1', 'writer', 'repo:5')`, the dispatcher routes the request to an optimized `check_repo_writer(1, 5)` function.
* Because these functions are generated for *your* specific model, the PostgreSQL query planner can optimize the join paths and indexes.

#### 2. List Queries

Real-world apps need to do more than just binary checks. You often need to populate the UI with data the user is actually allowed to see. Melange supports **List Queries** out of the box:

* **Object Filtering:** *"What repositories can User X write to?"*
* **Subject Filtering:** *"Which users have 'owner' access to this Organization?"*

These are generated as set-returning functions, allowing you to join them directly into your main application queries.

#### 3. Performance & Compliance

By moving from an interpreted approach to a compiled approach, the performance gains are significant.

* **Sub-millisecond latency:** In my benchmarks, most permission checks execute in under 1ms. At this point, the network latency between your app and your database is a bigger overhead than the authorization logic itself.
* **100% Spec Compliance:** Melange is tested against the official OpenFGA test suite, achieving 100% compliance with the 1.1 executable spec, including complex userset rewrites and wildcards.

Melange is built for the "Middle Phase." Most of us aren't Google, and we don't need a globally distributed auth cluster on Day 1. We just need a permission system that is flexible enough to grow but simple enough to live in our "boring" database.

Melange gives you the sophisticated modeling of OpenFGA with the reliability of a local Postgres function. It’s designed to carry you from your first user until the day you’re actually big enough to need a dedicated Zanzibar cluster. And because it uses the standard OpenFGA schema, your migration path to a standalone service like OpenFGA or SpiceDB is already written.

Melange is open source and available on **[GitHub](https://github.com/pthm/melange)**.