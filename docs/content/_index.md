---
title: Melange
layout: hextra-home
---

{{< hextra/hero-badge >}}
  <div class="hx-w-2 hx-h-2 hx-rounded-full hx-bg-primary-400"></div>
  <span>Free, open source</span>
  {{< icon name="arrow-circle-right" attributes="height=14" >}}
{{< /hextra/hero-badge >}}

<div class="hx-mt-6 hx-mb-6">
{{< hextra/hero-headline >}}
  Zero-copy Authorization&nbsp;<br class="sm:hx-block hx-hidden" />for PostgreSQL
{{< /hextra/hero-headline >}}
</div>

<div class="hx-mb-12">
{{< hextra/hero-subtitle >}}
  OpenFGA-compatible permission checking that queries&nbsp;<br class="sm:hx-block hx-hidden" />your existing domain tables directly.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx-mb-6">
{{< hextra/hero-button text="Get Started" link="docs" >}}
</div>

<div class="hx-mt-6"></div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Zero Tuple Sync"
    subtitle="No separate tuple storage. Permissions are computed directly from your existing domain tables, always in sync."
    class="hx-aspect-auto md:hx-aspect-[1.1/1] max-md:hx-min-h-[340px]"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(194,97,254,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="OpenFGA Compatible"
    subtitle="Write your authorization model using familiar OpenFGA DSL syntax. Melange compiles it to efficient PostgreSQL."
    class="hx-aspect-auto md:hx-aspect-[1.1/1] max-lg:hx-min-h-[340px]"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(142,53,74,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Pure PostgreSQL"
    subtitle="No external services required. Everything runs as PostgreSQL functions with zero external dependencies."
    class="hx-aspect-auto md:hx-aspect-[1.1/1] max-md:hx-min-h-[340px]"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(221,210,59,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Transaction Aware"
    subtitle="Permission checks see uncommitted changes within the same transaction. No stale authorization data."
  >}}
  {{< hextra/feature-card
    title="High Performance"
    subtitle="Optimized recursive CTEs provide 10-50x faster list operations. Query your permissions efficiently."
  >}}
  {{< hextra/feature-card
    title="Easy Integration"
    subtitle="Simple Go library with automatic migrations. Works with any PostgreSQL driver."
  >}}
{{< /hextra/feature-grid >}}
