#!/bin/sh
set -eu

config_dir="${HARNESS_CONFIG_DIR:-/workspace/config}"
run_config_path="${HARNESS_RUN_CONFIG_PATH:-${config_dir}/config.json}"
init_config_path="${HARNESS_INIT_CONFIG_PATH:-${config_dir}/init.json}"
generated_init_path="${HARNESS_GENERATED_INIT_PATH:-/tmp/harness-init-from-env.json}"
template_dir="${HARNESS_TEMPLATE_DIR:-/workspace/templates}"

json_escape() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

try_run_hub_from_env() {
    token="${MOLTEN_HUB_TOKEN:-}"
    if [ "${token}" = "" ]; then
        return 1
    fi

    hub_base_url="${MOLTEN_HUB_URL:-https://na.hub.molten.bot/v1}"
    session_key="${MOLTEN_HUB_SESSION_KEY:-main}"
    generated_init_dir=$(dirname "${generated_init_path}")
    mkdir -p "${generated_init_dir}"

    {
        printf '{\n'
        printf '  "base_url": "%s",\n' "$(json_escape "${hub_base_url}")"
        printf '  "agent_token": "%s",\n' "$(json_escape "${token}")"
        printf '  "session_key": "%s"\n' "$(json_escape "${session_key}")"
        printf '}\n'
    } > "${generated_init_path}"

    exec harness hub --init "${generated_init_path}"
}

if [ -f "${run_config_path}" ]; then
    exec harness run --config "${run_config_path}"
fi

if [ -f "${init_config_path}" ]; then
    exec harness hub --init "${init_config_path}"
fi

if try_run_hub_from_env; then
    :
fi

echo "missing config file: expected ${run_config_path} (run mode) or ${init_config_path} (hub mode)" >&2
echo "bootstrap run mode with:" >&2
echo "  cp ${template_dir}/run.example.json ${run_config_path}" >&2
echo "bootstrap hub mode with:" >&2
echo "  cp ${template_dir}/init.example.json ${init_config_path}" >&2
echo "or set MOLTEN_HUB_TOKEN (and optionally MOLTEN_HUB_URL) to auto-start hub mode." >&2
exit 10
