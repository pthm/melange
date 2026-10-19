// Sets data-theme on <html> before first paint: the choice saved by the
// top-bar toggle (studio.js), else the system preference. Loaded blocking
// from <head>; see assets/studio.css for the [data-theme] palettes.
try {
  document.documentElement.dataset.theme = localStorage.getItem("studio-theme") ?? (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
} catch {
  document.documentElement.dataset.theme = "light";
}
