// Pictures on an idea, an issue and a pain (REQ-671): besides the button, Ctrl+V with a
// picture on the clipboard and dropping a picture file on the page add it. Both go through
// the form of the page (form[data-attach]), so the server checks the same things as for
// the button: the right to work on the object, the type by the bytes, the size. When the
// page has no such form (guest, reader, closed idea) the script does nothing at all.
//
// None of the three ways loads the page again: the picture is sent in the background and shows up in the
// row of pictures, so a comment that was begun on the page is still in its field afterwards.
//
// Pasting text is never touched: the script only answers when the clipboard holds an image
// file. In a field it also stays out of the way when the clipboard carries text besides the
// image (a cell copied from a spreadsheet does), because there text is what is wanted.
(() => {
  const form = document.querySelector("form[data-attach]");
  if (!form || typeof fetch !== "function" || typeof FormData !== "function") return;

  const maxBytes = Number(form.dataset.max) || 5 << 20;
  let busy = false;

  const isImage = (f) => !!f && typeof f.type === "string" && /^image\/(png|jpeg|webp)$/.test(f.type);
  const editable = (el) => !!el && el instanceof Element && !!el.closest("input, textarea, select, [contenteditable]");

  // imageOf is the first picture among the files of a clipboard or a drag.
  const imageOf = (files) => [...(files || [])].find(isImage) || null;

  const clipboardImage = (data) => {
    if (!data) return { file: null, text: false };
    let file = imageOf(data.files);
    const items = [...(data.items || [])];
    if (!file) {
      const item = items.find((i) => i.kind === "file" && isImage({ type: i.type }));
      file = item && typeof item.getAsFile === "function" ? item.getAsFile() : null;
    }
    const text = items.some((i) => i.kind === "string" && i.type === "text/plain");
    return { file, text };
  };

  // tell shows a refusal above the form, in the markup of the other banners.
  const tell = (text) => {
    let note = form.parentNode.querySelector(".ws-up-note");
    if (!note) {
      note = document.createElement("div");
      note.className = "callout is-danger ws-up-note";
      note.setAttribute("role", "alert");
      const tt = document.createElement("div");
      tt.className = "tt";
      note.appendChild(tt);
      form.before(note);
    }
    note.querySelector(".tt").textContent = text;
  };

  // list is the row of pictures above the form; the first picture of an object makes it.
  const list = () => {
    let el = form.parentNode.querySelector(".ws-imgs");
    if (!el) {
      el = document.createElement("div");
      el.className = "ws-imgs ws-imgs-idea";
      form.parentNode.insertBefore(el, form.parentNode.querySelector(".ws-up-note") || form);
    }
    return el;
  };

  // added puts the picture the server rendered (the same markup as on a loaded page) at the end of the
  // row, empties the form and leaves the rest of the page as it is: the text of a comment that was begun
  // stays in its field, no page is loaded.
  const added = (html) => {
    const holder = document.createElement("template");
    holder.innerHTML = html;
    const node = holder.content.firstElementChild;
    if (!node) return false;
    list().appendChild(node);
    const note = form.parentNode.querySelector(".ws-up-note");
    if (note) note.remove();
    const input = form.querySelector('input[type="file"]');
    if (input) {
      input.value = "";
      input.dispatchEvent(new Event("change", { bubbles: true }));
    }
    const alt = form.querySelector('input[name="alt"]');
    if (alt) alt.value = "";
    return true;
  };

  const upload = async (file) => {
    if (busy) return;
    if (file.size > maxBytes) return tell(form.dataset.big || form.dataset.fail || "");
    busy = true;
    form.setAttribute("aria-busy", "true");
    try {
      const body = new FormData(form);
      // The pasted file replaces the (empty) file field; the server names the picture by its number.
      body.set("file", file, file.name || "image");
      // Asking for JSON: the server answers with the picture (or the reason it was refused) and not with a
      // redirect to the page, which would be loaded again.
      const res = await fetch(form.getAttribute("action"), { method: "POST", body, credentials: "same-origin", headers: { Accept: "application/json" } });
      let answer = null;
      try { answer = await res.json(); } catch (_) { answer = null; }
      if (res.ok && answer && answer.ok && typeof answer.html === "string" && added(answer.html)) return;
      tell((answer && typeof answer.message === "string" && answer.message) || form.dataset.fail || "");
    } catch (_) {
      tell(form.dataset.fail || "");
    } finally {
      busy = false;
      form.removeAttribute("aria-busy");
    }
  };

  // The button: the form is sent in the background too. Without a chosen file there is nothing to send, and
  // the page says so where it is.
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const input = form.querySelector('input[type="file"]');
    const file = input && input.files && input.files[0];
    if (!file) return tell(form.dataset.none || form.dataset.fail || "");
    upload(file);
  });

  document.addEventListener("paste", (e) => {
    const { file, text } = clipboardImage(e.clipboardData);
    if (!file) return;
    if (text && editable(e.target)) return;
    e.preventDefault();
    upload(file);
  });

  const hasFiles = (dt) => !!dt && [...(dt.types || [])].includes("Files");
  const overFile = (e) => !!(e.target instanceof Element) && !!e.target.closest("input[type=\"file\"]");

  document.addEventListener("dragover", (e) => {
    if (!hasFiles(e.dataTransfer) || overFile(e)) return;
    e.preventDefault();
    document.body.classList.add("ws-dropping");
  });
  const leave = () => document.body.classList.remove("ws-dropping");
  document.addEventListener("dragleave", (e) => { if (!e.relatedTarget) leave(); });
  document.addEventListener("drop", (e) => {
    leave();
    if (overFile(e)) return;
    const file = imageOf(e.dataTransfer && e.dataTransfer.files);
    if (!file) return;
    e.preventDefault();
    upload(file);
  });
})();
