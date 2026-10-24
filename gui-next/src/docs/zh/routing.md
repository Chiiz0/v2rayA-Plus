# RoutingA 与规则

规则端口支持绕过中国大陆、GFWList 和 RoutingA。透明代理选择“跟随规则端口”时使用相同分流策略。

RoutingA 可用表单或文本编辑，也可从本地文本文件导入、导出和插入模板。文本保存时由后端校验；错误不会清空草稿。已有规则会按原文保留。

```text
default: proxy
domain(geosite:cn)->direct
ip(geoip:private,geoip:cn)->direct
```

规则依赖当前 geoip.dat 与 geosite.dat 中实际存在的分类。自定义出口组需要先创建并连接节点。该版本不提供远程 RoutingA URL 自动导入或定时同步。

GFWList 可手动更新或按设置自动更新。更新后检查本地版本；更新文件不会替换当前未保存的设置草稿。
