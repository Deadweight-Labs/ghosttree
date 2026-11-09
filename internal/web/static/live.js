// Live work on issues: the page asks /ui/live for the issues it shows and
// swaps in the fragments the server sends when somebody starts or stops working.
(() => {
  const host = document.querySelector("[data-live-ids]");
  if (!host || !window.EventSource) return;
  const ids = host.dataset.liveIds.split(",").map((s) => s.trim()).filter(Boolean);
  if (!ids.length) return;
  const source = new EventSource("/ui/live?ids=" + encodeURIComponent(ids.join(",")));
  const swap = (selector, id, html, hideWhenEmpty) => {
    for (const el of document.querySelectorAll(selector.replace("{id}", id))) {
      if (el.innerHTML !== html) el.innerHTML = html;
      if (hideWhenEmpty) el.hidden = html === "";
    }
  };
  source.addEventListener("live", (event) => {
    let state;
    try { state = JSON.parse(event.data); } catch (_) { return; }
    for (const id of Object.keys(state)) {
      swap('[data-live-head="{id}"]', id, state[id].box, false);
      swap('[data-live-side="{id}"]', id, state[id].box, true);
      swap('[data-live-row="{id}"]', id, state[id].dot, false);
    }
  });
  source.addEventListener("session-ended", () => source.close());
})();
