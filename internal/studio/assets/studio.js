// Client-side filtering and expand/collapse for studio pages. Markup contract:
//   <input data-filter="#root">   filters [data-item] elements inside #root by
//                                 their data-text attribute, else their text
//   [data-group]                  hidden when none of its [data-item]s match
//   [data-empty-filter="#root"]   shown when a non-empty filter matches nothing
//                                 in #root (it may sit outside it: a <p> cannot
//                                 go inside a <ul>)
//   [data-theme-toggle]           flips light/dark and remembers the choice
//   .filter-bar input              focused by pressing "/"
//   details.whatif                 its textarea is disabled (not sent) while closed
//   [data-explain]                 toggles its row's .why-out (aria-expanded)
//   .sidebar [aria-current]        scrolled into view on load (large models)
document.addEventListener("input", (e) => {
  const input = e.target.closest("[data-filter]");
  if (!input) return;
  const root = document.querySelector(input.dataset.filter);
  const q = input.value.trim().toLowerCase();
  for (const el of root.querySelectorAll("[data-item]")) {
    el.hidden = q !== "" && !(el.dataset.text ?? el.textContent).toLowerCase().includes(q);
  }
  for (const g of root.querySelectorAll("[data-group]")) {
    const any = g.querySelector("[data-item]:not([hidden])");
    g.hidden = !any;
    if (q && any && "open" in g) g.open = true;
  }
  const empty = document.querySelector(`[data-empty-filter="${input.dataset.filter}"]`);
  if (empty) empty.hidden = q === "" || root.querySelector("[data-item]:not([hidden])") !== null;
});

// Theme: the top-bar toggle saves a choice; with none saved, follow the system.
document.addEventListener("click", (e) => {
  if (!e.target.closest("[data-theme-toggle]")) return;
  const root = document.documentElement;
  root.dataset.theme = root.dataset.theme === "dark" ? "light" : "dark";
  try { localStorage.setItem("studio-theme", root.dataset.theme); } catch {}
});
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", (e) => {
  try { if (localStorage.getItem("studio-theme")) return; } catch {}
  document.documentElement.dataset.theme = e.matches ? "dark" : "light";
});

// "/" focuses the page's filter (the main pane's first, else the sidebar's).
document.addEventListener("keydown", (e) => {
  if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey || e.target.closest?.("input, textarea, select")) return;
  const input = document.querySelector(".content .filter-bar input") ?? document.querySelector(".filter-bar input");
  if (!input) return;
  e.preventDefault();
  input.focus();
});

// What-if tuples apply only while their panel is open. toggle does not bubble.
document.addEventListener("toggle", (e) => {
  if (!e.target.matches?.("details.whatif")) return;
  e.target.querySelector("textarea").disabled = !e.target.open;
}, true);

// Explain chips toggle: a second click on the open chip clears its row's
// panel instead of explaining again. Chips in one row share one panel.
document.addEventListener("htmx:confirm", (e) => {
  const chip = e.detail.elt;
  if (!chip.matches("[data-explain]")) return;
  const row = chip.closest("li");
  const open = chip.getAttribute("aria-expanded") === "true";
  for (const c of row.querySelectorAll("[data-explain]")) c.setAttribute("aria-expanded", "false");
  if (open) {
    e.preventDefault();
    row.querySelector(".why-out").replaceChildren();
    return;
  }
  chip.setAttribute("aria-expanded", "true");
});

// Keep the current sidebar link in view; on a large model it can sit far down.
document.querySelector(".sidebar [aria-current]")?.scrollIntoView({ block: "center" });

// Cmd/Ctrl+Enter in a form's textarea submits the form (the what-if box).
document.addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey) || !e.target.matches?.("textarea")) return;
  e.preventDefault();
  e.target.form?.requestSubmit();
});
