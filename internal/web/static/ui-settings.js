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
    const roles = form ? [...form.querySelectorAll("[data-role]")] : [];
    if (!roles.length) continue;
    // A select holds the role, or radio buttons do: the checked one counts.
    const current = () => (roles.find((el) => el.checked) || roles[0]).value;
    const sync = () => {
      const first = days.options[0];
      first.textContent = current() === "guest" ? first.dataset.guest : first.dataset.member;
      // The styled menu of the select shows the new text, too.
      days.dispatchEvent(new Event("change"));
    };
    for (const el of roles) el.addEventListener("change", sync);
    sync();
  }

  // The model rows of a connection form: a row more, a row less. The last
  // remaining row is emptied, not removed. Without this script the form shows
  // its rows and one empty one.
  for (const group of document.querySelectorAll(".aimodels-f")) {
    const rows = group.querySelector("[data-rows]");
    const tpl = group.querySelector("template[data-rowtpl]");
    const add = group.querySelector("[data-addrow]");
    if (!rows || !tpl || !add) continue;
    add.hidden = false;
    add.addEventListener("click", () => {
      const row = tpl.content.firstElementChild.cloneNode(true);
      rows.append(row);
      const first = row.querySelector("input");
      if (first) first.focus();
    });
    group.addEventListener("click", (event) => {
      const del = event.target.closest("[data-delrow]");
      if (!del) return;
      const row = del.closest("[data-row]");
      if (rows.querySelectorAll("[data-row]").length > 1) row.remove();
      else for (const input of row.querySelectorAll("input")) input.value = "";
    });
  }

  // A choice of radio buttons that is saved by choosing (who the server's
  // connections are released to). "All organizations" needs the standard cap
  // first: the cursor goes there instead.
  for (const form of document.querySelectorAll("form[data-autosubmit]")) {
    const cap = form.querySelector("[data-defcap]");
    form.addEventListener("change", (event) => {
      if (!event.target.matches('input[type="radio"]')) return;
      if (event.target.value === "all" && cap && !cap.value) {
        cap.focus();
        return;
      }
      form.requestSubmit();
    });
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
      // Without an address the page names its own start (data-open): after a refused
      // invitation that is the members, where the person was, not "General".
      const start = pane.dataset.open && links.some((a) => a.getAttribute("href") === "#" + pane.dataset.open) ? pane.dataset.open : "";
      const id = owner ? owner.dataset.sec : (start || (links[0] ? links[0].getAttribute("href").slice(1) : ""));
      show(id);
      if (target && owner && target !== owner) target.scrollIntoView();
    };
    window.addEventListener("hashchange", fromHash);
    fromHash();
  }
})();
