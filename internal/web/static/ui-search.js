// Search: with this script, a search asks for the live page, whose groups come
// from /ui/search/stream as each kind is done. Without it, the page searches
// before it is sent.
(() => {
  const isSearch = (href) => {
    try {
      const u = new URL(href, location.href);
      return u.origin === location.origin && u.pathname === "/ui/search";
    } catch (_) {
      return false;
    }
  };
  document.addEventListener("submit", (event) => {
    const form = event.target;
    if (!(form instanceof HTMLFormElement) || (form.method || "get").toLowerCase() !== "get" || !isSearch(form.action)) return;
    if (form.querySelector('input[name="live"]')) return;
    const live = document.createElement("input");
    live.type = "hidden";
    live.name = "live";
    live.value = "1";
    form.appendChild(live);
  }, true);
  document.addEventListener("click", (event) => {
    const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
    if (!link || !isSearch(link.href)) return;
    const u = new URL(link.href, location.href);
    if (!u.search || u.searchParams.has("live")) return;
    u.searchParams.set("live", "1");
    link.href = u.pathname + u.search + u.hash;
  }, true);

  const box = document.querySelector("[data-search-live]");
  if (!box) return;
  if (!window.EventSource) {
    location.replace(box.dataset.sync);
    return;
  }
  const kinds = document.querySelector("[data-search-kinds]");
  const hits = document.querySelector("[data-search-hits]");
  const none = document.querySelector("[data-search-none]");
  const text = box.querySelector("[data-fmt]");
  const bar = box.querySelector(".bar > span");
  const stop = box.querySelector("[data-search-stop]");
  const total = Number(box.dataset.total) || 0;
  const pending = new Set(Array.from(document.querySelectorAll("template[data-kind-side]"), (t) => t.dataset.kindSide));
  const placed = {};
  const counts = {};
  const allCount = document.querySelector("[data-search-all]");
  const sources = new Set();
  let done = 0;
  let shown = 0;

  const progress = () => {
    text.textContent = text.dataset.fmt.replace("{done}", done).replace("{total}", total);
    bar.style.width = total ? (100 * done) / total + "%" : "0";
  };
  // put replaces what a kind shows in one place (side or body) by html.
  const put = (key, part, html) => {
    placed[key] = placed[key] || {};
    for (const node of placed[key][part] || []) node.remove();
    placed[key][part] = [];
    const marker = document.querySelector(`template[data-kind-${part}="${key}"]`);
    if (!marker || !html) return;
    const t = document.createElement("template");
    t.innerHTML = html;
    const nodes = Array.from(t.content.childNodes);
    marker.before(...nodes);
    placed[key][part] = nodes;
  };
  const retry = (key) => {
    const t = document.querySelector(`template[data-kind-retry="${key}"]`);
    put(key, "body", t ? t.innerHTML : "");
  };
  // sum shows the hits of all kinds that are done, "+" when one was cut at its cap.
  const sum = () => {
    if (!allCount) return;
    const list = Object.values(counts);
    allCount.textContent = list.reduce((n, c) => n + c.total, 0) + (list.some((c) => c.capped) ? "+" : "");
  };
  const reveal = () => {
    const any = (part) => Object.values(placed).some((p) => (p[part] || []).length > 0);
    kinds.hidden = !any("side");
    hits.hidden = !any("body");
  };
  const settle = () => {
    if (pending.size > 0) return;
    box.hidden = true;
    none.hidden = shown > 0 || Object.values(placed).some((p) => (p.body || []).length > 0);
  };
  const finish = (key) => {
    if (pending.delete(key)) {
      done++;
      progress();
    }
  };
  const open = (url, keys) => {
    const source = new EventSource(url);
    sources.add(source);
    const close = () => {
      source.close();
      sources.delete(source);
    };
    source.addEventListener("group", (event) => {
      if (!sources.has(source)) return;
      let ev;
      try { ev = JSON.parse(event.data); } catch (_) { return; }
      keys.delete(ev.key);
      if (ev.slow) {
        retry(ev.key);
      } else {
        put(ev.key, "side", ev.side || "");
        put(ev.key, "body", ev.body || "");
        if (ev.side) shown++;
        counts[ev.key] = { total: ev.total || 0, capped: !!ev.capped };
        sum();
      }
      finish(ev.key);
      reveal();
      settle();
    });
    source.addEventListener("done", () => {
      if (!sources.has(source)) return;
      close();
      for (const key of keys) finish(key);
      settle();
    });
    source.addEventListener("error", () => {
      if (!sources.has(source)) return;
      close();
      for (const key of keys) {
        retry(key);
        finish(key);
      }
      reveal();
      settle();
    });
  };

  stop.addEventListener("click", () => {
    for (const source of Array.from(sources)) source.close();
    sources.clear();
    for (const key of Array.from(pending)) {
      retry(key);
      finish(key);
    }
    reveal();
    settle();
  });
  document.addEventListener("click", (event) => {
    const more = event.target instanceof Element ? event.target.closest("[data-search-more]") : null;
    if (!more) return;
    more.disabled = true;
    more.setAttribute("aria-busy", "true");
    const key = more.dataset.searchMore;
    open(box.dataset.searchLive + "&only=" + encodeURIComponent(key), new Set([key]));
  });

  progress();
  open(box.dataset.searchLive, new Set(pending));
})();
