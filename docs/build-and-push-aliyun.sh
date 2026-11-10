#!/usr/bin/env sh
# 保留原镜像发布入口和环境变量，复用项目根目录的统一打包脚本。
#
# @author Andersen
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PROJECT_ROOT="$(dirname -- "${SCRIPT_DIR}")"

# 旧入口默认不询问，保持已有自动化任务的调用行为。
export DOCKER_NO_INTERACTIVE="${DOCKER_NO_INTERACTIVE:-true}"
exec bash "${PROJECT_ROOT}/package.sh" "$@"
