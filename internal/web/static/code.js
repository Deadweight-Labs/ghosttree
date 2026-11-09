// Code tab: the branch list closes on a click outside or Escape and can be
// narrowed by typing, "t" jumps to the file filter, the filter hides the rows
// of the level that is loaded. Without this script the page still works: the
// branch list is a plain disclosure and every link is a link.
(function () {
  "use strict";
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };

  function closeMenus(except) {
    $$("details[data-ct-dd][open]").forEach(function (d) { if (d !== except) d.removeAttribute("open"); });
  }

  document.addEventListener("click", function (e) {
    closeMenus(e.target.closest ? e.target.closest("details[data-ct-dd]") : null);
  });

  document.addEventListener("keydown", function (e) {
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === "Escape") { closeMenus(null); return; }
    var tag = (e.target.tagName || "").toLowerCase();
    if (tag === "input" || tag === "textarea" || tag === "select" || e.target.isContentEditable) return;
    if (e.key === "t") {
      var f = $("#ct-filter");
      if (f) { e.preventDefault(); f.focus(); }
    }
  });

  var filter = $("#ct-filter");
  if (filter) {
    filter.addEventListener("input", function () {
      var q = filter.value.trim().toLowerCase();
      $$("#ct-table .ct-row, #ct-nav a").forEach(function (row) {
        row.hidden = q !== "" && (row.getAttribute("data-name") || "").toLowerCase().indexOf(q) < 0;
      });
    });
  }

  var branches = $("[data-ct-branches]");
  if (branches) {
    var dd = branches.closest("details");
    dd.addEventListener("toggle", function () { if (dd.open) branches.focus(); });
    branches.addEventListener("input", function () {
      var q = branches.value.trim().toLowerCase();
      $$(".ct-opt", dd).forEach(function (o) {
        o.hidden = q !== "" && (o.getAttribute("data-branch") || "").toLowerCase().indexOf(q) < 0;
      });
    });
  }
})();
