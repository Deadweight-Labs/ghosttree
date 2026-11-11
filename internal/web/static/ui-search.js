// Search: with this script, a search asks for the live page, whose groups come
// from /ui/search/stream as each kind is done. Without it, the page searches
// before it is sent.
//
// On the live page the kinds of the list on the left are not links to a new page:
// a click filters the hits that are loaded, and fetches more of the kind (one
// stream of that kind) only when it has more hits than are loaded. Rows that are
// there stay; the new ones are put in after them. Each kind is one .hg (group)
// with its hits; the groups of a kind sit before the marker <template> of the
// slot. The address keeps the kind (kind=, n=): a new kind is a step of the
// history (pushState, popstate puts it back), a longer list replaces the step.
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
  const per = Number(box.dataset.per) || 5;
  const page = Number(box.dataset.page) || 20;
  const step = Number(box.dataset.step) || 20;
  const cap = Number(box.dataset.cap) || 100;
  const slots = new Set(Array.from(document.querySelectorAll("template[data-kind-side]"), (t) => t.dataset.kindSide));
  const pending = new Set(slots);
  const sides = {};
  const counts = {};
  const allCount = document.querySelector("[data-search-all]");
  const sources = new Set();
  let done = 0;

  // kind is the kind the page is narrowed to ("" = all); wanted how many hits of a
  // kind were asked for (n=); asked how many the streams were asked for so far.
  const startUrl = new URL(location.href);
  let kind = slots.has(box.dataset.kind) ? box.dataset.kind : "";
  const wanted = {};
  const asked = {};
  const firstShown = kind ? Math.min(Math.max(Number(startUrl.searchParams.get("n")) || page, page), cap) : per;
  if (kind && startUrl.searchParams.get("n")) wanted[kind] = firstShown;

  const progress = () => {
    text.textContent = text.dataset.fmt.replace("{done}", done).replace("{total}", total);
    bar.style.width = total ? (100 * done) / total + "%" : "0";
  };
  const groupOf = (key) => hits.querySelector(`.hg[data-group="${key}"]`);
  const rowsOf = (g) => Array.from(g.querySelectorAll(".hit:not(.retry)"));
  const hrefOf = (row) => { const a = row.querySelector("a.ht"); return a ? a.getAttribute("href") : ""; };

  // more is the link under a group: on the page of all kinds it narrows the page to
  // the kind, on the page of one kind it asks for the next step, and at the end of
  // the steps it leads to the list of the repository (data-full).
  const more = (g) => {
    const key = g.dataset.group;
    const sum = Number(g.dataset.total) || 0;
    const loaded = rowsOf(g).length;
    let href = "";
    let mode = "";
    let n = 0;
    if (!kind) {
      if (sum > Math.min(loaded, per)) { href = g.dataset.narrow || ""; mode = "kind"; }
    } else {
      const reach = Math.max(loaded, Math.min(sum, wanted[key] || page));
      if (sum > reach && reach < cap) {
        n = Math.min(reach + step, cap);
        href = (g.dataset.narrow || "") + "&n=" + n;
        mode = "step";
      } else if (reach >= cap && ("capped" in g.dataset || sum > reach) && g.dataset.full) {
        href = g.dataset.full;
        mode = "full";
      }
    }
    let a = g.querySelector(".morelink");
    if (!href) {
      if (a) a.hidden = true;
      return;
    }
    if (!a) {
      a = document.createElement("a");
      a.className = "row morelink";
      a.textContent = box.dataset.moreHits;
      g.append(a);
    }
    a.hidden = false;
    a.href = href;
    a.dataset.more = mode;
    a.dataset.n = n;
  };
  // apply shows what the chosen kind shows of the rows that are loaded.
  const apply = () => {
    for (const g of hits.querySelectorAll(".hg[data-group]")) {
      const mine = !kind || kind === g.dataset.group;
      g.hidden = !mine;
      if (!mine || g.hasAttribute("data-retry")) continue;
      rowsOf(g).forEach((row, i) => { row.hidden = !kind && i >= per; });
      more(g);
    }
    const exact = hits.querySelector(".hg[data-exact]");
    if (exact) {
      let any = false;
      for (const row of exact.querySelectorAll(".hit")) {
        const ok = !kind || row.dataset.facet === kind;
        row.hidden = !ok;
        any = any || ok;
      }
      exact.hidden = !any;
    }
    for (const a of document.querySelectorAll("[data-search-kinds] a.frow[data-kind]")) {
      const on = a.dataset.kind === kind;
      a.classList.toggle("on", on);
      if (on) a.setAttribute("aria-current", "true"); else a.removeAttribute("aria-current");
    }
  };
  const tpl = (html) => {
    const t = document.createElement("template");
    t.innerHTML = html;
    return t.content;
  };
  const dropBody = (key) => {
    const g = groupOf(key);
    if (g) g.remove();
  };
  // putSide puts the row of a kind in the list on the left, or only changes its number.
  const putSide = (key, html) => {
    const cur = sides[key];
    if (!html) return;
    const row = tpl(html).firstElementChild;
    if (!row) return;
    if (cur) {
      const c = cur.querySelector(".counter");
      const n = row.querySelector(".counter");
      if (c && n) c.textContent = n.textContent;
      return;
    }
    const marker = document.querySelector(`template[data-kind-side="${key}"]`);
    if (!marker) return;
    marker.before(row);
    sides[key] = row;
  };
  // mergeGroup puts the hits of a fresh answer into the group that is shown: the
  // rows that are there stay, the new ones come in after the row before them.
  const mergeGroup = (cur, fresh) => {
    for (const name of ["total", "narrow", "full"]) {
      if (name in fresh.dataset) cur.dataset[name] = fresh.dataset[name]; else delete cur.dataset[name];
    }
    if ("capped" in fresh.dataset) cur.dataset.capped = ""; else delete cur.dataset.capped;
    const label = cur.querySelector("[data-count]");
    const next = fresh.querySelector("[data-count]");
    if (label && next) label.textContent = next.textContent;
    const have = new Map(rowsOf(cur).map((row) => [hrefOf(row), row]));
    let prev = null;
    for (const row of rowsOf(fresh)) {
      const old = have.get(hrefOf(row));
      if (old) {
        prev = old;
        continue;
      }
      if (prev) prev.after(row); else cur.querySelector(".box-h").after(row);
      prev = row;
    }
  };
  // putBody puts the group of a kind in its place, or merges it into the one there.
  const putBody = (key, html) => {
    const cur = groupOf(key);
    if (!html) {
      if (cur && cur.hasAttribute("data-retry")) cur.remove();
      return;
    }
    const fresh = tpl(html).firstElementChild;
    if (!fresh) return;
    if (cur && !cur.hasAttribute("data-retry")) {
      mergeGroup(cur, fresh);
      return;
    }
    if (cur) cur.remove();
    const m = fresh.querySelector(".morelink");
    if (m) m.remove();
    const marker = document.querySelector(`template[data-kind-body="${key}"]`);
    if (marker) marker.before(fresh);
  };
  const retry = (key) => {
    // A kind that shows hits keeps them: only a kind without any gets the button.
    const cur = groupOf(key);
    if (cur && !cur.hasAttribute("data-retry")) return;
    const t = document.querySelector(`template[data-kind-retry="${key}"]`);
    if (cur) cur.remove();
    if (!t) return;
    const marker = document.querySelector(`template[data-kind-body="${key}"]`);
    if (marker) marker.before(tpl(t.innerHTML));
  };
  // sum shows the hits of all kinds that are done, "+" when one was cut at its cap.
  const sum = () => {
    if (!allCount) return;
    const list = Object.values(counts);
    allCount.textContent = list.reduce((n, c) => n + c.total, 0) + (list.some((c) => c.capped) ? "+" : "");
  };
  const reveal = () => {
    kinds.hidden = Object.keys(sides).length === 0;
    hits.hidden = !hits.querySelector(".hg");
  };
  // setUrl keeps the address in step with the page: a new kind is a step of the
  // history, a longer list of the same kind replaces the step.
  const setUrl = (push) => {
    const u = new URL(location.href);
    if (kind) u.searchParams.set("kind", kind); else u.searchParams.delete("kind");
    if (kind && wanted[kind] > page) u.searchParams.set("n", wanted[kind]); else u.searchParams.delete("n");
    if (u.href === location.href) return;
    try {
      history[push ? "pushState" : "replaceState"](null, "", u.pathname + u.search + u.hash);
    } catch (_) { /* an address that may not be set stays as it is */ }
  };
  const settle = () => {
    if (pending.size > 0) return;
    box.hidden = true;
    none.hidden = !!hits.querySelector(".hg");
    // A kind without hits has no row to choose: back to all of them.
    if (kind && !groupOf(kind) && sources.size === 0) {
      kind = "";
      apply();
      setUrl(false);
    }
  };
  const finish = (key) => {
    if (pending.delete(key)) {
      done++;
      progress();
    }
  };
  const busy = (key, on) => {
    const g = groupOf(key);
    const a = g && g.querySelector(".morelink");
    if (!a) return;
    if (on) a.setAttribute("aria-busy", "true"); else a.removeAttribute("aria-busy");
  };
  const open = (url, keys) => {
    const source = new EventSource(url);
    sources.add(source);
    const close = () => {
      source.close();
      sources.delete(source);
      for (const key of keys) busy(key, false);
    };
    source.addEventListener("group", (event) => {
      if (!sources.has(source)) return;
      let ev;
      try { ev = JSON.parse(event.data); } catch (_) { return; }
      keys.delete(ev.key);
      if (ev.slow) {
        retry(ev.key);
      } else {
        putSide(ev.key, ev.side || "");
        putBody(ev.key, ev.body || "");
        counts[ev.key] = { total: ev.total || 0, capped: !!ev.capped };
        sum();
      }
      finish(ev.key);
      busy(ev.key, false);
      apply();
      reveal();
      ensure(kind);
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
      apply();
      reveal();
      settle();
    });
  };
  // load asks the stream of one kind for n hits of it.
  const load = (key, n) => {
    asked[key] = n;
    const u = new URL(box.dataset.searchLive, location.href);
    u.searchParams.set("kind", key);
    u.searchParams.set("only", key);
    u.searchParams.set("n", n);
    busy(key, true);
    open(u.pathname + u.search, new Set([key]));
  };
  // ensure fetches more of the kind that is chosen, but only when it has more hits
  // than are loaded.
  function ensure(key) {
    if (!key) return;
    const g = groupOf(key);
    if (!g || g.hasAttribute("data-retry")) return;
    const want = Math.min(Math.max(wanted[key] || page, page), cap);
    const have = rowsOf(g).length;
    if (have >= Math.min(Number(g.dataset.total) || 0, want)) return;
    // What the streams were asked for already: asking again would give the same rows.
    if ((asked[key] || firstShown) >= want) return;
    load(key, want);
  }
  const choose = (next, push) => {
    if (next === kind) return;
    kind = next;
    apply();
    setUrl(push);
    ensure(kind);
  };

  stop.addEventListener("click", () => {
    for (const source of Array.from(sources)) source.close();
    sources.clear();
    for (const key of Array.from(pending)) {
      retry(key);
      finish(key);
    }
    apply();
    reveal();
    settle();
  });
  document.addEventListener("click", (event) => {
    if (!(event.target instanceof Element)) return;
    const more = event.target.closest("[data-search-more]");
    if (more) {
      more.disabled = true;
      more.setAttribute("aria-busy", "true");
      const key = more.dataset.searchMore;
      open(box.dataset.searchLive + "&only=" + encodeURIComponent(key), new Set([key]));
      return;
    }
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const facet = event.target.closest("[data-search-kinds] a.frow[data-kind]");
    if (facet) {
      event.preventDefault();
      choose(facet.dataset.kind, true);
      return;
    }
    const link = event.target.closest(".hg[data-group] > a.morelink");
    if (!link || link.dataset.more === "full") return;
    const key = link.closest(".hg").dataset.group;
    event.preventDefault();
    if (link.dataset.more === "kind") {
      choose(key, true);
      return;
    }
    wanted[key] = Number(link.dataset.n) || page;
    apply();
    setUrl(false);
    ensure(key);
  });
  addEventListener("popstate", () => {
    const u = new URL(location.href);
    const k = u.searchParams.get("kind") || "";
    kind = slots.has(k) ? k : "";
    const n = Number(u.searchParams.get("n"));
    if (kind && n > page) wanted[kind] = Math.min(n, cap);
    apply();
    ensure(kind);
  });

  progress();
  apply();
  reveal();
  open(box.dataset.searchLive, new Set(pending));
})();
