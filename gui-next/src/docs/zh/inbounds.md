# 监听地址与端口

在设置 → 地址与端口中配置 SOCKS5、HTTP、带规则的 SOCKS5/HTTP、VMess 和核心 API 端口。端口范围为 0–65535；0 关闭对应监听。冲突或权限问题会由服务端返回错误。

VMess 入站启用后，可打开分享链接供客户端使用。核心 API 服务按实际需求选择 HandlerService、LoggerService、StatsService。

后端地址用于浏览器连接管理服务，留空代表当前网页同源地址；它不会改变服务器的监听地址。服务器管理端口由启动时的 V2RAYA_ADDRESS 指定。Docker 中还需正确配置网络模式或端口映射。

本版本不提供上游新版的任意 JSON 自定义入站管理接口。
