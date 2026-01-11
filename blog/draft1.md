Over the holiday I have been hacking on a few projects and ideas. As part of this process I found myself having to solve a problem I have had to solve many times over in the past... Authorization.

Authorization (authz) is a term that is commonly overloaded and misunderstood, it commonly gets bundled with Authentication, for good reason they are very close cousins. Authentication is usually one of the first problems you solve, I have built more authentication systems than I have built full apps or products. most unfinished apps have a functioning authentication systems.

AUthorization especially in the early stages of product development easily becomes and afterthought, or is easily addressed by simple rules.

Consider a simple product, for the case of this example we will use GitHub as an example. We have 3 entities, Users, Organization (orgs) & Repositories (repos).

- Users are members of Orgs
- Repos belong to Orgs
- Users who are members of the Org a Repo belongs to can contribute to that Repo

This whole data model is probably captured in 5 database tables

- users
- orgs
- repos
- org_users - join table, represents user -> org membership
- org_repos - join table, represents repo -> org ownership

Answering the question, can this user contribute to this repo results in a somewhat complex query.

Say you have an API route, or some sort of service handler to update the description of a repository like

```
PUT /repos/:id
{
  "description": "Another NPM package, trying to conquer the world",
}
```

You likely have some sort of authentication system, maybe a middleware that looks up the user in your database or a stateless system with tokens. So you are starting from a point where you have an authenticated user and a repository ID.

Now you need to answer the question, does this User X have permission you modify Repository Y.

To answer this question in our model you need to answer a few intermediary questions.

- What organization does the repository belong to?
- Is the user a member of that organization?

You can try to do this in a single query, something like

```sql
SELECT * FROM repos
JOIN org_repos ON repos.id = org_repos.repo_id as or
JOIN orgs ON or.org_id = orgs.id as o
JOIN org_users ON o.id = org_users.org_id ou
WHERE repos.id = 'Y' AND ou.user_id = 'X'
```

If you get a result, you're good to go.

This works, this is probably good enough for this system.

But in the real world as your system grows and evolves as new entities are added to your domain model maintaining these rules becomes cumbersome and can be error prone.

If we add additional resources downstream of the repo, lets say issues belong to repos. now we have another additional JOIN required for every permission check.

In complex schemas you will write more code to authenticate your requests than you will to actually perform your business logic, nobody wants to be doing this.

This is relationship based authentication. It's hard.

There are a number of solutions to this problem, and a plethora of services you can run or buy that aim to provide a way of answering these questions for you. A large portion of these systems are based off a research paper published by Google entitled, "Zanzibar: Google’s Consistent, Global Authorization System".

This paper outlines Google's approach to solving this problem at scale for their systems which are answering these questions billions of times a day across their entire stack for all their products and the millions of relationships between them. This is fantastic proof, if it works for Google it must be good enough for us.

But we have 5 database tables and if we're lucky a few thousand users.

Google's problems are not our problems, are their solutions ours too?

There are a number of open source implementations of Zanzibar like systems and there are a bunch of SaaS solutions who will happily take your money to solve this problem too.

- Ory Keto
- SpiceDB
- Permify
- OpenFGA

Zanzibar and all these systems that implement versions of it or derivatives operate largely the same way. You model your authentication schema as a collection of entities with relationships between them.

```fga
type user

type organization:
  member: [user]

type repository
  owner: organization
  contributor: member from owner
```

Once you have your schema and your rules, you store tuples representing the objects in your system, things like

```
user:1 member org:10
repo:8 owner org:1
```

Then at runtime you can query this data to answer the question. This system is very flexible, you can describe the simplest and the most complex relationship based rules to define what or who can do what where in your system.

A common maxim that is perpetuated amongst product people is something like "never roll your own auth".

I disagree with blindly following this for a number of reasons. This advice is usually always given in good faith, it's trying to protect you from yourself. Auth/z is hard, it's been solved many times over, why re-invent the wheel?

In order to not re-invent the wheel you will now be faced with another hard problem. Synchronization.

There is another maxim that I would like to see people offer more KISS. "Keep it simple, stupid"

Outsourcing your auth doesn't absolve you of responsibility, there are still plenty of things that can go wrong and security holes you can introduce even when you are using an off the shelf auth system. It's not a golden ticket.

Adding complexity to your system in the name of external services, API calls and synchronization is more harmful than good.

So we roll our own.

We have 5 database tables and growing and some questions that needs answering.

Another team faced with exactly this problem came up with a neat solution. Take the simplicity of the database query we theorized before and the flexibility of the tuple based authz system and put them together. 

https://getrover.substack.com/p/how-we-rewrote-openfga-in-pure-postgres

Rover's teams approach is to create a database VIEW and derive your tuples from your existing domain tables and create a representation of your authz model in a table to query at runtime.

This has a number of benefits.

It's always in sync, you never have to update your tuples
Tuples updates are atomic, no double submit transaction handling
It's simple, no additional services to maintain.

Once they have their tuples they wrote a relatively simple PLpgSQL function to query them and a few helper scripts to construct the model tables that the function relies on to traverse the relationships between the tuples.

This was exactly the kind of solution to the problem I wanted, it strikes a perfect balance between sophistication and simplicity.

I took this solution and ran with it. But my needs outgrew the level of implementation they had completed.

The solution they put together is based off of OpenFGA's modelling language `fga`

The examples models I proposed earlier are a stripped back version of this modelling language.

In fga the model we proposed earlier might actually look like this

```
model
  schema 1.1

type user

type organization
  relations
    define member: [user]

type repository
  relations
    define org: [organization]
    define contributor: member from org
    define can_read: contributor
    define can_write: contributor
```

Here we have our 3 entities defined as "types" and relations between them, that we are familiar with.

Rover's pgfga solution can take this model and represent it in your database as series of rows for each relation, I recommend you read their blog post if you are interested to understand it more completely.

I wanted to be able to use more of the OpenFGA modelling language, notably wildcards and usersets. These are powerful concepts that allow you to model more complex authorization schemes like public access and groups.

I started extending their query and eventually got enough working that I could complete my project, but this problem and the accompanying solution got stuck in my head, I had been nerd-sniped.

So I got stuck in, implementing more and more of the OpenFGA spec in Postgres, after a few days I had full spec compatibility but it required more tables and heavy recursive queries to support more complex relationship changes and TTU (tuple to userset) patterns with a generalized query function.

After some more though I decided an an alternative approach remaining true to the spirit of their original solution.

Instead of using a generalized function for querying the tuples, generating specialised query functions for the relationships defined in the model.

This is what Melange does.

Melange is a tool I have put together that does this, It can take any OpenFGA 1.1 schema and generate specialized functions to query a tuples table directly in PostgreSQL.


