#!/bin/sh
set -eu

config_dir="${HARNESS_CONFIG_DIR:-/workspace/config}"
run_config_path="${HARNESS_RUN_CONFIG_PATH:-${config_dir}/config.json}"
init_config_path="${HARNESS_INIT_CONFIG_PATH:-${config_dir}/init.json}"
template_dir="${HARNESS_TEMPLATE_DIR:-/workspace/templates}"
hub_ui_listen="${HARNESS_HUB_UI_LISTEN-:7777}"

exec_hub() {
    exec harness hub "$@" --ui-listen "${hub_ui_listen}"
}

hub_config_status() {
    file_path="$1"
    if [ ! -f "${file_path}" ]; then
        return 1
    fi

    node -e '
const fs = require("node:fs");
const [, filePath] = process.argv;
function stripLineComments(data) {
  let out = "";
  let inString = false;
  let escaped = false;
  for (let i = 0; i < data.length; i++) {
    const ch = data[i];
    if (inString) {
      out += ch;
      if (escaped) {
        escaped = false;
        continue;
      }
      if (ch === "\\") {
        escaped = true;
        continue;
      }
      if (ch === "\"") {
        inString = false;
      }
      continue;
    }
    if (ch === "\"") {
      inString = true;
      out += ch;
      continue;
    }
    if (ch === "/" && i + 1 < data.length && data[i + 1] === "/") {
      while (i < data.length && data[i] !== "\n") {
        i++;
      }
      if (i < data.length && data[i] === "\n") {
        out += "\n";
      }
      continue;
    }
    out += ch;
  }
  return out;
}
try {
  const raw = fs.readFileSync(filePath, "utf8");
  const cfg = JSON.parse(stripLineComments(raw));
  const hubKeys = [
    "base_url",
    "bind_token",
    "agent_token",
    "session_key",
    "agent_harness",
    "agent_command",
    "profile",
    "dispatcher",
    "github_token",
    "openai_api_key",
    "baseUrl",
    "token",
    "sessionKey",
    "timeoutMs",
  ];
  const isHubConfig = hubKeys.some((key) => Object.prototype.hasOwnProperty.call(cfg, key));
  if (!isHubConfig) {
    process.exit(1);
  }
  const rawBaseURL = typeof cfg.base_url === "string" ? cfg.base_url : (typeof cfg.baseUrl === "string" ? cfg.baseUrl : "");
  const baseURL = rawBaseURL.trim();
  if (baseURL !== "") {
    try {
      const parsed = new URL(baseURL);
      const host = parsed.hostname.toLowerCase();
      const path = parsed.pathname.replace(/\/+$/, "");
      if (parsed.protocol !== "https:" || parsed.port !== "" || !host.endsWith(".hub.molten.bot") || host.split(".").length !== 4 || path !== "/v1") {
        process.exit(2);
      }
    } catch (_) {
      process.exit(2);
    }
  }
  process.exit(0);
} catch (_) {
  process.exit(1);
}
' "${file_path}"
}

