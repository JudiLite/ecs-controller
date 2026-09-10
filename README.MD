# ECS 控制台

一个简单的阿里云 ECS 管理面板，可以查看实例状态和流量，并在流量达到阈值时自动关机，避免产生额外费用。支持精选公共镜像与自定义镜像、多实例轮转与 DDNS 自动切换、Telegram Bot 双向交互控制及费用分析。

> 阿里云流量数据可能存在延迟，自动关机不能保证绝对不会产生费用。

## 部署

准备一台 Linux 服务器。使用域名和 HTTPS 时，请先把域名解析到服务器 IP，并确保 80/443 端口可以访问。当前支持 AMD64 和 ARM64。

在服务器上运行：

```bash
curl -fsSL https://raw.githubusercontent.com/JudiLite/ecs-controller/main/setup.sh \
  | sudo bash
```

脚本会进入部署菜单，可选择 Docker Compose 或原生 systemd/OpenRC 安装，配置域名、HTTPS、端口和容器名称，并提供状态、重启、日志和卸载操作。Docker 模式会自动生成 Compose 配置；填写域名并启用 HTTPS 后，会使用 Caddy 自动申请和续期证书。

安装脚本只负责部署环境，不会询问应用账号、阿里云 AccessKey 或其他业务配置。服务启动后，用浏览器打开安装完成时显示的地址，在首次登录页面完成应用配置。

部署完成后可以直接使用短命令进入服务菜单：

```bash
sudo ecs
```

也可以直接执行操作：

```bash
sudo ecs start
sudo ecs stop
sudo ecs restart
sudo ecs status
sudo ecs logs
```

部署向导完成时会同时在终端打印访问地址、部署目录、容器名称和服务管理命令，方便直接复制；完整信息也会保存到 `/opt/ecs-controller/DEPLOY_INFO.txt`。

```bash
sudo /opt/ecs-controller/setup.sh
```

部署向导会把本次选择保存到 `/opt/ecs-controller/.setup_state`，重新运行时会自动使用上次的配置作为默认值。Docker 部署文件、数据和访问信息位于 `/opt/ecs-controller/`。

如需直接使用底层原生安装器：

```bash
curl -fsSL https://raw.githubusercontent.com/JudiLite/ecs-controller/main/install.sh \
  | sudo sh
```

原生安装器支持 `ECS_HTTP_ADDR`、`ECS_COOKIE_SECURE` 和 `ECS_DATA_DIR` 等环境变量；使用 HTTPS 访问时请设置 `ECS_COOKIE_SECURE=1`。

### 运行配置与环境变量

配置文件位于 `/etc/ecs-controller/ecs-controller.env`，支持以下常用环境变量：

- `ECS_HTTP_ADDR`：服务监听地址与端口（默认 `127.0.0.1:43211`）。服务默认仅监听本机回环地址，建议通过反向代理（如 Nginx、Caddy）配置 HTTPS 访问，请勿直接向公网暴露此端口。
- `ECS_COOKIE_SECURE`：是否仅允许通过 HTTPS 传输 Cookie（`1` 开启，`0` 关闭，默认 `0`）。
- `ECS_DATA_DIR`：数据文件存储目录（默认 `/var/lib/ecs-controller`）。
- `ECS_MONITOR_INTERVAL`：流量与实例状态监控轮询周期，单位为秒（默认 `60`）。
- `TZ`：服务时区（默认 `Asia/Shanghai`）。

### 安装目录

- 稳定的程序入口：`/opt/ecs-controller/current/ecs-controller`
- 实际版本文件：`/opt/ecs-controller/releases/<提交 SHA>/`
- 后台更新程序：`/opt/ecs-controller/updater.sh`
- SQLite 数据库：`/var/lib/ecs-controller/data.sqlite`
- 凭据加密密钥：`/var/lib/ecs-controller/.secret_encryption.key`
- 在线更新状态：`/var/lib/ecs-controller/update/`
- 运行配置：`/etc/ecs-controller/ecs-controller.env`

备份时必须同时备份 `data.sqlite` 和 `.secret_encryption.key`，否则数据库中的 AccessKey Secret 等加密内容可能无法恢复。

### 服务管理

推荐使用统一服务菜单：

```bash
sudo ecs
```

Docker 部署也支持直接使用参数：

```bash
sudo ecs restart
sudo ecs status
sudo ecs logs
```

底层 systemd/OpenRC 命令仍然可用。原生安装修改 `/etc/ecs-controller/ecs-controller.env` 后，需要重启 `ecs-controller` 服务才能生效。Docker 部署会额外运行更新容器，网页在线更新时自动下载、校验 Release、重建应用容器并进行健康检查，失败会自动回滚。

服务默认只监听服务器本机的 `43211` 端口，请通过你现有的 HTTPS 访问方式打开面板，不要直接开放这个端口。

公网使用 Passkey 时必须使用 HTTPS。

### GitHub Release

