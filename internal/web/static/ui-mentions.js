// What an @name in a text stands for (REQ-677). The text keeps the handle that was
// typed or completed ("@mara"); this script asks /ui/mentions, with the project of
// the page, who that is, and the pill (ui-md.js, .mention) shows the full name. A
// handle that fits nobody, or fits several, stays as written. The same answer lists
// the people and agents of the project, which the editor of any page offers after
// @ (mentions.js completes them from a .comp-suggest list; this script writes the
// list into a composer that came without one).
(function () {
  "use strict";
  var known = {}, asked = {}, options = null, loading = null;
  var MAX = 50;

  function repo() { return (document.body && document.body.getAttribute("data-mention-repo")) || ""; }
  function url(extra) { return "/ui/mentions?project=" + encodeURIComponent(repo()) + extra; }
  function get(address) {
    if (typeof fetch !== "function") return Promise.resolve({});
    return fetch(address, { credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (res) { return res.ok ? res.json() : {}; })
      .catch(function () { return {}; });
  }

  function show(span) {
    var h = span.getAttribute("data-h"), hit = known[h];
    if (!hit || !hit.name) return;
    span.textContent = "@" + hit.name;
    span.setAttribute("title", hit.name);
    if (hit.agent) span.classList.add("mention-agent");
    dropTyped(span, hit.name);
  }

  // A full name typed out after the handle ("@Mara Keller") is the name the pill
  // now shows: the words after the pill that are the rest of it are not said twice.
  function dropTyped(span, name) {
    var h = span.getAttribute("data-h"), next = span.nextSibling;
    if (!next || next.nodeType !== 3 || name.toLowerCase().indexOf(h.toLowerCase()) !== 0) return;
    var rest = name.slice(h.length).split(/\s+/).filter(Boolean), text = next.nodeValue;
    for (var i = 0; i < rest.length; i++) {
      var m = new RegExp("^\\s+" + rest[i].replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "(?![\\p{L}\\p{N}])", "iu").exec(text);
      if (!m) break;
      text = text.slice(m[0].length);
    }
    if (text !== next.nodeValue) next.nodeValue = text;
  }

  // resolve gives the pills in root the full name of whom they mention.
  function resolve(root) {
    if (!repo()) return Promise.resolve();
    var spans = [].slice.call((root || document).querySelectorAll(".mention")).filter(function (s) { return !s.hasAttribute("data-h"); });
    if (!spans.length) return Promise.resolve();
    var want = [];
    spans.forEach(function (s) {
      var h = s.textContent.replace(/^@/, "");
      s.setAttribute("data-h", h);
      if (!(h in known) && !asked[h] && want.indexOf(h) < 0) want.push(h);
    });
    spans.forEach(show);
    var batches = [];
    for (var i = 0; i < want.length; i += MAX) batches.push(want.slice(i, i + MAX));
    return Promise.all(batches.map(function (batch) {
      batch.forEach(function (h) { asked[h] = true; });
      return get(url(batch.map(function (h) { return "&h=" + encodeURIComponent(h); }).join(""))).then(function (data) {
        var names = (data && data.names) || {};
        batch.forEach(function (h) { known[h] = names[h] || {}; });
        spans.forEach(show);
      });
    }));
  }

  // list writes the offered names into a composer without a list.
  function list(area) {
    var box = area.closest(".cx-w");
    if (!box || box.querySelector(".comp-suggest")) return;
    var ul = document.createElement("ul");
    ul.className = "comp-suggest";
    ul.setAttribute("role", "listbox");
    ul.hidden = true;
    (options || []).forEach(function (o) {
      if (!o || !o.handle) return;
      var li = document.createElement("li"), name = document.createElement("span");
      li.setAttribute("role", "option");
      li.setAttribute("data-handle", o.handle);
      li.setAttribute("aria-selected", "false");
      name.className = "comp-suggest-name";
      name.textContent = o.label || o.handle;
      li.appendChild(name);
      if (o.provider) {
        var small = document.createElement("small");
        small.textContent = o.provider;
        li.appendChild(small);
      }
      ul.appendChild(li);
    });
    box.appendChild(ul);
  }

  function offer(area) {
    if (!repo() || !area.closest || !area.closest(".cx-w")) return;
    if (options) return list(area);
    loading = loading || get(url("&options=1")).then(function (data) { options = (data && data.options) || []; });
    loading.then(function () { list(area); });
  }

  document.addEventListener("focusin", function (event) {
    var t = event.target;
    if (t && t.tagName === "TEXTAREA") offer(t);
  });

  window.GTMentions = { resolve: resolve, offer: offer };
})();
