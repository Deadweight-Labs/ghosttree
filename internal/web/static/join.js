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
  // Connect and Deny: the page stays put. The installer's loopback is a top-level
  // navigation in a second window, opened inside the click (later the browser
  // would block it) and pointed at the loopback once the server has approved. If
  // the window is blocked, or the loopback cannot be reached, the code on this page
  // is the way on. Without script the plain form post still works.
  const loopback = /^http:\/\/127\.0\.0\.1:\d+\//;
  for (const form of document.querySelectorAll("form[data-join-popup]")) {
    form.addEventListener("submit", async (event) => {
      if (!window.fetch || !window.URLSearchParams) return;
      const submitter = event.submitter;
      const approve = !submitter || submitter.value === "approve";
      event.preventDefault();
      let popup = null;
      if (approve) {
        try { popup = window.open("about:blank", "_blank"); } catch (error) { popup = null; }
      }
      const body = new URLSearchParams(new FormData(form));
      if (submitter && submitter.name) body.set(submitter.name, submitter.value);
      const closePopup = () => { try { if (popup) popup.close(); } catch (error) { /* already gone */ } };
      let response;
      try {
        response = await fetch(form.action, { method: "POST", body, cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } });
      } catch (error) {
        closePopup();
        location.reload();
        return;
      }
      if (!response.ok) {
        // The server said no (wrong confirmation, name taken, nothing waiting):
        // the same post as a form shows its message page.
        closePopup();
        form.submit();
        return;
      }
      try {
        const out = await response.json();
        if (popup && out.navigate && loopback.test(out.navigate)) popup.location.href = out.navigate;
        else closePopup();
      } catch (error) { closePopup(); }
      location.reload();
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
