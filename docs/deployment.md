# 生产部署指南

本文说明 Acme4go Server、远程 Agent 和业务应用的生产部署方式。示例优先使用 Docker Compose，同时提供直接运行二进制和 systemd 托管 Agent 的方法。

## 1. 部署拓扑与网络边界

Acme4go 使用同一个二进制和镜像提供两种角色：

| 角色 | 启动命令 | 入站端口 | 职责 |
|---|---|---|---|
| Server | `acme4go server` | 管理面 `8080`、Agent 面 `9443` | 管理端、ACME 签发、证书库、Agent 注册与期望状态 |
| Agent | `acme4go agent` | 无 | 主动连接 Server，将被授权的证书物化到业务主机 |

典型拓扑：

```text
浏览器 ── HTTP/HTTPS ──> Server :8080
                              │
                              ├─ SQLite、加密证书库、ACME 调度
                              └─ Agent API :9443（HTTPS / mTLS）
                                           ▲
                                           │ 仅由 Agent 主动发起连接
                                      Agent 主机
                                           │
                                           └─ deployments/<alias>/current/*.pem
                                                        ▲
                                                        │ 只读挂载
                                                  Nginx / EMQX / 应用
```

网络要求：

- 浏览器或管理端反向代理能够访问 Server 的 `8080`；
- Agent 主机能够出站访问 `ACME_AGENT_PUBLIC_URL` 对应的 `9443`；
- Agent 不监听端口，不需要为 Agent 主机开放入站防火墙；
- `9443` 使用 Acme4go 内部 CA 和 mTLS，不需要自行申请公网证书；
- Agent HTTPS 服务端证书有效期为 90 天，进入到期前 30 天窗口后由 Server 在运行中自动轮换，无需重启或人工申请；
- `9443` 不能经过会终止并丢弃客户端证书的普通 HTTP 反向代理；如需负载均衡，只能使用 TCP 四层透传。

## 2. 部署前准备

Docker 部署建议准备：

- Docker 24 或更高版本；
- Docker Compose v2；
- Server 持久化存储；
- 至少 32 字节的随机主密钥；
- 至少 12 位的管理员初始密码；
- 一个供浏览器访问的管理地址；
- 一个供所有 Agent 访问的固定 IP 或域名。

生成 Server 主密钥：

```bash
openssl rand -base64 48
```

主密钥用于加密 DNS 凭据、ACME 账户密钥、证书私钥和内部 Agent CA 私钥。Server 数据目录创建后，主密钥不能随意修改或丢失，应与 `./data` 目录分别备份。

### 2.1 选择公开地址

管理地址和 Agent 地址是两个独立配置，在第 4.1 节创建的 `compose.local.yaml` 中修改 `environment`：

```yaml
environment:
  ACME_PUBLIC_URL: "https://acme-admin.example.com"
  ACME_AGENT_PUBLIC_URL: "https://acme-agent.example.com:9443"
```

- `ACME_PUBLIC_URL` 是浏览器实际访问管理端的来源，用于安全 Cookie 和 CSRF Origin 校验；
- `ACME_AGENT_PUBLIC_URL` 是 Agent 实际连接的 HTTPS 地址，Server 会按它的 IP 或域名签发内部服务端证书；
- 两个地址可以使用同一主机，也可以使用不同域名；
- 应在注册第一个 Agent 前确定 `ACME_AGENT_PUBLIC_URL`，避免已有 Agent 保存旧地址后需要重新注册。

纯内网部署可以直接使用内网 IP：

```yaml
environment:
  ACME_PUBLIC_URL: "http://192.168.10.20:8080"
  ACME_AGENT_PUBLIC_URL: "https://192.168.10.20:9443"
```

远程 Agent 场景不得把 `ACME_AGENT_PUBLIC_URL` 配置为 `https://127.0.0.1:9443`。`127.0.0.1` 在 Agent 主机上表示 Agent 自己，而不是 Server。

修改 `ACME_AGENT_PUBLIC_URL` 后必须重启 Server，使 Agent HTTPS 证书按新地址重新签发。如果已有 Agent 保存的是旧地址，还需要确保旧地址继续可达，或撤销旧身份并重新注册。

### 2.2 从项目根目录的旧绑定目录迁移

如果旧版 Compose 把项目根目录的 `data/`、`deployments/`、`agent-data/` 或 `agent-deployments/` 绑定进容器，新版改为使用 `docs/` 同级目录后，需要先复制数据。不要直接启动空的 `docs/data`，否则会看到一个全新控制面。

