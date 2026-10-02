// Shell behaviour: "/" focuses the search field, the project selector submits
// on change, and the account menu closes on Escape or a click outside.
(() => {
  document.documentElement.classList.add("js");
  const shell = document.querySelector(".shell");
  const toggle = document.querySelector("[data-shell-toggle]");
  if (shell && toggle) {
    toggle.addEventListener("click", () => {
      const open = shell.toggleAttribute("data-open");
      toggle.setAttribute("aria-expanded", String(open));
    });
  }
  const search = document.getElementById("shell-search");
  if (search) {
    addEventListener("keydown", (event) => {
      if (event.key !== "/" || event.ctrlKey || event.metaKey || event.altKey) return;
      const target = event.target;
      if (target instanceof HTMLElement && target.closest("input, textarea, select, [contenteditable]")) return;
      event.preventDefault();
      search.focus();
    });
  }
  for (const select of document.querySelectorAll("select[data-autosubmit]")) {
    select.addEventListener("change", () => select.form && select.form.requestSubmit());
  }
  const account = document.querySelector("details.account");
  if (account) {
    addEventListener("keydown", (event) => {
      if (event.key === "Escape") account.open = false;
    });
    document.addEventListener("click", (event) => {
      if (account.open && !account.contains(event.target)) account.open = false;
    });
  }
})();
