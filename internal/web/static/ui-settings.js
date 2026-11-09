// Settings pages: invitation links that select themselves, the length of an invitation that
// follows the role, and the navigation that marks the section in view.
// No inline script, no inline style.
(() => {
  const selectAll = (el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
  };
  for (const link of document.querySelectorAll("[data-select]")) {
    link.addEventListener("focus", () => selectAll(link));
  }
  // The default length of an invitation depends on the role: a guest link
  // lives shorter. The first option names the length the server will use.
  for (const days of document.querySelectorAll("[data-days]")) {
    const form = days.closest("form");
    const role = form && form.querySelector("[data-role]");
    if (!role) continue;
    const sync = () => {
      const first = days.options[0];
      first.textContent = role.value === "guest" ? first.dataset.guest : first.dataset.member;
    };
    role.addEventListener("change", sync);
    sync();
  }

  // The navigation at the left opens one section at a time, each a page of its own
  // (the headings and boxes up to the next heading). The address names it:
  // #members shows the members, and so does a link to something inside them.
  // Without this script all sections stand under each other.
  const nav = document.querySelector(".snav");
  const pane = nav && nav.nextElementSibling;
  if (pane) {
    const links = [...nav.querySelectorAll('a[href^="#"]')];
    const kids = [...pane.children];
    let current = "";
    for (const kid of kids) {
      if (kid.classList.contains("sh2") && kid.id) current = kid.id;
      if (current) kid.dataset.sec = current;
    }
    const show = (id) => {
      let first = true;
      for (const kid of kids) {
        kid.hidden = Boolean(kid.dataset.sec) && kid.dataset.sec !== id;
        if (kid.classList.contains("sh2")) {
          kid.classList.toggle("is-first", !kid.hidden && first);
          if (!kid.hidden) first = false;
        }
      }
      for (const a of links) a.classList.toggle("on", a.getAttribute("href") === "#" + id);
    };
    const fromHash = () => {
      const target = location.hash.length > 1 ? document.getElementById(decodeURIComponent(location.hash.slice(1))) : null;
      const owner = target && target.closest("[data-sec]");
      const id = owner ? owner.dataset.sec : (links[0] ? links[0].getAttribute("href").slice(1) : "");
      show(id);
      if (target && owner && target !== owner) target.scrollIntoView();
    };
    window.addEventListener("hashchange", fromHash);
    fromHash();
  }
})();
