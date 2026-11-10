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
  // A link to a block of the log (#c12) from another page: the page opens as the
  // ghosttree view, which has no blocks, so it goes on to the raw log.
  if (document.querySelector(".sess-detail") && !document.querySelector(".flow") && /^#c\d+$/.test(location.hash)) {
    const url = new URL(location.href);
    url.searchParams.set("view", "raw");
    location.replace(url);
  }
  for (const el of document.querySelectorAll("time[data-clock]")) {
    const stamp = new Date(el.getAttribute("datetime"));
    if (!isNaN(stamp) && /^\d\d:\d\d$/.test(el.textContent.trim())) {
      el.textContent = stamp.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
    }
  }
})();