停止旧容器并从项目根目录执行；以下操作保留原目录作为回退副本：

```bash
set -eu
test -f data/acme4go.db
test ! -e docs/data/acme4go.db

mkdir -p docs/data docs/deployments
sudo cp -a data/. docs/data/
sudo cp -a deployments/. docs/deployments/
sudo chown -R 65532:65532 docs/data docs/deployments
chmod 700 docs/data
chmod 750 docs/deployments
```

Agent 主机按同样方式迁移：

```bash
set -eu
test -f agent-data/agent/identity.json
test ! -e docs/agent-data/agent/identity.json

mkdir -p docs/agent-data docs/agent-deployments
sudo cp -a agent-data/. docs/agent-data/
sudo cp -a agent-deployments/. docs/agent-deployments/
sudo chown -R 65532:65532 docs/agent-data docs/agent-deployments
chmod 700 docs/agent-data
chmod 750 docs/agent-deployments
```

确认新目录启动、登录、Agent 心跳和文件部署均正常后，再决定是否归档旧目录。

### 2.3 从旧命名卷迁移

首次部署可以跳过本节。如果旧版本已经使用 Docker 命名卷，切换到 `./data`、`./deployments`、`./agent-data` 和 `./agent-deployments` 前必须先迁移。不要在迁移前执行 `docker compose down -v` 或删除旧卷。

先进入部署目录并使用旧实例实际使用的 Compose 文件停止 Server，避免复制正在写入的 SQLite。以下假设旧文件名为 `compose.yaml`；如果使用其他文件名，请调整 `-f` 参数：

```bash
cd docs
docker compose -f compose.yaml stop acme4go
docker volume ls | grep acme4go
```

准备新目录，并将下面两个变量的值替换为 `docker volume ls` 输出中的实际名称。示例启用 `set -eu` 并先检查卷是否存在，避免卷名写错时继续复制空卷：

```bash
set -eu
OLD_SERVER_DATA_VOLUME='your_project_acme4go-data'
OLD_SERVER_DEPLOYMENT_VOLUME='your_project_acme4go-deployments'

docker volume inspect "$OLD_SERVER_DATA_VOLUME" "$OLD_SERVER_DEPLOYMENT_VOLUME"

mkdir -p data deployments
sudo chown -R 65532:65532 data deployments

docker run --rm \
  -v "$OLD_SERVER_DATA_VOLUME:/source:ro" \
  -v "$PWD/data:/target" \
  alpine:3.22 sh -c 'cp -a /source/. /target/'

docker run --rm \
  -v "$OLD_SERVER_DEPLOYMENT_VOLUME:/source:ro" \
  -v "$PWD/deployments:/target" \
  alpine:3.22 sh -c 'cp -a /source/. /target/'

sudo chown -R 65532:65532 data deployments
chmod 700 data
chmod 750 deployments
```

Agent 也必须先停止，再迁移身份和部署目录：

```bash
set -eu
OLD_AGENT_DATA_VOLUME='your_project_acme4go-agent-data'
OLD_AGENT_DEPLOYMENT_VOLUME='your_project_acme4go-agent-deployments'

docker volume inspect "$OLD_AGENT_DATA_VOLUME" "$OLD_AGENT_DEPLOYMENT_VOLUME"

# 使用旧 Agent 实例实际使用的 Compose 文件，此处假设旧文件名为 compose.agent.yaml。
docker compose -f compose.agent.yaml stop agent
mkdir -p agent-data agent-deployments
sudo chown -R 65532:65532 agent-data agent-deployments

docker run --rm \
  -v "$OLD_AGENT_DATA_VOLUME:/source:ro" \
  -v "$PWD/agent-data:/target" \
  alpine:3.22 sh -c 'cp -a /source/. /target/'

docker run --rm \
  -v "$OLD_AGENT_DEPLOYMENT_VOLUME:/source:ro" \
  -v "$PWD/agent-deployments:/target" \
  alpine:3.22 sh -c 'cp -a /source/. /target/'

sudo chown -R 65532:65532 agent-data agent-deployments
chmod 700 agent-data
chmod 750 agent-deployments
```

使用新的 Compose 配置启动并确认管理端、Agent 心跳和文件部署正常后，再按维护策略删除旧命名卷。迁移验证完成前保留旧卷作为回退副本。

## 3. 构建与发布镜像

Server 和 Agent 使用同一个镜像，通过启动命令选择角色，无需分别构建两个制品。

### 3.1 本地构建

直接构建指定版本：

