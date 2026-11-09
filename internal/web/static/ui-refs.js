// Hover card of references (pills REQ-5 and number chips #5). Every element
// with data-ref="REQ-5" gets the card of <template data-ref-card="REQ-5">, which
// the server wrote into the page for exactly the objects the viewer may read;
// an element without such a template gets no card. Opens after about 300 ms on
// hover and at once on keyboard focus, bridges the gap to the card, closes on
// Escape, folds over when there is no room. The card is built from the
// template's nodes (cloneNode), never from a string.
//
// GTRefs is the one place that knows where cards come from: the templates the
// server wrote, and for the references it did not know (the pills of the
// Markdown preview) /ui/refs, the same answer as /api/refs with the session of
// the browser. Asked once for all the keys on the page that have no card yet,
// at most 50 per call; "unknown" and "forbidden" are the same answer, so such a
// pill stays without a card.
(function () {
  "use strict";
  var KEY = /^(?:REQ|PAIN|TASK|IDEA|DEC)-\d+$/;
  function $$(s, c) { return [].slice.call((c || document).querySelectorAll(s)); }

  function template(key) {
    return KEY.test(key) ? document.querySelector('template[data-ref-card="' + key + '"]') : null;
  }
  // remote: what /ui/refs answered, by key; asked: keys that were asked for
  var remote = {}, asked = {};
  var KINDS = {
    REQ: { cls: "k-req", icon: '<circle cx="8" cy="8" r="6.25"/><circle cx="8" cy="8" r="1.4" fill="currentColor" stroke="none"/>' },
    PAIN: { cls: "k-pain", icon: '<circle cx="8" cy="8" r="6.25"/><path d="M8 4.7v3.9"/><circle cx="8" cy="11.1" r=".95" fill="currentColor" stroke="none"/>' },
    IDEA: { cls: "k-idea", icon: '<path d="M6.2 12.4h3.6M6.6 14.2h2.8"/><path d="M8 1.8a4.2 4.2 0 0 0-2.5 7.6c.5.4.8.9.8 1.5v.5h3.4v-.5c0-.6.3-1.1.8-1.5A4.2 4.2 0 0 0 8 1.8z"/>' },
    TASK: { cls: "k-task", icon: '<path d="M9.8 2.2 13.8 6.2 12 7.2l-1.2 3.1-1.2-1.2-3.5 3.5-.9-.9 3.5-3.5-1.2-1.2L9 5z"/>' },
    DEC: { cls: "k-dec", icon: '<path d="M8 14V8.4M8 8.4C8 6.4 5.6 6 4.2 4M8 8.4c0-2 2.4-2.4 3.8-4.4"/><path d="M2.6 5.6 4 3.6l2.2 1.2M13.4 5.6 12 3.6 9.8 4.8"/>' }
  };
  function node(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text) e.textContent = text;
    return e;
  }
  // cardOf builds the card of an answer with createElement and textContent only.
  function cardOf(key, info) {
    var kd = KINDS[key.split("-")[0]], frag = document.createDocumentFragment();
    var h = node("div", "pc-h"), k = node("span", "pc-k " + kd.cls);
    var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "i");
    svg.setAttribute("viewBox", "0 0 16 16");
    svg.setAttribute("aria-hidden", "true");
    var parsed = new DOMParser().parseFromString('<svg xmlns="http://www.w3.org/2000/svg">' + kd.icon + "</svg>", "image/svg+xml");
    [].slice.call(parsed.documentElement.childNodes).forEach(function (n) { svg.appendChild(document.importNode(n, true)); });
    k.appendChild(svg);
    k.appendChild(node("span", "", key));
    h.appendChild(k);
    if (info.zustand) h.appendChild(node("span", "state sm gray", info.zustand));
    frag.appendChild(h);
    frag.appendChild(node("div", "pc-t", info.titel));
    if (info.kurzfassung) frag.appendChild(node("div", "pc-b", info.kurzfassung));
    var by = [info.autor, info.zeit ? new Date(info.zeit).toLocaleDateString() : ""].filter(Boolean).join(" · ");
    if (by) {
      var m = node("div", "pc-m"), who = node("span", "pc-by");
      who.appendChild(node("span", "", by));
      m.appendChild(who);
      frag.appendChild(m);
    }
    return frag;
  }
  function safeLink(u) {
    return typeof u === "string" && u.charAt(0) === "/" && u.charAt(1) !== "/" ? u : "";
  }
  window.GTRefs = {
    // card: the nodes of the card of a key, or null
    card: function (key) {
      var t = template(key);
      if (t) return t.content.cloneNode(true);
      return remote[key] ? cardOf(key, remote[key]) : null;
    },
    // href: the address of the object, "" when the page does not know it
    href: function (key) {
      var t = template(key);
      if (t) return t.getAttribute("data-href") || "";
      return remote[key] ? safeLink(remote[key].link) : "";
    },
    // lookup: asks /ui/refs for the keys of the page without a card, calls done afterwards
    lookup: function (done) { lookup(done); }
  };

  function missingKeys() {
    var keys = [];
    for (var a of $$("[data-ref]")) {
      var key = a.getAttribute("data-ref");
      if (KEY.test(key) && !template(key) && !asked[key] && keys.indexOf(key) < 0) keys.push(key);
    }
    return keys.slice(0, 50);
  }
  function lookup(done) {
    var keys = missingKeys();
    if (!keys.length) { if (done) done(); return; }
    keys.forEach(function (k) { asked[k] = true; });
    var q = keys.map(function (k) { return "ref=" + encodeURIComponent(k); }).join("&");
    fetch("/ui/refs?" + q, { credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (r) { return r.ok ? r.json() : { refs: [] }; })
      .then(function (data) {
        (data.refs || []).forEach(function (info) {
          if (info && info.gefunden && KEY.test(info.ref)) remote[info.ref] = info;
        });
        linkify(document);
      })
      .catch(function () {})
      .then(function () { if (done) done(); });
  }
  // A pill the preview wrote as a plain span gets its address once it is known.
  function linkify(root) {
    for (var s of $$("span.ref[data-ref]", root)) {
      var href = window.GTRefs.href(s.getAttribute("data-ref"));
      if (!href) continue;
      var a = node("a", s.className);
      a.setAttribute("href", href);
      a.setAttribute("data-ref", s.getAttribute("data-ref"));
      while (s.firstChild) a.appendChild(s.firstChild);
      s.replaceWith(a);
    }
  }

  var seq = 0, card, cur = null, tOpen = 0, tClose = 0;

  function known(t) {
    var key = t && t.getAttribute("data-ref");
    return !!key && (!!template(key) || !!remote[key]);
  }
  // Unknown to the page and not yet asked: ask once, then open the card if it is still wanted.
  function askFor(t) {
    var key = t && t.getAttribute("data-ref");
    if (!key || !KEY.test(key) || known(t) || asked[key]) return false;
    lookup(function () { if (known(t) && t.matches(":hover")) open(t, false); });
    return true;
  }
  function newCard() {
    var c = document.createElement("div");
    c.className = "pcard";
    c.id = "pcard-" + (++seq);
    c.setAttribute("role", "tooltip");
    c.hidden = true;
    document.body.appendChild(c);
    return c;
  }
  function fill(c, key) {
    var nodes = window.GTRefs.card(key);
    c.textContent = "";
    if (nodes) c.appendChild(nodes);
    for (var bar of $$("[data-pct]", c)) bar.style.width = Math.max(0, Math.min(100, Number(bar.getAttribute("data-pct")) || 0)) + "%";
  }

  // Puts the card next to the pill: below by default, left aligned. Without room
  // it folds up or right aligned; if neither above nor below fits, it stands at the side.
  function place(c, trg) {
    var r = trg.getBoundingClientRect(), root = document.documentElement;
    var vw = root.clientWidth, vh = window.innerHeight, gap = 6, mg = 8;
    c.hidden = false;
    c.style.visibility = "hidden";
    c.style.left = "0px";
    c.style.top = "0px";
    var w = c.offsetWidth, h = c.offsetHeight, pos, top, left;
    if (r.bottom + gap + h <= vh - mg) pos = "b";
    else if (r.top - gap - h >= mg) pos = "t";
    else if (r.right + gap + w <= vw - mg) pos = "r";
    else if (r.left - gap - w >= mg) pos = "l";
    else pos = r.top > vh - r.bottom ? "t" : "b";
    if (pos === "b") top = r.bottom + gap;
    else if (pos === "t") top = r.top - gap - h;
    else top = r.top;
    if (pos === "r") left = r.right + gap;
    else if (pos === "l") left = r.left - gap - w;
    else { left = r.left; if (left + w > vw - mg) left = r.right - w; }
    top = Math.max(mg, Math.min(top, vh - h - mg));
    left = Math.max(mg, Math.min(left, vw - w - mg));
    c.style.left = Math.round(left + window.pageXOffset) + "px";
    c.style.top = Math.round(top + window.pageYOffset) + "px";
    c.style.visibility = "";
    c.setAttribute("data-pos", pos);
  }

  function hide() {
    clearTimeout(tOpen); clearTimeout(tClose);
    tOpen = tClose = 0;
    if (cur) { cur.removeAttribute("aria-describedby"); cur = null; }
    if (card) card.hidden = true;
  }
  function show(trg) {
    if (cur && cur !== trg) cur.removeAttribute("aria-describedby");
    card = card || newCard();
    fill(card, trg.getAttribute("data-ref"));
    cur = trg;
    trg.setAttribute("aria-describedby", card.id);
    place(card, trg);
  }
  function open(trg, now) {
    clearTimeout(tClose); tClose = 0;
    if (cur === trg) return;
    clearTimeout(tOpen);
    if (now) show(trg); else tOpen = setTimeout(function () { tOpen = 0; show(trg); }, cur ? 120 : 300);
  }
  function later() {
    clearTimeout(tOpen); tOpen = 0;
    if (cur && !tClose) tClose = setTimeout(hide, 160);
  }

  document.addEventListener("pointerover", function (e) {
    if (e.pointerType === "touch" || !e.target.closest) return;
    var inCard = e.target.closest(".pcard");
    if (inCard) { if (inCard === card) { clearTimeout(tClose); tClose = 0; } return; }
    var t = e.target.closest("[data-ref]");
    if (known(t)) open(t, false);
    else if (askFor(t)) return;
    else if (cur || tOpen) later();
  });
  document.documentElement.addEventListener("mouseleave", later);
  document.addEventListener("focusin", function (e) {
    var t = e.target.closest && e.target.closest("[data-ref]");
    if (known(t) && t.matches(":focus-visible")) open(t, true);
    else if (t && t.matches(":focus-visible")) lookup(function () { if (known(t)) open(t, true); });
  });
  document.addEventListener("focusout", function (e) { if (cur && e.target === cur) hide(); });
  document.addEventListener("keydown", function (e) { if (e.key === "Escape" && (cur || tOpen)) hide(); });
  document.addEventListener("click", function (e) { if (cur && e.target.closest && e.target.closest("[data-ref]")) hide(); });
  function relayout() { if (cur && card && !card.hidden) place(card, cur); }
  window.addEventListener("resize", relayout);
  window.addEventListener("load", relayout);

  // The card says what the pill is; the title would say it a second time as a tooltip.
  function scan(root) {
    for (var a of $$("[data-ref]", root)) {
      if (!known(a)) continue;
      var t = a.getAttribute("title");
      if (t) {
        if (!a.getAttribute("aria-label")) a.setAttribute("aria-label", t);
        a.removeAttribute("title");
      }
    }
  }
  scan(document);
  window.GTCards = { scan: scan, hide: hide };
})();
