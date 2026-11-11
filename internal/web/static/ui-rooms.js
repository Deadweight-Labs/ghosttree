// Rooms: the search field in the head of a room or thread filters the messages
// that are on the page. No inline script, no inline style.
(() => {
  const norm = (s) => s.toLocaleLowerCase();
  const wire = () => {
    for (const input of document.querySelectorAll("[data-room-filter]")) {
      if (input.dataset.bound) continue;
      input.dataset.bound = "true";
      const form = input.closest("form");
      if (form) form.addEventListener("submit", (event) => event.preventDefault());
      input.addEventListener("input", () => {
        const q = norm(input.value.trim());
        const scope = input.closest(".coord-conversation") || document;
        for (const li of scope.querySelectorAll(".coord-messages > li")) {
          li.hidden = q !== "" && !norm(li.textContent).includes(q);
        }
      });
    }
  };
  wire();
  // The page swaps its surfaces after an event; look again then.
  new MutationObserver(wire).observe(document.body, { childList: true, subtree: true });
})();
