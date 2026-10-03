// Pairing page: copy and share buttons, and a poll that reloads the page as
// soon as the server sees the machine (or the state changes otherwise).
(() => {
  for (const button of document.querySelectorAll("[data-copy]")) {
    button.addEventListener("click", () => {
      const source = document.querySelector(button.dataset.copy);
      if (!source || !navigator.clipboard) return;
      const label = button.textContent;
      navigator.clipboard.writeText(source.textContent.trim()).then(() => {
        button.textContent = button.dataset.copied || label;
        setTimeout(() => { button.textContent = label; }, 1500);
      });
    });
  }
  for (const button of document.querySelectorAll("[data-share]")) {
    if (!navigator.share) continue;
    button.hidden = false;
    button.addEventListener("click", () => {
      const source = document.querySelector(button.dataset.share);
      if (source) navigator.share({ text: source.textContent.trim() }).catch(() => {});
    });
  }
  const marker = document.querySelector("[data-join-state]");
  if (!marker) return;
  const state = marker.dataset.joinState;
  const nonce = marker.dataset.joinNonce || "";
  const poll = async () => {
    if (!document.hidden) {
      try {
        const response = await fetch("/join/pair/state", { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } });
        if (response.status === 401 || response.status === 403) { location.reload(); return; }
        if (response.ok) {
          const now = await response.json();
          if (now.state !== state || (now.nonce || "") !== nonce) { location.reload(); return; }
        }
      } catch (error) { /* offline for a moment: try again */ }
    }
    setTimeout(poll, 2000);
  };
  setTimeout(poll, 2000);
})();
