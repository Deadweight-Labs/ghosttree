// The outcome of an action floats over the page for a few seconds (.flash.fly). It
// belongs to the page it was written for, so it goes when the person moves on:
// to another tab of the page (a link, a changed #address), another page, or back
// to this one from the history. And once it is shown, the query values that
// asked for it (?notice=, ?done=, ?note=) leave the address, so a reload or a
// copied link does not bring it back.
(() => {
  const NOTE_PARAMS = ["notice", "done", "note"];
  const toasts = () => Array.from(document.querySelectorAll(".flash.fly"));
  const dismiss = () => toasts().forEach((el) => el.remove());

  // The toast floats under whatever stands at the top of the page, the header and the tab
  // band of a repository or organization, not over it: a toast over the tabs hides the very
  // tab the person is about to use next (N27). Placed when the page opens, where the page
  // is still at its top.
  const bottomOf = (el) => (el && el.getBoundingClientRect ? el.getBoundingClientRect().bottom : 0);
  const below = Math.max(bottomOf(document.querySelector(".gh-header")), bottomOf(document.querySelector(".rband")));
  if (below > 0) toasts().forEach((el) => { el.style.top = Math.round(below + 8) + "px"; });

  if (toasts().length && window.history && history.replaceState) {
    const url = new URL(location.href);
    let changed = false;
    for (const name of NOTE_PARAMS) {
      if (url.searchParams.has(name)) { url.searchParams.delete(name); changed = true; }
    }
    if (changed) history.replaceState(null, "", url.pathname + url.search + url.hash);
  }

  window.addEventListener("hashchange", dismiss);
  window.addEventListener("pageshow", (event) => { if (event.persisted) dismiss(); });
  document.addEventListener("click", (event) => {
    const target = event.target;
    const link = target && target.closest ? target.closest("a[href]") : null;
    if (link && !link.closest(".flash.fly")) dismiss();
  });
})();
