// Shell behaviour: "/" focuses the search field, the project selector submits
// on change, and the account menu closes on Escape or a click outside.
(() => {
  // Loaded from the head: the js class must be set before first paint.
  document.documentElement.classList.add("js");
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  function init() {
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
  const account = document.querySelector("details.account");
  if (account) {
    addEventListener("keydown", (event) => {
      if (event.key === "Escape") account.open = false;
    });
    document.addEventListener("click", (event) => {
      if (account.open && !account.contains(event.target)) account.open = false;
    });
  }
  // The greeting follows the visitor's clock, so the server renders a neutral
  // text and the browser picks the time of day and the date.
  const today = document.querySelector("[data-today]");
  if (today) {
    const now = new Date();
    today.dateTime = now.toISOString().slice(0, 10);
    today.textContent = now.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
  }
  const greet = document.querySelector("[data-greet-morning]");
  if (greet) {
    const hour = new Date().getHours();
    greet.textContent = hour < 12 ? greet.dataset.greetMorning : hour < 18 ? greet.dataset.greetAfternoon : greet.dataset.greetEvening;
  }
  for (const bar of document.querySelectorAll("[data-pct]")) {
    bar.style.width = Math.max(0, Math.min(100, Number(bar.dataset.pct) || 0)) + "%";
  }
  const refresh = Number(document.body.dataset.refresh) || 0;
  if (refresh > 0) {
    const tick = () => {
      const field = document.querySelector("input[name=user_code]");
      if (field && (field === document.activeElement || field.value)) {
        setTimeout(tick, refresh * 1000);
      } else {
        location.reload();
      }
    };
    setTimeout(tick, refresh * 1000);
  }
  }
})();
