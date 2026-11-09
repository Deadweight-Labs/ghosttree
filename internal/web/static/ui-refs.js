// Hover card of references (pills "#5" with their icon). Every element with
// data-ref="REQ-5" gets the card of <template data-ref-card="REQ-5">, which the
// server wrote into the page for exactly the objects the viewer may read; an
// element without such a template gets no card. Every other pill (pull request,
// commit, branch, organisation, machine, outside address, the waiting) carries
// its own card as JSON in data-pc, written by pill.go from what the page knows. Opens after about 300 ms on
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
  var KEY = /^(?:REQ|PAIN|TASK|IDEA|DEC|FEAT|KN)-\d+$/;
  // The lookup speaks the short forms of the API: a knowledge entry is "#1970" there.
  function apiRef(key) { return key.indexOf("KN-") === 0 ? "#" + key.slice(3) : key; }
  function localKey(ref) { return typeof ref === "string" && ref.charAt(0) === "#" ? "KN-" + ref.slice(1) : ref; }
  function $$(s, c) { return [].slice.call((c || document).querySelectorAll(s)); }

  function template(key) {
    return KEY.test(key) ? document.querySelector('template[data-ref-card="' + key + '"]') : null;
  }
  // remote: what /ui/refs answered, by key; asked: keys that were asked for
  var remote = {}, asked = {};
  function node(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text) e.textContent = text;
    return e;
  }
  // cardOf builds the card of an answer with createElement and textContent only.
  function cardOf(key, info) {
    var kd = window.GTPill.kind(key.split("-")[0]), frag = document.createDocumentFragment();
    var h = node("div", "pc-h"), k = node("span", "pc-k " + kd.cls);
    k.appendChild(window.GTPill.icon(kd.icon));
    k.appendChild(node("span", "", key.indexOf("KN-") === 0 ? "#" + key.slice(3) : key));
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
  // pcOf reads the card a pill carries itself (data-pc), or null.
  function pcOf(trg) {
    var raw = trg && trg.getAttribute && trg.getAttribute("data-pc");
    if (!raw) return null;
    try { var c = JSON.parse(raw); return c && typeof c === "object" ? c : null; } catch (e) { return null; }
  }
  // inlineCard builds the card of a pill without a key from its data-pc: the
  // icon is the one the pill shows, the state a pill, title and line as text.
  function inlineCard(trg, c) {
    var frag = document.createDocumentFragment(), h = node("div", "pc-h");
    var k = node("span", "pc-k " + (trg.className.match(/\b[ks]-[a-z]+\b/g) || []).join(" "));
    var ic = trg.querySelector("svg");
    if (ic) k.appendChild(ic.cloneNode(true));
    k.appendChild(node("span", "", c.l || ""));
    h.appendChild(k);
    if (c.s) h.appendChild(node("span", "state sm " + (/^(open|done|gray|bad|amber4)$/.test(c.t) ? c.t : "gray"), c.s));
    frag.appendChild(h);
    if (c.h) frag.appendChild(node("div", "pc-t" + (/\bmono\b/.test(trg.className) ? " mono" : ""), c.h));
    if (c.b) frag.appendChild(node("div", "pc-b", c.b));
    var by = [c.w, c.a].filter(Boolean).join(" · ");
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
    lookup: function (done) { lookup(done); },
    // resolve: upgrades the marks of "#1970" in root (ui-md.js) to pills
    resolve: function (root, done) { resolve(root, done); }
  };

  function missingKeys(extra) {
    var keys = [];
    function want(key) {
      if (KEY.test(key) && !template(key) && !asked[key] && keys.indexOf(key) < 0) keys.push(key);
    }
    // what the caller asks for goes first: the "#1970" of a text wait for the
    // answer, a pill of the page does not
    (extra || []).forEach(want);
    for (var a of $$("[data-ref]")) want(a.getAttribute("data-ref"));
    return keys.slice(0, 50);
  }
  // inflight: the request a key is on, so that a second caller waits for it
  // instead of finding the key "asked" and the answer not there yet.
  var inflight = {};
  function lookup(done, extra) {
    var keys = missingKeys(extra), waits = [];
    for (var k of Object.keys(inflight)) if (inflight[k] && waits.indexOf(inflight[k]) < 0) waits.push(inflight[k]);
    if (keys.length) {
      keys.forEach(function (k) { asked[k] = true; });
      var q = keys.map(function (k) { return "ref=" + encodeURIComponent(apiRef(k)); }).join("&");
      var req = fetch("/ui/refs?" + q, { credentials: "same-origin", headers: { Accept: "application/json" } })
        .then(function (r) { return r.ok ? r.json() : { refs: [] }; })
        .then(function (data) {
          (data.refs || []).forEach(function (info) {
            var key = info && info.gefunden ? localKey(info.ref) : "";
            if (key && KEY.test(key)) remote[key] = info;
          });
        })
        .catch(function () {})
        .then(function () {
          keys.forEach(function (k) { inflight[k] = null; });
          linkify(document);
        });
      keys.forEach(function (k) { inflight[k] = req; });
      waits.push(req);
    } else if (!waits.length) {
      linkify(document);
    }
    if (!done) return;
    if (!waits.length) done(); else Promise.all(waits).then(function () { done(); });
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
    // A pill that asks for its title (data-long) gets it from the answer.
    for (var p of $$("[data-ref][data-long]", root)) {
      var info = remote[p.getAttribute("data-ref")];
      if (info && info.titel && window.GTPill) window.GTPill.setTitle(p, info.titel);
    }
  }
  // resolve turns the marks a text wrote for a "#1970" (ui-md.js) into pills:
  // only for what the server knows and the viewer may read. A mark that is no
  // knowledge entry goes back to the plain text it was. A mark whose key the
  // first call had no room for is asked for in a call of its own.
  function resolve(root, done, round) {
    var marks = $$("[data-maybe]", root);
    if (!marks.length) { if (done) done(); return; }
    var keys = marks.map(function (m) { return m.getAttribute("data-maybe"); });
    lookup(function () {
      var again = false;
      marks.forEach(function (m) {
        var key = m.getAttribute("data-maybe"), href = window.GTRefs.href(key);
        var known = !!remote[key] || !!template(key);
        if (!known && !asked[key]) { again = true; return; }
        if (known && href && window.GTPill) m.replaceWith(window.GTPill.ref(key, { href: href }));
        else m.replaceWith(document.createTextNode(m.textContent));
      });
      if (again && (round || 0) < 8) resolve(root, done, (round || 0) + 1);
      else if (done) done();
    }, keys);
  }

  var seq = 0, card, cur = null, tOpen = 0, tClose = 0;

  var SEL = "[data-ref],[data-pc]";
  function known(t) {
    if (t && t.hasAttribute("data-pc")) return !!pcOf(t);
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
  function fill(c, trg) {
    var pc = pcOf(trg), key = trg.getAttribute("data-ref");
    var nodes = pc ? inlineCard(trg, pc) : window.GTRefs.card(key);
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
    fill(card, trg);
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
    var t = e.target.closest(SEL);
    if (known(t)) open(t, false);
    else if (askFor(t)) return;
    else if (cur || tOpen) later();
  });
  document.documentElement.addEventListener("mouseleave", later);
  document.addEventListener("focusin", function (e) {
    var t = e.target.closest && e.target.closest(SEL);
    if (known(t) && t.matches(":focus-visible")) open(t, true);
    else if (t && t.matches(":focus-visible")) lookup(function () { if (known(t)) open(t, true); });
  });
  document.addEventListener("focusout", function (e) { if (cur && e.target === cur) hide(); });
  document.addEventListener("keydown", function (e) { if (e.key === "Escape" && (cur || tOpen)) hide(); });
  document.addEventListener("click", function (e) { if (cur && e.target.closest && e.target.closest(SEL)) hide(); });
  function relayout() { if (cur && card && !card.hidden) place(card, cur); }
  window.addEventListener("resize", relayout);
  window.addEventListener("load", relayout);

  // The card says what the pill is; the title would say it a second time as a tooltip.
  function scan(root) {
    for (var a of $$(SEL, root)) {
      if (!known(a)) continue;
      var t = a.getAttribute("title");
      if (t) {
        if (!a.getAttribute("aria-label")) a.setAttribute("aria-label", t);
        a.removeAttribute("title");
      }
    }
  }
  scan(document);
  // ui-md.js ran before this script: its marks of "#1970" are resolved now.
  resolve(document);
  window.GTCards = { scan: scan, hide: hide };
})();
