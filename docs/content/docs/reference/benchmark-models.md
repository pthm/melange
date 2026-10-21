---
title: Benchmark schemas
description: "Complete schemas, data layout, and query selection behind the October 2026 performance measurements."
sidebar:
  exclude: true
---

These are the models behind the [Performance reference](../performance/). The October 2026 cloud run uses two scale fixtures and the small, individually defined OpenFGA compatibility fixtures. A feature's cost depends on its relation, query direction, and data distribution; the models below make those differences explicit.

## Kitchen-sink collaboration model

The source is `test/testutil/testdata/kitchen_sink_schema.fga`. The complete schema below omits only comments. It includes types and relations that are seeded for coverage but are not individually timed by the kitchen-sink suite.

```fga
model
  schema 1.1

type user

type service_account

type platform
  relations
    define admin: [user]

type mailing_list
  relations
    define subscriber: [user, service_account]

type group
  relations
    define member: [user, service_account, group#member]
    define banned: [user, user:*]
    define active_member: [user, user:*, group#active_member] but not banned

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user, group#member] or admin
    define billing: [user]
    define can_admin: admin
    define can_read: member
    define guest: [user, user:*]

type team
  relations
    define org: [organization]
    define maintainer: [user]
    define member: [user, group#member] or maintainer or member from org
    define can_read: member

type project
  relations
    define org: [organization]
    define admin: [user] or admin from org
    define editor: [user, group#member]
    define viewer: [user, team#member] or editor
    define public: [user:*, service_account:*]
    define contributor: editor or admin

type folder
  relations
    define parent: [folder, project]
    define org: [organization]
    define viewer: [user, group#member] or viewer from parent or member from org
    define editor: [user] or editor from parent
    define public: [user:*]
    define banned: [user]
    define co_owner: viewer and editor
    define protected: viewer from parent but not banned

type document
  relations
    define folder: [folder]
    define owner: [user]

    define editor: [user, group#member, folder#editor] or owner or editor from folder

    define viewer: [user, group#member, folder#viewer] or editor or viewer from folder or public
    define public: [user:*]

    define active: [user]
    define reviewer: [user]

    define blocked: [user, user:*]
    define restricted: [user]

    define can_edit: editor and active
    define gated: viewer and active
    define can_manage: editor and editor from folder
    define audience: [group#member] and active

    define can_view: viewer but not blocked
    define can_comment: gated but not blocked
    define can_review: viewer but not owner
    define can_read_safe: viewer but not restricted
    define can_share: can_edit but not banned from folder
    define visible: viewer but not owner_and_blocked
    define owner_and_blocked: owner and blocked

    define super_view: viewer or admin from platform
    define platform: [platform]

    define elevated: can_edit or can_manage

type report
  relations
    define document: [document]
    define audience: [group#member]
    define inherited_view: can_view from document

type comment
  relations
    define document: [document]
    define author: [user]
    define can_read: can_view from document
    define can_edit: author or can_manage from document
```

### Data shape

`GenerateKitchenSinkTuples` in `test/testutil/kitchensink_fixtures.go` creates deterministic, overlapping grants:

- Groups form membership chains; chain tails grant users and service accounts. Organizations combine owners, admins, direct members, and group usersets.
- Each organization has teams and projects. Some organization guest and project public relations grant wildcards.
- Each organization has several parallel folder chains. Root folders link to projects; deeper folders link to the previous level, and all folders also link to the organization. This is not an exponentially branching tree.
- Every folder has documents with direct owners, editors, group grants, and a `folder#editor` userset. Documents link to a platform singleton. Every fourth document is public, every seventh has a wildcard block, and every document has an active user.
- One report and comment per folder exercise pure userset, inherited exclusion, and TTU composition paths.

| Label | Organizations | Members/org | Teams/org | Projects/org | Folders/level/org | Documents/folder | Service accounts |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1K | 5 | 20 | 3 | 5 | 3 | 4 | 20 |
| 10K | 15 | 40 | 4 | 10 | 4 | 6 | 80 |
| 100K | 40 | 80 | 6 | 20 | 5 | 10 | 300 |
| 1M | 100 | 200 | 8 | 40 | 6 | 16 | 1,500 |

