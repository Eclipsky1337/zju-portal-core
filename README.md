<h1 align="center">ZJU Portal Core</h1>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25.6-00ADD8?logo=go&logoColor=white" alt="Go 1.25.6">
  <img src="https://img.shields.io/badge/release-v0.1.0--alpha-orange" alt="v0.1.0-alpha">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="AGPL-3.0"></a>
</p>

## Features

- 支持密码、短信、CAS 和 OAuth2 等认证流程。
- 提供 SOCKS5、HTTP CONNECT、DNS 和 TUN 入站。
- 支持 `rule`、`global`、`direct` 三种运行时路由模式。
- 支持远程 VPN DNS、备用 DNS、静态解析和 VPN 资源 Fake IP。
- 支持 VPN 资源路由或全流量 TUN 路由，并自动检测物理出口接口。
- 提供资源刷新、断线重连和 Resume State 持久化。
- 提供流量统计、逻辑连接与传输连接查询，以及运行时关闭连接。
- 提供 REST、SSE 和标准输入输出 JSONL 控制接口。

## Quick Start

```bash
./zju-portal-core --config config.yaml
```

也可以临时启用控制传输：

```bash
./zju-portal-core --config config.yaml --rest 127.0.0.1:9090
./zju-portal-core --config config.yaml --stdio
```

## Configuration

完整配置示例位于 [docs/config.yaml](docs/config.yaml)

## Docs

- [Control API 指引](docs/control-api.md)

## For Developers

Build:

```bash
git clone https://github.com/Eclipsky1337/zju-portal-core.git
cd zju-portal-core && go mod download
go build -o zju-portal-core .
```

## Credits

本项目基于 [zju-connect](https://github.com/Mythologyli/zju-connect) 开发，感谢原项目作者及贡献者。

## License

本项目基于 [GNU Affero General Public License v3.0](LICENSE) 发布。