```bash
docker build \
  --build-arg VERSION=1.0.0 \
  -t ccr.ccs.tencentyun.com/36knight/acme4go:1.0.0 \
  .
```

### 3.2 使用脚本打包并推送

项目根目录的 [`package.sh`](../package.sh) 会自动定位 Dockerfile 和项目源码，由 Docker 多阶段构建完成 Vue 前端、Go 后端及最终镜像，无需提前准备二进制或安装本机 Node/Go。脚本使用 Bash，可从其他工作目录通过绝对路径调用。

默认镜像仓库沿用项目现有配置：`ccr.ccs.tencentyun.com/36knight/acme4go`。先登录对应仓库，再指定版本执行：

```bash
docker login ccr.ccs.tencentyun.com
./package.sh 1.0.0
```

脚本会先询问是否构建，构建成功后再询问是否推送，最终镜像为 `ccr.ccs.tencentyun.com/36knight/acme4go:1.0.0`，镜像内程序版本也为 `1.0.0`。Server 和 Agent 共用该镜像。上面的脚本命令在项目根目录执行；无版本参数时使用 `latest`。

自动化构建并推送，跳过询问：

```bash
DOCKER_NO_INTERACTIVE=true ./package.sh 1.0.0
```

只构建、不推送：

```bash
DOCKER_NO_INTERACTIVE=true DOCKER_PUSH_ENABLED=false ./package.sh local
```

自定义仓库、镜像和平台，例如推送阿里云 ARM64 镜像：

```bash
docker login registry.cn-shanghai.aliyuncs.com
DOCKER_REGISTRY=registry.cn-shanghai.aliyuncs.com \
DOCKER_IMAGE_NAME=example/acme4go \
DOCKER_PLATFORM=linux/arm64 \
DOCKER_NO_INTERACTIVE=true \
./package.sh 1.0.0-arm64
```

| 配置 | 默认值 | 说明 |
|---|---|---|
| `DOCKER_REGISTRY` | `ccr.ccs.tencentyun.com` | Registry 地址，不带协议 |
| `DOCKER_IMAGE_NAME` | `36knight/acme4go` | 命名空间与镜像名 |
| `DOCKER_TAG` | `latest` | 命令行版本参数优先 |
| `DOCKER_PLATFORM` | `linux/amd64` | 每次构建一个平台 |
| `DOCKER_PUSH_ENABLED` | `true` | `false` 时只构建 |
| `DOCKER_NO_INTERACTIVE` | `false` | 自动化调用设为 `true` |
| `SKIP_DOCKER_CONFIRM` | `false` | 同样支持跳过询问 |

查看完整参数：`./package.sh --help`。构建失败立即终止，不推送同标签旧镜像；推送失败返回非零退出码。脚本复用本机已有 Docker 登录状态，不读取或保存仓库密码。

旧入口 [`docs/build-and-push-aliyun.sh`](build-and-push-aliyun.sh) 保留，改为调用同一打包逻辑，仍默认免交互，并兼容 `IMAGE`、`TAG`、`VERSION`、`PLATFORM`、`PUSH`、`DOCKERFILE`、`CONTEXT`、`ALIYUN_REGISTRY`、`ALIYUN_NAMESPACE`。它的默认仓库同样是上述腾讯云地址，文件名仅作为历史兼容入口保留。

默认构建上下文排除了根目录和 `docs/` 下约定的环境配置、Server/Agent 数据及部署目录。自定义数据目录、秘密文件或构建上下文时，应同步维护相应的 `.dockerignore`。

## 4. 启动 Server

### 4.1 单节点 Server（默认推荐）

单节点模式默认启用本机执行器。管理端、ACME 调度、Agent API 与本机证书部署位于同一进程，但两个网络入口仍然分离。

从仓库根目录复制 [`compose.yaml`](compose.yaml) 模板到本地配置副本；如果副本已经存在，不会覆盖已有配置：

```bash
if [ ! -e docs/compose.local.yaml ] && [ ! -L docs/compose.local.yaml ]; then
  (umask 077; cp docs/compose.yaml docs/compose.local.yaml)
fi
```

编辑 `docs/compose.local.yaml`。常用配置只有主密钥、首次管理员密码和两个访问地址；其余应用参数使用内置默认值。文件没有外部环境变量引用，不需要 `.env` 或 `--env-file`。

```yaml
environment:
  ACME_MASTER_KEY: "填写至少 32 字节的主密钥"
  ACME_ADMIN_PASSWORD: "填写至少 12 位的初始管理员密码"
  ACME_PUBLIC_URL: "http://127.0.0.1:8080"
  ACME_AGENT_PUBLIC_URL: "https://127.0.0.1:9443"
```

