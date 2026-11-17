// The script of the frame that draws Mermaid diagrams (/ui/mermaid-frame,
// mermaid_frame.go). It runs in a sandboxed page of its own, after the library
// (vendor/mermaid.min.js), and does one thing: a message {t:"draw", id, src,
// dark, vars} from the page that holds the frame is answered with {t:"svg", id,
// svg} or {t:"err", id, msg}. It answers nothing but its parent and draws one
// diagram after the other. The SVG is text; the page shows it only as the
// address of an image, so nothing in it can run there.
(function () {
  "use strict";
  var parent = window.parent, mermaid = window.mermaid;
  // The font of the page is not here and not in the image the SVG ends up in; both
  // have the system fonts only, so the diagram is measured with the font it is shown in.
  var FONT = "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans', Helvetica, Arial, sans-serif";
  // The most a source may be and may draw: more is refused by the library with an error.
  var MAX_TEXT = 20000, MAX_EDGES = 400;
  var seq = 0, chain = Promise.resolve();

  function reply(msg) { parent.postMessage(msg, "*"); }

  // The colours of the page, as the page reads them (rgb(), which the library can calculate with).
  var VARS = ["bg", "fg", "muted", "border", "subtle", "accent", "accentSubtle", "danger"];
  function config(d) {
    var v = d.vars || {}, ok = VARS.every(function (k) { return typeof v[k] === "string" && v[k].length < 64; });
    var c = {
      startOnLoad: false, securityLevel: "strict", suppressErrorRendering: true,
      maxTextSize: MAX_TEXT, maxEdges: MAX_EDGES, fontFamily: FONT, htmlLabels: false,
      flowchart: { htmlLabels: false }, theme: d.dark ? "dark" : "default"
    };
    if (ok) {
      c.theme = "base";
      c.themeVariables = {
        darkMode: !!d.dark, fontFamily: FONT, background: v.bg, mainBkg: v.accentSubtle,
        primaryColor: v.accentSubtle, primaryTextColor: v.fg, primaryBorderColor: v.accent,
        secondaryColor: v.subtle, secondaryTextColor: v.fg, secondaryBorderColor: v.border,
        tertiaryColor: v.bg, tertiaryTextColor: v.fg, tertiaryBorderColor: v.border,
        lineColor: v.muted, textColor: v.fg, nodeTextColor: v.fg, titleColor: v.fg,
        edgeLabelBackground: v.bg, clusterBkg: v.subtle, clusterBorder: v.border,
        noteBkgColor: v.subtle, noteTextColor: v.fg, noteBorderColor: v.border,
        actorBkg: v.accentSubtle, actorBorder: v.accent, actorTextColor: v.fg, actorLineColor: v.muted,
        signalColor: v.muted, signalTextColor: v.fg, labelBoxBkgColor: v.subtle, labelBoxBorderColor: v.border,
        labelTextColor: v.fg, loopTextColor: v.fg, activationBkgColor: v.subtle, activationBorderColor: v.border,
        sequenceNumberColor: v.bg, errorBkgColor: v.danger, errorTextColor: v.bg
      };
    }
    return c;
  }

  // The root element gets the size of its view box as width and height (the library writes 100% and a
  // maximum), so an image of it has the size of the diagram and the page scales it down to fit.
  function sized(svg) {
    return svg.replace(/^\s*<svg\b[^>]*>/, function (tag) {
      var vb = /\sviewBox="\s*[-\d.]+[\s,]+[-\d.]+[\s,]+([\d.]+)[\s,]+([\d.]+)\s*"/.exec(tag);
      if (!vb) return tag;
      var w = Math.ceil(Number(vb[1])), h = Math.ceil(Number(vb[2]));
      tag = tag.replace(/\s(?:width|height|style)="[^"]*"/g, "");
      return tag.slice(0, 4) + ' width="' + w + '" height="' + h + '"' + tag.slice(4);
    });
  }

  function one(d) {
    return Promise.resolve().then(function () {
      mermaid.initialize(config(d));
      return mermaid.render("mmd" + (++seq), d.src);
    }).then(function (r) {
      var svg = String(r && r.svg || "");
      if (!/^\s*<svg[\s>]/.test(svg) || svg.length > 4 << 20) throw new Error("no diagram");
      return sized(svg);
    }).then(function (svg) {
      reply({ t: "svg", id: d.id, svg: svg });
    }, function (e) {
      var msg = String(e && e.message || e || "").split("\n")[0].slice(0, 200);
      reply({ t: "err", id: d.id, msg: msg });
    }).then(function () {
      // A diagram that failed can leave its measuring box behind.
      document.body.textContent = "";
    });
  }

  window.addEventListener("message", function (ev) {
    var d = ev.data;
    if (ev.source !== parent || !d || d.t !== "draw" || typeof d.src !== "string" || typeof d.id !== "number") return;
    chain = chain.then(function () { return one(d); });
  });
  if (mermaid) reply({ t: "ready" }); else reply({ t: "fail" });
})();
