// Runs before the page paints so a chosen theme never flashes the other one.
// app.js keeps it in step with laman's config (the "theme" setting).
try {
  const t = localStorage.getItem("laman.theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
} catch { /* private window: follow the system */ }
