---
title: Concepts
weight: 2
sidebar:
  open: true
---

Understand the core concepts behind Melange's authorization model. These guides explain how Melange transforms OpenFGA schemas into specialized SQL functions, how permission relationships are modelled, and how your existing database tables become the source of truth for authorization data.

{{< cards >}}
{{< card link="how-it-works" title="How It Works" subtitle="Architecture, specialized SQL generation, and precomputed closures" icon="cog" >}}
{{< card link="modelling" title="Authorization Modelling" subtitle="Write OpenFGA schemas to define your permission model" icon="document-text" >}}
{{< card link="tuples-view" title="Tuples View" subtitle="Map your domain tables to authorization tuples" icon="table" >}}
{{< card link="migrations" title="Running Migrations" subtitle="Built-in vs external migration strategies for installing SQL functions" icon="arrow-circle-up" >}}
{{< /cards >}}
