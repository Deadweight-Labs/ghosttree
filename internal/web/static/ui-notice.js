// A banner for the outcome of an action comes with the query values the redirect
// carried (?done=, ?fail=, ...). Once it is shown they are taken out of the
// address, so a reload or a copied link does not bring the banner back.
(() => {
  const banner = document.querySelector("[data-strip]");
  if (!banner || !window.history || !history.replaceState) return;
  const url = new URL(location.href);
  let changed = false;
  for (const name of banner.dataset.strip.split(/\s+/).filter(Boolean)) {
    if (url.searchParams.has(name)) { url.searchParams.delete(name); changed = true; }
  }
  if (changed) history.replaceState(null, "", url.pathname + url.search + url.hash);
})();
