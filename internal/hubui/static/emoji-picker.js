(function initMoltenEmojiPicker(global) {
  const RECENT_STORAGE_KEY = "hub.ui.emoji.recent";
  const RECENT_CATEGORY_ID = "recent";
  const DEFAULT_PREVIEW = "🙂";
  const MAX_RECENT = 18;
  const VIEWPORT_GUTTER = 8;
  const PANEL_OFFSET = 8;
  const PANEL_MAX_WIDTH = 360;
  const PANEL_ESTIMATED_HEIGHT = 430;

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

  const CATEGORIES = [
    {
      id: "recent",
      label: "Frequently used",
      icon: "🕘",
      emojis: [],
    },
    {
      id: "people",
      label: "Smileys & People",
      icon: "😀",
      emojis: [
        { emoji: "😀", name: "grinning face", keywords: ["happy", "smile"] },
        { emoji: "😃", name: "grinning face with big eyes", keywords: ["happy", "smile"] },
        { emoji: "😄", name: "grinning face with smiling eyes", keywords: ["happy", "smile"] },
        { emoji: "😁", name: "beaming face with smiling eyes", keywords: ["happy", "smile"] },
        { emoji: "😂", name: "face with tears of joy", keywords: ["laugh", "funny"] },
        { emoji: "🤣", name: "rolling on the floor laughing", keywords: ["laugh", "funny"] },
        { emoji: "😊", name: "smiling face with smiling eyes", keywords: ["warm", "smile"] },
        { emoji: "🙂", name: "slightly smiling face", keywords: ["smile"] },
        { emoji: "🙃", name: "upside-down face", keywords: ["playful"] },
        { emoji: "😉", name: "winking face", keywords: ["playful", "wink"] },
        { emoji: "😍", name: "smiling face with heart-eyes", keywords: ["love", "heart"] },
        { emoji: "🥳", name: "partying face", keywords: ["party", "celebration"] },
        { emoji: "😎", name: "smiling face with sunglasses", keywords: ["cool"] },
        { emoji: "🤩", name: "star-struck", keywords: ["excited", "stars"] },
        { emoji: "🤖", name: "robot", keywords: ["bot", "agent", "automation"] },
        { emoji: "🧠", name: "brain", keywords: ["smart", "thinking", "mind"] },
        { emoji: "👋", name: "waving hand", keywords: ["hello", "wave"] },
        { emoji: "👍", name: "thumbs up", keywords: ["approve", "yes"] },
        { emoji: "👏", name: "clapping hands", keywords: ["applause"] },
        { emoji: "🙏", name: "folded hands", keywords: ["thanks", "please"] },
        { emoji: "💡", name: "light bulb", keywords: ["idea", "insight"] },
      ],
    },
    {
      id: "nature",
      label: "Animals & Nature",
      icon: "🌿",
      emojis: [
        { emoji: "🔥", name: "fire", keywords: ["hot", "energy", "burn"] },
        { emoji: "⚡", name: "high voltage", keywords: ["energy", "fast", "power"] },
        { emoji: "🌤️", name: "sun behind small cloud", keywords: ["weather", "sun"] },
        { emoji: "🌊", name: "water wave", keywords: ["ocean", "flow"] },
        { emoji: "🌿", name: "herb", keywords: ["leaf", "green", "nature"] },
        { emoji: "🍀", name: "four leaf clover", keywords: ["luck", "nature"] },
        { emoji: "🌙", name: "crescent moon", keywords: ["night", "moon"] },
        { emoji: "☀️", name: "sun", keywords: ["bright", "day"] },
        { emoji: "🪴", name: "potted plant", keywords: ["plant", "growth"] },
        { emoji: "🌵", name: "cactus", keywords: ["plant", "desert"] },
        { emoji: "🌸", name: "cherry blossom", keywords: ["flower", "pink"] },
        { emoji: "🦋", name: "butterfly", keywords: ["nature", "insect"] },
        { emoji: "🐸", name: "frog", keywords: ["animal"] },
        { emoji: "🐙", name: "octopus", keywords: ["animal", "ocean"] },
        { emoji: "🦊", name: "fox", keywords: ["animal"] },
        { emoji: "🦍", name: "gorilla", keywords: ["gorilla", "strength"] },
      ],
    },
    {
      id: "food",
      label: "Food & Drink",
      icon: "☕",
      emojis: [
        { emoji: "☕", name: "hot beverage", keywords: ["coffee", "tea"] },
        { emoji: "🍎", name: "red apple", keywords: ["fruit"] },
        { emoji: "🍇", name: "grapes", keywords: ["fruit"] },
        { emoji: "🍕", name: "pizza", keywords: ["food"] },
        { emoji: "🍔", name: "hamburger", keywords: ["food"] },
        { emoji: "🌮", name: "taco", keywords: ["food"] },
        { emoji: "🍣", name: "sushi", keywords: ["food"] },
        { emoji: "🍜", name: "steaming bowl", keywords: ["ramen", "noodles"] },
        { emoji: "🍪", name: "cookie", keywords: ["snack"] },
        { emoji: "🍩", name: "doughnut", keywords: ["dessert"] },
        { emoji: "🍿", name: "popcorn", keywords: ["snack"] },
        { emoji: "🥐", name: "croissant", keywords: ["breakfast"] },
        { emoji: "🍺", name: "beer mug", keywords: ["drink"] },
        { emoji: "🥤", name: "cup with straw", keywords: ["drink"] },
      ],
    },
    {
      id: "activity",
      label: "Activity",
      icon: "🎯",
      emojis: [
        { emoji: "🎯", name: "direct hit", keywords: ["goal", "target"] },
        { emoji: "🎮", name: "video game", keywords: ["game", "controller"] },
        { emoji: "🕹️", name: "joystick", keywords: ["game"] },
        { emoji: "🎲", name: "game die", keywords: ["game", "dice"] },
        { emoji: "🎨", name: "artist palette", keywords: ["art", "creative"] },
        { emoji: "🎧", name: "headphone", keywords: ["music", "audio"] },
        { emoji: "🎸", name: "guitar", keywords: ["music"] },
        { emoji: "🏁", name: "chequered flag", keywords: ["finish", "race"] },
        { emoji: "🚀", name: "rocket", keywords: ["launch", "ship", "fast"] },
        { emoji: "🛠️", name: "hammer and wrench", keywords: ["build", "tool", "fix"] },
        { emoji: "🏆", name: "trophy", keywords: ["win", "award"] },
        { emoji: "🏅", name: "sports medal", keywords: ["award", "win"] },
        { emoji: "⚽", name: "soccer ball", keywords: ["sport"] },
        { emoji: "🏀", name: "basketball", keywords: ["sport"] },
      ],
    },
    {
      id: "travel",
      label: "Travel & Places",
      icon: "🚗",
      emojis: [
        { emoji: "🚗", name: "automobile", keywords: ["car", "travel"] },
        { emoji: "🚕", name: "taxi", keywords: ["car", "travel"] },
        { emoji: "🚌", name: "bus", keywords: ["travel", "vehicle"] },
        { emoji: "🚲", name: "bicycle", keywords: ["travel", "bike"] },
        { emoji: "✈️", name: "airplane", keywords: ["flight", "travel"] },
        { emoji: "🚢", name: "ship", keywords: ["travel", "boat"] },
        { emoji: "🏠", name: "house", keywords: ["home"] },
        { emoji: "🏢", name: "office building", keywords: ["work", "building"] },
        { emoji: "🌉", name: "bridge at night", keywords: ["city", "bridge"] },
        { emoji: "🗽", name: "statue of liberty", keywords: ["landmark"] },
      ],
    },
    {
      id: "objects",
      label: "Objects",
      icon: "🔧",
      emojis: [
        { emoji: "🔧", name: "wrench", keywords: ["tool", "fix"] },
        { emoji: "⚙️", name: "gear", keywords: ["settings", "system"] },
        { emoji: "🧰", name: "toolbox", keywords: ["tools", "build"] },
        { emoji: "📦", name: "package", keywords: ["ship", "box"] },
        { emoji: "📌", name: "pushpin", keywords: ["pin"] },
        { emoji: "📍", name: "round pushpin", keywords: ["pin", "location"] },
        { emoji: "📎", name: "paperclip", keywords: ["attach"] },
        { emoji: "📝", name: "memo", keywords: ["note", "write"] },
        { emoji: "📚", name: "books", keywords: ["library", "read"] },
        { emoji: "🛰️", name: "satellite", keywords: ["space", "signal"] },
        { emoji: "💻", name: "laptop", keywords: ["computer", "code"] },
        { emoji: "⌨️", name: "keyboard", keywords: ["computer", "type"] },
        { emoji: "🖥️", name: "desktop computer", keywords: ["computer"] },
        { emoji: "📡", name: "satellite antenna", keywords: ["signal", "network"] },
        { emoji: "🔒", name: "lock", keywords: ["secure", "security"] },
      ],
    },
    {
      id: "symbols",
      label: "Symbols",
      icon: "✨",
      emojis: [
        { emoji: "✨", name: "sparkles", keywords: ["shine", "magic"] },
        { emoji: "❤️", name: "red heart", keywords: ["love", "heart"] },
        { emoji: "💯", name: "hundred points", keywords: ["score", "perfect"] },
        { emoji: "✅", name: "check mark button", keywords: ["done", "complete"] },
        { emoji: "❌", name: "cross mark", keywords: ["error", "stop"] },
        { emoji: "❓", name: "question mark", keywords: ["question", "help"] },
        { emoji: "❗", name: "exclamation mark", keywords: ["attention"] },
        { emoji: "➕", name: "plus", keywords: ["add"] },
        { emoji: "➖", name: "minus", keywords: ["subtract"] },
        { emoji: "♻️", name: "recycling symbol", keywords: ["recycle"] },
        { emoji: "📣", name: "megaphone", keywords: ["announce", "alert"] },
        { emoji: "🌀", name: "cyclone", keywords: ["spin", "flow"] },
      ],
    },
    {
      id: "flags",
      label: "Flags",
      icon: "🏳️",
      emojis: [
        { emoji: "🏁", name: "chequered flag", keywords: ["flag", "race"] },
        { emoji: "🚩", name: "triangular flag", keywords: ["flag"] },
        { emoji: "🏳️", name: "white flag", keywords: ["flag"] },
        { emoji: "🏴", name: "black flag", keywords: ["flag"] },
        { emoji: "🏳️‍🌈", name: "rainbow flag", keywords: ["flag", "pride"] },
        { emoji: "🏳️‍⚧️", name: "transgender flag", keywords: ["flag", "pride"] },
      ],
    },
  ];

  function categoryLabel(categoryID) {
    if (categoryID === RECENT_CATEGORY_ID) {
      return "Frequently used";
    }
    const category = CATEGORIES.find((item) => item.id === categoryID);
    return category ? category.label : "Emoji";
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
      global.localStorage.setItem(RECENT_STORAGE_KEY, JSON.stringify(emojis.slice(0, MAX_RECENT)));
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

  function normalizedText(value) {
    return String(value || "").trim().toLowerCase();
  }

  function dispatchInputEvents(input) {
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function matchesSearch(entry, query) {
    if (!query) {
      return true;
    }
    const haystack = [entry.name].concat(entry.keywords || []).join(" ").toLowerCase();
    return haystack.includes(query);
  }

  function categoryEntries(categoryID) {
    if (categoryID === RECENT_CATEGORY_ID) {
      const recent = safeReadRecent();
      const lookup = new Map();
      CATEGORIES.forEach((category) => {
        (category.emojis || []).forEach((entry) => {
          lookup.set(entry.emoji, entry);
        });
      });
      return recent.map((emoji) => lookup.get(emoji)).filter(Boolean);
    }
    const category = CATEGORIES.find((item) => item.id === categoryID);
    return category ? category.emojis.slice() : [];
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

    let activeCategory = RECENT_CATEGORY_ID;
    let searchValue = "";
    let open = false;

    panel.innerHTML = [
      '<div class="hub-emoji-picker-panel-shell">',
      '  <div class="hub-emoji-picker-panel-header">',
      '    <p class="hub-emoji-picker-panel-title">Pick one emoji</p>',
      '    <button class="hub-emoji-picker-clear" type="button">Clear</button>',
      "  </div>",
      '  <div class="hub-emoji-picker-toolbar">',
      '    <div class="hub-emoji-picker-categories" role="tablist" aria-label="Emoji categories"></div>',
      "  </div>",
      '  <label class="hub-emoji-picker-search-wrap">',
      '    <span class="hub-emoji-picker-search-icon" aria-hidden="true">⌕</span>',
      '    <span class="sr-only">Search emoji</span>',
      '    <input class="hub-emoji-picker-search" type="text" autocomplete="off" spellcheck="false" placeholder="Search emoji">',
      "  </label>",
      '  <div class="hub-emoji-picker-results"></div>',
      "</div>",
    ].join("");

    const categoriesNode = panel.querySelector(".hub-emoji-picker-categories");
    const clearButton = panel.querySelector(".hub-emoji-picker-clear");
    const searchInput = panel.querySelector(".hub-emoji-picker-search");
    const resultsNode = panel.querySelector(".hub-emoji-picker-results");
    const requestFrame = typeof global.requestAnimationFrame === "function"
      ? global.requestAnimationFrame.bind(global)
      : (cb) => global.setTimeout(cb, 0);

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
        renderCategories();
        renderResults();
        updatePanelPosition();
        if (searchInput) {
          searchInput.value = searchValue;
          requestFrame(() => {
            searchInput.focus();
            searchInput.select();
          });
        }
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
      if (open) {
        renderResults();
        updatePanelPosition();
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

    function visibleCategories() {
      const hasRecent = categoryEntries(RECENT_CATEGORY_ID).length > 0;
      return CATEGORIES.filter((category) => category.id !== RECENT_CATEGORY_ID || hasRecent);
    }

    function renderCategories() {
      if (!categoriesNode) {
        return;
      }
      const categories = visibleCategories();
      if (!categories.length) {
        categoriesNode.innerHTML = "";
        return;
      }
      if (!categories.some((category) => category.id === activeCategory)) {
        activeCategory = categories[0].id;
      }

      categoriesNode.innerHTML = categories.map((category) => {
        const selected = category.id === activeCategory;
        return `<button class="hub-emoji-picker-category${selected ? " active" : ""}" type="button" data-category="${category.id}" role="tab" aria-selected="${selected ? "true" : "false"}" title="${category.label}">${category.icon}</button>`;
      }).join("");
    }

    function renderResults() {
      if (!resultsNode) {
        return;
      }
      const query = normalizedText(searchValue);
      const groups = [];

      if (query) {
        CATEGORIES.forEach((category) => {
          const matches = categoryEntries(category.id).filter((entry) => matchesSearch(entry, query));
          if (matches.length > 0) {
            groups.push({ label: categoryLabel(category.id), entries: matches });
          }
        });
      } else {
        const recentEntries = categoryEntries(RECENT_CATEGORY_ID);
        if (activeCategory !== RECENT_CATEGORY_ID && recentEntries.length > 0) {
          groups.push({ label: categoryLabel(RECENT_CATEGORY_ID), entries: recentEntries });
        }
        const activeEntries = categoryEntries(activeCategory);
        if (activeEntries.length > 0) {
          groups.push({ label: categoryLabel(activeCategory), entries: activeEntries });
        }
      }

      if (groups.length === 0) {
        resultsNode.innerHTML = '<div class="hub-emoji-picker-empty">No emoji matched that search.</div>';
        return;
      }

      const selectedEmoji = normalizeEmojiValue(input.value);
      resultsNode.innerHTML = groups.map((group) => {
        const buttons = group.entries.map((entry) => {
          const selected = selectedEmoji === entry.emoji;
          return `<button class="hub-emoji-picker-option${selected ? " active" : ""}" type="button" data-emoji="${entry.emoji}" title="${entry.name}" aria-label="${entry.name}">${entry.emoji}</button>`;
        }).join("");
        return `<section class="hub-emoji-picker-group"><div class="hub-emoji-picker-group-label">${group.label}</div><div class="hub-emoji-picker-grid">${buttons}</div></section>`;
      }).join("");
    }

    renderCategories();
    sync();

    if (categoriesNode) {
      categoriesNode.addEventListener("click", (event) => {
        const button = event.target.closest("[data-category]");
        if (!button) {
          return;
        }
        activeCategory = button.getAttribute("data-category") || "people";
        searchValue = "";
        if (searchInput) {
          searchInput.value = "";
        }
        renderCategories();
        renderResults();
      });
    }

    if (clearButton) {
      clearButton.addEventListener("click", () => {
        setValue("");
        setOpen(false);
        toggle.focus();
      });
    }

    if (searchInput) {
      searchInput.addEventListener("input", () => {
        searchValue = searchInput.value;
        renderResults();
      });
    }

    if (resultsNode) {
      resultsNode.addEventListener("click", (event) => {
        const button = event.target.closest("[data-emoji]");
        if (!button) {
          return;
        }
        const emoji = normalizeEmojiValue(button.getAttribute("data-emoji") || "");
        if (!emoji) {
          return;
        }
        setValue(emoji);
        renderCategories();
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