首次部署可用 `openssl rand -base64 48` 生成主密钥；已有实例必须将原主密钥原样迁入，不能重新生成。远程浏览器或 Agent 访问时，将对应地址改为服务器实际 IP 或域名。宿主机端口直接修改 `ports` 冒号左侧的数字，并同步修改公开地址。

`image` 固定为 `ccr.ccs.tencentyun.com/36knight/acme4go:1.0.0`，需要其他仓库或版本就直接改这一行。先通过 `package.sh` 发布该标签或在本机构建同名镜像；使用私有仓库时先执行 `docker login ccr.ccs.tencentyun.com`。

以下命令在 `docs/` 中执行。镜像以 UID/GID `65532` 运行，启动前准备好挂载目录：

```bash
cd docs
chmod 600 compose.local.yaml
mkdir -p data deployments
sudo chown -R 65532:65532 data deployments
chmod 700 data
chmod 750 deployments
docker compose -f compose.local.yaml up -d
docker compose -f compose.local.yaml logs -f acme4go
```

Compose 直接绑定同级 `./data` 和 `./deployments`，对应容器内 `/app/data`、`/app/deployments`，与程序默认路径一致。原 `.env` 中的主密钥、公开地址、通知等自定义配置需显式迁入 Compose 才会继续生效。真实凭据仅填写在受控部署副本中，不提交到代码仓库；本地副本已从 Git 和 Docker 构建上下文排除。值中若包含字面量美元符号，按 Compose 语法写为 `$$`。

检查状态：

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
curl --insecure --fail https://127.0.0.1:9443/healthz
```

最后一条命令中的 `--insecure` 只用于本机连通性检查。正式 Agent 会使用 Join URL 中的内部 CA 验证 Server，不会跳过证书验证。

管理端优先使用 `ACME_PUBLIC_URL` 对应地址访问。CSRF 校验同时接受当前请求本身的同源 Origin，因此本机通过 `localhost` 或 `127.0.0.1` 访问都能正常退出、注册 Agent 和执行其他写操作；不同协议、端口或真正的跨站来源仍会被拒绝。

### 4.2 纯控制平面 Server

如果 Server 不应在本机写出业务证书，在 `compose.local.yaml` 现有 `environment` 下增加一行：

```yaml
ACME_LOCAL_DEPLOYMENT_ENABLED: "false"
```

关闭本机执行器后，Server 不允许创建 `LOCAL` 部署目标，所有部署目标必须选择一个已注册 Agent。

端口建议：

- `8080` 仅供管理端 HTTPS 反向代理访问；
- `9443` 供 Agent 直连；
- 防火墙可以把 `9443` 来源限制为 Agent 所在出口网段；
- 如果必须让负载均衡承载 `9443`，应使用 TCP 透传，使 TLS 和客户端证书仍由 Acme4go 进程终止。

## 5. 注册并启动远程 Agent

Agent 没有独立 Web 面板，也不提供健康检查端口。Agent 状态统一在 Server 管理端的“部署 Agent”页面查看，运行错误通过 Agent 容器日志或 systemd 日志排查。

### 5.1 注册前检查网络

在 Agent 主机上确认 Server 的 Agent 通道可达：

```bash
curl --insecure --fail https://192.168.10.20:9443/healthz
```

如果使用内网域名，还应确认 DNS 解析结果正确：

```bash
getent hosts acme-agent.example.com
```

`curl --insecure` 仅验证网络连通性。正式注册会使用 Join URL 携带的内部 CA 完成严格 TLS 校验。

### 5.2 在管理端生成 Join URL

1. 登录 Server 管理端；
2. 打开“部署 Agent”；
3. 点击“注册 Agent”；
4. 输入便于识别的名称，例如 `shanghai-nginx-01`；
5. 生成并复制完整的 `acme4go://join/...` URL。

Join URL 包含：

- Agent 应连接的 Server 地址；
- 一次性注册令牌；
- Acme4go 内部 CA 证书。

安全限制：

- 只在本次生成窗口展示；
- 十分钟后过期；
- 成功注册一次后立即失效；
- 有效期内等同于 Agent 注册凭据，不要发送到群聊、工单或提交到 Git。

### 5.3 使用 Docker Compose 启动 Agent

目标主机需要 [`compose.agent.yaml`](compose.agent.yaml) 模板，无需 `agent.env`。在仓库根目录执行以下命令，复制模板；如果本地副本已经存在，不会覆盖已有配置：