try_run_hub_from_env() {
    if ! node - "${run_config_path}" <<'NODE'
const fs = require("node:fs");
const path = require("node:path");

const [, , filePath] = process.argv;

function rawEnvironmentEntries() {
  const entries = Object.entries(process.env).map(([key, value]) => `${key}=${value}`);
  try {
    const parentEnv = fs.readFileSync(`/proc/${process.ppid}/environ`, "utf8");
    entries.push(...parentEnv.split("\0").filter(Boolean));
  } catch (_) {
  }
  return entries;
}

function envValue(...names) {
  for (const name of names) {
    const value = typeof process.env[name] === "string" ? process.env[name].trim() : "";
    if (value !== "") {
      return value;
    }
  }
  for (const entry of rawEnvironmentEntries()) {
    for (const name of names) {
      const prefix = `${name}:`;
      if (!entry.startsWith(prefix)) {
        continue;
      }
      const suffix = entry.slice(prefix.length).replace(/=$/, "").trim();
      if (suffix !== "") {
        return suffix;
      }
    }
  }
  return "";
}

function stripLineComments(data) {
  let out = "";
  let inString = false;
  let escaped = false;
  for (let i = 0; i < data.length; i++) {
    const ch = data[i];
    if (inString) {
      out += ch;
      if (escaped) {
        escaped = false;
        continue;
      }
      if (ch === "\\") {
        escaped = true;
        continue;
      }
      if (ch === "\"") {
        inString = false;
      }
      continue;
    }
    if (ch === "\"") {
      inString = true;
      out += ch;
      continue;
    }
    if (ch === "/" && i + 1 < data.length && data[i + 1] === "/") {
      while (i < data.length && data[i] !== "\n") {
        i++;
      }
      if (i < data.length && data[i] === "\n") {
        out += "\n";
      }
      continue;
    }
    out += ch;
  }
  return out;
}

function normalizedRegion(value) {
  const region = String(value || "").trim().toLowerCase();
  if (region === "" || region === "na") {
    return "na";
  }
  if (region === "eu") {
    return "eu";
  }
  return "";
}

function resolveBaseURL() {
  const explicitURL = envValue("MOLTEN_HUB_URL").replace(/\s+/g, "");
  if (explicitURL !== "") {
    if (explicitURL === "https://na.hub.molten.bot/v1" || explicitURL === "https://eu.hub.molten.bot/v1") {
      return explicitURL;
    }
    console.error("invalid MOLTEN_HUB_URL; expected https://na.hub.molten.bot/v1 or https://eu.hub.molten.bot/v1");
    process.exit(2);
  }

  const region = normalizedRegion(envValue("MOLTEN_HUB_REGION") || "na");
  if (region === "") {
    console.error("invalid MOLTEN_HUB_REGION; expected na or eu");
    process.exit(2);
  }
  return `https://${region}.hub.molten.bot/v1`;
}

function isHubConfig(doc) {
  if (!doc || typeof doc !== "object" || Array.isArray(doc)) {
    return false;
  }
  const hubKeys = [
    "base_url",
    "bind_token",
    "agent_token",
    "session_key",
    "agent_harness",
    "agent_command",
    "profile",
    "dispatcher",
    "github_token",
    "openai_api_key",
    "baseUrl",
    "token",
    "sessionKey",
    "timeoutMs",
  ];
  return hubKeys.some((key) => Object.prototype.hasOwnProperty.call(doc, key));
}

function readExistingHubConfig(targetPath) {
  try {
    const raw = fs.readFileSync(targetPath, "utf8");
    const parsed = JSON.parse(stripLineComments(raw));
    return isHubConfig(parsed) ? parsed : {};
  } catch (_) {
    return {};
  }
}

function stringValue(value) {
  return typeof value === "string" ? value.trim() : "";
}

const token = envValue("MOLTEN_HUB_TOKEN");
if (token === "") {
  process.exit(1);
}

const doc = readExistingHubConfig(filePath);
doc.version = stringValue(doc.version) || "v1";
doc.base_url = resolveBaseURL();
doc.agent_token = token;
delete doc.bind_token;
delete doc.bindToken;

const githubToken = envValue("GH_TOKEN", "GITHUB_TOKEN");
if (githubToken !== "") {
  doc.github_token = githubToken;
}

const envHarness = envValue("HARNESS_AGENT_HARNESS");
const existingHarness = stringValue(doc.agent_harness) || stringValue(doc.agentHarness);
doc.agent_harness = (envHarness || existingHarness || "codex").trim().toLowerCase();

const envCommand = envValue("HARNESS_AGENT_COMMAND");
const existingCommand = stringValue(doc.agent_command) || stringValue(doc.agentCommand);
if (envCommand !== "" || existingCommand !== "") {
  doc.agent_command = envCommand || existingCommand;
}

const sessionKey = envValue("MOLTEN_HUB_SESSION_KEY") || stringValue(doc.session_key) || stringValue(doc.sessionKey) || "main";
doc.session_key = sessionKey;

fs.mkdirSync(path.dirname(filePath), { recursive: true });
fs.writeFileSync(filePath, `${JSON.stringify(doc, null, 2)}\n`, { mode: 0o600 });
NODE
    then
        return 1
    fi

    if [ "${HARNESS_RUNTIME_CONFIG_PATH:-}" = "" ]; then
        export HARNESS_RUNTIME_CONFIG_PATH="${run_config_path}"
    fi
    exec_hub --config "${run_config_path}"
}

if try_run_hub_from_env; then
    :
fi

if [ -f "${run_config_path}" ]; then
    if hub_config_status "${run_config_path}"; then
        exec_hub --config "${run_config_path}"
    else
        hub_status=$?
        if [ "${hub_status}" = "2" ]; then
            echo "invalid hub config at ${run_config_path}; skipping persisted hub config" >&2
        else
            exec harness run --config "${run_config_path}"
        fi
    fi
fi

if [ -f "${init_config_path}" ]; then
    exec_hub --init "${init_config_path}"
fi

if [ "${HARNESS_RUNTIME_CONFIG_PATH:-}" = "" ]; then
    export HARNESS_RUNTIME_CONFIG_PATH="${run_config_path}"
fi

echo "no config file found; starting hub onboarding mode with defaults" >&2
echo "optional run config path: ${run_config_path}" >&2
echo "optional init config path: ${init_config_path}" >&2
echo "or set MOLTEN_HUB_TOKEN (and optionally MOLTEN_HUB_REGION=na|eu) for remote-hub bootstrap." >&2

exec_hub