本项目会通过 GitHub Actions 在 `main` 分支更新后自动构建并发布 Linux AMD64、Linux ARM64、Windows AMD64 和 Windows ARM64 安装包。部署脚本默认从本项目的 [GitHub Releases](https://github.com/JudiLite/ecs-controller/releases) 下载，不依赖上游仓库的 Release。

### 卸载服务

在服务器上运行：

```bash
curl -fsSL https://raw.githubusercontent.com/JudiLite/ecs-controller/main/uninstall.sh \
  | sudo sh
```

卸载脚本会自动停止并移除后台服务、清理安装目录与运行用户。默认**保留**数据目录（`/var/lib/ecs-controller`）与配置目录（`/etc/ecs-controller`），避免误删数据库和密钥。

若需彻底清除（包括数据和配置）：

```bash
curl -fsSL https://raw.githubusercontent.com/JudiLite/ecs-controller/main/uninstall.sh \
  | sudo env ECS_PURGE=1 sh
```

### 本地测试

在自己的电脑上测试时，需要安装 Go 1.26 或允许 Go 自动下载对应工具链。在项目根目录执行：

```bash
./run.sh
```

脚本会先检查并停止当前端口上由本项目启动的旧进程，再启动最新源码。启动后访问 `http://127.0.0.1:43211`。本地数据保存在 `.local/data`，本地源码模式不启用后台在线更新；阿里云相关功能需要在界面中配置账号凭据后才能调用。

## 第一次使用

1. 打开网页，设置管理员用户名和密码，密码至少 6 位。
2. 进入“账号管理”，添加阿里云账号。
3. 在“系统设置”中设置流量阈值、自动关机和通知方式。

## 阿里云权限

请在阿里云 RAM 中创建一个用于本项目的用户，开启“OpenAPI 调用访问”，然后把 AccessKey ID 和 AccessKey Secret 填入账号管理。

项目会根据功能使用以下 RAM 权限策略：

- **实例管理与自定义镜像**：`AliyunECSFullAccess`（ECS 完整管理权限，包含实例查询与启停、镜像列表查询 `DescribeImages`、系统盘及规格预检等）
- **实例流量**：`AliyunCloudMonitorReadOnlyAccess`（CloudMonitor 只读权限，用于获取实例网络流出量）
- **账号流量**：`AliyunCDTFullAccess`（Cloud Data Transfer 完整权限，用于获取公网流出流量）
- **创建实例与网络管理**：`AliyunVPCFullAccess`（VPC 完整权限）和 `AliyunECSFullAccess`（ECS 完整权限，用于创建 VPC、交换机、安全组，申请、绑定及释放 EIP）
- **消费情况与费用分析**：`AliyunBSSReadOnlyAccess`（BSS 账单服务只读权限，用于查询账户余额、账单明细和分项费用）
- **阿里云资源元数据服务角色**：`AliyunServiceRolePolicyForResourceMetaCenter`（由阿里云相关服务创建或使用服务关联角色时可能需要）

如果出现“CloudMonitor 不可用”或“CDT 不可用”，通常是对应的 RAM 权限没有开启。只查看和管理已有实例时，不需要开通创建实例、EIP 和账单权限。

## 主要功能

### 实例与镜像管理
- 查看 ECS 运行状态、配置规格、公网/私网 IP、流量等详细信息。
- **实例创建预检**：输入实例规格后自动预检 CPU 架构（x86_64 / arm64），动态过滤匹配的系统与自定义镜像。
- **精选系统镜像**：内置 Debian 12、Ubuntu 22.04/24.04、CentOS Stream 9、Alibaba Cloud Linux 3、Windows Server 2022 等官方镜像。
- **支持自定义镜像**：
  - 自动读取当前账号在对应地域、同 CPU 架构下的所有自定义镜像并展示镜像体积；
  - **系统盘容量自适应**：针对轻量自定义镜像（如小型 Alpine 镜像等），系统盘最小容量自适应放宽至镜像自身大小（支持 5GB 等轻量容量，无需强制限死 20GB/40GB）；
  - **Cloud-Init 智能适配**：对 Debian、Alpine 等自定义镜像自动注入初始化脚本，自动开启并配置 root 密码认证与 sshd 服务。
- **实例计费与成本优化**：
  - 支持普通按量付费与**抢占式实例（Spot，市场价自动竞价）**，抢占式实例被回收后支持自动重新购买/拉起恢复；
  - 支持普通停机（KeepCharging）与**节省停机模式（StopCharging，停机不收计算资源费用）**。
- **网络与安全自动化**：创建时自动按需规划专属 VPC、VSwitch（自动规划不重叠网段）及安全组（限制客户端 IP 白名单），支持分配普通公网 IP 或**系统托管 EIP**。
- **一键更换公网 IP**：针对使用系统托管 EIP 的实例，支持在控制台一键申请新 EIP、无缝替换并释放旧 EIP。
- **价格预估与配置预览**：创建前提供实时价格估算与配置汇总预览，支持防重提交，创建后可查看初始登录凭据。
- **实例控制**：支持启动、关机以及二次确认安全释放实例。

### 流量保护与运维自动化
- **双重流量监控**：同时监控 CloudMonitor 实例级流量与 CDT 账号级计费流量。
- **灵活保护动作**：任一流量达到设定阈值时执行保护（支持“停机并发送通知”或“仅发送告警”）。
- **超额自动防复跳**：因超额停机后自动阻塞定时开机，防止刚停机又被定时任务拉起造成扣费。
- **定时开关机日程**：支持为实例设置每日定时开机与关机时间（精确到分钟）。
- **自动保活与月度恢复**：支持实例异常停机自动保活（KeepAlive），以及每月 1 号重置流量周期后自动开机恢复。

### 多实例轮转与 DDNS 联动
- **多机轮转分组**：支持将多个账号或实例组成轮转池，按设定的计划周期轮流开机与关机。
- **平滑交接过渡**：支持设置提前开机预热与延迟关机，保障业务在实例切换时平滑不中断。
- **DDNS 自动切换**：集成 **Cloudflare、腾讯云 DNSPod、阿里云 AliDNS**，开机后自动将共享域名动态解析（DDNS）切换到当前运行实例的 IP / EIP。

### 通知中心与 Telegram Bot 交互
- **多渠道告警**：支持 Telegram、邮件（SMTP）和通用 Webhook 通知。
- **Telegram Bot 双向交互控制**：
  - 发送 `/traffic` 查看全账号流量总览与实例流量；
  - 发送 `/instances` 查看实例列表与分页详情；
  - 通过内联按钮交互进行刷新数据、远程开机、关机，以及带倒计时二次确认的安全释放；
  - 支持配置 SOCKS5 / HTTP 代理，保障网络连通。
- **每日定时日报摘要**：支持每天在指定时刻自动向已配置的通知渠道推送前一天的流量统计与消费日报。

### 费用分析与告警
- **费用总览与分项统计**：查看本月累计支出、实例/云盘/EIP/网络分项占比。
- **趋势预测与余额预估**：根据使用趋势提供月底预测费用以及账户余额预计可用天数。
- **阈值告警**：支持设置费用与余额阈值告警（费用告警仅发送通知，不会触发停机）。

### 安全、审计与个性化
- **Passkey 免密登录**：支持通过指纹、面容、系统 PIN 或安全密钥（WebAuthn）登录。
- **全方位操作审计**：记录网页、Telegram、定时任务、轮转、流量保护和后台任务的执行结果，且严格过滤密码与云凭据。
- **凭据加密存储**：数据库中的 AccessKey Secret 等敏感数据通过本地独立密钥 `.secret_encryption.key` 进行 AES-GCM 加密。
- **个性化定制**：支持在全局配置中上传自定义页面 Logo。

## 在线更新

进入“关于 → 版本中心”，点击检查更新即可。

只有 GitHub Release 的对应平台安装包、SHA256 校验文件和 Ed25519 签名全部准备好时才会更新。当前已安装程序使用内置公钥验证发布签名，再校验安装包 SHA256；任一校验失败都不会切换版本。原生部署由后台更新服务下载、校验、切换版本并重启控制台；Docker 部署由更新容器下载、校验、替换应用文件并重建容器。两种方式都会进行健康检查，失败时恢复更新前版本。更新不会删除账号、配置和流量数据。

Docker 部署完成后无需登录服务器手动重建镜像，在“关于 → 在线更新”点击检查更新并确认即可。服务菜单中的“更新服务”也会读取当前 Docker 配置直接更新，不会重新询问安装方式、域名、HTTPS 或容器名称。旧版本 Docker 部署如果页面仍显示不支持在线更新，重新运行一次部署向导以创建更新容器：

```bash
sudo /opt/ecs-controller/setup.sh
```

仓库发布者需要在 GitHub Actions 中配置名为 `ECS_RELEASE_SIGNING_KEY` 的 Ed25519 PEM 私钥。私钥只能保存在 GitHub Actions Secret 中；仓库和程序只保存对应公钥。

如果旧版本没有在线更新功能，重新运行一次安装命令即可。更新菜单不会改变现有部署方式和配置。

## 安全提醒

- 不要把 AccessKey 或 AccessKey Secret 发给别人。
- 公网访问请使用 HTTPS，不要直接暴露 `43211` 端口。
- 建议使用 RAM 子账号，不要直接使用阿里云主账号密钥。

## Passkey

登录后进入“系统设置 → 全局设置”，点击“设置 Passkey”。以后可以使用指纹、面容、系统 PIN 或安全密钥登录。

公网使用 Passkey 时必须配置 HTTPS；本地测试可以使用 `http://localhost`。

## 相关链接

- 二次开发项目：https://github.com/JudiLite/ecs-controller
- 问题反馈：https://github.com/JudiLite/ecs-controller/issues
- 上游项目（功能基础与部分代码来源）：https://github.com/elunez/ecs-controller
- 许可证：[MIT](./LICENSE)