```bash
if [ ! -e docs/compose.agent.local.yaml ] && [ ! -L docs/compose.agent.local.yaml ]; then
  (umask 077; cp docs/compose.agent.yaml docs/compose.agent.local.yaml)
fi
```

如果目标主机没有仓库，先将模板复制到部署目录，再创建同名本地副本。修改 `compose.agent.local.yaml` 的 `image` 为实际发布的镜像，并在 `environment` 中填入管理端刚生成的完整 Join URL：

```yaml
environment:
  ACME_AGENT_JOIN_URL: "acme4go://join/完整注册地址"
```

进入保存该文件的部署目录（以下以 `docs/` 为例），准备目录并启动：

```bash
cd docs
chmod 600 compose.agent.local.yaml
mkdir -p agent-data agent-deployments
sudo chown -R 65532:65532 agent-data agent-deployments
chmod 700 agent-data
chmod 750 agent-deployments
# 私有仓库首次拉取前登录
docker login ccr.ccs.tencentyun.com
docker compose -f compose.agent.local.yaml pull
docker compose -f compose.agent.local.yaml up -d
docker compose -f compose.agent.local.yaml logs -f agent
```

默认镜像为 `ccr.ccs.tencentyun.com/36knight/acme4go:1.0.0`。Agent 名称在管理端生成注册信息时填写；日志出现“Agent 注册成功”和“Agent 已启动”后，回到管理端确认节点 `ONLINE`、平台和版本正确、最后在线时间持续更新。

身份、客户端证书和私钥保存在 Compose 同级 `./agent-data` 中，部署文件保存在 `./agent-deployments` 中，分别绑定到 `/app/data` 和 `/app/deployments`。目录必须授予 UID/GID `65532`；身份目录必须持久化，且不能同时供多个 Agent 副本使用。

注册成功后直接清空 `compose.agent.local.yaml` 中的一次性 Join URL：

```yaml
ACME_AGENT_JOIN_URL: ""
```

重新创建容器，使一次性凭据从容器环境中移除：

```bash
docker compose -f compose.agent.local.yaml up -d --force-recreate
```

只要原身份目录继续挂载，后续启动不再需要 Join URL。身份目录丢失时，需撤销旧 Agent、生成新 Join URL 并重新注册。轮询、日志级别等高级配置仅在需要时直接添加到 Compose 的 `environment` 下。

### 5.4 直接运行 Agent 二进制

先构建二进制：

```bash
make build VERSION=0.1.0
```

在 Agent 主机创建独立系统用户和目录：

```bash
sudo useradd --system --home /var/lib/acme4go-agent --shell /usr/sbin/nologin acme4go
sudo install -m 0755 bin/acme4go /usr/local/bin/acme4go
sudo install -d -m 0700 -o acme4go -g acme4go /var/lib/acme4go-agent
sudo install -d -m 0750 -o acme4go -g acme4go /var/lib/acme4go-deployments
sudo install -d -m 0750 /etc/acme4go
```

创建 `/etc/acme4go/agent.env`：

```dotenv
ACME_ROLE=agent
ACME_AGENT_NAME=shanghai-nginx-01
ACME_AGENT_JOIN_URL=acme4go://join/<完整注册地址>
ACME_AGENT_POLL_INTERVAL=30s
ACME_DATA_DIR=/var/lib/acme4go-agent
ACME_DEPLOYMENT_DIR=/var/lib/acme4go-deployments
```

限制配置权限：

```bash
sudo chown root:acme4go /etc/acme4go/agent.env
sudo chmod 0640 /etc/acme4go/agent.env
```

创建 `/etc/systemd/system/acme4go-agent.service`：

```ini
[Unit]
Description=Acme4go certificate deployment agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=acme4go
Group=acme4go
Environment=ACME_ENV_FILE=/etc/acme4go/agent.env
ExecStart=/usr/local/bin/acme4go agent
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/acme4go-agent /var/lib/acme4go-deployments

[Install]
WantedBy=multi-user.target
```

