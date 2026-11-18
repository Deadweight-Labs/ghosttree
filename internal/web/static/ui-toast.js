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

  // Where the toast stands is the stylesheet's business (ui-parts.css): one corner of the window, the same on
  // every page, so it never lands on the tabs or on the card the person is reading (N27). Nothing is placed here.

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
