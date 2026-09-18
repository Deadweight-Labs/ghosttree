(() => {
  const workspace = document.querySelector(".coord-workspace");
  if (!workspace) return;

  const media = matchMedia("(max-width: 1100px)");
  const backdrop = document.querySelector("[data-coord-drawer-close]");
  document.documentElement.classList.add("coord-enhanced");
  let openPanel = null;
  let returnFocus = null;
  let outsideState = [];

  const toggles =
    () => [...document.querySelectorAll("[data-coord-drawer-target]")];
  const setOutsideInert = (panel) => {
    outsideState = [];
    let branch = panel;
    while (branch && branch !== document.body) {
      const parent = branch.parentElement;
      if (!parent) break;
      for (const sibling of parent.children) {
        if (
          sibling === branch || sibling === backdrop ||
          sibling.tagName === "SCRIPT"
        ) continue;
        outsideState.push([sibling, sibling.inert]);
        sibling.inert = true;
      }
      branch = parent;
    }
  };
  const restoreOutside = () => {
    for (const [element, wasInert] of outsideState) element.inert = wasInert;
    outsideState = [];
  };
  const close = () => {
    if (!openPanel) return;
    openPanel.hidden = true;
    openPanel.inert = true;
    restoreOutside();
    if (backdrop) backdrop.hidden = true;
    document.body.classList.remove("coord-drawer-open");
    toggles().forEach((button) =>
      button.setAttribute("aria-expanded", "false")
    );
    const focus = returnFocus;
    openPanel = null;
    returnFocus = null;
    if (focus && focus.isConnected) focus.focus();
  };
  const sync = () => {
    close();
    for (const id of ["coord-rooms", "coord-context"]) {
      const panel = document.getElementById(id);
      if (!panel) continue;
      panel.hidden = media.matches;
      panel.inert = media.matches;
    }
  };
  const open = (button) => {
    close();
    const panel = document.getElementById(button.dataset.coordDrawerTarget);
    if (!panel || !media.matches || !backdrop) return;
    returnFocus = button;
    openPanel = panel;
    panel.hidden = false;
    panel.inert = false;
    setOutsideInert(panel);
    backdrop.hidden = false;
    document.body.classList.add("coord-drawer-open");
    button.setAttribute("aria-expanded", "true");
    (panel.querySelector("a,button,input,select,textarea,summary") || panel)
      .focus();
  };
  const bindDrawers = () => {
    toggles().forEach((button) => {
      if (button.dataset.coordDrawerBound) return;
      button.dataset.coordDrawerBound = "true";
      button.addEventListener("click", () => open(button));
    });
  };
  bindDrawers();
  if (backdrop) backdrop.addEventListener("click", close);
  addEventListener("keydown", (event) => {
    if (event.key === "Escape") close();
  });
  media.addEventListener("change", sync);
  sync();

  const liveStatus = document.querySelector("[data-coord-live-status]");
  const announce = document.getElementById("coord-status");
  const setLive = (state, label, announcement = "") => {
    document.documentElement.dataset.coordLive = state;
    if (liveStatus) liveStatus.textContent = label;
    if (announcement && announce) announce.textContent = announcement;
  };
  if (!("EventSource" in window)) {
    setLive("unsupported", "Live-Updates nicht unterstützt");
    return;
  }

  const pageParams = () => new URLSearchParams(location.search);
  const currentRoom = () => pageParams().get("room") || "";
  const currentThread = () => pageParams().get("thread") || "";
  const anchorState = () => {
    const params = pageParams();
    const roomAround = params.get("around");
    const threadAround = params.get("thread_around");
    const id = threadAround
      ? `thread-message-${threadAround}`
      : (roomAround ? `message-${roomAround}` : "");
    const element = id ? document.getElementById(id) : null;
    const messages = document.querySelector(".coord-messages");
    const context = document.getElementById("coord-context");
    const container = threadAround ? context : messages;
    return {
      id,
      top: element && container
        ? element.getBoundingClientRect().top -
          container.getBoundingClientRect().top
        : null,
      container,
      messages,
      messageScrollTop: messages ? messages.scrollTop : 0,
      messageNearBottom: messages
        ? messages.scrollHeight - messages.scrollTop - messages.clientHeight <
          80
        : false,
      context,
      contextScrollTop: context ? context.scrollTop : 0,
    };
  };
  const restoreAnchor = (state) => {
    if (state.id && state.top !== null && state.container) {
      const element = document.getElementById(state.id);
      if (element) {
        const nextTop = element.getBoundingClientRect().top -
          state.container.getBoundingClientRect().top;
        state.container.scrollTop += nextTop - state.top;
      }
    }
    if (state.messages && state.container !== state.messages) {
      state.messages.scrollTop = state.messageNearBottom
        ? state.messages.scrollHeight
        : state.messageScrollTop;
    }
    if (state.context && state.container !== state.context) {
      state.context.scrollTop = state.contextScrollTop;
    }
    if (!state.id && state.messages) {
      state.messages.scrollTop = state.messageNearBottom
        ? state.messages.scrollHeight
        : state.messageScrollTop;
    }
    if (!state.id && state.context) {
      state.context.scrollTop = state.contextScrollTop;
    }
  };
  const replaceSurface = (selector, incomingDocument) => {
    const current = document.querySelector(selector);
    const incoming = incomingDocument.querySelector(selector);
    if (!current || !incoming) return;
    current.replaceChildren(...incoming.childNodes);
  };
  const captureFocus = () => {
    const element = document.activeElement;
    if (!element || element === document.body) return null;
    return {
      tag: element.tagName,
      id: element.id,
      name: element.getAttribute("name") || "",
      href: element.getAttribute("href") || "",
      text: element.textContent || "",
    };
  };
  const restoreFocus = (state) => {
    if (
      !state ||
      (document.activeElement && document.activeElement !== document.body)
    ) return;
    const candidates = [...document.querySelectorAll(state.tag.toLowerCase())];
    const match = candidates.find((element) =>
      (state.id && element.id === state.id) ||
      (state.href && element.getAttribute("href") === state.href) ||
      (state.name && element.getAttribute("name") === state.name &&
        element.textContent === state.text)
    );
    if (match) match.focus();
  };
  const draftControlKey = (control, index) =>
    `${control.tagName}:${control.getAttribute("name") || ""}:${index}`;
  const captureDrafts = () =>
    [...document.querySelectorAll("[data-coord-draft-key]")].map(
      (container) => {
        const controls = [
          ...container.querySelectorAll("input,select,textarea,button,summary"),
        ];
        return {
          key: container.dataset.coordDraftKey,
          open: container.tagName === "DETAILS" ? container.open : null,
          controls: controls.map((control, index) => ({
            key: draftControlKey(control, index),
            value: control.value,
            checked: control.checked,
            selectionStart: typeof control.selectionStart === "number"
              ? control.selectionStart
              : null,
            selectionEnd: typeof control.selectionEnd === "number"
              ? control.selectionEnd
              : null,
          })),
          focus: container.contains(document.activeElement)
            ? draftControlKey(
              document.activeElement,
              controls.indexOf(document.activeElement),
            )
            : "",
        };
      },
    );
  const restoreDrafts = (drafts) => {
    for (const draft of drafts) {
      const container = [...document.querySelectorAll("[data-coord-draft-key]")]
        .find((candidate) => candidate.dataset.coordDraftKey === draft.key);
      if (!container) continue;
      if (draft.open !== null && container.tagName === "DETAILS") {
        container.open = draft.open;
      }
      const controls = [
        ...container.querySelectorAll("input,select,textarea,button,summary"),
      ];
      for (const saved of draft.controls) {
        const index = controls.findIndex((control, position) =>
          draftControlKey(control, position) === saved.key
        );
        if (index < 0) continue;
        const control = controls[index];
        if (control.type === "checkbox" || control.type === "radio") {
          control.checked = saved.checked;
        } else if (control.tagName !== "BUTTON") control.value = saved.value;
      }
      if (draft.focus) {
        const focusIndex = controls.findIndex((control, position) =>
          draftControlKey(control, position) === draft.focus
        );
        if (focusIndex >= 0) {
          const control = controls[focusIndex];
          const saved = draft.controls.find((item) => item.key === draft.focus);
          control.focus();
          if (
            saved && saved.selectionStart !== null &&
            typeof control.setSelectionRange === "function"
          ) {
            control.setSelectionRange(saved.selectionStart, saved.selectionEnd);
          }
        }
      }
    }
  };

  let pending = new Set();
  let refreshing = false;
  const queueSurface = (selector) => pending.add(selector);
  const queueSurfaces = (selectors) => selectors.forEach(queueSurface);
  const roomSurfaces = [
    ".coord-conversation-head > div",
    ".coord-paging",
    ".coord-messages",
    "[data-coord-read-actions]",
  ];
  const contextSurfaces = [
    ".coord-participants",
    "[data-coord-standing-list]",
    "[data-coord-thread-list]",
  ];
  const threadSurfaces = [
    "[data-coord-thread-list]",
    ".coord-thread-detail > header",
    ".coord-thread-messages",
  ];
  const refreshVisible = async (event) => {
    queueSurface("[data-coord-sidebar-dynamic]");
    const room = currentRoom();
    const thread = currentThread();
    if (event.object_kind === "room" && event.object_id === room) {
      if (event.kind !== "read" && event.kind !== "attention") {
        queueSurfaces(roomSurfaces);
      }
      queueSurfaces(contextSurfaces);
    }
    if (event.object_kind === "discussion" && event.object_id === thread) {
      queueSurfaces(threadSurfaces);
    }
    if (event.object_kind === "thread") queueSurfaces(threadSurfaces);
    if (refreshing) return;
    refreshing = true;
    await new Promise((resolve) => setTimeout(resolve, 35));
    while (pending.size) {
      const selectors = [...pending];
      pending = new Set();
      const scroll = anchorState();
      const focus = captureFocus();
      const drafts = captureDrafts();
      try {
        const response = await fetch(location.href, {
          headers: { "X-Coord-Fragment": "visible" },
          credentials: "same-origin",
        });
        if (response.status === 403 || response.status === 404) {
          location.assign("/ui/coord");
          return;
        }
        if (!response.ok) throw new Error(`coord refresh ${response.status}`);
        const incoming = new DOMParser().parseFromString(
          await response.text(),
          "text/html",
        );
        selectors.forEach((selector) => replaceSurface(selector, incoming));
        bindDrawers();
        restoreAnchor(scroll);
        restoreDrafts(drafts);
        restoreFocus(focus);
        markHighestRenderedRead();
        setLive("live", "Live", "Unterhaltung aktualisiert");
      } catch (_) {
        setLive(
          "offline",
          "Live · Aktualisierung fehlgeschlagen",
          "Live-Aktualisierung fehlgeschlagen",
        );
      }
    }
    refreshing = false;
  };

  let lastReadSubmitted = "";
  const markHighestRenderedRead = () => {
    if (document.visibilityState !== "visible") return;
    const room = currentRoom();
    const messages = [
      ...document.querySelectorAll(
        ".coord-messages > .coord-message[id^='message-']",
      ),
    ];
    const last = messages[messages.length - 1];
    const csrf = document.querySelector("input[name='csrf_token']")?.value;
    if (!room || !last || !csrf) return;
    const sequence = last.id.slice("message-".length);
    const key = `${room}:${sequence}`;
    if (!/^\d+$/.test(sequence) || key === lastReadSubmitted) return;
    lastReadSubmitted = key;
    const body = new URLSearchParams({ csrf_token: csrf, room, sequence });
    fetch("/ui/coord/read", {
      method: "POST",
      body,
      credentials: "same-origin",
      redirect: "manual",
    }).catch(() => {
      lastReadSubmitted = "";
    });
  };

  const cursor = workspace.dataset.coordEventCursor || "0";
  let lastEventID = Number(cursor);
  const source = new EventSource(
    `/ui/coord/events?after=${encodeURIComponent(cursor)}`,
  );
  source.addEventListener("open", () => {
    setLive("live", "Live");
    markHighestRenderedRead();
  });
  source.addEventListener("coord.changed", (raw) => {
    try {
      const eventID = Number(raw.lastEventId);
      if (!Number.isSafeInteger(eventID) || eventID < 0) {
        location.reload();
        return;
      }
      if (eventID <= lastEventID) return;
      lastEventID = eventID;
      refreshVisible(JSON.parse(raw.data));
    } catch (_) {
      location.reload();
    }
  });
  source.addEventListener("resync", () => {
    setLive("resync", "Synchronisiere …");
    location.reload();
  });
  source.addEventListener("session-ended", () => {
    source.close();
    setLive("offline", "Sitzung beendet");
    location.assign("/ui/login");
  });
  source.onerror = () =>
    setLive(
      "offline",
      "Offline · Verbindung wird wiederholt",
      "Live-Verbindung unterbrochen",
    );
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") markHighestRenderedRead();
  });
})();
