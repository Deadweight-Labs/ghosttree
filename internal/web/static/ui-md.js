// Markdown preview of the composer. It builds DOM nodes (createElement,
// textContent) and never HTML from the text of a person, so nothing typed can
// become markup or script. Links only to http(s) and relative addresses.
// Supported: headings # to ###, bold, italic, code, code blocks, lists, task
// lists, quotes, links, @mentions, REQ-n, PAIN-n, TASK-n, IDEA-n, DEC-n, FEAT-n
// as pills (ui-pill.js) and #1970 as the pill of a knowledge entry.
// Port of the accepted mock (git-native-v2, gh6.js).
(function () {
  "use strict";
  var SVGNS = "http://www.w3.org/2000/svg";
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  // The icons and the pill are the ones of ui-pill.js, the same the server writes.
  function icon(name) { return window.GTPill.icon(name); }

  // Allowed: http(s) and relative addresses. Everything else (javascript:, data:, //host) is dropped.
  function safeUrl(u) {
    u = (u || "").trim();
    if (!u || /[\u0000-\u001f\u007f\s<>"\\]/.test(u)) return null;
    if (/^https?:\/\/[^\s\/]/i.test(u)) return u;
    if (/^[a-z][a-z0-9+.\-]*:/i.test(u)) return null;
    if (u.slice(0, 2) === "//") return null;
    return u;
  }

  // Address of a reference: the card of the page knows it (GTRefs), REQ and PAIN
  // have an address of their own that checks access, the others stay without a link.
  function refHref(key) {
    var h = window.GTRefs && window.GTRefs.href(key);
    if (!h) {
      var m = /^(REQ|PAIN)-(\d+)$/.exec(key);
      h = m ? (m[1] === "REQ" ? "/ui/requests/" : "/ui/pains/") + m[2] : "";
    }
    return safeUrl(h);
  }

  function pill(key) {
    return window.GTPill.ref(key, { href: refHref(key) });
  }
  // A "#1970" may be a knowledge entry or just a number: it stays text, marked,
  // until the server says it is one the reader may open (GTRefs.resolve).
  function maybeKnowledge(num) {
    var m = el("span", "", "#" + num);
    m.setAttribute("data-maybe", "KN-" + num);
    return m;
  }

  var ESC = "\\`*_[]()#>@-!";
  var RE_REF = /(REQ|PAIN|TASK|IDEA|DEC|FEAT)-(\d+)(?![\p{L}\p{N}_-])/uy;
  var RE_KN = /#(\d{1,15})(?![\p{L}\p{N}_-])/uy;
  var RE_MENTION = /@[\p{L}\p{N}][\p{L}\p{N}_-]*/uy;
  var RE_WORD = /[\p{L}\p{N}_]/u;

  function findRun(s, ch, k, from) {
    var idx = s.indexOf(ch, from);
    while (idx >= 0) {
      var e = idx;
      while (s.charAt(e) === ch) e++;
      if (e - idx === k) return idx;
      idx = s.indexOf(ch, e);
    }
    return -1;
  }
  function findBold(s, from) {
    var idx = s.indexOf("**", from);
    while (idx >= 0) {
      if (s.charAt(idx + 2) === "*") { idx++; continue; }
      if (idx > from && !/\s/.test(s.charAt(idx - 1))) return idx;
      idx = s.indexOf("**", idx + 2);
    }
    return -1;
  }
  function findItalic(s, from, ch) {
    var idx = s.indexOf(ch, from);
    while (idx >= 0) {
      if (s.charAt(idx + 1) === ch) { idx = s.indexOf(ch, idx + 2); continue; }
      var after = s.charAt(idx + 1);
      var ok = idx > from && !/\s/.test(s.charAt(idx - 1)) && (ch === "*" || !after || !RE_WORD.test(after));
      if (ok) return idx;
      idx = s.indexOf(ch, idx + 1);
    }
    return -1;
  }
  // End of the square or round bracket from position i (i points at the opening one), -1 if none.
  function matchBracket(s, i, open, close) {
    var d = 0;
    for (var j = i; j < s.length; j++) {
      var c = s.charAt(j);
      if (c === "\\") { j++; continue; }
      if (c === open) d++;
      else if (c === close) { d--; if (d === 0) return j; }
      else if (c === "\n" && open === "(") return -1;
    }
    return -1;
  }

  function inline(src, parent, o) {
    var buf = "", i = 0, n = src.length, deep = (o.d || 0) + 1;
    function flush() { if (buf) { parent.appendChild(document.createTextNode(buf)); buf = ""; } }
    function sub(tag, text) {
      flush();
      var e = el(tag);
      inline(text, e, { d: deep, noLink: o.noLink });
      parent.appendChild(e);
      return e;
    }
    while (i < n) {
      var c = src.charAt(i), j, m, prev = i ? src.charAt(i - 1) : "";
      if (c === "\n") { flush(); parent.appendChild(el("br")); i++; continue; }
      if (c === "\\" && i + 1 < n && ESC.indexOf(src.charAt(i + 1)) >= 0) { buf += src.charAt(i + 1); i += 2; continue; }
      if (c === "`") {
        var k = 1;
        while (src.charAt(i + k) === "`") k++;
        j = findRun(src, "`", k, i + k);
        if (j > 0) {
          var t = src.slice(i + k, j).replace(/\n/g, " ");
          if (t.length > 2 && t.charAt(0) === " " && t.charAt(t.length - 1) === " " && t.trim()) t = t.slice(1, -1);
          flush(); parent.appendChild(el("code", "", t)); i = j + k; continue;
        }
        buf += src.slice(i, i + k); i += k; continue;
      }
      if (deep < 7 && c === "*" && src.charAt(i + 1) === "*") {
        if (i + 2 < n && !/\s/.test(src.charAt(i + 2))) {
          j = findBold(src, i + 2);
          if (j > 0) { sub("strong", src.slice(i + 2, j)); i = j + 2; continue; }
        }
        buf += "**"; i += 2; continue;
      }
      if (deep < 7 && (c === "*" || c === "_") && src.charAt(i + 1) !== c && i + 1 < n && !/\s/.test(src.charAt(i + 1)) &&
          (c === "*" || !prev || !RE_WORD.test(prev))) {
        j = findItalic(src, i + 1, c);
        if (j > 0) { sub("em", src.slice(i + 1, j)); i = j + 1; continue; }
      }
      if (deep < 7 && c === "[" && !o.noLink) {
        var e1 = matchBracket(src, i, "[", "]");
        if (e1 > 0 && src.charAt(e1 + 1) === "(") {
          var e2 = matchBracket(src, e1 + 1, "(", ")");
          if (e2 > 0) {
            var raw = src.slice(e1 + 2, e2);
            m = /^\s*(<[^>]*>|\S*)(?:\s+(?:"([^"]*)"|'([^']*)'))?\s*$/.exec(raw);
            var url = m && safeUrl(m[1].replace(/^<|>$/g, ""));
            if (url) {
              flush();
              var a = el("a");
              a.setAttribute("href", url);
              if (/^https?:/i.test(url)) { a.target = "_blank"; a.rel = "noopener noreferrer"; }
              if (m[2] || m[3]) a.title = m[2] || m[3];
              inline(src.slice(i + 1, e1), a, { d: deep, noLink: true });
              parent.appendChild(a); i = e2 + 1; continue;
            }
            // Address not allowed: stays as text, nothing is linked
            buf += src.slice(i, e2 + 1); i = e2 + 1; continue;
          }
        }
      }
      if (c === "@" && !o.noLink && (!prev || !(RE_WORD.test(prev) || prev === "@" || prev === "." || prev === "/"))) {
        RE_MENTION.lastIndex = i;
        if ((m = RE_MENTION.exec(src))) { flush(); parent.appendChild(el("span", "mention", m[0])); i += m[0].length; continue; }
      }
      if (c === "#" && !o.noLink && (!prev || !/[\p{L}\p{N}_&#\/-]/u.test(prev))) {
        RE_KN.lastIndex = i;
        if ((m = RE_KN.exec(src))) { flush(); parent.appendChild(maybeKnowledge(m[1])); i += m[0].length; continue; }
      }
      if (!o.noLink && (c === "R" || c === "P" || c === "T" || c === "I" || c === "D" || c === "F") && (!prev || !/[\p{L}\p{N}_-]/u.test(prev))) {
        RE_REF.lastIndex = i;
        if ((m = RE_REF.exec(src))) { flush(); parent.appendChild(pill(m[0])); i += m[0].length; continue; }
      }
      buf += c; i++;
    }
    flush();
  }

  var RE_FENCE = /^ {0,3}(`{3,})[ \t]*([\w+#.\-]*)[^`]*$/;
  var RE_HEAD = /^ {0,3}(#{1,3})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$/;
  var RE_QUOTE = /^ {0,3}>/;
  var RE_ITEM = /^( *)([-*+]|\d{1,9}[.)])(?:[ \t]+(.*))?$/;
  var RE_TASK = /^\[([ xX])\](?:[ \t]+([\s\S]*))?$/;

  function interrupts(l) {
    if (RE_FENCE.test(l) || RE_HEAD.test(l) || RE_QUOTE.test(l)) return true;
    var m = RE_ITEM.exec(l);
    return !!m && !!m[3] && (!/^\d/.test(m[2]) || /^1[.)]$/.test(m[2]));
  }
  function indentOf(l) { return /^ */.exec(l)[0].length; }

  function parseList(lines, i, depth, out, words) {
    var first = RE_ITEM.exec(lines[i]), base = first[1].length, ordered = /^\d/.test(first[2]);
    var list = el(ordered ? "ol" : "ul");
    if (ordered) { var st = parseInt(first[2], 10); if (st !== 1) list.start = st; }
    var items = [], cur = null;
    while (i < lines.length) {
      var ln = lines[i];
      if (!ln.trim()) {
        var k = i + 1;
        while (k < lines.length && !lines[k].trim()) k++;
        if (k >= lines.length) break;
        var nm = RE_ITEM.exec(lines[k]), ind0 = indentOf(lines[k]);
        if (ind0 >= base + 2) { if (cur) cur.sub.push(""); i++; continue; }
        if (nm && ind0 >= base && /^\d/.test(nm[2]) === ordered) { i = k; continue; }
        break;
      }
      var ind = indentOf(ln), m = RE_ITEM.exec(ln);
      if (m && ind < base + 2) {
        if (ind < base || /^\d/.test(m[2]) !== ordered) break;
        cur = { text: [m[3] || ""], sub: [] }; items.push(cur); i++; continue;
      }
      if (!cur) break;
      if (ind >= base + 2) {
        if (cur.sub.length || m) cur.sub.push(ln.slice(base + 2));
        else cur.text.push(ln.trim());
        i++; continue;
      }
      if (!cur.sub.length && !interrupts(ln) && lines[i - 1].trim()) { cur.text.push(ln.trim()); i++; continue; }
      break;
    }
    items.forEach(function (it) {
      var li = el("li"), txt = it.text.join("\n"), t = RE_TASK.exec(txt);
      if (t) {
        li.className = "task";
        var on = t[1] !== " ", cb = el("span", "cbx" + (on ? " on" : ""));
        cb.setAttribute("role", "img");
        cb.setAttribute("aria-label", on ? words.done : words.open);
        if (on) cb.appendChild(icon("check"));
        li.appendChild(cb);
        txt = t[2] || "";
      }
      inline(txt, li, {});
      if (it.sub.length) blocks(it.sub, li, depth + 1, words);
      list.appendChild(li);
    });
    out.appendChild(list);
    return i;
  }

  function blocks(lines, out, depth, words) {
    var i = 0;
    while (i < lines.length) {
      var ln = lines[i], m;
      if (!ln.trim()) { i++; continue; }
      if ((m = RE_FENCE.exec(ln))) {
        var close = new RegExp("^ {0,3}`{" + m[1].length + ",}[ \\t]*$"), code = [];
        i++;
        while (i < lines.length && !close.test(lines[i])) { code.push(lines[i]); i++; }
        i++;
        var pre = el("pre"), cd = el("code", "", code.join("\n"));
        if (m[2]) pre.setAttribute("data-lang", m[2]);
        pre.appendChild(cd); out.appendChild(pre); continue;
      }
      if ((m = RE_HEAD.exec(ln))) {
        var h = el("h" + m[1].length);
        inline(m[2] || "", h, {});
        out.appendChild(h); i++; continue;
      }
      if (RE_QUOTE.test(ln) && depth < 5) {
        var inner = [];
        while (i < lines.length && RE_QUOTE.test(lines[i])) { inner.push(lines[i].replace(/^ {0,3}> ?/, "")); i++; }
        var bq = el("blockquote");
        blocks(inner, bq, depth + 1, words);
        out.appendChild(bq); continue;
      }
      if (RE_ITEM.test(ln) && depth < 5) { i = parseList(lines, i, depth, out, words); continue; }
      var buf = [ln.replace(/^ +/, "").replace(/\s+$/, "")];
      i++;
      while (i < lines.length && lines[i].trim() && !interrupts(lines[i])) { buf.push(lines[i].replace(/^ +/, "").replace(/\s+$/, "")); i++; }
      var p = el("p");
      inline(buf.join("\n"), p, {});
      out.appendChild(p);
    }
  }

  // render(text, words) returns a fragment. words: empty (text of an empty
  // preview), done and open (names of the task list boxes), all from the page.
  function render(text, words) {
    words = words || {};
    words = { empty: words.empty || "", done: words.done || "Done", open: words.open || "Open" };
    var frag = document.createDocumentFragment(), src = String(text == null ? "" : text).replace(/\r\n?/g, "\n");
    if (!src.trim()) { frag.appendChild(el("p", "muted", words.empty)); return frag; }
    blocks(src.split("\n"), frag, 0, words);
    return frag;
  }
  window.GTMarkdown = { render: render, safeUrl: safeUrl };

  // A text the server wrote as plain paragraphs (a comment) carries its source in
  // data-md; with the script it is shown as Markdown, without it stays as it is.
  function upgrade() {
    for (var box of document.querySelectorAll("[data-md]")) {
      var src = box.getAttribute("data-md");
      box.removeAttribute("data-md");
      box.textContent = "";
      box.appendChild(render(src, {}));
    }
    if (window.GTCards) window.GTCards.scan(document);
    if (window.GTRefs) window.GTRefs.resolve(document);
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", upgrade); else upgrade();
})();
