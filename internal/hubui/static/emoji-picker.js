(function initMoltenEmojiPicker(global) {
  const RECENT_STORAGE_KEY = "hub.ui.emoji.recent";
  const RECENT_CATEGORY_ID = "recent";
  const DEFAULT_PREVIEW = "😀";
  const MAX_RECENT = 18;

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
      label: "Recent",
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
    const input = root.querySelector("input");
    const toggle = root.querySelector(".hub-emoji-picker-toggle");
    const panel = root.querySelector(".hub-emoji-picker-panel");
    if (!input || !toggle || !panel) {
      return null;
    }

    let activeCategory = RECENT_CATEGORY_ID;
    let searchValue = "";
    let open = false;

    panel.innerHTML = [
      '<div class="hub-emoji-picker-selected">',
      '  <div class="hub-emoji-picker-selected-chip" aria-live="polite">',
      `    <span class="hub-emoji-picker-selected-emoji" aria-hidden="true">${DEFAULT_PREVIEW}</span>`,
      '    <span class="hub-emoji-picker-selected-text">Selected: none</span>',
      "  </div>",
      '  <button class="hub-emoji-picker-clear" type="button">Clear</button>',
      "</div>",
      '<div class="hub-emoji-picker-toolbar">',
      '  <div class="hub-emoji-picker-categories" role="tablist" aria-label="Emoji categories"></div>',
      "</div>",
      '<label class="hub-emoji-picker-search-wrap">',
      '  <span class="hub-emoji-picker-search-icon" aria-hidden="true">⌕</span>',
      '  <span class="sr-only">Search emoji</span>',
      '  <input class="hub-emoji-picker-search" type="text" autocomplete="off" spellcheck="false" placeholder="Search emoji">',
      "</label>",
      '<div class="hub-emoji-picker-results"></div>',
    ].join("");

    const categoriesNode = panel.querySelector(".hub-emoji-picker-categories");
    const clearButton = panel.querySelector(".hub-emoji-picker-clear");
    const searchInput = panel.querySelector(".hub-emoji-picker-search");
    const resultsNode = panel.querySelector(".hub-emoji-picker-results");
    const selectedEmojiNode = panel.querySelector(".hub-emoji-picker-selected-emoji");
    const selectedTextNode = panel.querySelector(".hub-emoji-picker-selected-text");

    function setOpen(nextOpen) {
      open = Boolean(nextOpen) && !toggle.disabled;
      panel.hidden = !open;
      panel.classList.toggle("hidden", !open);
      root.classList.toggle("hub-emoji-picker-open", open);
      toggle.setAttribute("aria-expanded", open ? "true" : "false");
      if (open) {
        if (searchInput) {
          searchInput.value = searchValue;
          global.requestAnimationFrame(() => {
            searchInput.focus();
            searchInput.select();
          });
        }
        renderResults();
      }
    }

    function setValue(nextValue) {
      const normalized = normalizeEmojiValue(nextValue);
      input.value = normalized;
      sync();
      if (normalized) {
        rememberEmoji(normalized);
      }
      dispatchInputEvents(input);
    }

    function sync() {
      const current = normalizeEmojiValue(input.value);
      if (input.value !== current) {
        input.value = current;
      }
      const preview = current || DEFAULT_PREVIEW;
      const previewNode = root.querySelector(".hub-emoji-picker-toggle-preview");
      if (previewNode) {
        previewNode.textContent = preview;
      }
      if (selectedEmojiNode) {
        selectedEmojiNode.textContent = preview;
      }
      if (selectedTextNode) {
        selectedTextNode.textContent = current ? `Selected: ${current}` : "Selected: none";
      }
      if (clearButton) {
        clearButton.disabled = !current;
      }
      toggle.title = current ? `Selected emoji: ${current}` : "Choose emoji";
      if (open) {
        renderResults();
      }
    }

    function setDisabled(disabled) {
      const nextDisabled = Boolean(disabled);
      toggle.disabled = nextDisabled;
      root.classList.toggle("hub-emoji-picker-disabled", nextDisabled);
      if (nextDisabled) {
        setOpen(false);
      }
    }

    function renderCategories() {
      if (!categoriesNode) {
        return;
      }
      const markup = CATEGORIES.filter((category) => category.id !== RECENT_CATEGORY_ID || safeReadRecent().length > 0)
        .map((category) => {
          const selected = category.id === activeCategory;
          return `<button class="hub-emoji-picker-category${selected ? " active" : ""}" type="button" data-category="${category.id}" role="tab" aria-selected="${selected ? "true" : "false"}" title="${category.label}">${category.icon}</button>`;
        })
        .join("");
      categoriesNode.innerHTML = markup;
      if (!categoriesNode.children.length) {
        activeCategory = "people";
        renderCategories();
      }
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

      resultsNode.innerHTML = groups.map((group) => {
        const buttons = group.entries.map((entry) => {
          const selected = normalizeEmojiValue(input.value) === entry.emoji;
          return `<button class="hub-emoji-picker-option${selected ? " active" : ""}" type="button" data-emoji="${entry.emoji}" title="${entry.name}" aria-label="${entry.name}">${entry.emoji}</button>`;
        }).join("");
        return `<section class="hub-emoji-picker-group"><div class="hub-emoji-picker-group-label">${group.label}</div><div class="hub-emoji-picker-grid">${buttons}</div></section>`;
      }).join("");
    }

    renderCategories();
    sync();

    categoriesNode.addEventListener("click", (event) => {
      const button = event.target.closest("[data-category]");
      if (!button) {
        return;
      }
      activeCategory = button.getAttribute("data-category") || "people";
      searchValue = "";
      renderCategories();
      renderResults();
    });

    clearButton.addEventListener("click", () => {
      setValue("");
      setOpen(false);
      input.focus();
    });

    searchInput.addEventListener("input", () => {
      searchValue = searchInput.value;
      renderResults();
    });

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
      input.focus();
    });

    toggle.addEventListener("mousedown", (event) => {
      event.preventDefault();
    });

    toggle.addEventListener("click", () => {
      setOpen(!open);
    });

    input.addEventListener("input", sync);

    root.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && open) {
        event.preventDefault();
        setOpen(false);
        toggle.focus();
      }
      if ((event.key === "ArrowDown" || event.key === "Enter") && !open && event.target === input) {
        event.preventDefault();
        setOpen(true);
      }
    });

    document.addEventListener("click", (event) => {
      if (!root.contains(event.target)) {
        setOpen(false);
      }
    });

    return {
      sync,
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
    attach: attach,
    limitGraphemes: limitGraphemes,
  };
})(window);
