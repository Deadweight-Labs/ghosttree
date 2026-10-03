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
})();
