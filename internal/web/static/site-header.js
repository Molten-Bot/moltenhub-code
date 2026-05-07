(function initMoltenHubHeader() {
  const THEME_KEY = "hubui.theme";
  const THEME_MODES = ["light", "dark", "night", "pink"];
  const DEFAULT_THEME_MODE = "light";
  const LOGO_ROTATION_INTERVAL_MS = 8_000;
  const AGENT_LOGO_URLS = Object.freeze({
    codex: "/static/logos/codex-cli.svg",
    claude: "/static/logos/claude-code.svg",
    auggie: "/static/logos/augment.svg",
    augment: "/static/logos/augment.svg",
    pi: "/static/logos/pi.svg",
  });

  function applyPersistedTheme() {
    let theme = DEFAULT_THEME_MODE;
    try {
      const stored = localStorage.getItem(THEME_KEY);
      if (THEME_MODES.includes(stored)) {
        theme = stored;
      }
    } catch (_err) {
      theme = DEFAULT_THEME_MODE;
    }

    const root = document.documentElement;
    root.classList.remove(...THEME_MODES);
    root.classList.add(theme);
    root.setAttribute("data-theme", theme);
  }

  function agentAuthLabel(harness) {
    const normalized = String(harness || "").trim().toLowerCase();
    if (normalized === "codex") return "Codex";
    if (normalized === "claude") return "Claude Code";
    if (normalized === "auggie") return "Auggie";
    if (normalized === "augment") return "Auggie";
    if (normalized === "pi") return "Pi";
    return "Agent";
  }

  function resolveAgentLogoURL(harnessLabel) {
    const label = String(harnessLabel || "").trim().toLowerCase();
    if (AGENT_LOGO_URLS[label]) {
      return AGENT_LOGO_URLS[label];
    }
    if (label.includes("claude")) return AGENT_LOGO_URLS.claude;
    if (label.includes("auggie") || label.includes("augment")) return AGENT_LOGO_URLS.auggie;
    if (/\bpi\b/.test(label)) return AGENT_LOGO_URLS.pi;
    if (label.includes("codex")) return AGENT_LOGO_URLS.codex;
    return "";
  }

  function normalizeHeaderState(config, auth) {
    const rawConfig = config && typeof config === "object" ? config : {};
    const rawAuth = auth && typeof auth === "object" ? auth : {};
    const authHarness = String(rawAuth.harness || "").trim().toLowerCase();
    const configHarness = String(rawConfig.configuredHarness || rawConfig.harness || "").trim().toLowerCase();
    const attrHarness = String(rawConfig.agentHarness || "").trim().toLowerCase();
    const harness = authHarness || configHarness || attrHarness;
    const configLabel = String(rawConfig.configuredAgentLabel || rawConfig.agentLabel || "").trim();
    const label = harness ? agentAuthLabel(harness) : (configLabel || "Agent");
    const logoURL = resolveAgentLogoURL(harness || label);
    return { label, logoURL };
  }

  function replaceLucideIcons(root) {
    const api = window.lucide;
    if (!api || typeof api.createIcons !== "function") {
      return;
    }
    try {
      api.createIcons({ root });
    } catch (_err) {
      // Header remains usable if icon replacement fails.
    }
  }

  function headerTemplate(homeHref) {
    return `
      <header class="header site-header">
        <a class="brand-lockup site-header-home" href="${homeHref}" aria-label="Molten Hub Code home" data-site-header-home>
          <span class="brand-logo-group">
            <img
              id="moltenhub-logo"
              class="brand-logo rotating-brand-logo brand-logo-visible"
              src="/static/logo.svg"
              alt="MoltenHub logo">
            <img
              id="configured-agent-logo"
              class="configured-agent-logo rotating-brand-logo"
              src="/static/logos/codex-cli.svg"
              alt="Codex logo">
          </span>
          <span class="site-header-copy">
            <span class="title site-header-title">Molten Hub Code</span>
            <span id="configured-agent-gorilla-subtitle" class="site-header-subtitle">Codex is now a 600LB Gorilla!</span>
          </span>
        </a>
        <div class="status-row" aria-label="Connection status">
          <div id="local-conn-item" class="status-item status-item-compact status-item-compact-expandable" title="Local: Ready" aria-label="Local: Ready" tabindex="0">
            <span id="local-conn-dot" class="dot online"></span>
            <span id="local-conn-text" class="status-tooltip">Local: Ready</span>
          </div>
          <div id="hub-conn-item" class="status-item status-item-compact status-item-compact-expandable" title="Molten Hub: Ready" aria-label="Molten Hub: Ready" tabindex="0">
            <span id="hub-conn-dot" class="dot online"></span>
            <span id="hub-conn-text" class="status-tooltip">Molten Hub: Ready</span>
          </div>
          <div id="resource-metrics-item" class="status-item status-item-metrics" title="Resource metrics" aria-label="Resource metrics">
            <span id="resource-cpu-chip" class="metric-chip">
              <span id="resource-cpu-icon" class="metric-icon metric-icon-neutral" aria-hidden="true">
                <i data-lucide="cpu" class="metric-icon-glyph" aria-hidden="true"></i>
              </span>
              <span class="metric-copy">
                <span class="metric-label">CPU</span>
                <span id="resource-cpu-text" class="status-value metric-value">--</span>
              </span>
            </span>
            <span id="resource-mem-chip" class="metric-chip">
              <span id="resource-mem-icon" class="metric-icon metric-icon-neutral" aria-hidden="true">
                <i data-lucide="memory-stick" class="metric-icon-glyph" aria-hidden="true"></i>
              </span>
              <span class="metric-copy">
                <span class="metric-label">MEM</span>
                <span id="resource-mem-text" class="status-value metric-value">--</span>
              </span>
            </span>
            <span id="resource-disk-chip" class="metric-chip">
              <span id="resource-disk-icon" class="metric-icon metric-icon-neutral" aria-hidden="true">
                <i data-lucide="hard-drive" class="metric-icon-glyph" aria-hidden="true"></i>
              </span>
              <span class="metric-copy">
                <span class="metric-label">I/O</span>
                <span id="resource-metrics-text" class="status-value metric-value">--</span>
                <span id="resource-metrics-unit" class="metric-unit">MB/s</span>
              </span>
            </span>
          </div>
        </div>
      </header>
    `;
  }

  class MoltenHubCodeHeader extends HTMLElement {
    constructor() {
      super();
      this.logoRotationTimer = null;
      this.logoRotationPhase = 0;
      this.headerConfig = {};
      this.agentAuth = {};
    }

    connectedCallback() {
      if (!this.firstElementChild) {
        const homeHref = this.getAttribute("home-href") || "/";
        this.innerHTML = headerTemplate(homeHref);
      }
      this.headerConfig = {
        agentHarness: this.getAttribute("agent-harness") || "codex",
        agentLabel: this.getAttribute("agent-label") || "Codex",
      };
      this.renderAgent();
      replaceLucideIcons(this);
    }

    disconnectedCallback() {
      this.stopLogoRotation();
    }

    update(config = {}, auth = {}) {
      this.headerConfig = {
        ...this.headerConfig,
        ...(config && typeof config === "object" ? config : {}),
      };
      this.agentAuth = auth && typeof auth === "object" ? auth : {};
      this.renderAgent();
    }

    logoNodes() {
      return {
        moltenHubLogo: this.querySelector("#moltenhub-logo"),
        configuredAgentLogo: this.querySelector("#configured-agent-logo"),
        subtitle: this.querySelector("#configured-agent-gorilla-subtitle"),
      };
    }

    setLogoVisibility(showConfiguredLogo) {
      const { moltenHubLogo, configuredAgentLogo } = this.logoNodes();
      if (moltenHubLogo) {
        moltenHubLogo.classList.toggle("brand-logo-visible", !showConfiguredLogo);
      }
      if (configuredAgentLogo) {
        configuredAgentLogo.classList.toggle("brand-logo-visible", showConfiguredLogo);
      }
    }

    stopLogoRotation() {
      if (this.logoRotationTimer !== null) {
        window.clearInterval(this.logoRotationTimer);
        this.logoRotationTimer = null;
      }
    }

    shouldRotateLogos(configuredAgentLogo) {
      if (!configuredAgentLogo || configuredAgentLogo.classList.contains("hidden")) {
        return false;
      }
      if (typeof window.matchMedia === "function" &&
          window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
        return false;
      }
      return true;
    }

    syncLogoRotation(configuredAgentLogo) {
      this.stopLogoRotation();
      if (!this.shouldRotateLogos(configuredAgentLogo)) {
        this.logoRotationPhase = 0;
        this.setLogoVisibility(false);
        return;
      }
      this.logoRotationPhase = 0;
      this.setLogoVisibility(false);
      this.logoRotationTimer = window.setInterval(() => {
        this.logoRotationPhase = this.logoRotationPhase === 0 ? 1 : 0;
        this.setLogoVisibility(this.logoRotationPhase === 1);
      }, LOGO_ROTATION_INTERVAL_MS);
    }

    renderAgent() {
      const { configuredAgentLogo, subtitle } = this.logoNodes();
      const headerState = normalizeHeaderState(this.headerConfig, this.agentAuth);
      if (configuredAgentLogo) {
        configuredAgentLogo.classList.toggle("hidden", !headerState.logoURL);
        if (headerState.logoURL) {
          configuredAgentLogo.src = headerState.logoURL;
          configuredAgentLogo.alt = `${headerState.label} logo`;
        }
      }
      if (subtitle) {
        subtitle.textContent = headerState.label === "Agent"
          ? "Select your agent to get started."
          : `${headerState.label} is now a 600LB Gorilla!`;
      }
      this.syncLogoRotation(configuredAgentLogo);
    }
  }

  applyPersistedTheme();

  if (!customElements.get("moltenhub-code-header")) {
    customElements.define("moltenhub-code-header", MoltenHubCodeHeader);
  }

  window.MoltenHubHeader = {
    update(config = {}, auth = {}) {
      document.querySelectorAll("moltenhub-code-header").forEach((header) => {
        if (typeof header.update === "function") {
          header.update(config, auth);
        }
      });
    },
    agentAuthLabel,
    resolveAgentLogoURL,
  };
})();
