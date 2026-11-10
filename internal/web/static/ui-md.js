// The one Markdown renderer of the page: the preview of the composer, comments,
// notes, reasons, descriptions, room messages and documents all come through
// here. The server writes the source of a text into data-md (markdown.go) and the
// blocks as plain HTML for a reader without the script; this script replaces
// them with the same text as DOM nodes. It builds DOM nodes (createElement,
// textContent) and never HTML from the text of a person, so nothing typed can
// become markup or script. Links only to http(s) and relative addresses.
// Supported: headings # to ######, bold, italic, strikethrough, code, code
// blocks, lists, task lists, quotes, tables, rules, links, bare addresses,
// @mentions, REQ-n, PAIN-n, TASK-n, IDEA-n, DEC-n, FEAT-n as pills (ui-pill.js)
// and #1970 as the pill of a knowledge entry once the server confirms it.
// A box with data-md-anchors (the documents) gives its headings an address
// "h-text" (a number after it counts up until it is free) and fills the nav
// with data-toc with the outline.
// Every pass over a text is linear in its length (no scan starts again from each
// marker), and a text over MAX_MD is not rendered at all: the plain blocks the
// server wrote stay. Port of the accepted mock (git-native-v2, gh6.js).
(function () {
  "use strict";
  // Texts over this many characters stay as the server wrote them (64 KB, the size
  // a room message may have), and no bare address is longer than MAX_URL.
  var MAX_MD = 65536, MAX_URL = 2048, MAX_CELLS = 20000;
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  // The icons and the pill are the ones of ui-pill.js, the same the server writes.
  function icon(name) { return window.GTPill.icon(name); }

  // Allowed: http(s) and relative addresses. Everything else (javascript:, data:, //host) is dropped,
  // and so is an address with a control, space or direction mark (U+202A-202E, U+2066-2069,
  // U+200E, U+200F) in it: those reorder the text around a link and make it read as another host.
  var RE_DIRECTION = /[\u200e\u200f\u202a-\u202e\u2066-\u2069]/;
  var RE_BAD_URL = /[\u0000-\u001f\u007f\s<>"\\]/;
  function safeUrl(u) {
    u = (u || "").trim();
    if (!u || RE_BAD_URL.test(u) || RE_DIRECTION.test(u)) return null;
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

  var ESC = "\\`*_[]()#>@-!~|<+.";
  var RE_REF = /(REQ|PAIN|TASK|IDEA|DEC|FEAT)-(\d+)(?![\p{L}\p{N}_-])/uy;
  var RE_KN = /#(\d{1,15})(?![\p{L}\p{N}_-])/uy;
  // A name may hold a dot or a colon between its words ("@claude:mainex"), never at its end.
  var RE_MENTION = /@[\p{L}\p{N}][\p{L}\p{N}_-]*(?:[.:][\p{L}\p{N}][\p{L}\p{N}_-]*)*/uy;
  var RE_WORD = /[\p{L}\p{N}_]/u;
  // The match stops one character past MAX_URL, so a longer address is told by its length and
  // a scan never runs on to the end of a long text (RE_REST takes such a word once).
  var RE_URL = /https?:\/\/[^\s<>"\\]{1,2049}/iy;
  var RE_AUTOLINK = /<(https?:\/\/[^\s<>"\\]{1,2040})>/iy;
  var RE_REST = /[^\s<>"\\]+/y;

  // The end of a bare address: closing brackets only when it opened one, no
  // sentence punctuation or emphasis marks.
  // The brackets are counted once and the count follows each cut, so a long run of
  // closing ones costs no more than the address is long.
  function trimUrl(u) {
    var n = { "(": 0, ")": 0, "[": 0, "]": 0 }, end = u.length, k;
    for (k = 0; k < end; k++) if (u.charAt(k) in n) n[u.charAt(k)]++;
    while (end > 0) {
      var last = u.charAt(end - 1);
      if ((last === ")" && n["("] >= n[")"]) || (last === "]" && n["["] >= n["]"])) break;
      if (!/[.,;:!?'*_~)\]}]/.test(last)) break;
      if (last in n) n[last]--;
      end--;
    }
    return u.slice(0, end);
  }
  function link(url, parent) {
    var a = el("a");
    a.setAttribute("href", url);
    if (/^https?:/i.test(url)) { a.target = "_blank"; a.rel = "noopener noreferrer"; }
    parent.appendChild(a);
    return a;
  }

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
  // The closing marks of bold and italic are found once per text, in one pass, and each
  // opener asks for the first one after it. (Searching on from every opener made a text of
  // many unclosed markers quadratic.) A run of stars is read as pairs from its start, so
  // what can close is the last pair of a run for bold and the last star of a run of odd
  // length for italic; the one before it may not be a space, and for "_" the one after it
  // may not be a letter.
  function boldClosers(s) {
    var out = [], i = s.indexOf("*");
    while (i >= 0) {
      var b = i;
      while (s.charAt(b) === "*") b++;
      if (b - i >= 2 && !/\s/.test(s.charAt(b - 3))) out.push(b - 2);
      i = s.indexOf("*", b);
    }
    return out;
  }
  function italicClosers(s, ch) {
    var out = [], i = s.indexOf(ch);
    while (i >= 0) {
      var b = i;
      while (s.charAt(b) === ch) b++;
      if ((b - i) % 2 === 1) {
        var after = s.charAt(b);
        if (!/\s/.test(s.charAt(b - 2)) && (ch === "*" || !after || !RE_WORD.test(after))) out.push(b - 1);
      }
      i = s.indexOf(ch, b);
    }
    return out;
  }
  // The first closing mark after position from in the sorted list, -1 if none.
  function firstAfter(list, from) {
    var lo = 0, hi = list.length;
    while (lo < hi) {
      var mid = (lo + hi) >> 1;
      if (list[mid] > from) hi = mid; else lo = mid + 1;
    }
    return lo < list.length ? list[lo] : -1;
  }
  // The end of every square and round bracket in one pass with a stack: sq[i] and rd[i] hold the
  // position of the closing bracket of the one at i, -1 if it has none. A backslash takes the next
  // character; a round bracket does not reach across a line break.
  function bracketPairs(s) {
    var sq = new Int32Array(s.length).fill(-1), rd = new Int32Array(s.length).fill(-1), ss = [], rs = [];
    for (var j = 0; j < s.length; j++) {
      var c = s.charAt(j);
      if (c === "\\") j++;
      else if (c === "[") ss.push(j);
      else if (c === "]") { if (ss.length) sq[ss.pop()] = j; }
      else if (c === "(") rs.push(j);
      else if (c === ")") { if (rs.length) rd[rs.pop()] = j; }
      else if (c === "\n") rs.length = 0;
    }
    return { sq: sq, rd: rd };
  }

  function inline(src, parent, o) {
    var buf = "", i = 0, n = src.length, deep = (o.d || 0) + 1, closers = {}, pairs = null;
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
          j = firstAfter(closers.bold || (closers.bold = boldClosers(src)), i + 2);
          if (j > 0) { sub("strong", src.slice(i + 2, j)); i = j + 2; continue; }
        }
        buf += "**"; i += 2; continue;
      }
      if (deep < 7 && c === "~" && src.charAt(i + 1) === "~" && src.charAt(i + 2) !== "~" && i + 2 < n && !/\s/.test(src.charAt(i + 2))) {
        j = src.indexOf("~~", i + 2);
        if (j > i + 2 && !/\s/.test(src.charAt(j - 1))) { sub("del", src.slice(i + 2, j)); i = j + 2; continue; }
      }
      if (deep < 7 && (c === "*" || c === "_") && src.charAt(i + 1) !== c && i + 1 < n && !/\s/.test(src.charAt(i + 1)) &&
          (c === "*" || !prev || !RE_WORD.test(prev))) {
        j = firstAfter(closers[c] || (closers[c] = italicClosers(src, c)), i + 1);
        if (j > 0) { sub("em", src.slice(i + 1, j)); i = j + 1; continue; }
      }
      if (deep < 7 && c === "[" && !o.noLink) {
        pairs = pairs || bracketPairs(src);
        var e1 = pairs.sq[i];
        if (e1 > 0 && src.charAt(e1 + 1) === "(") {
          var e2 = pairs.rd[e1 + 1];
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
      if (c === "<" && !o.noLink) {
        RE_AUTOLINK.lastIndex = i;
        if ((m = RE_AUTOLINK.exec(src)) && safeUrl(m[1])) {
          flush(); link(m[1], parent).appendChild(document.createTextNode(m[1])); i += m[0].length; continue;
        }
      }
      if ((c === "h" || c === "H") && !o.noLink && (!prev || !RE_WORD.test(prev))) {
        RE_URL.lastIndex = i;
        if ((m = RE_URL.exec(src))) {
          if (m[0].length > MAX_URL || RE_DIRECTION.test(m[0])) {
            // Too long or with a direction mark: the whole word stays text, so no later letter of it starts a scan again.
            RE_REST.lastIndex = i;
            var word = RE_REST.exec(src)[0];
            buf += word; i += word.length; continue;
          }
          var bare = trimUrl(m[0]);
          if (safeUrl(bare)) {
            flush(); link(bare, parent).appendChild(document.createTextNode(bare)); i += bare.length; continue;
          }
        }
      }
      if (c === "@" && !o.noLink &&(!prev || !(RE_WORD.test(prev) || prev === "@" || prev === "." || prev === "/"))) {
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

  // The fence, heading and table-separator lines are read by hand: as one regular expression
  // each (the same shapes) they backtrack through a long run of blanks once per blank,
  // so a line of 60 KB of spaces held the tab for seconds.
  var RE_FENCE_OPEN = /^ {0,3}(`{3,})/;
  var RE_FENCE_INFO = /^[ \t]*([\w+#.\-]*)/;
  // [line, backticks, language] or null: three or more backticks, then a word, and no more backticks.
  var RE_FENCE = { exec: function (l) {
    var m = RE_FENCE_OPEN.exec(l);
    if (!m) return null;
    var rest = l.slice(m[0].length);
    return rest.indexOf("`") >= 0 ? null : [l, m[1], RE_FENCE_INFO.exec(rest)[1]];
  }, test: function (l) { return !!this.exec(l); } };
  var RE_HASHES = /^ {0,3}(#{1,6})/;
  // [line, hashes, text] or null: up to six hashes, a blank, the text without the closing hashes
  // ("## a ##" says "a") and without the blanks around it. A line break character cannot be in it.
  var RE_HEAD = { exec: function (l) {
    var m = RE_HASHES.exec(l);
    if (!m) return null;
    var rest = l.slice(m[0].length), k = 0;
    if (!rest) return [l, m[1], undefined];
    while (rest.charAt(k) === " " || rest.charAt(k) === "\t") k++;
    if (!k) return null;
    var text = rest.slice(k), end = text.length;
    if (!text) return [l, m[1], ""];
    if (/[\n\r\u2028\u2029]/.test(text)) return null;
    while (text.charAt(end - 1) === " " || text.charAt(end - 1) === "\t") end--;
    var h = end;
    while (text.charAt(h - 1) === "#") h--;
    if (h < end && h > 0 && (text.charAt(h - 1) === " " || text.charAt(h - 1) === "\t")) {
      end = h - 1;
      while (text.charAt(end - 1) === " " || text.charAt(end - 1) === "\t") end--;
    }
    return [l, m[1], text.slice(0, end)];
  }, test: function (l) { return !!this.exec(l); } };
  var RE_QUOTE = /^ {0,3}>/;
  var RE_ITEM = /^( *)([-*+]|\d{1,9}[.)])(?:[ \t]+(.*))?$/;
  var RE_TASK = /^\[([ xX])\](?:[ \t]+([\s\S]*))?$/;
  var RE_RULE = /^ {0,3}([-*_])(?: *\1){2,} *$/;
  // A separator line: cells of dashes (a colon on either end), between bars, with blanks around them.
  var RE_SEP = { test: function (l) {
    var k = 0, n = l.length, lead;
    while (l.charAt(k) === " ") k++;
    lead = k;
    if (l.charAt(k) === "|") { if (lead > 3) return false; k++; }
    for (;;) {
      while (l.charAt(k) === " ") k++;
      if (l.charAt(k) === ":") k++;
      if (l.charAt(k) !== "-") return false;
      while (l.charAt(k) === "-") k++;
      if (l.charAt(k) === ":") k++;
      while (l.charAt(k) === " ") k++;
      if (k >= n) return true;
      if (l.charAt(k) !== "|") return false;
      k++;
      while (l.charAt(k) === " ") k++;
      if (k >= n) return true;
    }
  } };

  // The cells of a table row; a \| is a bar in the text.
  function cellsOf(l) {
    l = l.trim();
    if (l.charAt(0) === "|") l = l.slice(1);
    if (l.charAt(l.length - 1) === "|" && l.charAt(l.length - 2) !== "\\") l = l.slice(0, -1);
    var out = [], cur = "";
    for (var k = 0; k < l.length; k++) {
      if (l.charAt(k) === "\\" && l.charAt(k + 1) === "|") { cur += "|"; k++; }
      else if (l.charAt(k) === "|") { out.push(cur.trim()); cur = ""; }
      else cur += l.charAt(k);
    }
    out.push(cur.trim());
    return out;
  }
  // A table starts at line i: a row with a bar and under it a separator line with as many cells.
  function tableAt(lines, i) {
    if (i + 1 >= lines.length) return false;
    var a = lines[i], b = lines[i + 1];
    return a.indexOf("|") >= 0 && b.indexOf("|") >= 0 && RE_SEP.test(b) && cellsOf(a).length === cellsOf(b).length;
  }

  function interrupts(l) {
    if (RE_FENCE.test(l) || RE_HEAD.test(l) || RE_QUOTE.test(l) || RE_RULE.test(l)) return true;
    var m = RE_ITEM.exec(l);
    return !!m && !!m[3] && (!/^\d/.test(m[2]) || /^1[.)]$/.test(m[2]));
  }
  function indentOf(l) { return /^ */.exec(l)[0].length; }
  // A line without its leading spaces and its trailing white space. (As /\s+$/ it
  // backtracks through every run of blanks inside the line.)
  function trimLine(l) {
    var a = 0, b = l.length;
    while (a < b && l.charAt(a) === " ") a++;
    while (b > a && /\s/.test(l.charAt(b - 1))) b--;
    return l.slice(a, b);
  }

  function parseList(lines, i, depth, out, words) {
    var first = RE_ITEM.exec(lines[i]), base = first[1].length, ordered = /^\d/.test(first[2]);
    var list = el(ordered ? "ol" : "ul");
    if (ordered) { var st = parseInt(first[2], 10); if (st !== 1) list.start = st; }
    var items = [], cur = null, filled = 0;
    while (i < lines.length) {
      var ln = lines[i];
      if (!ln.trim()) {
        // The next line with text is the same for every blank line of a run: look for it once.
        var k = Math.max(filled, i + 1);
        while (k < lines.length && !lines[k].trim()) k++;
        filled = k;
        if (k >= lines.length) break;
        var nm = RE_ITEM.exec(lines[k]), ind0 = indentOf(lines[k]);
        if (ind0 >= base + 2) { if (cur) cur.sub.push(""); i++; continue; }
        if (nm && ind0 >= base && /^\d/.test(nm[2]) === ordered) { i = k; continue; }
        break;
      }
      if (RE_RULE.test(ln) && indentOf(ln) < base + 2) break;
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

  // A table goes into a box of its own that scrolls sideways, so a wide one
  // never makes the page scroll.
  function parseTable(lines, i, out) {
    var head = cellsOf(lines[i]), align = cellsOf(lines[i + 1]).map(function (c) {
      var l = c.charAt(0) === ":", r = c.charAt(c.length - 1) === ":";
      return l && r ? "c" : r ? "r" : "";
    });
    var rows = 0;
    while (i + 2 + rows < lines.length && lines[i + 2 + rows].trim() && lines[i + 2 + rows].indexOf("|") >= 0) rows++;
    // A table of more cells than anyone reads is shown as the lines it was written in; one
    // header of thousands of columns over thousands of rows would make millions of nodes.
    if (head.length * (rows + 1) > MAX_CELLS) {
      var raw = el("p");
      inline(lines.slice(i, i + 2 + rows).map(trimLine).join("\n"), raw, {});
      out.appendChild(raw);
      return i + 2 + rows;
    }
    var wrap = el("div", "tbl"), table = el("table"), thead = el("thead"), tr = el("tr");
    function cell(tag, text, k, row) {
      var c = el(tag);
      if (align[k]) c.className = "al-" + align[k];
      inline(text, c, {});
      row.appendChild(c);
    }
    head.forEach(function (t, k) { cell("th", t, k, tr); });
    thead.appendChild(tr); table.appendChild(thead);
    var body = el("tbody");
    for (i += 2; i < lines.length && lines[i].trim() && lines[i].indexOf("|") >= 0; i++) {
      var row = el("tr"), cs = cellsOf(lines[i]);
      for (var k = 0; k < head.length; k++) cell("td", cs[k] || "", k, row);
      body.appendChild(row);
    }
    if (body.firstChild) table.appendChild(body);
    wrap.appendChild(table); out.appendChild(wrap);
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
      if (RE_RULE.test(ln)) { out.appendChild(el("hr")); i++; continue; }
      if (RE_QUOTE.test(ln) && depth < 5) {
        var inner = [];
        while (i < lines.length && RE_QUOTE.test(lines[i])) { inner.push(lines[i].replace(/^ {0,3}> ?/, "")); i++; }
        var bq = el("blockquote");
        blocks(inner, bq, depth + 1, words);
        out.appendChild(bq); continue;
      }
      if (tableAt(lines, i)) { i = parseTable(lines, i, out); continue; }
      if (RE_ITEM.test(ln) && depth < 5) { i = parseList(lines, i, depth, out, words); continue; }
      var buf = [trimLine(ln)];
      i++;
      while (i < lines.length && lines[i].trim() && !interrupts(lines[i]) && !tableAt(lines, i)) { buf.push(trimLine(lines[i])); i++; }
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
    // Too long to read as Markdown within a moment: shown as it is, nothing is interpreted.
    if (src.length > MAX_MD) { frag.appendChild(el("pre", "", src)); return frag; }
    blocks(src.split("\n"), frag, 0, words);
    return frag;
  }
  window.GTMarkdown = { render: render, safeUrl: safeUrl, maxLength: MAX_MD };

  // The address of a heading: "h-" and its words, a number after it counts up
  // until nothing on the page has it (markdown.go writes the same for a page
  // without the script).
  function anchorOf(text, used) {
    var slug = text.toLowerCase().replace(/[^\p{L}\p{N}\s_-]+/gu, "").trim().replace(/[\s_-]+/g, "-") || "section";
    // "#" cannot be in an address, so the counter of a word is kept next to the addresses taken
    // and the n-th heading of the same words does not count up from 2 again.
    var id = "h-" + slug, n = used["#" + slug] || 2;
    while (used[id] || document.getElementById(id)) id = "h-" + slug + "-" + n++;
    used[id] = true;
    if (id !== "h-" + slug) used["#" + slug] = n;
    return id;
  }
  // The headings of a rendered text get their address; the outline goes into the nav.
  function anchors(box) {
    var used = {}, heads = box.querySelectorAll("h1,h2,h3,h4,h5,h6"), nav = document.querySelector("[data-toc]");
    for (var h of heads) h.id = anchorOf(h.textContent, used);
    if (!nav) return;
    var list = nav.querySelector("ol"), top = 6;
    list.textContent = "";
    for (var h1 of heads) top = Math.min(top, Number(h1.tagName.charAt(1)));
    for (var h2 of heads) {
      var li = el("li", "lv" + Math.min(3, Number(h2.tagName.charAt(1)) - top)), a = el("a", "", h2.textContent);
      a.setAttribute("href", "#" + h2.id);
      li.appendChild(a);
      list.appendChild(li);
    }
    nav.hidden = heads.length < 2;
  }

  // The address in the URL, decoded; one that is not valid percent-encoding ("#%E0") is taken as it is.
  function hashId() {
    var raw = location.hash.slice(1);
    try { return decodeURIComponent(raw); } catch (e) { return raw; }
  }

  // A text the server wrote as plain blocks carries its source in data-md; with
  // the script it is shown as Markdown, without it stays as it is.
  function upgrade(root) {
    var boxes = (root || document).querySelectorAll("[data-md]"), done = false;
    for (var box of boxes) {
      if (!document.contains(box)) continue;
      var src = box.getAttribute("data-md");
      box.removeAttribute("data-md");
      // Over the limit the plain blocks of the server stay in the box.
      if (src.length > MAX_MD) { done = true; continue; }
      box.textContent = "";
      box.appendChild(render(src, {}));
      if (box.hasAttribute("data-md-anchors")) {
        anchors(box);
        var t = location.hash.length > 1 && document.getElementById(hashId());
        if (t) t.scrollIntoView();
      }
      done = true;
    }
    done = refify(root || document) || done;
    if (!done) return;
    if (window.GTCards) window.GTCards.scan(root || document);
    if (window.GTRefs) window.GTRefs.resolve(root || document);
  }

  // A text the page keeps as it is (the transcript of a session, with the hits of
  // a search in it) is not Markdown, but the keys in it are still references:
  // inside a box with data-refify every "REQ-5" in a text node becomes the pill.
  var RE_KEYS = /(REQ|PAIN|TASK|IDEA|DEC|FEAT)-(\d+)(?![\p{L}\p{N}_-])/gu;
  function refify(root) {
    var found = false;
    for (var box of root.querySelectorAll("[data-refify]")) {
      box.removeAttribute("data-refify");
      var walker = document.createTreeWalker(box, NodeFilter.SHOW_TEXT), nodes = [], t;
      while ((t = walker.nextNode())) {
        if (!t.parentNode.closest("a, pre, code, .ref")) nodes.push(t);
      }
      for (var node of nodes) {
        var text = node.nodeValue, last = 0, m, frag = null;
        RE_KEYS.lastIndex = 0;
        while ((m = RE_KEYS.exec(text))) {
          if (m.index > 0 && /[\p{L}\p{N}_-]/u.test(text.charAt(m.index - 1))) continue;
          frag = frag || document.createDocumentFragment();
          frag.appendChild(document.createTextNode(text.slice(last, m.index)));
          frag.appendChild(pill(m[0]));
          last = m.index + m[0].length;
        }
        if (!frag) continue;
        frag.appendChild(document.createTextNode(text.slice(last)));
        node.replaceWith(frag);
        found = true;
      }
    }
    return found;
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", function () { upgrade(); }); else upgrade();

  // Text the page brings later (a room that refreshes, search results) is drawn the same way.
  if (window.MutationObserver) {
    var queued = false;
    new MutationObserver(function (list) {
      if (queued) return;
      for (var m of list) {
        for (var n of m.addedNodes) {
          if (n.nodeType === 1 && (n.matches("[data-md],[data-refify]") || n.querySelector("[data-md],[data-refify]"))) {
            queued = true;
            requestAnimationFrame(function () { queued = false; upgrade(); });
            return;
          }
        }
      }
    }).observe(document.documentElement, { childList: true, subtree: true });
  }
})();
