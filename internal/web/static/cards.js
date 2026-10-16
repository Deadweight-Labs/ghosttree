// Pfade und Befehle in einer Mulde: Kopieren-Knopf und Klick selektiert alles.
(() => {
  const select = (node) => {
    const range = document.createRange();
    range.selectNodeContents(node);
    const sel = getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  };
  document.addEventListener("click", async (event) => {
    const code = event.target.closest(".g-cmd code");
    if (code) select(code);
    const button = event.target.closest("[data-g-copy]");
    if (!button) return;
    const source = button.parentElement.querySelector("code");
    if (!source) return;
    select(source);
    try {
      await navigator.clipboard.writeText(source.textContent);
    } catch (_) {
      document.execCommand && document.execCommand("copy");
    }
    button.classList.add("is-done");
    setTimeout(() => button.classList.remove("is-done"), 1500);
  });
})();

// Auswahlleiste der Review: erscheint ab der ersten Auswahl und zählt mit.
(() => {
  const bar = document.querySelector("[data-bulk]");
  if (!bar) return;
  const boxes = () => document.querySelectorAll('input.g-sel[form="gbulk"]');
  const update = () => {
    const n = [...boxes()].filter((box) => box.checked).length;
    bar.classList.toggle("is-idle", n === 0);
    const count = bar.querySelector("[data-bulk-count]");
    if (count && n > 0) count.textContent = (bar.dataset.fmt || "").replace(/\d+/, String(n));
  };
  document.addEventListener("change", (event) => {
    if (event.target.matches && event.target.matches("input.g-sel")) update();
  });
  update();
})();
