// Holt den Block "Kurz erklärt" nach, sobald der Server ihn erzeugt hat. Die
// Seite wartet nicht: sie liefert einen unsichtbaren Platzhalter, und dieses
// Skript lädt die Seite im Hintergrund neu und tauscht ihn aus, bis alle da sind.
(function () {
  if (window.__ghosttreeExplain) return;
  window.__ghosttreeExplain = true;
  var tries = 0, max = 30, every = 3000;
  function tick() {
    var waiting = document.querySelectorAll("[data-explain-pending]:not([data-explain-done])");
    if (!waiting.length || tries++ >= max) return;
    fetch(location.href, { credentials: "same-origin", cache: "no-store" })
      .then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (html) {
        if (!html) return;
        var doc = new DOMParser().parseFromString(html, "text/html");
        waiting.forEach(function (el) {
          var next = doc.getElementById(el.id);
          if (next && !next.hasAttribute("data-explain-pending")) {
            el.replaceWith(document.importNode(next, true));
          } else if (!next) {
            // Die Seite kennt den Platzhalter nicht mehr: es kommt nichts mehr.
            el.setAttribute("data-explain-done", "");
          }
        });
      })
      .catch(function () {})
      .then(function () { setTimeout(tick, every); });
  }
  setTimeout(tick, 2500);
})();
