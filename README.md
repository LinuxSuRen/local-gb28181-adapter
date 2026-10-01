# local-gb28181-adapter

把本地摄像头以国标 **GB/T 28181** 虚拟设备（IPC）形式接入上级国标平台。

- **后端**：Go（SIP 信令（sipgo）+ PS/RTP 推流 + 内嵌 RTSP 服务器 + ffmpeg 取流 + 管理 REST API）
- **前端**：Vue 3 + Element Plus 管理台（内嵌进单二进制）
- **媒体链路**：ffmpeg 拉源转 H264（无 B 帧）→ 推流给内嵌 RTSP 服务器（gortsplib）→
  平台 INVITE 点播时从 RTSP 旁路取流，自研封装 **PS over RTP**（GB28181 标准）推给平台；无需部署 mediamtx
- **PTZ**：纯 mock（虚拟位置状态机），平台下发 DeviceControl 云台指令只更新虚拟状态

设计参考 [local-onvif-adapter](https://github.com/linuxsuren/local-onvif-adapter)，
目标是把 USB / RTSP / 测试源等本地视频源快速接入 WVP-GB28181-pro 等国标平台。

## 架构

```
┌──────────────────────────── host (Linux/macOS/Windows) ──────────────────────┐
│                                                                              │
│  ┌────────────────────────────────────────────┐                              │
│  │ local-gb28181-adapter (Go, 单二进制)       │                              │
│  │  :8080 HTTP  管理 UI + REST API            │   ffmpeg 按需抓帧 → JPEG     │
│  │  :5060 UDP   SIP 信令                      │◀──────────────────┐          │
│  │   ├ REGISTER 注册（digest 认证）           │                   │          │
│  │   ├ MESSAGE  心跳/目录/设备信息/PTZ 控制   │                   │          │
│  │   └ INVITE/ACK/BYE 点播会话                │                   │          │
│  │  :8554 内嵌 RTSP 服务器 (gortsplib)        │◀── ffmpeg 推流 H264          │
│  │  媒体旁路: RTSP tap → H264 → PES/PS → RTP  │─── UDP / TCP(RFC4571) ──▶   │
│  └────────────────────────────────────────────┘        平台媒体接收端口      │
│     ffmpeg 拉本地源: v4l2 / avfoundation / dshow / rtsp / testsrc → H264     │
└──────────────────────────────────────────────────────────────────────────────┘
          ▲ SIP (UDP 5060) + RTP/PS                    ▲
       GB28181 上级平台（WVP-GB28181-pro + ZLMediaKit 等）
```

- **多摄像头 = 多国标通道**：每个启用的摄像头对应一个 20 位国标通道编码
  （`<设备前8位>00 132 <7位序号>` 自动派生，或手动指定），目录顺序与配置一致
- 在 UI 里把摄像头标记为"红外"后，目录中的通道名会带 `infrared` 关键词，平台可按名称识别红外通道
- **收流模式**：支持 UDP 与 TCP 被动（平台监听、设备主动连接，RFC 4571 帧格式）；
  TCP 主动（平台连设备）首版不支持

## 快速开始

### 容器一键启动（推荐）

```bash
make up        # docker compose up -d --build（host 网络，单容器）
```

启动后：

- 管理台：<http://localhost:8080/>
- 在管理台"上级平台对接"卡片中填写平台编码、平台地址（`host:5060`）、注册密码并启用
- 添加摄像头（自动枚举本机设备），启用后即可在平台上看到通道并点播

Linux 下接入 USB 摄像头，在 `docker-compose.yml` 中取消对应设备映射注释：

```yaml
    volumes:
      - /dev/video0:/dev/video0
```

compose 使用 **host 网络模式**：SIP 注册与 RTP 推流都最顺畅。macOS Docker Desktop
需较新版本（支持 host networking）。

### 本地开发

```bash
make setup     # 前端依赖（首次）
make run       # 构建并本地运行（需本机 ffmpeg）
make test      # go test -race
make lint      # go vet
```

前端单独开发调试：`cd web && npm run dev`（/api 代理到 127.0.0.1:8080）。

## 摄像头源类型

管理台添加摄像头时**自动枚举本机摄像头**（内置、USB，名称与连接方式直接列出），
用户从列表中选择即可，无需关心平台差异；跨平台适配由服务端完成：

| 内部类型 | 平台 | 枚举方式 | 源形式 |
|---|---|---|---|
| `v4l2` | Linux | 扫描 `/dev/video*` + sysfs 读取名称与连接方式（过滤 UVC 元数据节点） | `/dev/video0` |
| `avfoundation` | macOS | `ffmpeg -list_devices`（过滤屏幕采集） | 设备索引 `0` |
| `dshow` | Windows | `ffmpeg -list_devices` | 设备名 |
| `rtsp` | 任意 | 手动填写 | `rtsp://user:pass@ip:554/stream` |
| `testsrc` | 任意 | 无需设备 | 留空 |

## 与 WVP-GB28181-pro 对接示例

1. WVP 侧新建上级平台不涉及；本适配器作为**下级设备**接入：
   - 平台编码 = WVP 的 SIP ID（配置文件 `sip.ip` 所属域下的平台编号）
   - 平台地址 = WVP 的 SIP 服务器 `ip:port`
   - 注册密码 = WVP 添加设备时配置的密码
2. 在 WVP「国标设备」中添加设备，ID 填本适配器的**设备编码**（管理台首页可复制）
3. 本适配器注册成功后，WVP 中刷新设备目录即可看到全部通道
4. 点播即点即用：INVITE 到达后自动从取流旁路封装 PS/RTP 推流

## 目录结构

```
├── cmd/local-gb28181-adapter/       # 服务入口（flag/env 装配、端口漂移）
├── internal/
│   ├── api/                  # 管理 REST API + 静态 UI 托管 + 快照路由
│   ├── config/               # 平台配置与摄像头列表持久化（data/config.json）
│   ├── gbcode/               # 国标 20 位编码 + PTZ 控制指令解析（A50F）
│   ├── gbsdp/                # GB28181 SDP 解析/构造（UDP / TCP 被动）
│   ├── manscdp/              # MANSCDP XML 消息（目录/设备信息/心跳）
│   ├── psrtp/                # PS 封装（PSM/PES/pack header）+ RTP 发送
│   ├── ptzmock/              # 虚拟云台状态机
│   ├── rtspserver/           # 内嵌 RTSP 服务器（gortsplib，多读者 + tap 旁路）
│   ├── sig/                  # 国标信令：注册/心跳/目录/INVITE 会话
│   ├── snapshot/             # ffmpeg 抓帧（带短缓存）
│   └── stream/               # ffmpeg 取流进程监督（退避重启）+ 设备枚举
├── web/                      # Vue3 管理台（构建产物内嵌）
├── Dockerfile                # 多阶段：node 构建前端 → go 构建后端 → alpine+ffmpeg
├── docker-compose.yml        # 单容器（host 网络）
└── Makefile
```

## 运行参数

| 参数 | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `--http-addr` | `HTTP_ADDR` | `:8080` | HTTP 监听（UI/API） |
| `--advertise-ip` | `ADVERTISE_IP` | 自动探测 | 对外宣告 IP（SDP c= 行 / Contact） |
| `--rtsp-server` | `RTSP_SERVER` | `true` | 启用内嵌 RTSP 服务器（gortsplib） |
| `--rtsp-push` | `RTSP_PUSH` | `rtsp://127.0.0.1:8554` | ffmpeg 推流目标（仅 `--rtsp-server=false` 时使用，指向外部 mediamtx） |
| `--rtsp-port` | `RTSP_PORT` | `8554` | RTSP 端口（内嵌服务器监听） |
| `--data-dir` | `DATA_DIR` | `./data` | 配置持久化目录 |
| `--ffmpeg-bin` | `FFMPEG_BIN` | `ffmpeg` | ffmpeg 路径 |
| `--log-level` | `LOG_LEVEL` | `info` | 日志级别 |

平台对接参数（SIP 监听端口、平台编码/地址/密码、注册有效期、心跳间隔）在管理台
「上级平台对接」卡片中配置，保存后立即生效（信令栈自动重载）。

### 端口漂移

HTTP 与 RTSP 端口被占用时（`EADDRINUSE`），启动会自动向后尝试下一个端口
（8080 → 8081 → …；8554 → 8555 → …，各最多 20 个候选），直到能监听为止；
SIP UDP 端口同样支持漂移（5060 → 5061 → …）。漂移只影响本次运行，不写入配置。

### PS 封装说明

GB28181 要求 RTP 封装 PS（Program Stream）。本适配器自研封装器：
IDR 帧前插入 PSM（H264 stream type 0x1B）、每帧 pack header + PES（PTS，B 帧已禁用），
RTP 按 1400 字节切片（PT 96、y= 行 SSRC）。CRC-32/MPEG 与时间戳编码均有单元测试覆盖。

## 发布

发布通过 GitHub Release 驱动，**二进制与容器镜像一次发布**：

1. 打 tag 并推送：`git tag v0.1.0 && git push origin v0.1.0`
2. 在 GitHub Releases 页面基于该 tag 创建并发布 Release（`release` workflow 自动触发）
3. workflow 自动完成：
   - **二进制**：构建前端 → 六平台交叉编译（linux/darwin/windows × amd64/arm64）
     → 打包 tar.gz / zip → 生成 `checksums.txt` → 上传到该 Release
   - **容器镜像**：buildx 多架构构建（linux/amd64 + linux/arm64）→ 推送
     `ghcr.io/linuxsuren/local-gb28181-adapter:<版本>` 与 `:latest`（GITHUB_TOKEN 认证，无需额外 secret）
4. 产物缺失时可在 Actions 页面手动 `workflow_dispatch` 指定 tag 补传（`--clobber` 覆盖）

发布后拉取镜像：

```bash
docker pull ghcr.io/linuxsuren/local-gb28181-adapter:latest
docker run -d --network host -v ./data:/data ghcr.io/linuxsuren/local-gb28181-adapter:latest
```

本地验证发布配置：`make snapshot`（需要 goreleaser，不打 tag、不上传）。

## 依赖

- Go 1.24+、Node 20+（构建前端）
- [emiago/sipgo](https://github.com/emiago/sipgo)（SIP 栈）、gortsplib（内嵌 RTSP）
- ffmpeg：唯一的运行时依赖（采集 + H264 转码）；容器镜像内置，本地运行需自行安装
  （`brew install ffmpeg`）
- 无需 mediamtx：RTSP 服务由 gortsplib 内嵌提供
