/* Commit list: the copy button next to a SHA puts the full hash into the clipboard
   and shows a tick for a moment. Without script the button stays hidden. */
(() => {
  for (const b of document.querySelectorAll("[data-copy-sha]")) {
    b.hidden = false;
    b.addEventListener("click", () => {
      const sha = b.dataset.copySha || "";
      try { if (navigator.clipboard) navigator.clipboard.writeText(sha).catch(() => {}); } catch (_) {}
      b.classList.add("done");
      setTimeout(() => b.classList.remove("done"), 1600);
    });
  }
})();