加载并启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now acme4go-agent
sudo journalctl -u acme4go-agent -f
```

确认注册成功后，从 `/etc/acme4go/agent.env` 删除或清空 `ACME_AGENT_JOIN_URL`，再重启服务：

```bash
sudo systemctl restart acme4go-agent
```

Agent 的持续身份保存在 `/var/lib/acme4go-agent/agent/identity.json`，权限为 `0600`。

## 6. 为 Agent 创建文件部署目标

Agent 注册成功只表示身份和通信已经建立，不会自动获得所有证书。必须在管理端显式创建部署目标：

1. 打开“文件部署”；
2. 点击“新建目标”；
3. 执行节点选择刚注册的 Agent；
4. 填写目标名称；
5. 从下拉框选择证书系列；系列编码、名称和完整域名组合会同时展示；
6. 设置目录别名，例如 `nginx-public`；
7. 启用自动部署并保存。

如果已经存在匹配证书，Agent 会在下一次对账时下载并验证证书，然后写入：

```text
<ACME_DEPLOYMENT_DIR>/nginx-public/
├── active.version
├── current -> versions/<SHA256 指纹>
└── versions/<SHA256 指纹>/
    ├── certificate.pem
    ├── fullchain.pem
    ├── private-key.pem
    └── metadata.json
```

业务应用应固定读取：

```text
nginx-public/current/fullchain.pem
nginx-public/current/private-key.pem
```

Agent 会校验证书指纹、SAN、有效期以及公私钥匹配关系，写入新版本并完成 `fsync` 后才原子切换 `current`。失败不会覆盖当前可用版本。

管理端可以从两个位置观察结果：

- “部署 Agent”：节点在线状态、平台、版本、目标数量、最后在线时间和身份到期时间；
- “文件部署”：目标状态、当前证书、最后切换时间、错误信息和部署记录。

## 7. 将证书挂载给业务容器

### 7.1 Server 本机执行器

把 Server Compose 同级的 `./deployments` 目录只读挂载给业务容器。下面的业务 Compose 文件应与 `deployments` 目录位于同一级：

```yaml
services:
  nginx:
    image: nginx:alpine
    volumes:
      - ./deployments:/etc/acme4go:ro
```

### 7.2 Agent 执行器

把 Agent 的同级 `./agent-deployments` 目录只读挂载给同一主机上的业务容器。下面的业务 Compose 文件应与 `agent-deployments` 目录位于同一级：

```yaml
services:
  nginx:
    image: nginx:alpine
    volumes:
      - ./agent-deployments:/etc/acme4go:ro
