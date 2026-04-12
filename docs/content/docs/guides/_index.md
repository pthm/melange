---
title: Guides
weight: 3
sidebar:
  open: true
---

Task-oriented guides for using Melange's client libraries, SQL functions, and operational patterns. Each guide includes examples in Go, TypeScript, and SQL where applicable. For direct SQL function signatures, see the [SQL API reference](../reference/sql-api).

{{< cards >}}
{{< card link="modelling-guide" title="Modelling Guide" subtitle="Build an authorization model from scratch with end-to-end examples" icon="document-text" >}}
{{< card link="checking-permissions" title="Checking Permissions" subtitle="Validate access using the Checker API" icon="shield-check" >}}
{{< card link="listing-objects" title="Listing Objects" subtitle="Find all objects a subject can access" icon="collection" >}}
{{< card link="listing-subjects" title="Listing Subjects" subtitle="Find all subjects with access to an object" icon="users" >}}
{{< card link="caching" title="Caching" subtitle="In-memory, request-scoped, and custom cache strategies" icon="lightning-bolt" >}}
{{< card link="contextual-tuples" title="Contextual Tuples" subtitle="Inject temporary tuples at check time for request context" icon="variable" >}}
{{< card link="testing-authorization" title="Testing Authorization" subtitle="Write integration tests for your permission setup" icon="beaker" >}}
{{< card link="scaling" title="Scaling" subtitle="Expression indexes, materialized views, and dedicated tables" icon="trending-up" >}}
{{< card link="migrations" title="Running Migrations" subtitle="Built-in vs versioned strategies, CI/CD integration, schema evolution" icon="arrow-circle-up" >}}
{{< card link="troubleshooting" title="Troubleshooting" subtitle="Diagnose common issues with melange doctor" icon="search" >}}
{{< /cards >}}
