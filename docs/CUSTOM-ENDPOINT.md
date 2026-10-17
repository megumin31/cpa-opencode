# Optional custom gateway endpoint / 可选自定义网关端点

## English

By default, `worker_endpoint` is empty and existing direct OpenCode routing is
unchanged. To opt in, merge into the **private** CPA plugin configuration:

```yaml
plugins:
  configs:
    opencode:
      worker_endpoint: https://YOUR-WORKER.YOUR-SUBDOMAIN.workers.dev
      worker_token: REPLACE_WITH_PRIVATE_ENTRY_TOKEN
```

These are placeholders, not working credentials. Use the HTTPS **origin only**,
without a path, query, userinfo or custom port. The token must match the Worker's
`GATEWAY_TOKEN` secret. An enabled endpoint without a token is a configuration
error. An empty endpoint ignores any retained token and restores direct access.
Do not put tokens in URLs, source control, screenshots or shared logs.

Deploy the gateway from the separately delivered Worker package, then enter its
origin and securely provision your own entry token on both sides. Worker source,
tests and deployment instructions are not part of this plugin repository.
No upstream service key is stored in the Worker: the plugin sends its existing
per-request authorization headers through the Worker. Consequently, the Worker
operator and Cloudflare are trusted with service credentials, prompts and replies.
Use only a Worker you control/trust, and protect the private CPA config accordingly.

The plugin internally maps the two fixed upstream origins:

- `https://opencode.ai/...` → `WORKER_ORIGIN/opencode/...`
- `https://models.opencode.ai/...` → `WORKER_ORIGIN/models/...`

Keep the auth-file `server` as `https://opencode.ai/console`. Keep
`allowed_destinations` and `isolated_test_guard` entries as the **original**
OpenCode URLs; do not replace them with Worker URLs. Original credential
destination checks and exact model/endpoint checks run before mapping. Protocol
selection, session identity and host-specific behavior retain the original URL.
The gateway supports only the documented fixed Console/catalog/inference routes,
not arbitrary custom-provider domains. New upstream routes require reviewed code
changes. Gateway failures never retry directly or switch to another origin.
Existing discovery caches may still serve previously loaded routes.

`proxy_url` remains an HTTP(S)/SOCKS forward proxy and, if configured, carries the
connection **to the Worker**. It is not the Worker endpoint setting. The Worker
streams bodies, preserves upstream errors/status/Retry-After, rejects redirects,
and performs no application retries, caching or request logging. It is a bounded
reverse proxy, with no IP rotation, quota bypass or model-price guarantees.
Cloudflare platform limits and account logging settings still apply; consult and
review them before deployment. Tests use only fake keys and synthetic fetches.

Plugin verification: run `make check-offline` with the prepared Go toolchain and
dependencies. Gateway tests and deployment are maintained in the separate Worker
package. Offline tests do not prove live gateway deployment behavior.

## 简体中文

`worker_endpoint` 默认留空，保持原来的 OpenCode 直连。启用时，按上面的示例在
CPA 私有配置中填写你信任的 Worker HTTPS 根地址，以及与 Worker 的
`GATEWAY_TOKEN` secret 相同的 `worker_token`。示例均为占位符，不含真实密钥。
只填域名根地址，不带路径、查询参数或自定义端口；启用却缺少入口 token 会报配置错误。
将地址留空即可恢复直连，保留的 token 会被忽略。入口鉴权用于防止公开转发滥用。

先按独立交付的 Worker 包中的说明部署网关，再安全填写地址和自行设置的入口 token。
Worker 源码、测试及部署说明不放入此插件仓库。不要把 token 放入 URL、Git 或共享日志。
Worker 不保存 OpenCode 服务密钥，只透传插件原有请求中的认证信息；因此你需要信任
Worker 运营方及 Cloudflare，它们会处理服务密钥、提示内容和响应。

认证文件中的 `server` 保持 `https://opencode.ai/console`；`allowed_destinations`
与 `isolated_test_guard` 仍填写原始 OpenCode 地址，不要改成 Worker 地址。
插件先验证原目标及精确模型/端点限制，再在统一出站层转换地址，协议、会话身份与主机
相关逻辑保持原样。一个入口地址同时覆盖 Console、公开目录和四种推理协议；
只支持已列明的固定上游及路径，不支持任意第三方模型域名。

Worker 失败不会自动回退直连，不自动重试 POST，不自动绕过限额；已有目录缓存仍可能
继续使用。`proxy_url` 仍是传统 HTTP/SOCKS 正向代理，不能拿来填写 Worker 地址。
响应流直接透传，保留状态、错误和 Retry-After；拒绝重定向，不做应用级缓存或内容日志。
仍需自行审核 Cloudflare 平台限制与账户日志设置。本次测试仅使用假值和假上游，
实际部署、真实入口 token 安全配置与获准后的线上验证需要另行完成。
