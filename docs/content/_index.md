---
title: Melange
layout: hextra-home
---

{{< hextra/hero-container image="/images/mascot.png" imageWidth="400" imageHeight="400" imageClass="hx:my-auto" class="hx:items-center" >}}

{{< hextra/hero-badge >}}
  <div class="hx:w-2 hx:h-2 hx:rounded-full hx:bg-primary-400"></div>
  <span>Free, open source</span>
  {{< icon name="arrow-circle-right" attributes="height=14" >}}
{{< /hextra/hero-badge >}}

<div class="hx:mt-6 hx:mb-6">
{{< hextra/hero-headline >}}
  Zanzibar-Style Authorization&nbsp;<br class="hx:sm:block hx:hidden" />for PostgreSQL
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-12">
{{< hextra/hero-subtitle >}}
  Implement fine-grained, relationship-based access control without external services.&nbsp;<br class="hx:sm:block hx:hidden" />Melange compiles OpenFGA schemas into SQL functions that run inside your existing database.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-6 hx:flex hx:gap-4">
{{< hextra/hero-button text="Get Started" link="docs" >}}
{{< hextra/hero-button text="GitHub" link="https://github.com/pthm/melange" style="background: #000;" >}}
</div>

{{< /hextra/hero-container >}}

<div class="hx:mt-6"></div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Pure PostgreSQL"
    subtitle="Zero external dependencies. No sidecars, no services. Everything runs as SQL functions inside your database."
    icon="database"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(51,103,145,0.2),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="OpenFGA Compatible"
    subtitle="100% schema 1.1 conformance. Use familiar DSL syntax with full feature support, tested against the official test suite."
    icon="badge-check"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(0,180,140,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Optimized for Speed"
    subtitle="Constant-time permission checks (300-600 μs) that stay fast from thousands to millions of tuples. Carefully benchmarked and tested at scale."
    icon="lightning-bolt"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(194,97,254,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="No Tuple Sync"
    subtitle="Permissions derived from views over your domain tables. Always in sync, transaction-aware, zero data duplication."
    icon="refresh"
  >}}
  {{< hextra/feature-card
    title="Transaction Aware"
    subtitle="Permission checks see uncommitted changes within the same transaction. No stale authorization data."
    icon="clock"
  >}}
  {{< hextra/feature-card
    title="Language Agnostic"
    subtitle="Go client library included, or call SQL functions directly from any language. Use in triggers, RLS policies, or application code."
    icon="puzzle"
  >}}
{{< /hextra/feature-grid >}}
