// Every absolute time of the pages is shown in the reader's own time zone.
// The server writes <time datetime="..." data-local="..."> with its own zone as
// the text (what stays without scripts); this rewrites that text, and the
// tooltip, from the datetime attribute. Kinds: clock (time of day today, else
// date and time), datetime, full, daytime (Today/Yesterday words come from the
// element), title (tooltip only, the text is already relative).
(() => {
  const pad = (n) => String(n).padStart(2, "0");
  const hm = (d) => `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const dm = (d) => d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
  const sameDay = (a, b) =>
    a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
  const full = (d) =>
    `${d.toLocaleDateString(undefined, { day: "2-digit", month: "2-digit", year: "numeric" })}, ${hm(d)}`;
  const kinds = {
    clock: (d, now) => (sameDay(d, now) ? hm(d) : `${dm(d)} ${hm(d)}`),
    datetime: (d) => `${dm(d)} ${hm(d)}`,
    full: (d) => full(d),
    daytime: (d, now, el) => {
      const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
      if (sameDay(d, now) && el.dataset.today) return `${el.dataset.today} ${hm(d)}`;
      if (sameDay(d, yesterday) && el.dataset.yesterday) return `${el.dataset.yesterday} ${hm(d)}`;
      const weekday = d.getFullYear() === now.getFullYear()
        ? `${d.toLocaleDateString(undefined, { weekday: "short" })} ${dm(d)}`
        : `${dm(d)} ${d.getFullYear()}`;
      return `${weekday} ${hm(d)}`;
    },
  };
  const apply = (root) => {
    const now = new Date();
    for (const el of root.querySelectorAll("time[data-local]")) {
      const d = new Date(el.getAttribute("datetime"));
      if (Number.isNaN(d.getTime())) continue;
      const kind = el.dataset.local;
      if (kind !== "title" && kinds[kind]) el.textContent = kinds[kind](d, now, el);
      el.title = full(d);
      el.removeAttribute("data-local");
    }
  };
  apply(document);
  // Rooms insert messages while the page is open.
  new MutationObserver((records) => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (node instanceof Element) apply(node.matches("time[data-local]") ? node.parentNode : node);
      }
    }
  }).observe(document.body, { childList: true, subtree: true });
})();
