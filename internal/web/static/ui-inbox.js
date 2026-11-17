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
  const announce = (text) => {
    let note = $("[data-answered-note]");
    if (!note) {
      note = document.createElement("div");
      note.className = "callout ok";
      note.setAttribute("role", "status");
      note.setAttribute("data-answered-note", "");
      const line = document.createElement("div");
      line.className = "tt";
      note.appendChild(line);
      const anchor = $(".ntools") || $(".box");
      if (anchor && anchor.parentNode) anchor.parentNode.insertBefore(note, anchor);
    }
    const line = $(".tt", note);
    if (line) line.textContent = text;
  };
  const settle = (row, opts, text) => {
    const box = row.closest(".box");
    if (box) countDown($(".box-h .counter", box));
    const cats = (row.dataset.cat || "").split(" ").filter(Boolean).concat("alle");
    for (const cat of cats) countDown($('.ifilter [data-f="' + cat + '"] .counter'));
    announce((opts.dataset.answered || "") + " " + text);
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
