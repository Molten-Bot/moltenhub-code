(function () {
  "use strict";

  const THEME_KEY = "hubui.theme";
  const THEME_MODES = ["light", "dark", "night", "pink"];
  const DEFAULT_THEME_MODE = "light";
  const HOME_PATH = "/";
  const HUB_PROFILE_DEEP_LINK_HASH = "#agent-profile";
  const HUB_LOGIN_URL = "https://molten.bot/login?target=hub";
  const HUB_DASHBOARD_URL = "https://app.molten.bot/hub";
  const THEME_ICON_NAMES = {
    light: "sun",
    dark: "moon",
    night: "star",
    pink: "heart",
  };

  function isHomePage() {
    return window.location.pathname === HOME_PATH;
  }

  function normalizePath(path) {
    const value = String(path || HOME_PATH).trim() || HOME_PATH;
    if (value.length > 1) {
      return value.replace(/\/+$/, "");
    }
    return value;
  }

  function replaceLucideIcons(root) {
    const api = window.lucide;
    if (!api || typeof api.createIcons !== "function") {
      return;
    }
    try {
      if (root && typeof root.querySelector === "function") {
        api.createIcons({ root });
        return;
      }
      api.createIcons();
    } catch (_err) {
      // Keep navigation usable if icon replacement fails.
    }
  }

  function trackDockEvent(name, params) {
    const eventName = String(name || "").trim();
    if (!eventName || typeof window.gtag !== "function") {
      return;
    }
    const payload = { send_to: "G-BY33RFG2WB" };
    for (const [key, value] of Object.entries(params || {})) {
      if (typeof value === "string" && value.trim()) {
        payload[key] = value.trim();
        continue;
      }
      if (typeof value === "number" && Number.isFinite(value)) {
        payload[key] = value;
        continue;
      }
      if (typeof value === "boolean") {
        payload[key] = value;
      }
    }
    try {
      window.gtag("event", eventName, payload);
    } catch (_err) {
      // Analytics must not block dock navigation.
    }
  }

  function themeLabel(theme) {
    const normalized = THEME_MODES.includes(theme) ? theme : DEFAULT_THEME_MODE;
    return normalized.charAt(0).toUpperCase() + normalized.slice(1);
  }

  function loadThemeMode() {
    try {
      const raw = localStorage.getItem(THEME_KEY);
      return THEME_MODES.includes(raw) ? raw : DEFAULT_THEME_MODE;
    } catch (_err) {
      return DEFAULT_THEME_MODE;
    }
  }

  function currentThemeMode() {
    const activeTheme = document.documentElement.getAttribute("data-theme");
    return THEME_MODES.includes(activeTheme) ? activeTheme : loadThemeMode();
  }

  function nextThemeMode(theme) {
    const currentIndex = THEME_MODES.indexOf(theme);
    const safeIndex = currentIndex >= 0 ? currentIndex : Math.max(0, THEME_MODES.indexOf(DEFAULT_THEME_MODE));
    return THEME_MODES[(safeIndex + 1) % THEME_MODES.length];
  }

  function syncThemeToggle(theme) {
    const themeToggleButton = document.getElementById("theme-toggle");
    const themeToggleLabel = document.getElementById("theme-toggle-label");
    const themeToggleIcon = document.getElementById("theme-toggle-icon");
    const themeToggleTooltip = document.getElementById("theme-toggle-tooltip");
    if (!themeToggleButton || !themeToggleLabel || !themeToggleIcon) {
      return;
    }
    const currentTheme = THEME_MODES.includes(theme) ? theme : DEFAULT_THEME_MODE;
    const currentLabel = themeLabel(currentTheme);
    themeToggleLabel.textContent = currentLabel;
    const iconName = THEME_ICON_NAMES[currentTheme] || THEME_ICON_NAMES[DEFAULT_THEME_MODE];
    themeToggleIcon.innerHTML = `<i data-lucide="${iconName}" class="theme-toggle-icon-glyph" aria-hidden="true"></i>`;
    replaceLucideIcons(themeToggleIcon);
    if (themeToggleTooltip) {
      themeToggleTooltip.textContent = currentLabel;
    }
    themeToggleButton.title = currentLabel;
    themeToggleButton.setAttribute("aria-label", `Switch theme. Currently: ${currentLabel}`);
  }

  function applyThemeMode(theme, persist) {
    const normalized = THEME_MODES.includes(theme) ? theme : DEFAULT_THEME_MODE;
    const root = document.documentElement;
    root.classList.remove("light", "dark", "night", "pink");
    root.classList.add(normalized);
    root.setAttribute("data-theme", normalized);
    syncThemeToggle(normalized);
    if (persist) {
      try {
        localStorage.setItem(THEME_KEY, normalized);
      } catch (_err) {
        // Ignore localStorage failures.
      }
    }
  }

  function initThemeToggle(root) {
    if (isHomePage()) {
      return;
    }
    applyThemeMode(loadThemeMode(), false);
    const themeToggleButton = root.querySelector("#theme-toggle");
    if (!themeToggleButton || themeToggleButton.dataset.bottomDockBound === "true") {
      return;
    }
    themeToggleButton.dataset.bottomDockBound = "true";
    themeToggleButton.addEventListener("click", () => {
      const nextTheme = nextThemeMode(currentThemeMode());
      applyThemeMode(nextTheme, true);
      trackDockEvent("theme_changed", { theme: nextTheme });
    });
  }

  function routeStudioLinksToHome(root) {
    if (isHomePage()) {
      return;
    }
    const links = [
      root.querySelector("#prompt-mode-builder"),
      root.querySelector("#prompt-mode-library"),
      root.querySelector("#prompt-mode-json"),
    ];
    for (const link of links) {
      if (!link) {
        continue;
      }
      link.classList.remove("active");
      link.setAttribute("aria-selected", "false");
      link.addEventListener("click", (event) => {
        event.preventDefault();
        const hash = link.hash || "";
        trackDockEvent("dock_home_mode_opened", {
          source: "bottom_dock",
          prompt_mode: String(link.id || "").replace("prompt-mode-", ""),
        });
        window.location.assign(`${HOME_PATH}${hash}`);
      });
    }
  }

  function syncPageNavLinks(root) {
    const hashDisplay = String(window.location.hash || "").replace(/^#/, "").trim();
    const currentDisplay = hashDisplay === "releases" || hashDisplay === "dashboard" ? hashDisplay : "studio";
    const links = root.querySelectorAll("[data-app-display]");
    links.forEach((link) => {
      const linkDisplay = String(link.getAttribute("data-app-display") || "").trim() || "dashboard";
      const active = linkDisplay === currentDisplay;
      link.classList.toggle("active", active);
      if (active) {
        link.setAttribute("aria-current", "true");
      } else {
        link.removeAttribute("aria-current");
      }
    });
  }

  function applyGitHubProfileLink(profileURL) {
    const githubProfileLink = document.getElementById("github-profile-link");
    if (!githubProfileLink) {
      return;
    }
    const normalized = String(profileURL || "").trim();
    if (!/^https:\/\/github\.com\/[^/?#]+/i.test(normalized)) {
      return;
    }
    githubProfileLink.href = normalized;
  }

  async function resolveGitHubProfileLink() {
    if (isHomePage()) {
      return;
    }
    try {
      const response = await fetch("/api/github/profile", { cache: "no-store" });
      let body = null;
      try {
        body = await response.json();
      } catch (_err) {
        body = null;
      }
      if (!response.ok || !body || body.ok === false) {
        return;
      }
      applyGitHubProfileLink(body.profileUrl || body.profile_url);
    } catch (_err) {
      // Keep static fallback when profile resolution fails.
    }
  }

  function applyHubDockState(hub) {
    const configured = Boolean(hub && hub.configured);
    const dockGroup = document.getElementById("moltenbot-hub-dock-group");
    const hubLink = document.getElementById("moltenbot-hub-link");
    const plus = document.getElementById("moltenbot-hub-plus");
    const profileButton = document.getElementById("moltenbot-hub-profile-button");
    const connectURL = String(hub && hub.connect_url || hub && hub.connectURL || HUB_LOGIN_URL).trim();
    const dashboardURL = String(hub && hub.dashboard_url || hub && hub.dashboardURL || HUB_DASHBOARD_URL).trim();

    if (dockGroup) {
      dockGroup.setAttribute("data-configured", configured ? "true" : "false");
    }
    if (plus) {
      plus.classList.toggle("hidden", configured);
    }
    if (hubLink) {
      hubLink.classList.remove("hidden");
      hubLink.href = configured ? (dashboardURL || HUB_DASHBOARD_URL) : (connectURL || HUB_LOGIN_URL);
      const title = configured ? "Open Molten Hub" : "Configure Molten Hub";
      hubLink.title = title;
      hubLink.setAttribute("aria-label", title);
    }
    if (profileButton) {
      profileButton.hidden = !configured;
      if (configured && profileButton.dataset.bottomDockBound !== "true") {
        profileButton.dataset.bottomDockBound = "true";
        profileButton.addEventListener("click", () => {
          trackDockEvent("hub_profile_opened", { source: "dock" });
          window.location.assign(`${HOME_PATH}${HUB_PROFILE_DEEP_LINK_HASH}`);
        });
      }
    }
  }

  async function resolveHubDockState() {
    if (isHomePage()) {
      return;
    }
    try {
      const response = await fetch("/api/hub-setup", { cache: "no-store" });
      let body = null;
      try {
        body = await response.json();
      } catch (_err) {
        body = null;
      }
      if (!response.ok || !body || body.ok === false) {
        applyHubDockState(null);
        return;
      }
      applyHubDockState(body.hub);
    } catch (_err) {
      applyHubDockState(null);
    }
  }

  function bindStaticDockAnalytics(root) {
    if (isHomePage()) {
      return;
    }
    const githubProfileLink = root.querySelector("#github-profile-link");
    const hubLink = root.querySelector("#moltenbot-hub-link");
    if (githubProfileLink) {
      githubProfileLink.addEventListener("click", () => {
        trackDockEvent("github_profile_opened", { source: "dock" });
      });
    }
    if (hubLink) {
      hubLink.addEventListener("click", () => {
        const configured = root.querySelector("#moltenbot-hub-dock-group")?.getAttribute("data-configured") === "true";
        trackDockEvent(configured ? "hub_dashboard_link_opened" : "hub_setup_opened", { source: "dock" });
      });
    }
  }

  function bindPageNavAnalytics(root) {
    root.querySelectorAll("[data-app-display]").forEach((link) => {
      if (link.dataset.bottomDockPageNavBound === "true") {
        return;
      }
      link.dataset.bottomDockPageNavBound = "true";
      link.addEventListener("click", () => {
        trackDockEvent("site_page_nav_opened", {
          source: "dock",
          target: String(link.getAttribute("data-app-display") || "").trim(),
        });
      });
    });
  }

  function init() {
    const root = document.querySelector(".page-bottom-dock");
    if (!root || root.dataset.bottomDockReady === "true") {
      return;
    }
    root.dataset.bottomDockReady = "true";
    replaceLucideIcons(root);
    syncPageNavLinks(root);
    window.addEventListener("hashchange", () => syncPageNavLinks(root));
    routeStudioLinksToHome(root);
    initThemeToggle(root);
    bindStaticDockAnalytics(root);
    bindPageNavAnalytics(root);
    void resolveGitHubProfileLink();
    void resolveHubDockState();
  }

  window.HubBottomDock = Object.assign(window.HubBottomDock || {}, {
    init,
    applyThemeMode,
    syncThemeToggle,
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init, { once: true });
  } else {
    init();
  }
})();