User counts, group chain lengths, folder depths, and exact tuple counts are in the [performance dataset table](../performance/#benchmark-models). These parameters change together; this is a workload growth study rather than a controlled experiment varying only tuple count.

### Physical storage

Unlike the hierarchy fixture's domain tables, this model uses a flat base table. `melange_tuples` is a regular view over it, not a materialized view:

```sql
CREATE TABLE kitchen_sink_tuples (
    subject_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    relation TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL
);
CREATE INDEX ks_by_object
    ON kitchen_sink_tuples (object_type, object_id, relation);
CREATE INDEX ks_by_subject
    ON kitchen_sink_tuples (subject_type, subject_id, relation);
CREATE INDEX ks_by_edge
    ON kitchen_sink_tuples (object_type, relation, subject_type, subject_id);
CREATE VIEW melange_tuples AS
    SELECT subject_type, subject_id, relation, object_type, object_id
    FROM kitchen_sink_tuples;
```

### What is actually timed

The kitchen-sink suite calls generated SQL directly through `database/sql`; it drains list rows. It does not measure the Go Checker's input validation or pagination. Names below are suffixes under `BenchmarkKitchenSinkCheck`, `ListObjects`, or `ListSubjects`.

| Benchmark suffix | Subject / object selection | Relation and feature path |
| --- | --- | --- |
| Check `PlainUser` | Organization owner; first document by ID | `document.can_view`: union, TTU, nested usersets, wildcard, exclusion |
| Check `MemberCanView` | Direct organization member; same document | Same relation with a different subject |
| Check `Wildcard` | Literal `user:*`; same document | Same relation with a wildcard query subject, not a named public user |
| Check `CanEdit` | Owner subject; same document | `document.can_edit`: `editor and active` |
| Check `UsersetSubject` | A stored `group#member` organization grant | `organization.member`; querying the userset itself |
| Check `ServiceAccount` | Stored service-account/group membership pair | `group.member` |
| ListObjects `DocCanView` | Owner subject; document objects | `document.can_view`, including exclusion |
| ListObjects `DocGated` | Owner subject; document objects | `document.gated`: `viewer and active` |
| ListObjects `DocCanEdit` | Owner subject; document objects | `document.can_edit`: `editor and active` |
| ListObjects `DocEditor_Member` | Direct organization member; document objects | `document.editor`, including usersets and inherited editors |
| ListObjects `FolderViewer` | Owner subject; folder objects | `folder.viewer`, recursive TTU plus organization anchor |
| ListObjects `UsersetSubject_OrgMember` | Same userset as check; organizations | `organization.member` |
| ListObjects `ServiceAccount_GroupMember` | Same service account as check; groups | `group.member` |
| ListSubjects `viewer`, `can_view`, `gated`, `can_edit` | First document by ID; user subjects | The corresponding document relation |
| ListSubjects `FolderViewer`, `FolderEditor` | First folder by ID; user subjects | `folder.viewer`, `folder.editor` |

The selected first document has a wildcard block: its `can_view` subject list is empty even though its `viewer` list is broad. `gated` and `can_edit` subject lists each return one user. That difference explains why “intersection is always slower than exclusion” would be misleading. Setup picks an organization owner and direct member without an explicit ordering; replaying the fixture does not establish that every possible subject has the measured latency.

`Expand` and `Explain` use that document's `can_view`. `Bulk` submits three checks across document, folder, and organization types. Relation presence in the schema does not mean every combination is covered by a separate timing.

## Organization / repository / pull request model

This is `test/testutil/testdata/schema.fga`, used by `test/benchmark_test.go`. These benchmarks call the Go `Checker`, except where a benchmark explicitly calls SQL. The full schema is:

```fga
model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
    define billing_manager: [user]
    define can_read: member
    define can_admin: admin
    define can_delete: owner

type team
  relations
    define org: [organization]
    define maintainer: [user]
    define member: [user] or maintainer
    define can_read: member
    define can_admin: maintainer

type repository
  relations
    define org: [organization]
    define owner: [user]
    define admin: [user] or owner
    define maintainer: [user] or admin
    define writer: [user] or maintainer
    define reader: [user] or writer or can_read from org
    define author: [user] or owner
    define banned: [user:*]
    define can_read: reader
    define can_write: writer
    define can_admin: admin
    define can_delete: owner
    define can_review: can_read but not author
    define can_read_safe: can_read but not banned
    define can_deploy: can_admin from org

type issue
  relations
    define repo: [repository]
    define author: [user]
    define assignee: [user]
    define can_read: can_read from repo
    define can_edit: author or can_write from repo
    define can_close: author or can_write from repo
    define can_delete: can_admin from repo

type pull_request
  relations
    define repo: [repository]
    define author: [user]
    define reviewer: [user]
    define banned: can_admin from repo
    define can_read: can_read from repo
    define can_edit: author or can_write from repo
    define can_merge: can_write from repo
    define can_review: can_read from repo but not author
    define can_review_strict: can_read from repo but not banned
    define can_delete: can_admin from repo
```

The scale loader populates organization memberships, repositories linked to those organizations, and PRs linked to repositories with authors. Other schema types and relation definitions support integration tests but are not all populated by this scale loader. `melange_tuples` is a `UNION ALL` view over domain tables, including `organization_members`, `repositories`, and `pull_requests`, with IDs cast to text. See `test/testutil/testdata/tuples_view.sql.tmpl` and `domain_tables.sql` for the view and indexes; these timings include that view's work.

| Label | Users | Organizations | Members/org | Repositories/org | PRs/repository | Tuples |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 1K | 100 | 5 | 20 | 10 | 10 | 1,150 |
| 10K | 500 | 10 | 50 | 50 | 20 | 21,000 |
| 100K | 2,000 | 20 | 100 | 100 | 50 | 204,000 |
| 1M | 10,000 | 50 | 200 | 200 | 100 | 2,020,000 |

`ListAccessiblePRs_PageN` repeatedly requests the first page for the first user, with limit N. The accessible PR universe is 100, 1,000, 5,000, and 20,000 objects respectively; the returned payload is bounded by the page limit. It does not benchmark traversing every page. Bulk results vary batch size and duplication; caching and four-worker concurrency use the 10K label (21,000 tuples).

## OpenFGA compatibility fixtures

`BenchmarkOpenFGA_All` loads the official `consolidated_1_1_tests.yaml` plus Melange's `test/openfgatests/testdata/*.yaml`. Each model receives its own database and seeded tuples. Every assertion has a named benchmark; `ListUsers` is the suite name for subject listing. Bulk batches contain that fixture's check assertions, so batch sizes differ across fixtures.

These timings run Melange against OpenFGA models. They do not compare Melange with the OpenFGA service. A name-based feature category can contain several features and is not a controlled per-feature measurement. The [named fixture table](../performance/#feature-examples-on-small-fixtures) keeps operations separate and identifies the examples behind each range.

For reproducing the run and interpreting server/client timing, see [Methodology](../performance/#methodology) and [Benchmarking](../../contributing/benchmarking/).
