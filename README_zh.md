<div align="right">
  <a href="README.md">English</a> | <strong>简体中文</strong>
</div>

# OpenCode CLIProxyAPI (CPA) 插件

专为 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA) 设计的高性能 C-shared 动态库插件，将 OpenCode LLM 生态系统（包含免费模型与 Console 工作区模型）无缝接入标准 OpenAI 与 Anthropic 兼容端点。

---

## 特性

- **内置免费模型支持**：免充值开箱即用 OpenCode 官方精选的免费模型（`deepseek-v4.1-flash-free`、`cat-jam-free`、`space-bunny-free`、`big-pickle` 等）。
- **自动化网关适配**：
  - 自动合成符合 OpenCode 规范的 EID 会话标识符（`ses_<12位hex><14位base62>`）。
  - 透明注入并隔离网关必须的指纹验证工具，对下游客户端自定义工具调用（Tool Calls）零干扰并完整透传。
  - 原生流式自适应，确保流式（streaming）与非流式（non-streaming）下游客户端均能稳定接收响应。
- **全协议跨端兼容**：全面兼容 OpenAI Chat Completions（`/v1/chat/completions`）与 OpenAI Responses API（`/v1/responses`），自动完成协议规范化与推理控制字段映射。
- **Console 工作区与 Service Key**：支持配置 OpenCode Console 服务密钥，使用会话持久化和 Prompt 缓存调用自定义付费模型。

---

## 可用免费模型

本插件自动发现并路由 OpenCode 提供的所有免费模型，在 CPA 中通过 `opencode-zen/<model-id>` 别名进行调用：

| 模型名称 | CPA 模型 ID | 上下文窗口 | 说明 |
| :--- | :--- | :--- | :--- |
| **DeepSeek V4.1 Flash Free** | `opencode-zen/deepseek-v4.1-flash-free` | 128K | 极速推理与代码生成 |
| **Cat Jam Free** | `opencode-zen/cat-jam-free` | 128K | 高吞吐 Agentic 代码模型 |
| **Space Bunny Free** | `opencode-zen/space-bunny-free` | 1M | 超长上下文代码模型 |
| **Big Pickle** | `opencode-zen/big-pickle` | 200K | 强逻辑与综合推理 |
| **LongCat 2.5 Preview Free** | `opencode-zen/longcat-2.5-preview-free` | 1M | 超长上下文通用模型 |
| **MiMo V2.6 Flash Free** | `opencode-zen/mimo-v2.6-flash-free` | 200K | 轻量级 Flash 快速响应模型 |
| **Nemotron 3.5 Super VL Free** | `opencode-zen/nemotron-3.5-super-vl-free` | 1M | 多模态视觉与语言理解 |
| **Muse Spark 1.3 Contributor Free** | `opencode-zen/muse-spark-1.3-contributor-free` | 1M | Responses API 贡献者模型 |

---

## 安装与部署

### 1. 下载预编译动态库

