// Inbox: "Reply" opens the reply field inside the row, "Cancel" and Escape close
// it again. Without this script "Reply" is a link to the message. No inline script.
(() => {
  const $ = (s, c) => (c || document).querySelector(s);
  const $$ = (s, c) => [...(c || document).querySelectorAll(s)];

  const setOpen = (row, open) => {
    const box = $(".inreply", row);
    const btn = $("[data-reply]", row);
    if (!box) return;
    box.hidden = !open;
    row.classList.toggle("open", open);
    if (btn) btn.setAttribute("aria-expanded", String(open));
    if (open) {
      const ta = $("textarea", box);
      if (ta) ta.focus();
    }
  };

  for (const btn of $$("[data-reply]")) {
    btn.addEventListener("click", (event) => {
      event.preventDefault();
      const row = btn.closest(".nrow");
      setOpen(row, $(".inreply", row).hidden);
    });
  }
  for (const cancel of $$(".nrow [data-cancel]")) {
    cancel.addEventListener("click", () => {
      const row = cancel.closest(".nrow");
      setOpen(row, false);
      const btn = $("[data-reply]", row);
      if (btn) btn.focus();
    });
  }
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    for (const row of $$(".nrow.open")) setOpen(row, false);
  });

  // Proposed answers: a click sends the form in the background. When the server
  // took the answer the question leaves the list, as it does after a reload, and a
  // line says what it was answered with; the numbers go down by one. When anything
  // fails the form is sent the plain way, which ends on the inbox with the entry as
  // the server has it.
  const countDown = (el) => {
    const n = el ? parseInt(el.textContent, 10) : 0;
    if (n > 0) el.textContent = String(n - 1);
  };
  // The bell in the header counts the same rows as the chip "All": it goes down with it, and without
  // a reload; at zero the number leaves.
  const bellDown = () => {
    const bell = $('a[data-top="inbox"]');
    const badge = bell && $(".top-count", bell);
    if (!badge) return;
    const n = Math.max(0, parseInt(badge.textContent, 10) - 1);
    if (n === 0) {
      badge.remove();
      bell.removeAttribute("title");
      return;
    }
    badge.textContent = String(n);
    const text = n === 1 ? bell.dataset.tOne : (bell.dataset.tMany || "").replace(/\d+/, String(n));
    if (text) bell.setAttribute("title", text);
  };
  // What the click did floats over the page for a moment, with the way to the place it went to; it
  // does not move the list.
  const announce = (text, href, view) => {
    for (const old of $$("[data-answered-note]")) old.remove();
    const note = document.createElement("div");
    note.className = "ibtoast";
    note.setAttribute("role", "status");
    note.setAttribute("data-answered-note", "");
    const line = document.createElement("span");
    line.className = "tt";
    line.textContent = text;
    note.appendChild(line);
    if (href && view) {
      const link = document.createElement("a");
      link.href = href;
      link.textContent = view;
      note.appendChild(link);
    }
    const header = $(".gh-header");
    if (header) note.style.top = Math.round(header.getBoundingClientRect().bottom + 8) + "px";
    document.body.appendChild(note);
    setTimeout(() => note.remove(), 9000);
  };
  const settle = (row, opts, text) => {
    const box = row.closest(".box");
    const link = $("a.lnk", row);
    if (box) countDown($(".box-h .counter", box));
    const cats = (row.dataset.cat || "").split(" ").filter(Boolean).concat("alle");
    for (const cat of cats) countDown($('.ifilter [data-f="' + cat + '"] .counter'));
    bellDown();
    announce((opts.dataset.answered || "") + " " + text, link && link.getAttribute("href"), opts.dataset.view);
    row.remove();
    if (box && !$(".nrow", box)) box.remove();
  };
  document.addEventListener("submit", async (event) => {
    const form = event.target;
    if (!form.matches || !form.matches("form[data-quick]") || !window.fetch) return;
    event.preventDefault();
    const opts = form.closest(".opts");
    const row = form.closest(".nrow");
    const body = new URLSearchParams(new FormData(form));
    body.delete("next");
    const buttons = $$("button", opts);
    for (const b of buttons) b.disabled = true;
    try {
      const response = await fetch(form.action, {
        method: "POST", body, credentials: "same-origin", cache: "no-store",
        headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
      });
      if (!response.ok) throw new Error(String(response.status));
      const result = await response.json();
      settle(row, opts, (result.option && result.option.text) || $("button", form).dataset.text);
    } catch (_) {
      for (const b of buttons) b.disabled = false;
      form.submit();
    }
  });
})();

// The old address of a task was /ui/tasks#task-N, which leads to the inbox with
// that anchor. A task has its own page now (/ui/t/N leads to it in its repository).
(() => {
  const m = /^#task-(\d+)$/.exec(location.hash);
  if (m && location.pathname === "/ui/inbox") location.replace("/ui/t/" + m[1]);
})();
