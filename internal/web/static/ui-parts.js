// Behaviour of the components in components.html: menus (div.dd with a button,
// details.dd), composer (write/preview, mode with fields and button label), the
// copy button, command blocks with OS tabs. No inline script, no inline style.
(() => {
  const $ = (s, c) => (c || document).querySelector(s);
  const $$ = (s, c) => [...(c || document).querySelectorAll(s)];

  // Menus: a div.dd opens with its button (data-dd), a details.dd by itself. Both
  // close with Escape (focus goes back to the button) and with a click outside,
  // and both are moved back into the window when they would stick out of it.
  // details.rvk (tokens page) and details.cfm (everywhere else) are the confirmation popovers; they close the same way.
  const OPEN = ".dd.open, details.dd[open], details.rvk[open], details.cfm[open]";
  const trigger = (dd) => $(":scope > [data-dd], :scope > summary", dd);
  const closeMenus = (except) => {
    for (const dd of $$(OPEN)) {
      if (dd === except || (except && dd.contains(except))) continue;
      if (dd.tagName === "DETAILS") {
        dd.open = false;
      } else {
        dd.classList.remove("open");
        const b = $("[data-dd]", dd);
        if (b) b.setAttribute("aria-expanded", "false");
      }
    }
  };
  const resetPop = (dd) => {
    const pop = $(":scope > .pop, :scope > .rvp", dd);
    if (!pop) return null;
    pop.style.transform = "";
    pop.style.top = "";
    pop.style.bottom = "";
    pop.style.maxHeight = "";
    return pop;
  };
  // A menu of more than ten entries gets a filter field at its top (the head line, when
  // there is one, stays above it). Typing hides the entries that do not contain the text.
  const filterable = (dd) => {
    const pop = $(":scope > .pop", dd);
    if (!pop || $(".pf", pop)) return;
    const opts = $$(".opt", pop);
    if (opts.length <= 10) return;
    const find = document.createElement("input");
    find.type = "search";
    find.className = "pf";
    find.autocomplete = "off";
    find.placeholder = "Filter …";
    find.setAttribute("aria-label", "Filter");
    find.addEventListener("input", () => {
      const q = find.value.trim().toLowerCase();
      for (const opt of opts) opt.hidden = q !== "" && !opt.textContent.toLowerCase().includes(q);
    });
    find.addEventListener("keydown", (event) => { if (event.key === "Enter") event.preventDefault(); });
    const head = $(":scope > .ph", pop);
    if (head) head.after(find); else pop.prepend(find);
  };
  // 8 px of air to the edge of the window; opens above the button when there is
  // more room there than below.
  const place = (dd) => {
    const pop = resetPop(dd);
    if (!pop) return;
    const box = pop.getBoundingClientRect();
    const width = document.documentElement.clientWidth;
    let shift = 0;
    if (box.right > width - 8) shift = width - 8 - box.right;
    if (box.left + shift < 8) shift = 8 - box.left;
    if (shift) pop.style.transform = "translateX(" + Math.round(shift) + "px)";
    const room = window.innerHeight - 8;
    if (box.bottom > room) {
      const at = dd.getBoundingClientRect();
      const above = at.top - 12;
      const below = room - at.bottom - 4;
      if (above > below) {
        pop.style.top = "auto";
        pop.style.bottom = "calc(100% + 4px)";
        pop.style.maxHeight = Math.max(160, Math.min(above, 420)) + "px";
      } else {
        pop.style.maxHeight = Math.max(160, Math.min(below, 420)) + "px";
      }
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
      if (open) {
        filterable(dd);
        place(dd);
        const find = $(".pf", dd);
        if (find) {
          find.value = "";
          find.dispatchEvent(new Event("input"));
          find.focus();
        }
      } else {
        resetPop(dd);
      }
      return;
    }
    if (!event.target.closest(".dd .pop")) closeMenus(event.target.closest("details.dd, details.rvk, details.cfm"));
  });
  document.addEventListener("toggle", (event) => {
    const d = event.target;
    if (!d.matches || !d.matches("details.dd, details.rvk, details.cfm")) return;
    if (d.open) {
      closeMenus(d);
      filterable(d);
      place(d);
    } else {
      resetPop(d);
    }
  }, true);
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    const open = $$(OPEN);
    if (!open.length) return;
    const last = open[open.length - 1];
    closeMenus();
    // One Escape closes one thing: the menu, not the field or dialog it stands in
    event.stopImmediatePropagation();
    const t = trigger(last);
    if (t) t.focus();
  });

  // Composer: write/preview. The preview renders the Markdown (ui-md.js) into
  // nodes and takes the height of the text field while that is visible; the
  // toolbar belongs to writing, so it goes while the preview is open.
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
        if (window.GTRefs) window.GTRefs.resolve(pv);
      }
      w.hidden = preview;
      pv.hidden = !preview;
      const tools = $(".wtools", cx);
      if (tools) tools.hidden = preview;
      for (const x of $$("[data-wtab]", cx)) {
        const li = x.closest("li");
        li.classList.toggle("sel", x === tab);
        if (x === tab) x.setAttribute("aria-current", "true"); else x.removeAttribute("aria-current");
      }
    });
  }

  // Toolbar: every button works on the text that is marked (or on the line the cursor is in).
  // The text goes in through the editing command of the browser, so Ctrl+Z takes a step back.
  const put = (ta, from, to, text, selFrom, selTo) => {
    ta.focus();
    ta.setSelectionRange(from, to);
    let done = false;
    try { done = document.execCommand("insertText", false, text); } catch (_) { done = false; }
    if (!done || ta.value.slice(from, from + text.length) !== text) {
      ta.setRangeText(text, from, to, "end");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    }
    ta.setSelectionRange(selFrom, selTo);
  };
  // **text**, _text_, `text`: marks the text or, when it is marked already, takes the marks off.
  const wrap = (ta, pre, post) => {
    const a = ta.selectionStart, b = ta.selectionEnd, v = ta.value;
    const text = v.slice(a, b);
    if (v.slice(a - pre.length, a) === pre && v.slice(b, b + post.length) === post) {
      put(ta, a - pre.length, b + post.length, text, a - pre.length, b - pre.length);
    } else if (text.length >= pre.length + post.length && text.startsWith(pre) && text.endsWith(post)) {
      const inner = text.slice(pre.length, text.length - post.length);
      put(ta, a, b, inner, a, a + inner.length);
    } else {
      put(ta, a, b, pre + text + post, a + pre.length, a + pre.length + text.length);
    }
  };
  // A marker at the start of every line the selection touches; all lines marked already: take it off.
  const lines = (ta, mark, has) => {
    const v = ta.value;
    const a = ta.selectionStart;
    let b = ta.selectionEnd;
    if (b > a && v[b - 1] === "\n") b -= 1;
    const from = v.lastIndexOf("\n", a - 1) + 1;
    let to = v.indexOf("\n", b);
    if (to < 0) to = v.length;
    const rows = v.slice(from, to).split("\n");
    const all = rows.every((r) => has.test(r));
    const out = rows.map((r) => (all ? r.replace(has, "") : mark + r.replace(has, ""))).join("\n");
    put(ta, from, to, out, from, from + out.length);
  };
  const TOOLS = {
    bold: (ta) => wrap(ta, "**", "**"),
    italic: (ta) => wrap(ta, "_", "_"),
    quote: (ta) => lines(ta, "> ", /^> ?/),
    list: (ta) => lines(ta, "- ", /^[-*] (?!\[[ xX]\] )/),
    tasks: (ta) => lines(ta, "- [ ] ", /^[-*] \[[ xX]\] /),
    code: (ta) => {
      const a = ta.selectionStart, b = ta.selectionEnd, text = ta.value.slice(a, b);
      if (text.includes("\n")) put(ta, a, b, "```\n" + text + "\n```", a + 4, a + 4 + text.length);
      else wrap(ta, "`", "`");
    },
    link: (ta) => {
      const a = ta.selectionStart, b = ta.selectionEnd, text = ta.value.slice(a, b);
      if (/^https?:\/\/\S+$/.test(text)) put(ta, a, b, "[](" + text + ")", a + 1, a + 1);
      else if (text) put(ta, a, b, "[" + text + "](url)", a + text.length + 3, a + text.length + 6);
      else put(ta, a, b, "[](url)", a + 1, a + 1);
    },
    at: (ta) => {
      const a = ta.selectionStart, b = ta.selectionEnd;
      const lead = a > 0 && !/\s/.test(ta.value[a - 1]) ? " @" : "@";
      put(ta, a, b, lead, a + lead.length, a + lead.length);
    },
  };
  document.addEventListener("click", (event) => {
    const button = event.target.closest(".wtools [data-tool]");
    if (!button) return;
    const ta = $("textarea", button.closest(".cx"));
    if (ta && TOOLS[button.dataset.tool]) TOOLS[button.dataset.tool](ta);
  });
  // One tab stop for the whole toolbar; the arrow keys move inside it.
  document.addEventListener("keydown", (event) => {
    const bar = event.target.closest && event.target.closest(".wtools");
    if (!bar || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    const all = $$("[data-tool]", bar);
    const at = all.indexOf(event.target.closest("[data-tool]"));
    if (at < 0) return;
    event.preventDefault();
    let next = event.key === "Home" ? 0 : event.key === "End" ? all.length - 1 : at + (event.key === "ArrowRight" ? 1 : -1);
    next = (next + all.length) % all.length;
    for (const x of all) x.tabIndex = x === all[next] ? 0 : -1;
    all[next].focus();
  });
  document.addEventListener("keydown", (event) => {
    if (!(event.ctrlKey || event.metaKey) || event.altKey || event.shiftKey) return;
    const ta = event.target;
    if (!ta.matches || !ta.matches(".cx textarea")) return;
    const tool = { b: "bold", i: "italic", k: "link" }[event.key.toLowerCase()];
    if (!tool) return;
    event.preventDefault();
    TOOLS[tool](ta);
  });

  // Composer with a mode (state, signal): the entry that is chosen sets the button label and
  // class, the placeholder, whether the words are required and the fields that belong to it.
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
        btn.className = "gbtn " + (c.cls === undefined ? "pri" : c.cls);
        if (c.tip) btn.title = c.tip; else btn.removeAttribute("title");
      }
      label();
      if (c.ph) { ta.placeholder = c.ph; ta.setAttribute("aria-label", c.ph); }
      ta.required = !!c.need;
      for (const el of $$("[data-show]", cx)) {
        el.hidden = el.dataset.show.split(" ").indexOf(mode) < 0;
        for (const f of $$("[data-need]", el)) f.required = !el.hidden;
      }
      for (const r of $$("input[data-mode-input]", cx)) {
        if (r.type === "radio") r.checked = r.value === mode; else r.value = mode;
      }
    };
    cx.gtSetMode = set;
    for (const r of $$("input[type=radio][data-mode-input]", cx)) r.addEventListener("change", () => { if (r.checked) set(r.value); });
    ta.addEventListener("input", label);
    if (cx.dataset.unset === undefined) set(cx.dataset.mode);
  }

  // A text that was written is never thrown away. While typing it is kept in this tab (with the mode and the
  // fields); a send marks it as sent. When the page comes back from a refusal (?fail=, ?comment=failed) the text
  // is put back, the way it was; when the send went through, the copy is dropped.
  const draft = {
    get: (k) => { try { return JSON.parse(sessionStorage.getItem(k) || "null"); } catch (_) { return null; } },
    set: (k, v) => { try { sessionStorage.setItem(k, JSON.stringify(v)); } catch (_) { /* full or blocked: the text stays in the field */ } },
    drop: (k) => { try { sessionStorage.removeItem(k); } catch (_) { /* nothing to drop */ } },
  };
  const refused = /[?&](fail=|comment=(empty|too_long|failed))/.test(location.search);
  for (const form of $$("form[data-draft]")) {
    const cx = $(".cx", form);
    const ta = cx && $("textarea", cx);
    if (!ta) continue;
    const key = "gt-draft:" + form.getAttribute("action") + ":" + $$("input[type=hidden]", form).filter((i) => i.name !== "csrf_token").map((i) => i.name + "=" + i.value).join("&");
    const fields = () => $$(".cx-m input.gfield, .cx-m select.gfield", cx).filter((f) => f.name);
    const snapshot = (sent) => ({ t: ta.value, m: cx.dataset.mode || "", f: Object.fromEntries(fields().map((f) => [f.name, f.value])), sent });
    const saved = draft.get(key);
    if (saved) {
      if (saved.sent && !refused) {
        draft.drop(key);
      } else if (saved.t && ta.value !== saved.t) {
        ta.value = saved.t;
        if (saved.m && cx.gtSetMode) cx.gtSetMode(saved.m);
        for (const f of fields()) {
          if (saved.f && saved.f[f.name] !== undefined) { f.value = saved.f[f.name]; f.dispatchEvent(new Event("change", { bubbles: true })); }
        }
        ta.dispatchEvent(new Event("input"));
        const closed = ta.closest("[hidden]");
        if (closed) closed.hidden = false;
        if (saved.sent) ta.focus();
      }
    }
    const keep = () => { if (ta.value.trim()) draft.set(key, snapshot(false)); else draft.drop(key); };
    ta.addEventListener("input", keep);
    cx.addEventListener("change", keep);
    form.addEventListener("submit", () => { if (ta.value.trim()) draft.set(key, snapshot(true)); });
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

  // The remove card of a comment closes with its Cancel button, with Escape and with a click outside.
  document.addEventListener("click", (event) => {
    const cancel = event.target.closest("[data-cdel-close]");
    if (cancel) {
      const d = cancel.closest("details.cdel");
      d.open = false;
      $("summary", d).focus();
      return;
    }
    for (const d of $$("details.cdel[open]")) if (!d.contains(event.target)) d.open = false;
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    for (const d of $$("details.cdel[open]")) {
      d.open = false;
      $("summary", d).focus();
    }
  });

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

  // Copy: the one button of the interface (template "uicopy"). The text comes from
  // data-copy-text, from the element data-copy-from names, from the command block
  // around the button (data-copy-here) or is the address of the page
  // (data-copy-page). The tick shows only after the browser took the text. Over
  // plain HTTP the clipboard API is missing: then a hidden field and execCommand
  // do it, and when that fails too the text is selected, so Ctrl+C is one key away.
  const copyText = async (text) => {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return true;
      }
    } catch (_) { /* fall through to the older way */ }
    const keep = document.activeElement;
    const field = document.createElement("textarea");
    field.value = text;
    field.setAttribute("readonly", "");
    field.setAttribute("aria-hidden", "true");
    field.style.position = "fixed";
    field.style.top = "0";
    field.style.opacity = "0";
    document.body.appendChild(field);
    field.select();
    let ok = false;
    try { ok = document.execCommand("copy"); } catch (_) { ok = false; }
    field.parentNode.removeChild(field);
    if (keep && keep.focus) keep.focus();
    return ok;
  };
  const copySource = (b) => {
    if (b.hasAttribute("data-copy-page")) return null;
    if (b.dataset.copyFrom) return $(b.dataset.copyFrom);
    const here = b.closest(".cmdc, .g-cmd, .copyline");
    return here ? $(".cmd, code", here) : null;
  };
  document.addEventListener("click", async (event) => {
    const b = event.target.closest("button.copy");
    if (!b) return;
    const source = copySource(b);
    let text = "";
    if (b.hasAttribute("data-copy-page")) text = location.origin + location.pathname;
    else if (b.dataset.copyText) text = b.dataset.copyText;
    else if (source) text = (source.innerText || source.textContent).trim();
    if (!text) return;
    if (await copyText(text)) {
      b.classList.add("done");
      clearTimeout(b.copyTimer);
      b.copyTimer = setTimeout(() => b.classList.remove("done"), 1600);
    } else if (source) {
      const range = document.createRange();
      range.selectNodeContents(source);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    }
  });

  // A colour dot in front of a hex field shows the colour that stands in it.
  for (const dot of $$("[data-swatch]")) {
    const field = $("input", dot.parentNode);
    if (!field) continue;
    const paint = () => {
      const value = "#" + field.value.trim().replace(/^#/, "");
      dot.style.background = /^#[0-9a-f]{6}$/i.test(value) ? value : "";
    };
    field.addEventListener("input", paint);
    paint();
  }

  // OS tabs: macOS or Linux by platform, a click switches.
  const platform = ((navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || "").toLowerCase();
  const preferred = /mac|iphone|ipad/.test(platform) ? "mac" : "lin";
  // The choice counts for every command block of the page.
  const groups = $$("[data-ostabs]");
  const pickAll = (os) => {
    for (const w of groups) {
      for (const t of $$('[role="tab"]', w)) t.setAttribute("aria-selected", t.dataset.os === os ? "true" : "false");
      for (const p of $$('[role="tabpanel"]', w)) p.hidden = p.dataset.os !== os;
    }
  };
  for (const w of groups) {
    for (const t of $$('[role="tab"]', w)) t.addEventListener("click", () => pickAll(t.dataset.os));
  }
  pickAll(preferred);
})();
