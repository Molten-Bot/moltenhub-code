(function initMoltenEmojiPicker(global) {
  const RECENT_STORAGE_KEY = "hub.ui.emoji.recent";
  const EMOJI_MART_SCRIPT_URL = "/static/emoji-mart-browser.js";
  const EMOJI_MART_DATA_URL = "/static/emoji-mart-data.json";
  const DEFAULT_PREVIEW = "🙂";
  const VIEWPORT_GUTTER = 8;
  const PANEL_OFFSET = 8;
  const PANEL_MAX_WIDTH = 360;
  const PANEL_ESTIMATED_HEIGHT = 430;

  let emojiMartScriptPromise = null;
  let emojiMartDataPromise = null;

  function splitGraphemes(value) {
    const text = String(value || "");
    if (!text) {
      return [];
    }
    if (typeof Intl === "object" && Intl && typeof Intl.Segmenter === "function") {
      try {
        const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
        const out = [];
        for (const part of segmenter.segment(text)) {
          if (part && typeof part.segment === "string" && part.segment !== "") {
            out.push(part.segment);
          }
        }
        if (out.length > 0) {
          return out;
        }
      } catch (_err) {
        // Fall back to code point splitting when Segmenter is unavailable.
      }
    }
    return Array.from(text);
  }

  function limitGraphemes(value, maxGraphemes) {
    const max = Number.isFinite(maxGraphemes) && maxGraphemes > 0 ? Math.floor(maxGraphemes) : 0;
    if (max <= 0) {
      return "";
    }
    const graphemes = splitGraphemes(value);
    if (graphemes.length <= max) {
      return graphemes.join("");
    }
    return graphemes.slice(0, max).join("");
  }

  function normalizeEmojiValue(value) {
    return limitGraphemes(String(value || "").trim(), 1);
  }

  function dispatchInputEvents(input) {
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function safeReadRecent() {
    try {
      const raw = global.localStorage.getItem(RECENT_STORAGE_KEY);
      const parsed = JSON.parse(raw || "[]");
      if (!Array.isArray(parsed)) {
        return [];
      }
      return parsed
        .map((value) => normalizeEmojiValue(value))
        .filter((value) => value !== "");
    } catch (_err) {
      return [];
    }
  }

  function writeRecent(emojis) {
    try {
      global.localStorage.setItem(RECENT_STORAGE_KEY, JSON.stringify(emojis.slice(0, 18)));
    } catch (_err) {
      // Ignore persistence failures.
    }
  }

  function rememberEmoji(emoji) {
    const normalized = normalizeEmojiValue(emoji);
    if (!normalized) {
      return;
    }
    const next = [normalized].concat(safeReadRecent().filter((value) => value !== normalized));
    writeRecent(next);
  }

  function requestFrame(callback) {
    if (typeof global.requestAnimationFrame === "function") {
      return global.requestAnimationFrame(callback);
    }
    return global.setTimeout(callback, 0);
  }

  function loadEmojiMartScript() {
    if (global.EmojiMart && typeof global.EmojiMart.Picker === "function") {
      return Promise.resolve(global.EmojiMart);
    }
    if (emojiMartScriptPromise) {
      return emojiMartScriptPromise;
    }

    emojiMartScriptPromise = new Promise((resolve, reject) => {
      const existing = global.document.querySelector('script[data-emoji-mart-script="true"]');
      if (existing) {
        existing.addEventListener("load", () => resolve(global.EmojiMart), { once: true });
        existing.addEventListener("error", () => reject(new Error("emoji-mart script failed to load")), { once: true });
        return;
      }

      const script = global.document.createElement("script");
      script.src = EMOJI_MART_SCRIPT_URL;
      script.async = true;
      script.setAttribute("data-emoji-mart-script", "true");
      script.addEventListener("load", () => resolve(global.EmojiMart), { once: true });
      script.addEventListener("error", () => reject(new Error("emoji-mart script failed to load")), { once: true });
      global.document.head.appendChild(script);
    });

    return emojiMartScriptPromise;
  }

  function loadEmojiMartData() {
    if (emojiMartDataPromise) {
      return emojiMartDataPromise;
    }

    emojiMartDataPromise = global.fetch(EMOJI_MART_DATA_URL)
      .then((response) => {
        if (!response.ok) {
          throw new Error(`emoji-mart data request failed: ${response.status}`);
        }
        return response.json();
      });

    return emojiMartDataPromise;
  }

  function pickerTheme() {
    const root = global.document && global.document.documentElement ? global.document.documentElement : null;
    const explicitScheme = root ? String(root.style.colorScheme || "").trim().toLowerCase() : "";
    if (explicitScheme === "dark") {
      return "dark";
    }
    if (typeof global.matchMedia === "function" && global.matchMedia("(prefers-color-scheme: dark)").matches) {
      return "dark";
    }
    return "light";
  }

  function attach(root) {
    if (!root) {
      return null;
    }

    const input = root.querySelector(".hub-emoji-picker-input") || root.querySelector("input");
    const toggle = root.querySelector(".hub-emoji-picker-toggle");
    const panel = root.querySelector(".hub-emoji-picker-panel");
    const togglePreviewNode = root.querySelector(".hub-emoji-picker-toggle-preview");
    const toggleTextNode = root.querySelector(".hub-emoji-picker-toggle-text");
    if (!input || !toggle || !panel) {
      return null;
    }

    panel.innerHTML = [
      '<div class="hub-emoji-picker-panel-shell">',
      '  <div class="hub-emoji-picker-panel-header">',
      '    <p class="hub-emoji-picker-panel-title">Pick one emoji</p>',
      '    <button class="hub-emoji-picker-clear" type="button">Clear</button>',
      "  </div>",
      '  <div class="hub-emoji-picker-body">',
      '    <div class="hub-emoji-picker-state">Loading emoji picker...</div>',
      "  </div>",
      "</div>",
    ].join("");

    const clearButton = panel.querySelector(".hub-emoji-picker-clear");
    const bodyNode = panel.querySelector(".hub-emoji-picker-body");

    let open = false;
    let pickerNode = null;
    let pickerReady = false;
    let pickerLoadPromise = null;

    function viewportSize() {
      const doc = global.document && global.document.documentElement ? global.document.documentElement : null;
      const width = Number(global.innerWidth) || Number(doc && doc.clientWidth) || 0;
      const height = Number(global.innerHeight) || Number(doc && doc.clientHeight) || 0;
      return { width, height };
    }

    function panelWidthForViewport(width) {
      const maxPanelWidth = Math.max(240, width - VIEWPORT_GUTTER * 2);
      return Math.min(PANEL_MAX_WIDTH, maxPanelWidth);
    }

    function updatePanelPosition() {
      if (!open) {
        return;
      }
      const { width: viewportWidth, height: viewportHeight } = viewportSize();
      if (viewportWidth <= 0 || viewportHeight <= 0) {
        return;
      }

      const rect = root.getBoundingClientRect();
      const panelWidth = panelWidthForViewport(viewportWidth);
      const maxLeft = Math.max(VIEWPORT_GUTTER, viewportWidth - panelWidth - VIEWPORT_GUTTER);
      const left = Math.min(Math.max(rect.left, VIEWPORT_GUTTER), maxLeft);
      const spaceBelow = viewportHeight - rect.bottom - VIEWPORT_GUTTER;
      const spaceAbove = rect.top - VIEWPORT_GUTTER;
      const placeBelow = spaceBelow >= PANEL_ESTIMATED_HEIGHT || spaceBelow >= spaceAbove;

      panel.style.left = `${Math.round(left)}px`;
      panel.style.width = `min(${PANEL_MAX_WIDTH}px, calc(100vw - ${VIEWPORT_GUTTER * 2}px))`;
      panel.style.maxHeight = `calc(100vh - ${VIEWPORT_GUTTER * 2}px)`;
      if (placeBelow) {
        panel.style.top = `${Math.round(rect.bottom + PANEL_OFFSET)}px`;
        panel.style.bottom = "auto";
        panel.setAttribute("data-placement", "bottom");
      } else {
        panel.style.top = "auto";
        panel.style.bottom = `${Math.round(viewportHeight - rect.top + PANEL_OFFSET)}px`;
        panel.setAttribute("data-placement", "top");
      }
    }

    function attachPositionWatchers() {
      global.addEventListener("resize", updatePanelPosition);
      global.addEventListener("scroll", updatePanelPosition, true);
    }

    function detachPositionWatchers() {
      global.removeEventListener("resize", updatePanelPosition);
      global.removeEventListener("scroll", updatePanelPosition, true);
    }

    function handleOutsidePointer(event) {
      if (!open) {
        return;
      }
      const target = event.target;
      if (!target) {
        return;
      }
      if (root.contains(target) || panel.contains(target)) {
        return;
      }
      setOpen(false);
    }

    function handleEscape(event) {
      if (!open || event.key !== "Escape") {
        return;
      }
      event.preventDefault();
      setOpen(false);
      toggle.focus();
    }

    function attachOutsideWatchers() {
      global.addEventListener("mousedown", handleOutsidePointer);
      global.addEventListener("touchstart", handleOutsidePointer);
      global.addEventListener("keydown", handleEscape);
    }

    function detachOutsideWatchers() {
      global.removeEventListener("mousedown", handleOutsidePointer);
      global.removeEventListener("touchstart", handleOutsidePointer);
      global.removeEventListener("keydown", handleEscape);
    }

    function renderPickerState(message, stateClass) {
      if (!bodyNode) {
        return;
      }
      bodyNode.innerHTML = `<div class="hub-emoji-picker-state${stateClass ? ` ${stateClass}` : ""}">${message}</div>`;
    }

    function setOpen(nextOpen) {
      const next = Boolean(nextOpen) && !toggle.disabled;
      if (open === next) {
        if (open) {
          updatePanelPosition();
        }
        return;
      }

      open = next;
      panel.hidden = !open;
      panel.classList.toggle("hidden", !open);
      root.classList.toggle("hub-emoji-picker-open", open);
      toggle.setAttribute("aria-expanded", open ? "true" : "false");

      if (open) {
        attachOutsideWatchers();
        attachPositionWatchers();
        updatePanelPosition();
        void ensurePickerLoaded();
      } else {
        detachOutsideWatchers();
        detachPositionWatchers();
        panel.style.top = "";
        panel.style.bottom = "";
        panel.style.left = "";
        panel.style.width = "";
        panel.style.maxHeight = "";
        panel.removeAttribute("data-placement");
      }
    }

    function sync() {
      const current = normalizeEmojiValue(input.value);
      if (input.value !== current) {
        input.value = current;
      }

      const preview = current || DEFAULT_PREVIEW;
      if (togglePreviewNode) {
        togglePreviewNode.textContent = preview;
      }
      if (toggleTextNode) {
        const emptyLabel = String(toggleTextNode.getAttribute("data-empty-label") || "Choose an emoji").trim() || "Choose an emoji";
        toggleTextNode.textContent = current ? `Selected: ${current}` : emptyLabel;
        toggleTextNode.classList.toggle("is-empty", !current);
      }

      const label = current ? `Selected emoji: ${current}` : "Choose an emoji";
      toggle.title = label;
      toggle.setAttribute("aria-label", label);

      if (clearButton) {
        clearButton.disabled = !current;
      }
    }

    function setValue(nextValue) {
      const normalized = normalizeEmojiValue(nextValue);
      input.value = normalized;
      if (normalized) {
        rememberEmoji(normalized);
      }
      sync();
      dispatchInputEvents(input);
    }

    function setDisabled(disabled) {
      const nextDisabled = Boolean(disabled);
      toggle.disabled = nextDisabled;
      root.classList.toggle("hub-emoji-picker-disabled", nextDisabled);
      if (nextDisabled) {
        setOpen(false);
      }
    }

    function mountPickerNode(nextPickerNode) {
      if (!bodyNode) {
        return;
      }
      bodyNode.innerHTML = "";
      bodyNode.appendChild(nextPickerNode);
    }

    function ensurePickerLoaded() {
      if (pickerReady) {
        requestFrame(updatePanelPosition);
        return Promise.resolve();
      }
      if (pickerLoadPromise) {
        return pickerLoadPromise;
      }

      renderPickerState("Loading emoji picker...");
      pickerLoadPromise = Promise.all([loadEmojiMartScript(), loadEmojiMartData()])
        .then((loaded) => {
          const emojiMart = loaded[0];
          const data = loaded[1];
          if (!emojiMart || typeof emojiMart.Picker !== "function") {
            throw new Error("EmojiMart.Picker unavailable");
          }

          pickerNode = new emojiMart.Picker({
            data,
            autoFocus: true,
            i18n: {
              categories: {
                activity: "Activity",
                flags: "Flags",
                foods: "Food & Drink",
                frequent: "Frequently used",
                nature: "Animals & Nature",
                objects: "Objects",
                people: "Smileys & People",
                places: "Travel & Places",
                search: "Search Results",
                symbols: "Symbols",
              },
              search: "Search emoji",
            },
            maxFrequentRows: 2,
            onClickOutside: () => {
              if (!open) {
                return;
              }
              setOpen(false);
              toggle.focus();
            },
            onEmojiSelect: (emoji) => {
              if (!emoji || !emoji.native) {
                return;
              }
              setValue(emoji.native);
              setOpen(false);
              toggle.focus();
            },
            previewPosition: "none",
            searchPosition: "sticky",
            skinTonePosition: "none",
            theme: pickerTheme(),
          });
          pickerNode.classList.add("hub-emoji-mart");
          mountPickerNode(pickerNode);
          pickerReady = true;
          sync();
          requestFrame(() => {
            updatePanelPosition();
            const searchInput = panel.querySelector('input[type="search"]');
            if (open && searchInput && typeof searchInput.focus === "function") {
              searchInput.focus();
            }
          });
        })
        .catch((_err) => {
          renderPickerState("Emoji picker unavailable.", "is-error");
        })
        .finally(() => {
          pickerLoadPromise = null;
        });

      return pickerLoadPromise;
    }

    sync();

    if (clearButton) {
      clearButton.addEventListener("click", () => {
        setValue("");
        setOpen(false);
        toggle.focus();
      });
    }

    toggle.addEventListener("click", () => {
      if (toggle.disabled) {
        return;
      }
      setOpen(!open);
    });

    toggle.addEventListener("keydown", (event) => {
      if (open || toggle.disabled) {
        return;
      }
      if (event.key === "Enter" || event.key === " " || event.key === "ArrowDown") {
        event.preventDefault();
        setOpen(true);
      }
    });

    input.addEventListener("input", sync);

    return {
      sync,
      setValue,
      setDisabled,
      open: function openPicker() {
        setOpen(true);
      },
      close: function closePicker() {
        setOpen(false);
      },
    };
  }

  global.MoltenEmojiPicker = {
    attach,
    limitGraphemes,
  };
})(window);
