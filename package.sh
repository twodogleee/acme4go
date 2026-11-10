#!/usr/bin/env bash
# 构建包含 Vue 管理端与 Go 程序的镜像，并按配置推送；Server 和 Agent 共用同一镜像。
#
# @author Andersen
set -euo pipefail

# usage 输出命令参数、环境变量及常用调用示例，不要求本机安装 Docker。
usage() {
  cat <<'HELP'
用法：./package.sh [镜像版本]

可从任意目录调用。Dockerfile 自动构建前端和后端，无需预先准备二进制。

环境变量：
  DOCKER_REGISTRY         仓库地址，默认 ccr.ccs.tencentyun.com
  DOCKER_IMAGE_NAME       命名空间/镜像名，默认 36knight/acme4go
  DOCKER_TAG              镜像版本，默认 latest；命令行版本优先
  DOCKER_PLATFORM         单个目标平台，默认 linux/amd64
  DOCKER_PUSH_ENABLED     true/false，默认 true；false 表示只构建
  DOCKER_NO_INTERACTIVE   true/false，默认 false；true 表示不询问
  SKIP_DOCKER_CONFIRM     true/false，默认 false；true 同样跳过询问

兼容原发布脚本的 IMAGE、TAG、VERSION、PLATFORM、PUSH、DOCKERFILE、CONTEXT、
ALIYUN_REGISTRY、ALIYUN_NAMESPACE。IMAGE 可覆盖完整镜像仓库（不带标签）。
VERSION 覆盖写入 Go 程序的版本，默认与镜像标签一致。

示例：
  ./package.sh 1.0.0
  DOCKER_PUSH_ENABLED=false ./package.sh local
  DOCKER_NO_INTERACTIVE=true ./package.sh 1.0.0
  DOCKER_PLATFORM=linux/arm64 ./package.sh 1.0.0-arm64

推送前请自行 docker login 对应仓库；脚本不保存密码、不自动登录。
HELP
}

# fail 输出可定位的错误并终止，防止构建失败后继续推送旧镜像。
fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

# confirm 在交互模式读取明确选择；无人值守调用必须显式启用免交互模式。
confirm() {
  local answer
  if [[ "$PACKAGE_NO_INTERACTIVE" == true || "$PACKAGE_SKIP_CONFIRM" == true ]]; then
    return 0
  fi
  [[ -t 0 ]] || fail '非交互终端请设置 DOCKER_NO_INTERACTIVE=true；仅构建可同时设置 DOCKER_PUSH_ENABLED=false。'
  read -r -p "$1 [y/N]: " answer || return 1
  [[ "$answer" == y || "$answer" == Y ]]
}

if [[ "${1:-}" == --help || "${1:-}" == -h ]]; then
  usage
  exit 0
fi
[[ $# -le 1 ]] || fail '只支持一个镜像版本参数，使用 --help 查看用法。'

PACKAGE_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_REGISTRY="${DOCKER_REGISTRY:-${ALIYUN_REGISTRY:-ccr.ccs.tencentyun.com}}"
PACKAGE_NAME="${DOCKER_IMAGE_NAME:-${ALIYUN_NAMESPACE:-36knight}/acme4go}"
PACKAGE_IMAGE="${IMAGE:-${PACKAGE_REGISTRY%/}/${PACKAGE_NAME}}"
PACKAGE_TAG="${1:-${DOCKER_TAG:-${TAG:-latest}}}"
PACKAGE_VERSION="${VERSION:-${PACKAGE_TAG}}"
PACKAGE_PLATFORM="${DOCKER_PLATFORM:-${PLATFORM:-linux/amd64}}"
PACKAGE_PUSH="${DOCKER_PUSH_ENABLED:-${PUSH:-true}}"
PACKAGE_NO_INTERACTIVE="${DOCKER_NO_INTERACTIVE:-false}"
PACKAGE_SKIP_CONFIRM="${SKIP_DOCKER_CONFIRM:-false}"
PACKAGE_DOCKERFILE="${DOCKERFILE:-${PACKAGE_ROOT}/Dockerfile}"
PACKAGE_CONTEXT="${CONTEXT:-${PACKAGE_ROOT}}"
PACKAGE_FULL_IMAGE="${PACKAGE_IMAGE}:${PACKAGE_TAG}"

[[ "$PACKAGE_TAG" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$ ]] || fail '镜像版本不是有效的 Docker 标签。'
[[ "$PACKAGE_VERSION" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.+-]*$ ]] || fail '程序版本仅允许字母、数字、下划线、点、加号和中划线。'
[[ "$PACKAGE_IMAGE" != *://* && "$PACKAGE_IMAGE" != *[[:space:]]* && "$PACKAGE_IMAGE" != -* && "$PACKAGE_IMAGE" != *@* && "${PACKAGE_IMAGE##*/}" != *:* ]] || fail '镜像仓库应为不含协议、标签和摘要的地址，例如 registry.example.com/team/acme4go。'
[[ "$PACKAGE_PLATFORM" != *,* ]] || fail '此脚本每次构建一个平台；请分别指定 linux/amd64 或 linux/arm64。'
for PACKAGE_BOOLEAN in "$PACKAGE_PUSH" "$PACKAGE_NO_INTERACTIVE" "$PACKAGE_SKIP_CONFIRM"; do
  [[ "$PACKAGE_BOOLEAN" == true || "$PACKAGE_BOOLEAN" == false ]] || fail '推送和交互开关只支持 true 或 false。'
done
[[ -f "$PACKAGE_DOCKERFILE" ]] || fail "Dockerfile 不存在：${PACKAGE_DOCKERFILE}"
[[ -d "$PACKAGE_CONTEXT" ]] || fail "构建上下文目录不存在：${PACKAGE_CONTEXT}"
command -v docker >/dev/null 2>&1 || fail '未找到 docker 命令。'
docker info >/dev/null 2>&1 || fail '无法连接 Docker，请启动 Docker Desktop 或检查 Docker 上下文。'

printf '镜像：%s\n程序版本：%s\n平台：%s\n构建目录：%s\n' \
  "$PACKAGE_FULL_IMAGE" "$PACKAGE_VERSION" "$PACKAGE_PLATFORM" "$PACKAGE_CONTEXT"
if ! confirm '是否开始构建镜像？'; then
  printf '已取消构建。\n'
  exit 0
fi

PACKAGE_STARTED=$SECONDS
if ! DOCKER_BUILDKIT=1 docker build \
  --platform "$PACKAGE_PLATFORM" \
  --build-arg "VERSION=${PACKAGE_VERSION}" \
  -f "$PACKAGE_DOCKERFILE" \
  -t "$PACKAGE_FULL_IMAGE" \
  "$PACKAGE_CONTEXT"; then
  fail '镜像构建失败，未执行推送。'
fi
printf '构建成功，耗时 %s 秒。\n' "$((SECONDS - PACKAGE_STARTED))"

if [[ "$PACKAGE_PUSH" == true ]] && confirm '是否推送刚构建的镜像？'; then
  PACKAGE_STARTED=$SECONDS
  docker push "$PACKAGE_FULL_IMAGE" || fail '镜像推送失败，请检查仓库登录状态、权限和网络。'
  printf '推送成功，耗时 %s 秒。\n' "$((SECONDS - PACKAGE_STARTED))"
else
  printf '已完成本地构建，跳过推送。\n'
fi
printf '镜像：%s\n' "$PACKAGE_FULL_IMAGE"
