(function initMoltenHubHeader() {
  const THEME_KEY = "hubui.theme";
  const THEME_MODES = ["light", "dark", "night", "pink"];
  const DEFAULT_THEME_MODE = "light";
  const LOGO_ROTATION_INTERVAL_MS = 8_000;
  const HUB_LOGIN_URL = "https://molten.bot/login?target=hub";
  const HUB_DASHBOARD_URL = "https://app.molten.bot/hub";
  const AGENT_LOGO_URLS = Object.freeze({
    codex: "/static/logos/codex-cli.svg",
    claude: "/static/logos/claude-code.svg",
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
    return "Agent";
  }

  function resolveAgentLogoURL(harnessLabel) {
    const label = String(harnessLabel || "").trim().toLowerCase();
    if (AGENT_LOGO_URLS[label]) {
      return AGENT_LOGO_URLS[label];
    }
    if (label.includes("claude")) return AGENT_LOGO_URLS.claude;
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

  function formatCompactMetricNumber(value, suffix = "") {
    if (!Number.isFinite(value) || value < 0) {
      return "--";
    }
    if (value >= 100) {
      return `${Math.round(value)}${suffix}`;
    }
    if (value >= 10) {
      return `${Math.round(value * 10) / 10}${suffix}`;
    }
    if (value > 0 && value < 1) {
      return `<1${suffix}`;
    }
    return `${Math.round(value)}${suffix}`;
  }

  function formatDiskThroughput(valueInMBs) {
    if (!Number.isFinite(valueInMBs) || valueInMBs < 0) {
      return { value: "--", unit: "MB/s" };
    }
    if (valueInMBs < 1) {
      return {
        value: formatCompactMetricNumber(valueInMBs * 1000),
        unit: "KB/s",
      };
    }
    if (valueInMBs >= 1000) {
      return {
        value: formatCompactMetricNumber(valueInMBs / 1000),
        unit: "GB/s",
      };
    }
    return {
      value: formatCompactMetricNumber(valueInMBs),
      unit: "MB/s",
    };
  }

  function metricSeverityClass(value) {
    if (!Number.isFinite(value) || value < 0) return "metric-icon-neutral";
    if (value < 65) return "metric-icon-good";
    if (value <= 85) return "metric-icon-warn";
    return "metric-icon-bad";
  }

  function headerTemplate() {
    return `
      <header class="header site-header">
        <a class="brand-lockup site-header-home" href="/" aria-label="Agent_00 home" data-site-header-home>
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
            <span class="title site-header-title">Agent_00</span>
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
          <div id="speech-conn-item" class="status-item status-item-compact status-item-compact-expandable hidden" title="Whisper: Connected" aria-label="Whisper: Connected" tabindex="0">
            <span id="speech-conn-dot" class="dot online"></span>
            <span id="speech-conn-text" class="status-tooltip">Whisper: Connected</span>
          </div>
          <div id="resource-metrics-item" class="status-item status-item-metrics" title="Resource metrics" aria-label="Resource metrics" tabindex="0">
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

  const NAV_ITEMS = Object.freeze([
    { href: "/", label: "Home" },
  ]);

  function normalizePath(path) {
    const value = String(path || "/").trim() || "/";
    if (value.length > 1) {
      return value.replace(/\/+$/, "");
    }
    return value;
  }

  function navTemplate(activePath) {
    const currentPath = normalizePath(activePath);
    const links = NAV_ITEMS.map((item) => {
      const itemPath = normalizePath(item.href);
      const current = itemPath === currentPath ? ` aria-current="page"` : "";
      return `<a href="${item.href}"${current}>${item.label}</a>`;
    }).join("");
    return `<nav class="site-page-nav" aria-label="Primary">${links}</nav>`;
  }

  class MoltenHubCodeHeader extends HTMLElement {
    constructor() {
      super();
      this.logoRotationTimer = null;
      this.logoRotationPhase = 0;
      this.headerConfig = {};
      this.agentAuth = {};
      this.resourceMetricVisible = {
        cpu: false,
        mem: false,
        disk: false,
      };
    }

    connectedCallback() {
      if (!this.firstElementChild) {
        this.innerHTML = headerTemplate();
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

    resourceNodes() {
      return {
        item: this.querySelector("#resource-metrics-item"),
        cpuChip: this.querySelector("#resource-cpu-chip"),
        memChip: this.querySelector("#resource-mem-chip"),
        diskChip: this.querySelector("#resource-disk-chip"),
        cpuIcon: this.querySelector("#resource-cpu-icon"),
        cpuText: this.querySelector("#resource-cpu-text"),
        memIcon: this.querySelector("#resource-mem-icon"),
        memText: this.querySelector("#resource-mem-text"),
        diskIcon: this.querySelector("#resource-disk-icon"),
        diskText: this.querySelector("#resource-metrics-text"),
        diskUnit: this.querySelector("#resource-metrics-unit"),
      };
    }

    setMetricIcon(iconNode, value) {
      if (!iconNode) return;
      iconNode.classList.remove("metric-icon-neutral", "metric-icon-good", "metric-icon-warn", "metric-icon-bad");
      iconNode.classList.add(metricSeverityClass(value));
    }

    markMetricVisible(metric, value) {
      if (Number.isFinite(value) && value > 0) {
        this.resourceMetricVisible[metric] = true;
      }
      return this.resourceMetricVisible[metric] === true;
    }

    setMetricChipVisibility(showCPU, showMem, showDisk) {
      const nodes = this.resourceNodes();
      if (nodes.cpuChip) nodes.cpuChip.hidden = !showCPU;
      if (nodes.memChip) nodes.memChip.hidden = !showMem;
      if (nodes.diskChip) nodes.diskChip.hidden = !showDisk;
      if (nodes.item) nodes.item.hidden = !(showCPU || showMem || showDisk);
    }

    setMetricText(cpu, mem, disk) {
      const nodes = this.resourceNodes();
      if (nodes.cpuText) nodes.cpuText.textContent = formatCompactMetricNumber(cpu, "%");
      if (nodes.memText) nodes.memText.textContent = formatCompactMetricNumber(mem, "%");
      if (nodes.diskText) {
        const diskThroughput = formatDiskThroughput(disk);
        nodes.diskText.textContent = diskThroughput.value;
        if (nodes.diskUnit) {
          nodes.diskUnit.textContent = diskThroughput.unit;
        }
      } else if (nodes.diskUnit) {
        nodes.diskUnit.textContent = "MB/s";
      }
      this.setMetricIcon(nodes.cpuIcon, cpu);
      this.setMetricIcon(nodes.memIcon, mem);
      this.setMetricIcon(nodes.diskIcon, disk);
    }

    updateResourceMetrics(snapshot) {
      const resources = snapshot && typeof snapshot.resources === "object" && snapshot.resources !== null
        ? snapshot.resources
        : null;
      if (!resources) {
        const showCPU = Boolean(this.resourceMetricVisible.cpu);
        const showMem = Boolean(this.resourceMetricVisible.mem);
        const showDisk = Boolean(this.resourceMetricVisible.disk);
        this.setMetricChipVisibility(showCPU, showMem, showDisk);
        this.setMetricText(NaN, NaN, NaN);
        return;
      }

      const cpuRaw = Number(resources.cpu_percent);
      const memRaw = Number(resources.memory_percent);
      const diskRaw = Number(resources.disk_io_mb_s);
      const cpu = Number.isFinite(cpuRaw) ? cpuRaw : 0;
      const mem = Number.isFinite(memRaw) ? memRaw : 0;
      const disk = Number.isFinite(diskRaw) ? diskRaw : 0;
      const showCPU = this.markMetricVisible("cpu", cpu);
      const showMem = this.markMetricVisible("mem", mem);
      const showDisk = this.markMetricVisible("disk", disk);
      this.setMetricChipVisibility(showCPU, showMem, showDisk);
      this.setMetricText(showCPU ? cpu : NaN, showMem ? mem : NaN, showDisk ? disk : NaN);
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

    connectionNodes() {
      return {
        localItem: this.querySelector("#local-conn-item"),
        localDot: this.querySelector("#local-conn-dot"),
        localText: this.querySelector("#local-conn-text"),
        hubItem: this.querySelector("#hub-conn-item"),
        hubDot: this.querySelector("#hub-conn-dot"),
        hubText: this.querySelector("#hub-conn-text"),
        speechItem: this.querySelector("#speech-conn-item"),
        speechDot: this.querySelector("#speech-conn-dot"),
        speechText: this.querySelector("#speech-conn-text"),
      };
    }

    setIndicator(itemNode, dot, textNode, label, online, text) {
      const message = `${label}: ${text}`;
      if (dot) {
        dot.classList.toggle("online", online);
      }
      if (textNode) {
        textNode.textContent = message;
      }
      if (itemNode) {
        itemNode.title = message;
        itemNode.setAttribute("aria-label", message);
      }
    }

    updateLocalConnection(online, text) {
      const nodes = this.connectionNodes();
      this.setIndicator(nodes.localItem, nodes.localDot, nodes.localText, "Local", online, text);
    }

    updateSpeechConnection(connected, text = "") {
      const nodes = this.connectionNodes();
      if (!nodes.speechItem) return;
      const online = Boolean(connected);
      nodes.speechItem.classList.toggle("hidden", !online);
      nodes.speechItem.setAttribute("aria-hidden", online ? "false" : "true");
      this.setIndicator(nodes.speechItem, nodes.speechDot, nodes.speechText, "Whisper", online, text || "Connected");
    }

    applyHubDotMode(mode) {
      const { hubDot } = this.connectionNodes();
      if (!hubDot) return;
      hubDot.classList.remove("http", "disconnected");
      if (mode === "http_long_poll") {
        hubDot.classList.add("http");
        return;
      }
      if (mode === "disconnected") {
        hubDot.classList.add("disconnected");
      }
    }

    setHubConnectionActionable(actionable, href = "", tone = "") {
      const { hubItem } = this.connectionNodes();
      if (!hubItem) return;
      hubItem.classList.toggle("status-item-action", actionable);
      hubItem.classList.toggle("status-item-action-online", actionable && tone === "online");
      hubItem.classList.toggle("status-item-action-offline", actionable && tone === "offline");
      hubItem.setAttribute("data-actionable", actionable ? "true" : "false");
      hubItem.setAttribute("data-href", href);
    }

    setHubConnection(mode, text, hubSetup = {}) {
      const nodes = this.connectionNodes();
      const configured = Boolean(hubSetup && hubSetup.configured);
      const dashboardURL = String(hubSetup && (hubSetup.dashboardURL || hubSetup.dashboard_url) || HUB_DASHBOARD_URL).trim() || HUB_DASHBOARD_URL;
      const connectURL = String(hubSetup && (hubSetup.connectURL || hubSetup.connect_url) || HUB_LOGIN_URL).trim() || HUB_LOGIN_URL;
      const connected = mode === "ws" || mode === "http_long_poll" || mode === "connected";
      const actionable = connected || mode === "disconnected";
      const actionTone = connected ? "online" : (mode === "disconnected" ? "offline" : "");
      const targetURL = connected || configured ? dashboardURL : connectURL;
      let message = String(text || "").trim();
      if (mode === "disconnected" && !message) {
        message = configured
          ? "Configured locally. Restart runtime to connect."
          : "Connect to Molten Hub";
      }
      this.setIndicator(nodes.hubItem, nodes.hubDot, nodes.hubText, "Molten Hub", connected, message);
      this.applyHubDotMode(mode);
      this.setHubConnectionActionable(actionable, targetURL, actionTone);
    }

    updateConnectionStatus(snapshot, options = {}) {
      const hubSetup = options && typeof options.hubSetup === "object" && options.hubSetup !== null
        ? options.hubSetup
        : {};
      if (!snapshot || typeof snapshot.connection !== "object" || snapshot.connection === null) {
        this.setHubConnection("disconnected", "Hub status unavailable", hubSetup);
        return;
      }

      const conn = snapshot.connection;
      const domain = typeof conn.hub_domain === "string" ? conn.hub_domain.trim() : "";
      const baseURL = typeof conn.hub_base_url === "string" ? conn.hub_base_url.trim() : "";
      const transport = typeof conn.hub_transport === "string" ? conn.hub_transport.trim() : "";
      const detail = typeof conn.hub_detail === "string" ? conn.hub_detail.trim() : "";
      const target = domain || baseURL || "hub";

      if (transport === "ws") {
        this.setHubConnection("ws", `Connected via WebSocket to ${target}`, hubSetup);
        return;
      }
      if (transport === "http_long_poll") {
        this.setHubConnection("http_long_poll", `Connected via HTTP long polling to ${target}`, hubSetup);
        return;
      }
      if (conn.hub_connected) {
        this.setHubConnection("connected", `Connected to ${target} (transport pending)`, hubSetup);
        return;
      }
      if (transport === "reachable") {
        this.setHubConnection("reachable", detail || `Hub endpoint is live at ${target}. Connecting...`, hubSetup);
        return;
      }
      if (transport === "retrying") {
        this.setHubConnection("retrying", detail || `Hub endpoint is waking up at ${target}. Retrying ping every 12s.`, hubSetup);
        return;
      }
      if (domain || baseURL) {
        this.setHubConnection("disconnected", detail || `Disconnected from ${target}`, hubSetup);
        return;
      }
      this.setHubConnection("disconnected", "", hubSetup);
    }
  }

  class MoltenHubCodeNav extends HTMLElement {
    connectedCallback() {
      const activePath = this.getAttribute("active-path") || window.location.pathname || "/";
      this.innerHTML = navTemplate(activePath);
    }
  }

  applyPersistedTheme();

  if (!customElements.get("moltenhub-code-header")) {
    customElements.define("moltenhub-code-header", MoltenHubCodeHeader);
  }
  if (!customElements.get("moltenhub-code-nav")) {
    customElements.define("moltenhub-code-nav", MoltenHubCodeNav);
  }

  let connectionStatusStream = null;
  let connectionStatusStarted = false;

  function updateResourceMetrics(snapshot) {
    document.querySelectorAll("moltenhub-code-header").forEach((header) => {
      if (typeof header.updateResourceMetrics === "function") {
        header.updateResourceMetrics(snapshot);
      }
    });
  }

  async function loadResourceMetrics() {
    const response = await fetch("/api/status", { cache: "no-store" });
    if (!response.ok) {
      throw new Error(`status http ${response.status}`);
    }
    updateResourceMetrics(await response.json());
  }

  function updateLocalConnection(online, text) {
    document.querySelectorAll("moltenhub-code-header").forEach((header) => {
      if (typeof header.updateLocalConnection === "function") {
        header.updateLocalConnection(online, text);
      }
    });
  }

  function updateConnectionStatus(snapshot, options = {}) {
    document.querySelectorAll("moltenhub-code-header").forEach((header) => {
      if (typeof header.updateConnectionStatus === "function") {
        header.updateConnectionStatus(snapshot, options);
      }
    });
  }

  function updateSpeechConnection(connected, text = "") {
    document.querySelectorAll("moltenhub-code-header").forEach((header) => {
      if (typeof header.updateSpeechConnection === "function") {
        header.updateSpeechConnection(connected, text);
      }
    });
  }

  async function loadConnectionStatus(options = {}) {
    const response = await fetch("/api/status", { cache: "no-store" });
    if (!response.ok) {
      throw new Error(`status http ${response.status}`);
    }
    const snapshot = await response.json();
    updateLocalConnection(true, "Connected");
    updateConnectionStatus(snapshot, options);
    updateResourceMetrics(snapshot);
  }

  async function loadSharedHubSetup() {
    const response = await fetch("/api/hub-setup", { cache: "no-store" });
    if (!response.ok) {
      return {};
    }
    const body = await response.json();
    return body && typeof body.hub === "object" && body.hub !== null ? body.hub : {};
  }

  function startConnectionStatus(options = {}) {
    let streamOptions = options;
    const initialOptionsPromise = options && typeof options.hubSetup === "object" && options.hubSetup !== null
      ? Promise.resolve(options)
      : loadSharedHubSetup().then((hubSetup) => ({ ...options, hubSetup })).catch(() => options);
    void initialOptionsPromise.then((initialOptions) => {
      streamOptions = initialOptions;
      return loadConnectionStatus(initialOptions);
    }).catch((err) => {
      const message = err && err.message ? err.message : "Unable to load status";
      updateLocalConnection(false, `Unable to load status: ${message}`);
      updateConnectionStatus(null, options);
    });
    if (connectionStatusStarted || connectionStatusStream || typeof EventSource !== "function") {
      return;
    }
    connectionStatusStarted = true;
    connectionStatusStream = new EventSource("/api/stream");
    connectionStatusStream.onopen = () => {
      updateLocalConnection(true, "Connected");
    };
    connectionStatusStream.onmessage = (event) => {
      try {
        const snapshot = JSON.parse(event.data);
        updateConnectionStatus(snapshot, streamOptions);
        updateResourceMetrics(snapshot);
      } catch (_err) {
        // Ignore malformed event packets.
      }
    };
    connectionStatusStream.onerror = () => {
      updateLocalConnection(false, "Reconnecting...");
      if (connectionStatusStream) {
        connectionStatusStream.close();
        connectionStatusStream = null;
      }
      connectionStatusStarted = false;
      window.setTimeout(() => startConnectionStatus(streamOptions), 1500);
    };
  }

  function startResourceMetrics() {
    startConnectionStatus();
  }

  window.MoltenHubHeader = {
    update(config = {}, auth = {}) {
      document.querySelectorAll("moltenhub-code-header").forEach((header) => {
        if (typeof header.update === "function") {
          header.update(config, auth);
        }
      });
    },
    updateLocalConnection,
    updateConnectionStatus,
    updateSpeechConnection,
    updateResourceMetrics,
    startConnectionStatus,
    startResourceMetrics,
    agentAuthLabel,
    resolveAgentLogoURL,
    formatCompactMetricNumber,
    formatDiskThroughput,
    metricSeverityClass,
    navItems: NAV_ITEMS,
  };
})();
