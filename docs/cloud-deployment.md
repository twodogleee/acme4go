# 阿里云证书上传与云产品部署

管理端新增独立的“云部署”入口，包含“证书上传”和“产品部署”。云部署凭据统一在“凭据管理”维护。文件部署继续通过本机或 Agent 交付 PEM；云部署由 Server 使用阿里云 API 执行，不要求部署 Agent。

## 1. 三种使用方式

| 需求 | 配置方式 | 执行结果 |
| --- | --- | --- |
| 只上传、不部署 | 创建上传规则，选择云部署凭据和本地证书系列 | 证书链和私钥上传到阿里云证书中心，记录云证书 ID |
| 部署阿里云已有证书 | 创建产品目标，来源选择“阿里云已有证书” | 分页搜索并选择证书，直接更新 OSS/CDN 的证书绑定，不重新上传 |
| 本地续期后自动部署 | 创建产品目标，来源选择本地系列，开启自动同步 | 使用当前版本；需要时上传，再更新产品绑定并核验 |

部署目标本身可以完成“上传 + 部署”，无需事先创建上传规则。多个目标使用同一凭据、同一证书版本时共享上传映射。一张证书可以分别部署到多个域名和多个产品。

自动同步默认关闭。关闭时，保存配置后点击“立即上传”或“立即部署”；开启时，保存后会处理当前证书，并跟随后续签发的新版本。证书系列本身也需要开启自动续期，才能在到期前持续产生新证书。

续期产生新的云证书 ID，产品绑定随目标任务切换。固定选择的“已有云证书”不会按名称猜测后继证书；要使用更新后的云证书，需要编辑目标重新选择并执行。

## 2. 接入流程与资源范围

1. 在“凭据管理”中保存具备所需 RAM 权限的 AccessKeyId 和 AccessKeySecret，并勾选“云部署”。同一份密钥可以同时勾选“DNS 验证”；仅云部署时无需填写根域名。
2. 只上传时创建上传规则。部署时选择证书来源和云产品，并填写下面的资源定位信息。
3. 选择手动执行或自动同步，在“执行记录”中查看排队、上传、校验云证书、更新产品、确认生效和最终结果。

| 产品 | 必填字段 | 前提 |
| --- | --- | --- |
| CDN | 加速域名，例如 `cdn.example.com` | 域名已接入阿里云 CDN；证书覆盖该域名 |
| OSS | Bucket 名称、地域（例如 `cn-hangzhou`）、自定义域名 | 普通 Bucket 已有启用的精确自定义域名绑定；证书覆盖该域名 |

当前使用阿里云中国站证书中心 `cn-hangzhou`，账号采用静态 AccessKey 授权。OSS 目标地域可独立填写。暂未支持国际站证书中心、STS/角色授权、Agentic Bucket、泛域名 CNAME、DCDN、负载均衡或其他产品。

OSS 前面使用 CDN 时，客户端 HTTPS 通常在 CDN 终止，应创建 CDN 目标。如果同时需要 OSS 自定义源站域名的 HTTPS，另建 OSS 目标。系统不会创建 Bucket、CDN 域名、DNS 记录或 OSS 域名绑定。

## 3. RAM 权限

以下为本实现调用的权限清单。按实际启用的能力组合授权；支持资源级授权的操作可进一步限定到实际域名和 Bucket。

| 能力 | Action |
| --- | --- |
| 浏览/读取云证书 | `yundun-cert:ListUserCertificateOrder`、`yundun-cert:GetUserCertificateDetail` |
| 上传本地证书 | 以上查询权限，以及 `yundun-cert:UploadUserCertificate` |
| CDN 部署与核验 | `cdn:SetCdnDomainSSLCertificate`、`cdn:DescribeDomainCertificateInfo` |
| OSS 部署与核验 | `oss:ListCname`、`oss:PutCname` |
| OSS 绑定证书的关联权限 | `yundun-cert:DescribeSSLCertificatePrivateKey`、`yundun-cert:DescribeSSLCertificatePublicKeyDetail`、`yundun-cert:CreateSSLCertificate` |

仅上传的策略示例：

```json
{
  "Version": "1",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "yundun-cert:ListUserCertificateOrder",
      "yundun-cert:GetUserCertificateDetail",
      "yundun-cert:UploadUserCertificate"
    ],
    "Resource": "*"
  }]
}
```

