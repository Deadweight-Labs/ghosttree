// Sessions: "/" focuses the page's own search field (it wins over the top bar),
// times are shown in the reader's time zone, and "Copy link" copies the address.
(() => {
  const own = document.querySelector("[data-sessions-search]");
  if (own) {
    addEventListener("keydown", (event) => {
      if (event.key !== "/" || event.ctrlKey || event.metaKey || event.altKey) return;
      const target = event.target;
      if (target instanceof HTMLElement && target.closest("input, textarea, select, [contenteditable]")) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      own.focus();
    }, true);
  }
  for (const el of document.querySelectorAll("time[data-clock]")) {
    const stamp = new Date(el.getAttribute("datetime"));
    if (!isNaN(stamp) && /^\d\d:\d\d$/.test(el.textContent.trim())) {
      el.textContent = stamp.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
    }
  }
  const copy = document.querySelector("[data-copy-link]");
  if (copy) {
    copy.addEventListener("click", async () => {
      const url = location.origin + location.pathname;
      try {
        await navigator.clipboard.writeText(url);
        const label = copy.textContent;
        copy.textContent = "✓";
        setTimeout(() => { copy.textContent = label; }, 1200);
      } catch (_) { /* clipboard not available: the address bar has the link */ }
    });
  }
})();
