#!/bin/sh
set -eu

config_dir="${HARNESS_CONFIG_DIR:-/workspace/config}"
run_config_path="${HARNESS_RUN_CONFIG_PATH:-${config_dir}/config.json}"
init_config_path="${HARNESS_INIT_CONFIG_PATH:-${config_dir}/init.json}"

if [ -f "${run_config_path}" ]; then
    exec harness run --config "${run_config_path}"
fi

if [ -f "${init_config_path}" ]; then
    exec harness hub --init "${init_config_path}"
fi

echo "missing config file: expected ${run_config_path} (run mode) or ${init_config_path} (hub mode)" >&2
exit 10
