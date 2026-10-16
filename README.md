# LAN 文件服务器

用 Go 实现的局域网文件管理服务，保留原版网页和 API：目录浏览、单文件/文件夹批量上传、下载、目录 ZIP 和删除。默认监听 `0.0.0.0:3000`，适合在可信 LAN 中使用。

服务不启用登录认证，适合隔离的可信局域网；不要将端口转发到公网或不可信网络。

## 功能

- 原网页支持目录树、拖放与目录选择、上传队列、进度和速度显示。
- 上传限制默认 10 GiB，流式写入临时文件后原子替换目标文件。
- `GET /api/config`、`GET /api/list`、`POST /api/upload-file`、`GET /api/download-file`、`GET /api/download-folder`、`DELETE /api/item` 与原版参数保持兼容。
- `/healthz` 返回 HTTP 204，可供 systemd/监控使用。
- 路径通过 Go `os.Root` 限定在存储根目录内；目录列表和 ZIP 下载忽略符号链接。
- 无第三方 Go 依赖，提供 Linux amd64/arm64 Release。

## 本地运行

需要 Go 1.25 或更新版本。

```sh
go test -race ./...
go vet ./...

HOST=127.0.0.1 \
PORT=3000 \
STORAGE_ROOT=./storage \
go run .
```

打开 `http://127.0.0.1:3000`。

### 配置项

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `HOST` | `0.0.0.0` | 监听地址 |
| `PORT` | `3000` | HTTP 端口 |
| `STORAGE_ROOT` | `./storage` | 数据存储根目录 |
| `MAX_UPLOAD_BYTES` | `10737418240` | 单文件最大字节数 |

## Linux 安装与服务管理

解压对应架构 Release 包后，以具有 sudo 权限的账户执行：

```sh
sudo ./scripts/install.sh
```

安装脚本以 `lee` 作为默认服务账户，将二进制安装至 `/home/lee/lan-file-server/bin/`，数据放在 `/home/lee/lan-file-server/storage/`，并创建 `lan-file-server.service`、启用开机自启。升级时会保留存储目录内容及权限。

```sh
./scripts/service.sh status
./scripts/service.sh restart
./scripts/service.sh logs
./scripts/service.sh enable
./scripts/service.sh disable
```

当前安装包的 systemd unit 固定以服务器上的 `lee` 用户运行。

## 发布

更新 `VERSION` 和 `CHANGELOG.md`，提交后打版本标签：

```sh
./scripts/release.sh
gh release create "v$(tr -d '\r\n' < VERSION)" \
  dist/lan-file-server_*_linux_amd64.tar.gz \
  dist/lan-file-server_*_linux_arm64.tar.gz \
  dist/SHA256SUMS
```

GitHub Actions 会在推送 `v*.*.*` 标签时运行测试并创建 Release。

## 安全与数据边界

- 此服务可读写、下载并删除存储区内的文件，且不要求登录；只应在可信网络中开放端口。
- 不要将 TCP 3000 转发到公网；跨不可信网络访问时，应通过 VPN 或其他受控网络通道。
- 建议限制防火墙只允许需要访问的 LAN 网段连接 TCP 3000。
- `storage/`、运行日志、环境文件和构建目录已排除在 Git 之外；Release 仅包含源码、静态页面与程序，不包含用户文件。
