(() => {
  const media = matchMedia("(max-width: 1100px)");
  const toggles = [...document.querySelectorAll("[data-coord-drawer-target]")];
  const backdrop = document.querySelector("[data-coord-drawer-close]");
  if (!toggles.length || !backdrop) return;
  document.documentElement.classList.add("coord-enhanced");
  let openPanel = null;
  let returnFocus = null;
  let outsideState = [];

  const setOutsideInert = (panel) => {
    outsideState = [];
    let branch = panel;
    while (branch && branch !== document.body) {
      const parent = branch.parentElement;
      if (!parent) break;
      for (const sibling of parent.children) {
        if (sibling === branch || sibling === backdrop || sibling.tagName === "SCRIPT") continue;
        outsideState.push([sibling, sibling.inert]);
        sibling.inert = true;
      }
      branch = parent;
    }
  };
  const restoreOutside = () => {
    for (const [element, wasInert] of outsideState) element.inert = wasInert;
    outsideState = [];
  };

  const close = () => {
    if (!openPanel) return;
    openPanel.hidden = true;
    openPanel.inert = true;
    restoreOutside();
    backdrop.hidden = true;
    document.body.classList.remove("coord-drawer-open");
    toggles.forEach((button) => button.setAttribute("aria-expanded", "false"));
    const focus = returnFocus;
    openPanel = null;
    returnFocus = null;
    if (focus) focus.focus();
  };
  const sync = () => {
    close();
    for (const id of ["coord-rooms", "coord-context"]) {
      const panel = document.getElementById(id);
      if (!panel) continue;
      panel.hidden = media.matches;
      panel.inert = media.matches;
    }
  };
  const open = (button) => {
    close();
    const panel = document.getElementById(button.dataset.coordDrawerTarget);
    if (!panel || !media.matches) return;
    returnFocus = button;
    openPanel = panel;
    panel.hidden = false;
    panel.inert = false;
    setOutsideInert(panel);
    backdrop.hidden = false;
    document.body.classList.add("coord-drawer-open");
    button.setAttribute("aria-expanded", "true");
    (panel.querySelector("a,button,input,select,textarea,summary") || panel).focus();
  };
  toggles.forEach((button) => button.addEventListener("click", () => open(button)));
  backdrop.addEventListener("click", close);
  addEventListener("keydown", (event) => { if (event.key === "Escape") close(); });
  media.addEventListener("change", sync);
  sync();
})();
