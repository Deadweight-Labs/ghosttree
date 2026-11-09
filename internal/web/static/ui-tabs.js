// Tab bands (variant C, in place). What does not fit in the row shrinks to an
// icon with its number (li.ico), the name as tooltip; the order never changes
// and the open tab keeps its name. If even that does not fit, the row scrolls:
// a fade shows the end, a "+n" mark counts the tabs out of sight and moves on
// when pressed. Nothing goes into a menu.
// The server already puts "active" and "mention" tabs of the secondary group
// behind the separator (li.aux) and the rest of that group as icons (li.icx).
(() => {
  const PAD = 12; // room at both ends for the folder card of the open tab (ui-shell.css)

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

  // One tooltip for all icon tabs. It stands in the body and is placed with
  // position: fixed, so no scrolling row can cut it off.
  let tip = null;
  let tipFor = null;
  const hideTip = () => {
    if (tip) tip.hidden = true;
    tipFor = null;
  };
  const showTip = (a) => {
    const li = a.closest("li");
    if (!li || !(li.classList.contains("ico") || li.classList.contains("icx"))) return;
    if (tipFor === a) return;
    if (!tip) {
      tip = document.createElement("div");
      tip.className = "utip";
      tip.setAttribute("aria-hidden", "true");
      document.body.appendChild(tip);
    }
    tipFor = a;
    tip.textContent = a.getAttribute("data-tip");
    tip.hidden = false;
    tip.style.visibility = "hidden";
    tip.style.left = "0px";
    tip.style.top = "0px";
    const r = a.getBoundingClientRect();
    const w = tip.offsetWidth;
    const vw = document.documentElement.clientWidth;
    const left = Math.max(8, Math.min(r.left + r.width / 2 - w / 2, vw - w - 8));
    tip.style.left = Math.round(left) + "px";
    tip.style.top = Math.round(r.bottom + 4) + "px";
    tip.style.visibility = "";
  };
  const tipLink = (e) => (e.target.closest ? e.target.closest(".utabs a[data-tip]") : null);
  document.addEventListener("mouseover", (e) => { const a = tipLink(e); if (a) showTip(a); });
  document.addEventListener("mouseout", (e) => { if (tipLink(e)) hideTip(); });
  document.addEventListener("focusin", (e) => { const a = tipLink(e); if (a) showTip(a); });
  document.addEventListener("focusout", (e) => { if (tipLink(e)) hideTip(); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") hideTip(); });
  addEventListener("scroll", hideTip, true);

  for (const nav of document.querySelectorAll(".utabs-nav[data-fit]")) {
    const ul = nav.querySelector(".utabs");
    if (!ul) continue;
    const box = nav.parentElement;
    const fadeL = box.querySelector(".utabs-fade.l");
    const fadeR = box.querySelector(".utabs-fade.r");
    const more = box.querySelector("[data-more]");
    const items = [...ul.querySelectorAll(":scope > li[data-tab]")].filter((li) => !li.classList.contains("aux"));
    const tabs = [...ul.querySelectorAll(":scope > li:not(.tsep)")];
    const overflows = () => ul.scrollWidth > ul.clientWidth;

    // The tabs out of sight at the right end (the fade and the mark cover them), and the left end.
    const edges = () => {
      const scrolls = nav.classList.contains("scroll");
      const room = nav.getBoundingClientRect();
      const atEnd = nav.scrollLeft >= nav.scrollWidth - nav.clientWidth - 1;
      let hidden = 0;
      if (fadeR) fadeR.hidden = !scrolls || atEnd;
      if (scrolls && !atEnd && more) {
        const edge = more.getBoundingClientRect().left - 4;
        for (const li of tabs) if (li.getBoundingClientRect().right > edge) hidden++;
        more.querySelector(".mn").textContent = "+" + hidden;
        more.setAttribute("aria-label", moreLabel(hidden));
        if (fadeR) fadeR.hidden = hidden === 0;
      }
      if (fadeL) {
        let before = 0;
        for (const li of tabs) if (li.getBoundingClientRect().left < room.left + PAD - 1) before++;
        fadeL.hidden = !(scrolls && before > 0);
      }
    };

    const showActive = () => {
      const sel = ul.querySelector(":scope > li.sel");
      if (!sel) return;
      const room = nav.getBoundingClientRect();
      const r = sel.getBoundingClientRect();
      // clear of the fades: 40 px at the left, 72 px (fade and mark) at the right
      if (r.left < room.left + PAD + 28) nav.scrollLeft += r.left - room.left - PAD - 28;
      else if (r.right > room.right - PAD - 60) nav.scrollLeft += r.right - room.right + PAD + 60;
    };

    const fit = () => {
      for (const li of items) li.classList.remove("ico");
      nav.classList.remove("scroll");
      if (overflows()) {
        for (const evenMention of [false, true]) {
          for (let k = items.length - 1; k >= 0 && overflows(); k--) {
            const c = items[k].classList;
            if (c.contains("sel") || (c.contains("att") && !evenMention)) continue;
            c.add("ico");
          }
        }
        if (overflows()) {
          nav.classList.add("scroll");
          showActive();
        }
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
