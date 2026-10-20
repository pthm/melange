---
title: Concepts
description: "How Melange compiles OpenFGA schemas into SQL, how relationships are modelled, and how your tables act as the source of truth."
weight: 2
sidebar:
  open: true
---

How Melange transforms OpenFGA schemas into SQL functions, how permission relationships are modelled, and how your existing database tables serve as the source of truth for authorization data.

{{< cards >}}
{{< card link="components" title="Key Components" subtitle="CLI, schema, tuples view, and runtime libraries" icon="template" >}}
{{< card link="how-it-works" title="How It Works" subtitle="Architecture, specialized SQL generation, and precomputed closures" icon="cog" >}}
{{< card link="modelling" title="Authorization Modelling" subtitle="Write OpenFGA schemas to define your permission model" icon="document-text" >}}
{{< card link="tuples-view" title="Tuples View" subtitle="Map your domain tables to authorization tuples" icon="table" >}}
{{< /cards >}}
