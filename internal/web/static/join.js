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
  // The code box copies on a click anywhere in it, like the button.
  for (const box of document.querySelectorAll("code[data-copy]")) {
    box.addEventListener("click", () => {
      const button = box.parentElement && box.parentElement.querySelector("button[data-copy]");
      if (button) button.click();
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
  // After the approval the browser goes to the installer's loopback once. When
  // that fails (other machine, blocked) the code on the page is the way on, and a
  // reload or a step back must not send the browser there again.
  const go = document.querySelector("[data-join-go]");
  if (go) {
    const key = "gt-join-go:" + (marker.dataset.joinNonce || "");
    let tried = false;
    try { tried = sessionStorage.getItem(key) === "1"; } catch (error) { /* storage blocked */ }
    if (!tried) {
      try { sessionStorage.setItem(key, "1"); } catch (error) { /* storage blocked */ }
      setTimeout(() => { location.href = go.dataset.joinGo; }, 1500);
    }
  }
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
