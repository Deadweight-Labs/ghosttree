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
