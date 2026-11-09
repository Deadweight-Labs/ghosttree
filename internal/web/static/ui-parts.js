// Behaviour of the components in components.html: menus with a button
// (data-dd), composer (write/preview, mode with fields and button label),
// copyable command blocks with OS tabs. No inline script, no inline style.
(() => {
  const $ = (s, c) => (c || document).querySelector(s);
  const $$ = (s, c) => [...(c || document).querySelectorAll(s)];

  // Menus that open with a button: Escape or a click outside closes them.
  const closeMenus = (except) => {
    for (const dd of $$(".dd.open")) {
      if (dd === except) continue;
      dd.classList.remove("open");
      const b = $("[data-dd]", dd);
      if (b) b.setAttribute("aria-expanded", "false");
    }
  };
  document.addEventListener("click", (event) => {
    const button = event.target.closest("[data-dd]");
    if (button) {
      const dd = button.closest(".dd");
      const open = !dd.classList.contains("open");
      closeMenus(dd);
      dd.classList.toggle("open", open);
      button.setAttribute("aria-expanded", String(open));
      return;
    }
    if (!event.target.closest(".dd .pop")) closeMenus();
  });
  document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeMenus(); });

  // Composer: write/preview. The preview renders the Markdown (ui-md.js) into
  // nodes and takes the height of the text field while that is visible.
  for (const tab of $$(".cx [data-wtab]")) {
    tab.addEventListener("click", (event) => {
      event.preventDefault();
      const cx = tab.closest(".cx");
      const preview = tab.dataset.wtab === "p";
      const ta = $("textarea", cx);
      const pv = $(".cx-pv", cx);
      const w = $(".cx-w", cx);
      if (preview) {
        if (!w.hidden) pv.style.minHeight = w.offsetHeight + "px";
        pv.textContent = "";
        if (window.GTMarkdown) {
          pv.appendChild(window.GTMarkdown.render(ta.value, { empty: pv.dataset.empty, done: pv.dataset.done, open: pv.dataset.open }));
        } else {
          pv.textContent = ta.value;
        }
        if (window.GTCards) window.GTCards.scan(pv);
      }
      w.hidden = preview;
      pv.hidden = !preview;
      for (const x of $$("[data-wtab]", cx)) {
        const li = x.closest("li");
        li.classList.toggle("sel", x === tab);
        if (x === tab) x.setAttribute("aria-current", "true"); else x.removeAttribute("aria-current");
      }
    });
  }

  // Composer with a mode (status, signal): button label, placeholder and the
  // fields that belong to the mode follow the selection.
  for (const cx of $$(".cx[data-cfg]")) {
    let cfg = {};
    try { cfg = JSON.parse(cx.dataset.cfg); } catch (_) { continue; }
    const btn = $("[data-submit]", cx);
    const ta = $("textarea", cx);
    const label = () => {
      const c = cfg[cx.dataset.mode];
      if (c && btn) btn.textContent = c.labelText && ta.value.trim() ? c.labelText : c.label;
    };
    const set = (mode) => {
      const c = cfg[mode];
      if (!c) return;
      cx.dataset.mode = mode;
      if (btn) {
        btn.className = "gbtn " + (c.cls || "pri");
        if (c.tip) btn.title = c.tip; else btn.removeAttribute("title");
      }
      label();
      if (c.ph) { ta.placeholder = c.ph; ta.setAttribute("aria-label", c.ph); }
      for (const el of $$("[data-show]", cx)) el.hidden = el.dataset.show.split(" ").indexOf(mode) < 0;
      const hidden = $("input[data-mode-input]", cx);
      if (hidden) hidden.value = mode;
    };
    const modeMenu = $(".dd[data-pick]", cx);
    for (const opt of $$(".opt[data-val]", modeMenu || cx)) opt.addEventListener("click", () => set(opt.dataset.val));
    ta.addEventListener("input", label);
    set(cx.dataset.mode);
  }

  // Selects that show their choice in the button: label, check mark, hidden field.
  for (const dd of $$(".dd[data-pick]")) {
    for (const opt of $$(".opt[data-val]", dd)) {
      opt.addEventListener("click", () => {
        const shown = $(".ddv", dd);
        if (shown) shown.textContent = opt.dataset.label || opt.textContent.trim();
        for (const o of $$(".opt", dd)) {
          const chk = $(".chk", o);
          if (chk) chk.classList.toggle("off", o !== opt);
        }
        const field = $("input[data-pick-input]", dd);
        if (field) field.value = opt.dataset.val;
        if (field) field.dispatchEvent(new Event("change", { bubbles: true }));
        closeMenus();
      });
    }
  }

  // Reactions: without script every button is a form that reloads the page. With
  // script the form is sent in the background; the answer is the row of reactions
  // as HTML (the server draws names and counts) and it replaces the old row. A
  // failure leaves the row as it is and lets the form go the plain way.
  const placePicker = (details) => {
    const pop = $(".rpop", details);
    if (!pop) return;
    // Moves the picker sideways until it lies inside the window (8px margin).
    pop.style.left = "0px";
    const box = pop.getBoundingClientRect();
    const width = document.documentElement.clientWidth;
    let shift = 0;
    if (box.right > width - 8) shift = width - 8 - box.right;
    if (box.left + shift < 8) shift = 8 - box.left;
    pop.style.left = shift + "px";
  };
  document.addEventListener("toggle", (event) => {
    const d = event.target;
    if (d.classList && d.classList.contains("rpick") && d.open) {
      for (const other of $$(".rpick[open]")) if (other !== d) other.open = false;
      placePicker(d);
    }
  }, true);
  document.addEventListener("click", (event) => {
    if (event.target.closest(".rpick")) return;
    for (const d of $$(".rpick[open]")) d.open = false;
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    for (const d of $$(".rpick[open]")) {
      d.open = false;
      const s = $("summary", d);
      if (s) s.focus();
    }
  });
  document.addEventListener("submit", async (event) => {
    const form = event.target;
    const row = form.closest && form.closest("[data-reacts]");
    if (!row || !window.fetch || !window.DOMParser) return;
    event.preventDefault();
    const body = new URLSearchParams(new FormData(form));
    body.delete("next");
    row.classList.toggle("busy", true);
    try {
      const response = await fetch(form.action, {
        method: "POST", body, credentials: "same-origin", cache: "no-store",
        headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "text/html" },
      });
      if (!response.ok) throw new Error(String(response.status));
      const doc = new DOMParser().parseFromString(await response.text(), "text/html");
      const fresh = $("[data-reacts]", doc);
      row.replaceWith(fresh ? document.adoptNode(fresh) : document.createComment(""));
    } catch (_) {
      row.classList.toggle("busy", false);
      form.submit();
    }
  });

  // Copy: text of the target into the clipboard, the button says "Copied" for a moment.
  for (const b of $$("[data-ui-copy]")) {
    const caption = $(".cl", b) || b;
    const original = caption.textContent;
    b.addEventListener("click", () => {
      const target = $(b.dataset.uiCopy);
      if (!target) return;
      try { if (navigator.clipboard) navigator.clipboard.writeText(target.innerText.trim()).catch(() => {}); } catch (_) {}
      caption.textContent = b.dataset.copied || original;
      setTimeout(() => { caption.textContent = original; }, 1600);
    });
  }

  // OS tabs: macOS or Linux by platform, a click switches.
  const platform = ((navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || "").toLowerCase();
  const preferred = /mac|iphone|ipad/.test(platform) ? "mac" : "lin";
  for (const w of $$("[data-ostabs]")) {
    const tabs = $$('[role="tab"]', w);
    const panels = $$('[role="tabpanel"]', w);
    const pick = (os) => {
      for (const t of tabs) t.setAttribute("aria-selected", t.dataset.os === os ? "true" : "false");
      for (const p of panels) p.hidden = p.dataset.os !== os;
    };
    for (const t of tabs) t.addEventListener("click", () => pick(t.dataset.os));
    pick(preferred);
  }
})();
