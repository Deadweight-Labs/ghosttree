// Tab band with overflow (variant C). What does not fit in the row becomes an
// icon with its number, the name as tooltip, and moves behind the thin
// separator. The active tab never moves; tabs with a mention (.att) move last.
// The server already puts "active" and "mention" tabs of the secondary group
// behind the separator (li.aux) and the rest of that group as icons (li.icx).
(() => {
  for (const nav of document.querySelectorAll(".utabs-nav[data-fit]")) {
    const ul = nav.querySelector(".utabs");
    if (!ul) continue;
    const items = [...ul.querySelectorAll(":scope > li[data-tab]")].filter((li) => !li.classList.contains("aux"));
    const statics = [...ul.querySelectorAll(":scope > li.icx")];
    let sep = ul.querySelector(":scope > li.tsep");
    if (!sep) {
      sep = document.createElement("li");
      sep.className = "tsep";
      sep.setAttribute("aria-hidden", "true");
      sep.hidden = true;
      ul.insertBefore(sep, statics[0] || null);
    }
    const iconOf = (li) => {
      const a = li.querySelector("a");
      const out = document.createElement("li");
      const link = document.createElement("a");
      const name = a.querySelector("span").textContent;
      out.className = "icx dyn" + (li.classList.contains("att") ? " att" : "");
      link.href = a.getAttribute("href");
      link.setAttribute("data-tip", name);
      for (const child of a.children) {
        if (child.tagName.toLowerCase() === "svg" || child.classList.contains("counter")) link.appendChild(child.cloneNode(true));
      }
      const count = link.querySelector(".counter");
      link.setAttribute("aria-label", name + (count ? ", " + count.textContent : ""));
      out.appendChild(link);
      return out;
    };
    const fit = () => {
      for (const li of items) li.hidden = false;
      for (const dyn of ul.querySelectorAll(":scope > li.dyn")) dyn.remove();
      nav.classList.remove("scroll");
      let anchor = statics[0] || null;
      const showSep = () => {
        sep.hidden = !(statics.length || ul.querySelector(":scope > li.dyn") || ul.querySelector(":scope > li.aux"));
      };
      showSep();
      if (ul.scrollWidth <= nav.clientWidth) return;
      for (const evenMention of [false, true]) {
        for (let k = items.length - 1; k >= 0 && ul.scrollWidth > nav.clientWidth; k--) {
          const c = items[k].classList;
          if (items[k].hidden || c.contains("sel") || (c.contains("att") && !evenMention)) continue;
          items[k].hidden = true;
          const icon = iconOf(items[k]);
          ul.insertBefore(icon, anchor);
          anchor = icon;
          showSep();
        }
      }
      if (ul.scrollWidth > nav.clientWidth) nav.classList.add("scroll");
    };
    fit();
    let waiting = 0;
    addEventListener("resize", () => { clearTimeout(waiting); waiting = setTimeout(fit, 60); });
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(fit);
  }
})();
