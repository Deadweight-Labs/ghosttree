(() => {
  for (const input of document.querySelectorAll("[data-select]")) {
    input.addEventListener("focus", () => input.select());
  }
  for (const button of document.querySelectorAll("[data-copy]")) {
    const input = button.parentElement.querySelector("input");
    const label = button.textContent;
    button.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(input.value);
      } catch (_) {
        input.select();
        document.execCommand("copy");
      }
      button.textContent = button.dataset.done || label;
      setTimeout(() => { button.textContent = label; }, 1800);
    });
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
})();
