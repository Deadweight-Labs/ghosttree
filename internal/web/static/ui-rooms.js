// Rooms: the search field in the head of a room or thread filters the messages
// that are on the page, and the field of the member list filters the people and
// agents. No inline script, no inline style.
(() => {
  const norm = (s) => s.toLocaleLowerCase();
  // What a search reads of a message: head, second line, quote, text, card. Not
  // the hidden forms and buttons that belong to it.
  const readable = (li) =>
    [...li.querySelectorAll(".rm-head, .cm-quote, .cm-bubble, .ask-t, .rq4-h, .slab-t")]
      .map((el) => el.textContent).join(" ");
  const filterMessages = (input) => {
    const q = norm(input.value.trim());
    const scope = input.closest(".coord-conversation") || document;
    let shown = 0;
    for (const li of scope.querySelectorAll(".coord-messages > li")) {
      if (li.classList.contains("cm-new") || li.classList.contains("coord-empty")) {
        li.hidden = q !== "";
        continue;
      }
      li.hidden = q !== "" && !norm(readable(li)).includes(q);
      if (!li.hidden) shown += 1;
    }
    const none = scope.querySelector(".rnone");
    if (none) none.hidden = q === "" || shown > 0;
  };
  const filterHere = (input) => {
    const q = norm(input.value.trim());
    const scope = input.closest(".rwho-pop, .rtside .sblk") || document;
    let shown = 0;
    for (const li of scope.querySelectorAll(".rm-here-item")) {
      li.hidden = q !== "" && !norm(li.textContent).includes(q);
      if (!li.hidden) shown += 1;
    }
    const none = scope.querySelector(".rwho-none");
    if (none) none.hidden = q === "" || shown > 0;
  };
  const wire = () => {
    for (const input of document.querySelectorAll("[data-room-filter], [data-here-filter]")) {
      if (input.dataset.bound) continue;
      input.dataset.bound = "true";
      const form = input.closest("form");
      if (form) form.addEventListener("submit", (event) => event.preventDefault());
      const run = input.hasAttribute("data-here-filter") ? filterHere : filterMessages;
      input.addEventListener("input", () => run(input));
    }
  };
  // A pop-up inside the scrolling chat would be cut at its edge: it is set by the button
  // it belongs to, on the window, above or below as there is room.
  document.addEventListener("toggle", (event) => {
    const menu = event.target;
    if (!(menu instanceof HTMLDetailsElement) || !menu.hasAttribute("data-float")) return;
    const pop = menu.querySelector(":scope > .pop");
    if (!pop) return;
    if (!menu.open) {
      for (const name of ["position", "top", "bottom", "left", "right", "transform"]) pop.style.removeProperty(name);
      return;
    }
    // After the generic placing of menus (ui-parts.js), which keeps the pop-up inside its parent.
    setTimeout(() => {
      if (!menu.open) return;
      const button = menu.querySelector(":scope > summary").getBoundingClientRect();
      const room = window.innerHeight - button.bottom;
      pop.style.position = "fixed";
      pop.style.transform = "none";
      pop.style.left = "auto";
      // Right edge under the button, but never so far right that the left edge
      // leaves the screen (a phone is narrower than the pop-up).
      const right = Math.min(window.innerWidth - button.right, window.innerWidth - pop.offsetWidth - 8);
      pop.style.right = `${Math.max(8, right)}px`;
      pop.style.maxHeight = `${Math.max(200, window.innerHeight - 24)}px`;
      if (room < pop.offsetHeight + 16 && button.top > room) {
        pop.style.top = "auto";
        pop.style.bottom = `${window.innerHeight - button.top + 4}px`;
      } else {
        pop.style.top = `${button.bottom + 4}px`;
        pop.style.bottom = "auto";
      }
    }, 0);
  }, true);
  // A floating menu sits at fixed coordinates under its button: whatever moves
  // the button (the message list, the page, a resized window) closes it.
  const closeFloating = () => {
    for (const menu of document.querySelectorAll("details[data-float][open]")) menu.open = false;
  };
  document.addEventListener("scroll", (event) => {
    if (!(event.target instanceof Element) || !event.target.matches(".coord-messages")) return;
    closeFloating();
  }, true);
  window.addEventListener("scroll", closeFloating, { passive: true });
  window.addEventListener("resize", closeFloating);
  // Where a tap is what a hover is elsewhere, the tools of the touched message unfold
  // under it (CSS, :focus-within); the list brings them into view.
  const touchLike = window.matchMedia("(hover: none), (max-width: 767px)");
  document.addEventListener("focusin", (event) => {
    if (!touchLike.matches || !(event.target instanceof Element)) return;
    const msg = event.target.closest(".coord-messages .rm-msg");
    if (!msg) return;
    requestAnimationFrame(() => msg.querySelector(".cm-tools")?.scrollIntoView({ block: "nearest" }));
  });
  wire();
  // The page swaps its surfaces after an event; look again then.
  new MutationObserver(wire).observe(document.body, { childList: true, subtree: true });
})();
