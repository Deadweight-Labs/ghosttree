// Tab bands. Every tab carries its name and keeps its place on every page; the
// open one is only marked. A row that is wider than its room scrolls: a fade shows
// the end and an arrow moves on when pressed. Nothing is shrunk to an icon.
// The server puts the second group of tabs behind a separator (li.tsep).
// Wide enough for a menu (desktop), the tabs of that second group that do not fit
// go into a "More" menu at the end of the row, so no tab is cut off or hidden behind
// an arrow; the open tab and a tab that wants attention (a number of mentions) stay.
// On a phone the row scrolls as before.
(() => {
  const PAD = 12; // room at both ends for the folder card of the open tab (ui-shell.css)
  const SNAP = 24; // where a tab stands after the row scrolled to it (scroll-padding-inline in ui-shell.css)

  // The label of a tab or segment is set bold when it is open. Bold is wider and
  // would push the neighbours, so a hidden copy in bold reserves the room.
  const reserve = () => {
    for (const a of document.querySelectorAll(".utabs > li > a, .seg > a, .seg > button:not(.seg-more)")) {
      for (const node of [...a.childNodes]) {
        const text = node.nodeType === 3 ? node.textContent.trim() : "";
        const isLabel = node.nodeType === 1 && node.tagName === "SPAN" && !node.className && node.children.length === 0;
        if (!text && !isLabel) continue;
        const label = isLabel ? node : document.createElement("span");
        if (!isLabel) {
          label.textContent = node.textContent.trim();
          node.replaceWith(label);
        }
        label.className = "tl";
        label.setAttribute("data-t", label.textContent);
      }
    }
  };
  reserve();

  const moreLabel = (n) => (document.body.getAttribute("data-more") || "+{n}").replace("{n}", n);

  for (const nav of document.querySelectorAll(".utabs-nav[data-fit]")) {
    const ul = nav.querySelector(".utabs");
    if (!ul) continue;
    const box = nav.parentElement;
    const fadeL = box.querySelector(".utabs-fade.l");
    const fadeR = box.querySelector(".utabs-fade.r");
    const more = box.querySelector("[data-more]");
    const tabs = [...ul.querySelectorAll(":scope > li:not(.tsep)")];
    const overflows = () => ul.scrollWidth > ul.clientWidth;
    const menuWide = matchMedia("(min-width: 768px)");
    const shown = () => tabs.filter((li) => !li.hidden);

    // The tabs that went into the menu come back, the menu goes.
    const unfold = () => {
      for (const li of tabs) li.hidden = false;
      for (const li of ul.querySelectorAll(":scope > li.tmore")) li.remove();
    };
    const entry = (li) => {
      const a = li.querySelector(":scope > a");
      const opt = document.createElement("a");
      opt.className = "opt";
      opt.href = a.getAttribute("href");
      const icon = a.querySelector("svg.i");
      if (icon) opt.appendChild(icon.cloneNode(true));
      const label = document.createElement("span");
      const tl = a.querySelector(".tl");
      label.textContent = tl ? tl.getAttribute("data-t") : a.textContent.trim();
      opt.appendChild(label);
      const n = a.querySelector(".counter:not(.att)");
      if (n) {
        const small = document.createElement("small");
        small.textContent = n.textContent;
        opt.appendChild(small);
      }
      return opt;
    };
    // Folds the last tabs of the second group, one after the other, until the row fits.
    const fold = () => {
      const candidates = tabs.filter((li) => li.classList.contains("aux") && !li.classList.contains("sel") && !li.classList.contains("att")).reverse();
      if (!candidates.length) return;
      const more = document.createElement("li");
      more.className = "tmore";
      more.innerHTML = '<details class="dd"><summary class="tmore-btn"><span class="tl"></span><svg class="i" viewBox="0 0 16 16" aria-hidden="true"><path d="m4.2 6.2 3.8 3.8 3.8-3.8"/></svg></summary><div class="pop r"></div></details>';
      const label = more.querySelector(".tl");
      label.textContent = document.body.getAttribute("data-more-menu") || "…";
      const menu = more.querySelector(".pop");
      ul.appendChild(more);
      const moved = [];
      for (const li of candidates) {
        if (!overflows()) break;
        li.hidden = true;
        moved.unshift(li);
      }
      if (!moved.length) { more.remove(); return; }
      for (const li of moved) {
        menu.appendChild(entry(li));
        // Something folded wants a look (open tasks wait for someone): the dot goes onto "More" too, so it is not lost.
        const wait = li.querySelector(".band-wait");
        if (wait && !more.querySelector("summary .band-wait")) more.querySelector("summary").appendChild(wait.cloneNode(true));
      }
    };


    // The tabs out of sight at the right end (the fade and the mark cover them), and the left end.
    const edges = () => {
      const scrolls = nav.classList.contains("scroll");
      const room = nav.getBoundingClientRect();
      const atEnd = nav.scrollLeft >= nav.scrollWidth - nav.clientWidth - 1;
      let hidden = 0;
      if (fadeR) fadeR.hidden = !scrolls || atEnd;
      if (scrolls && !atEnd && more) {
        const edge = more.getBoundingClientRect().left - 4;
        for (const li of shown()) if (li.getBoundingClientRect().right > edge) hidden++;
        if (fadeR) fadeR.hidden = hidden === 0;
      }
      if (fadeL) {
        let before = 0;
        for (const li of shown()) if (li.getBoundingClientRect().left < room.left + PAD - 1) before++;
        fadeL.hidden = !(scrolls && before > 0);
      }
    };

    // The open tab is brought into view with as little movement as possible, so that a
    // band that fits stays where it is and one that scrolls starts the same on every page.
    const showActive = () => {
      const sel = ul.querySelector(":scope > li.sel");
      if (!sel) return;
      const room = nav.getBoundingClientRect();
      const r = sel.getBoundingClientRect();
      // The right fade and its arrow (96 px) lie over the end of the row: the tab stands clear of them.
      if (r.right > room.right - PAD - 84) nav.scrollLeft += r.right - (room.right - PAD - 84);
      else if (r.left < room.left + SNAP) nav.scrollLeft += r.left - room.left - SNAP;
    };

    const fit = () => {
      unfold();
      nav.classList.remove("scroll");
      if (overflows() && menuWide.matches) fold();
      if (overflows()) {
        nav.classList.add("scroll");
        showActive();
      }
      edges();
    };

    nav.addEventListener("scroll", edges, {passive: true});
    if (more) {
      more.addEventListener("click", () => {
        const calm = matchMedia("(prefers-reduced-motion: reduce)").matches;
        nav.scrollBy({left: Math.max(80, nav.clientWidth - 2 * PAD - 72), behavior: calm ? "auto" : "smooth"});
      });
    }
    fit();
    let waiting = 0;
    addEventListener("resize", () => { clearTimeout(waiting); waiting = setTimeout(fit, 60); });
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(fit);
  }
  // A row of segments that is wider than its room scrolls. A "+n" mark sticks to
  // its right end as long as segments are out of sight, and moves on when pressed.
  for (const seg of document.querySelectorAll(".seg")) {
    const mark = document.createElement("button");
    mark.type = "button";
    mark.className = "seg-more";
    mark.hidden = true;
    seg.appendChild(mark);
    const update = () => {
      mark.hidden = true;
      const atEnd = seg.scrollLeft >= seg.scrollWidth - seg.clientWidth - 1;
      if (atEnd) return;
      mark.hidden = false;
      const edge = mark.getBoundingClientRect().left - 4;
      let hidden = 0;
      for (const c of seg.children) if (c !== mark && c.getBoundingClientRect().right > edge) hidden++;
      if (!hidden) { mark.hidden = true; return; }
      mark.textContent = "+" + hidden;
      mark.setAttribute("aria-label", moreLabel(hidden));
    };
    mark.addEventListener("click", () => {
      const calm = matchMedia("(prefers-reduced-motion: reduce)").matches;
      seg.scrollBy({left: Math.max(80, seg.clientWidth - 72), behavior: calm ? "auto" : "smooth"});
    });
    seg.addEventListener("scroll", update, {passive: true});
    const start = () => {
      mark.hidden = true;
      const on = seg.querySelector(":scope > a.on, :scope > button.on");
      if (on && on.getBoundingClientRect().right > seg.getBoundingClientRect().right - 8) seg.scrollLeft = on.offsetLeft - 8;
      update();
    };
    start();
    let waiting = 0;
    addEventListener("resize", () => { clearTimeout(waiting); waiting = setTimeout(start, 60); });
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(start);
  }
})();