项目发布的 [Releases](https://github.com/megumin31/cpa-opencode-zen/releases) 均在原生验证门禁通过后自动打包：
- `opencode-<tag>-linux-amd64.tar.gz`（适用于 x86_64）
- `opencode-<tag>-linux-arm64.tar.gz`（适用于 aarch64）

下载对应架构的压缩包，校验 SHA-256 后解压出 `opencode-v0.1.0.so`（注意：**请勿重命名该 .so 文件**，保持版本号命名不变），详见 [发布说明与重试机制文档](docs/RELEASE.md)。

此外，每次代码提交均会通过 GitHub Actions 自动构建最新的 Linux 二进制：
1. 访问项目的 [Actions](https://github.com/megumin31/cpa-opencode-zen/actions) 页面；
2. 选择最近一次成功的 **Build Linux plugins** 工作流；
3. 在 **Artifacts** 下下载 `opencode-linux-amd64-<commit>` 或 `opencode-linux-arm64-<commit>`；
4. 将解压得到的 `opencode-v0.1.0.so` 放置于 CPA 的 `plugins/` 目录中：
   ```bash
   cp opencode-v0.1.0.so /path/to/cliproxyapi/plugins/
   chmod 700 /path/to/cliproxyapi/plugins/opencode-v0.1.0.so
   ```

### 2. 配置 CPA

在 CPA 的配置文件 `config.yaml` 中添加插件声明：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    opencode:
      enabled: true
      request_timeout_seconds: 0
      allowed_destinations:
        - https://opencode.ai/console
        - https://opencode.ai/inference
```

### 3. 配置 OpenCode 认证凭据

在 CPA 的 `auth-dir` 目录（默认路径为 `~/.cli-proxy-api/`）下创建认证文件 `opencode.json`：

```json
{
  "type": "opencode",
  "key": "public",
  "server": "https://opencode.ai/console",
  "label": "OpenCode Service"
}
```

> **说明**：
> - 若仅使用免费模型，`"key"` 填写 `"public"` 即可开箱即用；
> - 若拥有 OpenCode Console Service Key（`oc_sk_...`），将其填入 `"key"` 字段即可无缝解锁工作区付费与自定义模型。

### 4. 重启并验证 CPA

```bash
systemctl --user restart cliproxyapi
# 或直接重启 CPA 主进程
```

调用模型列表端点确认模型是否已正确挂载：
```bash
curl http://localhost:8317/v1/models -H "Authorization: Bearer <your-cpa-api-key>" | jq '.data[].id'
```

---

## 调用示例

### 对话补全（Chat Completions，`/v1/chat/completions`）

```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <your-cpa-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "opencode-zen/cat-jam-free",
    "messages": [{"role": "user", "content": "用 Go 语言写一个快速排序算法。"}],
    "stream": false
  }'
```

### 工具调用（Tool Calling）

客户端声明的 tools 会被完整保留并由模型原生调用，插件会在网关层自动隔离下游工具与平台指纹工具：

```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <your-cpa-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "opencode-zen/space-bunny-free",
    "messages": [{"role": "user", "content": "使用 calc 工具计算 123 * 456。"}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "calc",
        "description": "计算数学表达式",
        "parameters": {
          "type": "object",
          "properties": {"expr": {"type": "string"}},
          "required": ["expr"]
        }
      }
    }],
    "tool_choice": "auto"
  }'
```

---

## 源码构建与本地验证

构建环境要求：Go **1.26+**（CI 固定使用 1.26.9）、C 编译器（GCC 或 Clang）、Python 3、GNU Make、Bash 及 GNU `timeout`。原生宿主测试需要在启用了 CGO 的 Linux 环境下运行。

```bash
# 在允许联网的环境下预先准备两个 Go 模块的依赖缓存
go mod download
go -C cmd/hostcheck mod download

# 运行完整的离线验证门禁（包含针对本地 mock 的原生真实宿主测试）
make check-offline

# 仅编译共享动态库（C ABI 入口保留在根目录包）
make build
# 编译产物位于：build/opencode-v0.1.0.so

# 从共享常量打印插件提供商标识、版本号及文件名元数据
make metadata
```

`check-offline` 包含了两个 Go 模块的单元测试、vet 静态分析与 race 竞态检测、Python 协议用例、Shell 脚本预检、SDK 复现脚本，以及原生宿主在 `--quiesce`、`--cancel-first`、同实例 `--reconfigure` 和空闲 `--reenable` 模式下的完整验证。该命令禁用模块外网下载并使用本地 loopback 服务。

已知的 CPA 上游宿主在活跃请求下的 disable 路径缺陷作为独立的 `make hostcheck-known-bug` 目标保留，不会干扰主门禁。详见 [构建与验证文档](docs/BUILD.md)。

项目架构将根目录的 C-ABI 包装层与 Go 插件核心引擎及其内部的 catalog、protocol、diagnostics、fault 子包严格分离。供开发者抓包的 Python 工具位于 `tools/officialwire`；`cmd/hostcheck` 保持为独立的 Go 子模块，以便安全白盒测试 CPA 原生宿主内部逻辑。详见 [架构设计与依赖边界文档](docs/ARCHITECTURE.md)。

---

## 开源协议

[AGPL-3.0](LICENSE)
