// Pictures of the workshop: a click, Enter or Space on a picture opens it large in a
// dialog (a native <dialog>: Escape closes it, focus stays inside, the page behind
// is inert). Left and right step through the pictures of the page. Without script
// the link opens the picture on its own.
(() => {
  const dlg = document.getElementById("ws-lb");
  if (!dlg || typeof dlg.showModal !== "function") return;
  const img = dlg.querySelector("img");
  const cap = dlg.querySelector(".ws-lb-cap");
  const links = () => [...document.querySelectorAll("a[data-zoom]")];
  let opener = null;
  const show = (a) => {
    opener = a;
    img.src = a.getAttribute("href");
    img.alt = a.dataset.alt || "";
    cap.textContent = a.dataset.alt || "";
  };
  document.addEventListener("click", (e) => {
    const a = e.target instanceof Element ? e.target.closest("a[data-zoom]") : null;
    if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey) return;
    e.preventDefault();
    show(a);
    dlg.showModal();
  });
  dlg.addEventListener("click", (e) => {
    if (e.target === dlg) dlg.close();
  });
  dlg.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    const all = links();
    const at = all.indexOf(opener);
    if (at < 0 || all.length < 2) return;
    e.preventDefault();
    show(all[(at + (e.key === "ArrowRight" ? 1 : all.length - 1)) % all.length]);
  });
  dlg.addEventListener("close", () => {
    img.removeAttribute("src");
    if (opener) opener.focus();
  });
})();
