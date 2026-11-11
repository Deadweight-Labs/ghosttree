// The reference pill in script: the same markup the server writes (pill.go).
// Kind as a coloured icon, the number quiet next to it, the title after it in
// the long form. Everything is built with createElement and textContent, never
// from a string of a person's text. Loaded before ui-md.js and ui-refs.js, which
// both build pills with it.
//
//   GTPill.ref("REQ-5")                  icon #5            (short, the default)
//   GTPill.ref("REQ-5", {title: "…"})    icon #5 Title      (long)
//   GTPill.make({cls, icon, text, href}) any other kind, e.g. a branch
//   GTPill.kind("REQ")                   {cls, icon} of a prefix, or null
(function () {
  "use strict";
  var SVGNS = "http://www.w3.org/2000/svg";

  // The icons are the same as the server's (icons16.go): fixed constants.
  var ICON = {
    issue: '<circle cx="8" cy="8" r="6.25"/><circle cx="8" cy="8" r="1.4" fill="currentColor" stroke="none"/>',
    pain: '<circle cx="8" cy="8" r="6.25"/><path d="M8 4.7v3.9"/><circle cx="8" cy="11.1" r=".95" fill="currentColor" stroke="none"/>',
    idea: '<path d="M6.2 12.4h3.6M6.6 14.2h2.8"/><path d="M8 1.8a4.2 4.2 0 0 0-2.5 7.6c.5.4.8.9.8 1.5v.5h3.4v-.5c0-.6.3-1.1.8-1.5A4.2 4.2 0 0 0 8 1.8z"/>',
    pin: '<path d="M9.8 2.2 13.8 6.2 12 7.2l-1.2 3.1-1.2-1.2-3.5 3.5-.9-.9 3.5-3.5-1.2-1.2L9 5z"/>',
    // the signpost of a decision
    decide: '<path d="M8 1.8v12.4M5.8 14.2h4.4"/><path d="M8 3.2h4.4L14 4.9l-1.6 1.7H8z"/><path d="M8 8.2H3.6L2 9.9l1.6 1.7H8z"/>',
    feature: '<path d="M8 1.9 13.6 5v6L8 14.1 2.4 11V5z"/><path d="M2.6 5.1 8 8.2l5.4-3.1M8 8.2v5.8"/>',
    book: '<path d="M8 3.6C6.9 2.5 5.2 2.2 1.9 2.2v9.5c3.3 0 5 .4 6.1 1.7 1.1-1.3 2.8-1.7 6.1-1.7V2.2C10.8 2.2 9.1 2.5 8 3.6z"/><path d="M8 3.6v9.8"/>',
    pr: '<circle cx="3.75" cy="3.75" r="1.6"/><circle cx="3.75" cy="12.25" r="1.6"/><circle cx="12.25" cy="12.25" r="1.6"/><path d="M3.75 5.35v5.3M12.25 10.65V6.5a2 2 0 0 0-2-2H8M9.6 2.9 8 4.5l1.6 1.6"/>',
    commit: '<circle cx="8" cy="8" r="2.6"/><path d="M1.5 8h3.9M10.6 8h3.9"/>',
    branch: '<circle cx="4" cy="3.75" r="1.6"/><circle cx="4" cy="12.25" r="1.6"/><circle cx="12" cy="5.5" r="1.6"/><path d="M4 5.35v5.3M12 7.1c0 2.6-8 1.2-8 3.6"/>',
    ext: '<path d="M9.5 2.5h4v4M13.5 2.5 8 8M12 9.5v3a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h3"/>',
    check: '<path d="m3.5 8.4 3 3 6-6.5"/>'
  };
  var cache = {};
  function icon(name) {
    if (!ICON[name]) return null;
    if (!cache[name]) {
      var doc = new DOMParser().parseFromString('<svg xmlns="' + SVGNS + '">' + ICON[name] + "</svg>", "image/svg+xml");
      var s = document.createElementNS(SVGNS, "svg");
      s.setAttribute("class", "i");
      s.setAttribute("viewBox", "0 0 16 16");
      s.setAttribute("aria-hidden", "true");
      [].slice.call(doc.documentElement.childNodes).forEach(function (n) { s.appendChild(document.importNode(n, true)); });
      cache[name] = s;
    }
    return cache[name].cloneNode(true);
  }

  // The numbered kinds, by the prefix of their key. KN is a knowledge entry,
  // written KN-1970 in text and on the page: a bare #1970 is a pull request.
  var KINDS = {
    REQ: { icon: "issue", cls: "k-req" },
    PAIN: { icon: "pain", cls: "k-pain" },
    IDEA: { icon: "idea", cls: "k-idea" },
    TASK: { icon: "pin", cls: "k-task" },
    DEC: { icon: "decide", cls: "k-dec" },
    FEAT: { icon: "feature", cls: "k-feat" },
    KN: { icon: "book", cls: "k-know" }
  };
  function kind(prefix) { return KINDS[prefix] || null; }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  // make builds one pill. spec: cls (kind class), icon (name), text, title
  // (long form), href (a link when set), ref (key of a numbered kind, the card
  // comes from the server), card (object of the card of any other kind),
  // mono, name (what a screen reader says).
  function make(spec) {
    var p = el(spec.href ? "a" : "span", "ref pill " + (spec.cls || "") + (spec.mono ? " mono" : ""));
    if (spec.href) p.setAttribute("href", spec.href);
    if (spec.ref) p.setAttribute("data-ref", spec.ref);
    else if (spec.card) p.setAttribute("data-pc", JSON.stringify(spec.card));
    if (spec.name) p.setAttribute("aria-label", spec.name);
    var ic = icon(spec.icon);
    if (ic) p.appendChild(ic);
    p.appendChild(el("span", "num", spec.text));
    if (spec.title) p.appendChild(el("span", "pt", spec.title));
    return p;
  }

  // ref builds the pill of a key "REQ-5" or "KN-1970": "#5" (knowledge: "KN-1970")
  // next to the icon.
  // href may be empty (then it is a span until the address is known).
  function ref(key, o) {
    o = o || {};
    var m = /^([A-Z]+)-(\d+)$/.exec(key), kd = m && KINDS[m[1]];
    if (!kd) return null;
    return make({ cls: kd.cls, icon: kd.icon, text: m[1] === "KN" ? key : "#" + m[2], title: o.title, href: o.href, ref: key });
  }

  // setTitle gives a short pill its title (long form), or takes it away.
  function setTitle(p, title) {
    var t = p.querySelector(".pt");
    if (!title) { if (t) t.remove(); return; }
    if (!t) { t = el("span", "pt"); p.appendChild(t); }
    t.textContent = title;
  }

  window.GTPill = { icon: icon, kind: kind, make: make, ref: ref, setTitle: setTitle };
})();