```

宿主机目录结构示例：

```text
docs/
├── compose.agent.local.yaml
├── agent-data/
└── agent-deployments/
```

证书和证书链权限为 `0644`，私钥和元数据权限为 `0640`。容器镜像默认使用 UID/GID `65532`；非 root 业务容器如果无法读取私钥，应通过只读目录挂载和补充组授予 GID `65532` 的读取权限，不要把私钥改成全局可读。

Nginx 配置示例：

```nginx
ssl_certificate     /etc/acme4go/nginx-public/current/fullchain.pem;
ssl_certificate_key /etc/acme4go/nginx-public/current/private-key.pem;
```

Acme4go 不执行 SSH、Docker Socket、Nginx reload 或任意命令。业务应用应自行定期重读证书，或部署一个权限受限的 watcher 观察 `active.version` 变化后只发送固定 reload 信号。

## 8. 管理端反向代理

下面的 Nginx 配置只代理管理端 `8080`：

```nginx
server {
    listen 443 ssl http2;
    server_name acme-admin.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

对应配置必须是：

```dotenv
ACME_PUBLIC_URL=https://acme-admin.example.com
ACME_TRUSTED_PROXY_CIDRS=127.0.0.1/32
```

应用根据 `ACME_PUBLIC_URL` 判断 Cookie 的 Secure 属性并执行 CSRF Origin 校验。示例中的 Nginx 与 Acme4go 在同一主机，所以只信任 `127.0.0.1/32`；若代理运行在容器网络，应填写实际且尽可能窄的代理网段。未配置可信代理时，应用忽略所有转发头。

不要把 Agent `9443` 配置到上述普通 HTTP 反向代理中。如果 `acme-agent.example.com:9443` 经过负载均衡，必须使用四层 TCP 透传。

## 9. Secret 与编排平台

敏感环境变量支持 `_FILE`：

```dotenv
ACME_MASTER_KEY_FILE=/run/secrets/acme_master_key
ACME_ADMIN_PASSWORD_FILE=/run/secrets/acme_admin_password
```

同名普通变量和 `_FILE` 同时存在时，文件优先。Secret 文件末尾换行会被自动去除，实际主密钥仍必须不少于 32 字节。

Kubernetes 部署要求：

- Server 使用单副本和 `Recreate` 策略；
- Server 数据卷使用 `ReadWriteOnce`；
- 不要让两个 Server 同时指向同一个 SQLite 数据卷；
- Agent 可以使用单副本 Deployment、DaemonSet 的独立 PVC 模板或宿主服务；
- 每个 Agent 身份目录或数据卷只能对应一个 Agent 实例；
- `9443` 使用四层 Service 或直连，不能由会终止客户端证书的普通 Ingress 代转。

## 10. 健康检查与日志

Server 健康接口：

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
curl --insecure --fail https://127.0.0.1:9443/healthz
```

- `/healthz` 表示进程存活；
- `/readyz` 表示 Server 初始化完成；
- `/readyz` 会实际检查 SQLite 连接可用性和当前 Agent TLS 服务端证书，而不是返回固定成功；
- Docker Compose 健康检查使用 `ACME_LISTEN_ADDRESS` 对应的容器内监听地址；更改端口时还需同步调整端口映射与公开地址；
- Agent 通道的 `/healthz` 不要求客户端证书，但服务端证书仍由内部 CA 签发；
- 健康接口不会触发外部 ACME 或 DNS 测试；
- 阿里云 DNS 权限应通过管理端“DNS 凭据”的显式测试操作验证。

Docker 日志：

```bash
cd docs
docker compose -f compose.local.yaml logs -f acme4go
docker compose -f compose.agent.local.yaml logs -f agent
```

Server 和 Agent 的 Compose 配置统一使用 Docker `local` 日志驱动。每个日志分片最大 `20 MB`，每个容器最多保留 `5` 个分片，旧分片由 Docker 自动压缩并在超出数量后删除，因此单个容器最多保留约 `100 MB` 的控制台日志。日志仍通过上述 `docker compose logs` 命令查看，不需要挂载或维护应用日志目录。

日志驱动参数只在创建容器时生效。升级包含该配置的 Compose 文件后，需要重新创建现有容器：

```bash
docker compose -f compose.local.yaml up -d --force-recreate
docker compose -f compose.agent.local.yaml up -d --force-recreate
```

systemd Agent 日志：

```bash
sudo journalctl -u acme4go-agent -f
```

Agent 使用独立任务固定每 30 秒发送心跳，单次请求最多等待 10 秒；部署轮询间隔、慢下载和身份续签不会推迟心跳。管理端超过四个心跳周期（2 分钟）未收到心跳后显示离线。客户端身份证书有效期为 180 天，并在到期前 30 天自动续签。

Agent 业务入口总并发仍为 8 个，拆分为普通请求全局 6 个、单节点 2 个，以及心跳/身份续签独立共享的全局 2 个、单节点 1 个。超出单节点并发返回 429，类别全局额度耗尽返回 503；小型 POST 请求体读取期限为 5 秒，认证查询另有最多 8 个并发、每次最多 2 秒的预算。单节点总请求仍限制为每分钟 600 次；部署结果另行限制为每分钟 120 次，十分钟内完全相同的结果只保留一条。维护任务按 `ACME_HISTORY_RETENTION`（默认 730 天）清理部署记录，并限制到最近 100000 条，但始终保留每个启用目标最近一次成功回执作为在用证书监控依据。

部署回执必须携带目标配置的 `revision`，过期修订返回 409，缺失修订返回 400。包含该协议修复的版本需要配套升级 Server 和全部 Agent；升级前后的协议不应长期混用。配置与恢复行为详见 [运维查询与通知](operations-and-notifications.md)。

## 11. 备份与升级

### 11.1 Server 备份

必须成组备份：

- Server `./data` 目录；
- `ACME_MASTER_KEY` 或对应 Secret；
- 必要时备份 `./deployments`，它可以由加密证书库重新物化，但备份有助于快速恢复当前运行状态。

备份前应停止旧 Server 或使用底层存储的一致性快照。不要直接复制一个仍在频繁写入的 SQLite 文件作为唯一备份。

Server 归档可以解压到新的数据目录，并将 `ACME_DATA_DIR` 指向归档中的 `data` 目录。应完整保留 `certificates` 下的层级和文件名，使用同一主密钥，在停止 Server 的恢复环境中先运行 `acme4go doctor`。新材料使用相对证书库的路径；旧绝对路径会受约束地映射到当前目录，不读取原位置。数据库契约版本 6 用于阻止旧程序误读新路径，回退程序须使用升级前备份。诊断只读检查已有材料，不初始化缺失数据库或 CA；通过诊断后仍需进行实际服务运行验收。

### 11.2 Agent 备份

- `./agent-data` 包含 Agent 身份，丢失后必须重新注册；
- `./agent-deployments` 包含业务当前读取的证书文件；
- Agent 缓存和部署文件可以从 Server 重新收敛，但身份私钥不能从 Server 恢复。

### 11.3 升级顺序

1. 备份完整 Server `./data` 目录和主密钥；
2. 执行 `make verify` 或使用已验证镜像；
3. 停止旧 Server；
4. 使用新镜像启动单个 Server；
5. 检查 `/readyz`、管理端登录、Agent 心跳和一个部署目标；
6. Agent 按节点逐台滚动升级；
7. 确认新 Agent 版本、心跳和部署记录正常。

Server 启动时会自动应用 `internal/store/schema.sql`，再依据 `schema_migrations` 执行尚未完成的 Go 幂等迁移；不需要也不应手工执行 `migrations/*.sql`。引入版本记录后的第一次启动会执行并登记现有迁移，后续启动只做轻量版本与结构检查，不会重复归并历史数据或重建证书版本唯一索引。迁移失败时不会写入完成记录，Server 会直接启动失败并保留错误日志；此时应停止重试、保留升级前备份并排查原因，不能用历史 SQL 文件强行补表。应用当前只支持向前自动升级；旧镜像检测到数据库迁移版本更高时会拒绝启动，回退前必须恢复与旧镜像匹配的数据库备份。

不要同时启动两个 Server 指向同一 SQLite 数据目录。升级 Agent 时也不要让新旧两个容器同时使用同一个 Agent 身份目录。

## 12. 常见故障排查

### 12.1 管理端登录、退出或写操作返回 CSRF 错误

先确认请求携带登录响应返回的 `X-CSRF-Token`，再确认浏览器 Origin 与当前地址的协议、主机和端口一致。本机可以分别通过 `http://localhost:<端口>` 或 `http://127.0.0.1:<端口>` 使用，但不能让页面来自一个地址、API 请求却发送到另一个地址。HTTPS 反向代理还应正确设置 `ACME_PUBLIC_URL`；只有确实需要读取转发头时才配置 `ACME_TRUSTED_PROXY_CIDRS`。

### 12.2 Agent 无法连接或一直离线

在 Agent 主机执行：

```bash
curl --insecure --fail https://<Server 地址>:9443/healthz
```

然后检查：

- `ACME_AGENT_PUBLIC_URL` 是否误用了 `127.0.0.1`；
- Agent 主机是否能解析配置中的域名；
- Server 防火墙和安全组是否允许 `9443`；
- 中间代理是否错误终止了 mTLS；
- Server 修改 Agent 地址后是否已经重启；
- Agent 日志是否包含证书主机名不匹配、连接拒绝或超时。

### 12.3 注册令牌无效、已使用或已过期

Join URL 十分钟过期且只能使用一次。回到管理端重新生成，不要重复使用旧 URL。

### 12.4 Agent 重启后要求新的 Join URL

说明 Agent 没有找到原身份文件。检查：

- `./agent-data` 目录是否仍然存在；
- 新容器是否把正确目录挂载到 `/app/data`；
- 直接运行时 `ACME_DATA_DIR` 是否发生变化；
- 是否在另一个工作目录复制了 `compose.agent.local.yaml`，导致同级相对挂载路径指向新的空目录。

如果原身份目录已经丢失，应在管理端撤销旧 Agent，然后生成新 Join URL 重新注册。

### 12.5 Agent 在线但没有部署证书

注册不会自动分发所有证书。确认：

- 已在“文件部署”中新建目标；
- 执行节点选择的是当前 Agent；
- 目标已经绑定证书系列，且该系列存在有效的当前证书版本；
- 目标处于启用状态；
- Agent 日志和“部署记录”中没有指纹、SAN、私钥或文件权限错误。

### 12.6 文件已经更新，但 Nginx 或应用仍使用旧证书

Acme4go 只负责原子更新文件，不执行 reload。检查业务应用是否：

- 读取 `current/fullchain.pem` 和 `current/private-key.pem`；
- 正确挂载了当前 Agent 或 Server 的部署目录；
- 有权限读取 `0640` 的私钥；
- 在 `active.version` 变化后执行了受控 reload 或自动重读。

## 13. 容量与资源建议

小规模部署（数百张证书、数十个 Agent）可以从以下资源开始：

- Server：1 vCPU、256–512 MiB 内存、1 GiB 持久盘；
- Agent：0.1 vCPU、32–64 MiB 内存、100 MiB 持久盘；
- DNS 签发任务默认最多并发 2 个，避免触发 CA 和 DNS API 限流；
- 保留历史证书版本用于回退，并按组织保留策略清理无引用的旧目录。