OSS 的关联权限来自官方 [PutCname 权限说明](https://help.aliyun.com/zh/oss/developer-reference/putcname)。本程序向 OSS/CDN 提交云证书 ID，不主动从证书中心下载已有云证书的私钥。

其他接口与权限参考：[上传证书](https://help.aliyun.com/zh/ssl-certificate/developer-reference/api-cas-2020-04-07-uploadusercertificate)、[证书列表](https://help.aliyun.com/zh/ssl-certificate/developer-reference/api-cas-2020-04-07-listusercertificateorder)、[证书详情](https://help.aliyun.com/zh/ssl-certificate/developer-reference/api-cas-2020-04-07-getusercertificatedetail)、[CDN 更新证书](https://help.aliyun.com/zh/cdn/developer-reference/api-cdn-2018-05-10-setcdndomainsslcertificate)、[CDN 查询证书](https://help.aliyun.com/zh/cdn/developer-reference/api-cdn-2018-05-10-describedomaincertificateinfo)、[OSS 查询域名](https://help.aliyun.com/zh/oss/developer-reference/listcname)。

## 4. 任务执行、续期与恢复

任务先入 SQLite 持久队列，再由独立工作者串行执行，HTTP 请求立即返回任务 ID。单次任务最长执行三分钟，不阻塞 ACME 签发或文件部署。启动和每三十秒扫描自动规则；本地新证书签发后会主动唤醒工作者。

- **幂等上传**：按账号和 SHA-256 指纹复用云证书 ID；上传使用确定名称及 ClientToken。上传成功但本地登记前进程退出时，重新查询云端指纹恢复。自动查重最多扫描一万张有效证书，超过后明确失败。
- **证书校验**：上传或复用已有映射后，单独进入“校验云证书”阶段。详情优先读取 `CertSha2`，缺失时兼容 `Sha2`；统一大小写、冒号和空白格式，并要求完整的 64 位十六进制 SHA-256。`Fingerprint` 是 SHA-1，不能代替本地 SHA-256。字段缺失或无效与真实指纹不一致分别报错，都不会继续部署。列表查重遇到同名或同指纹证书时，必须经详情确认后才复用。
- **重启恢复**：将中断的运行任务重新入队，保留已有上传结果；部署前读取产品当前绑定，已经生效则直接确认。
- **真实结果**：CDN 精确匹配加速域名，要求 HTTPS 为 `on`。接口返回 `ServerCertificate` 时，解析第一张叶证书并核对 SHA-256 指纹，允许 CDN 与证书中心的 ID 不同；证书内容缺失时才回退到非空证书 ID 一致。有 `Status` 时要求其为 `success`；实际响应缺少该字段或值为 null/空白时，只有叶证书指纹一致且 HTTPS 已开启才确认成功，仅 ID 一致不足以放行。明确的 `checking`、`failed` 等非成功状态以及格式错误仍不通过。无效证书内容或指纹冲突不能通过 ID 相同绕过。OSS 核对域名证书 ID 与启用状态。这是云端配置核验，不等同于从每个公网边缘节点进行 TLS 探测。
- **核验诊断**：提交部署后立即查询，之后每三秒核验；运行记录显示最近的证书身份、HTTPS 开关和证书状态检查结果。超时会明确显示“本地确认超时，不代表云端部署失败”，并保留最近核验原因；查询 API 失败与状态尚未匹配分别说明，不保存原始响应或证书材料。
- **失败重试**：自动规则对同一配置修订、同一证书版本最多尝试三次，间隔至少五分钟；仍失败可手动重新执行。上传成功、部署失败时保留上传映射。三分钟内未确认生效会记录失败，云端操作可能仍在传播，重试先重新核验。
- **版本隔离**：排队时固定本地版本，执行时发现已被新版本替代会拒绝旧任务；自动规则会另外安排当前版本。
- **并发保护**：同一目标只有一个活动任务，运行中不能编辑、删除目标或修改关联账号。OSS 更新已有证书时携带 PreviousCertId，拒绝在查询后发生的并发覆盖；首次绑定证书按接口要求使用 Force。CDN 接口没有同等的比较更新参数，应避免其他系统同时管理同一域名证书。
- **保留云资源**：删除本地配置不会删除云证书或解绑产品。旧云证书保留，便于人工切回；当前未实现自动清理或一键回滚。已产生配置或映射的账号只能停用，不能删除。

固定云证书被外部删除、权限撤销或域名绑定移除时，任务会失败并保留说明。对本系统已登记的云证书，不应在阿里云手动删除仍被管理的版本。

阿里云显示“上传成功”，本系统却在证书校验阶段失败，表示云端证书已存在，后续校验或部署尚未完成。更新服务后，在原规则或目标上点击“立即上传”或“立即部署”创建新的执行记录；仍为同一本地版本、同一云凭据时会复用已保存的云证书 ID，无需删除云证书或重新申请。旧失败记录保留。如新记录仍提示指纹不一致，按错误中的本地与云端 SHA-256 对照证书详情，不能关闭校验或只按域名放行。

凭据合并、用途权限和升级说明见[统一凭据管理](credentials.md)。

阿里云 CDN 控制台正常、本系统却在“确认生效”阶段超时，表示部署请求已被接受，但在任务期限内没有满足本地核验条件，不能仅据此判断云端更新失败。更新服务后重新执行目标即可先核验现有绑定；相同证书内容能够通过 SHA-256 确认，即使 CDN 返回的证书 ID 与证书中心不同，也无需再次部署。原失败记录作为历史保留。若新任务仍无法确认，查看记录中的指纹、HTTPS 和证书状态诊断，区分真实证书不一致、云端尚在检测、缺失字段或查询失败。

## 5. 安全与持久化

AccessKeyId 和 AccessKeySecret 使用现有 Vault 分别加密；账号响应只有脱敏标识。任务、规则配置和上传映射不保存证书私钥。已有云证书查询使用 `CertFilter=true`，上传材料经 HTTPS 请求正文发送；禁止凭据请求重定向到任意地址。测试 CA、域名不匹配或过期证书不会被用于产品部署。

云部署使用 `cloud_plans`、`cloud_uploads`、`cloud_jobs`，直接引用统一凭据，不建立独立云账号表。版本 7 初始化云部署表，版本 9 扩展现有凭据的用途字段，版本 10 修复早期版本遗留的空云表结构。旧云账号数据不做自动迁移；只有确认旧云表均为空才重建，存在记录时会保留数据并明确中止升级。现有 SQLite 一致备份包含这些表，恢复时仍需原主密钥。升级前按现有备份流程保存数据。

## 6. 模块与后续扩展

```text
Vue 云部署页面 → /api/cloud/* → app 编排与持久任务 → store
                                      │
                                      ├─ cloud.Catalog → 阿里云证书中心
                                      └─ cloud.Deployer → CDN / OSS / 后续产品
```

| 模块 | 职责 |
| --- | --- |
| `internal/cloud/provider.go` | 证书仓库、产品适配器和界面字段描述契约，不依赖 app/store |
| `internal/cloud/aliyun` | 官方 SDK 调用、证书元数据、CDN/OSS 的产品参数及核验 |
| `internal/app/cloud.go` | 云凭据引用、互斥证书来源、上传规则与部署目标的业务校验 |
| `internal/app/cloud_worker.go` | 上传复用、任务阶段、续期调度和恢复 |
| `internal/store/cloud.go` | 持久化、活动任务互斥和历史查询 |
| `frontend/src/components/cloud` | 账号管理、已有证书选择器和通用配置表单 |

新增阿里云产品时实现 `cloud.Deployer` 的 `Product`、`Validate`、`Deploy`、`Verify`，在 `aliyun.NewRegistry` 注册。产品所需文本字段由 `Product.Fields` 返回，现有界面可直接渲染；新增需要资源发现、复杂多选等交互的产品时，再扩展字段契约或增加专用表单。应用层上传流程和任务调度无需新增按产品分支。

证书仓库接口 `Catalog` 与产品接口分离；部署适配器不上传证书。当前注册的是一个阿里云证书仓库，不提前引入多云厂商管理模型。

## 7. 验证范围

适配器测试通过模拟 RPC 和 OSS 官方 SDK 的 HTTP 传输验证参数、签名、并发保护、幂等和状态核验；应用测试使用真实临时 SQLite 与加密证书材料验证只上传、已有云证书部署、自动续期、失败重试及重启恢复。

这些测试不调用真实阿里云账户。实际 RAM 策略、产品限制、区域可用性与边缘传播，需要在配置授权后通过真实目标完成接入验收。
