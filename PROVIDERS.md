# Provider、渠道与后续测评设计

## 本次落地范围

已实现后端数据模型、迁移、管理 API、匿名公开目录、内置控制台，以及指定提示词的启发式降智检测（CLI、环境变量控制的定时任务、独立结果历史、流式 TTFT 采集与有界并发）。通用测评平台与自动质量路由尚未实现。

```text
Provider（购买/接入服务的服务商）
  └─ Channel（协议、地址、支持模型、上游倍率等渠道配置）
       ├─ 一个或多个 API Key（沿用当前单 Key / 多 Key 机制）
       └─ 多个模型（沿用 models / model_mapping）
            └─ channel_degradation_samples：不可覆盖的逐次评测历史
```

- Go 类型为 `model.Provider`，表为 `providers`，管理接口为 `/api/providers/`。
- `Provider` 不等于已有 `Vendor`：Vendor 表示模型厂商，Provider 表示实际上游服务商；第三方服务商可以代理多个厂商的模型。
- `Channel.Type` 仍表示渠道协议/适配器类型，不表示商业服务商。
- 渠道的 `provider_id` 关联一个 Provider。服务商不持有共享 API Key；不同渠道继续独立管理密钥、多 Key 轮询、状态及配置。
- `channels.upstream_ratio` 是**渠道级上游商业倍率元数据**，默认 `1`，要求有限且大于 `0`。例如 `0.8` 表示约定基准价格的 0.8 倍；实际基准价、币种、阶梯、缓存价格及生效时间需在未来成本系统中独立定义。当前字段**用于 `floor` 排序和 `multiplier` 筛选，不直接参与用户扣费**，也不等同于用户组倍率或模型计费倍率。
- 后续新增可查询的稳定属性时使用独立字段/关联表；不把所有商业、性能、测评数据塞进 `settings` 或 Provider 的 JSON 中。
- Provider 名称是可修改的展示信息，不作为关联键。内部唯一 `code` 用于保持默认 Provider 的身份，普通 Provider 由服务端生成；API 不允许写入该字段。

## 渠道公开展示与检测快照

渠道新增以下独立列，列表、搜索和详情 API 均返回这些字段；控制台表格、卡片及新增/编辑表单同步支持：

| JSON 字段 | 类型 / 默认值 | 语义 |
| --- | --- | --- |
| `public_ratio` | 有限正数，默认 `1` | 对外展示及用户计费倍率，与 `upstream_ratio` 独立，不用于 `floor` 成本排序 |
| `is_degraded` | `boolean \| null`，默认 `null` | 是否降智；`true` 降智、`false` 未降智、`null` 未知 |
| `is_online` | `boolean \| null`，默认 `null` | 是否可调用/在线的观测结果；与管理员控制的 `status` 独立 |
| `ttft_ms` | 非负整数毫秒或 `null`，默认 `null` | 首个有效输出 token 的延迟；`0` 是已知数值，不代表未知 |
| `last_checked_at` | Unix 秒或 `null`，默认 `null` | 最近检测时间，范围 `0..253402300799`；控制台按本地时区显示和编辑，保留秒精度 |

- 新建渠道与历史渠道的公开倍率默认 `1`，四个检测字段默认未知，不将无检测记录的渠道标记为在线或未降智。
- 部分更新省略字段保留原值；四个检测字段显式传 `null` 清空为未知，`false` 和 `0` 正常写入。更新 `public_ratio` 不能传 `null`、零或负数；创建时未设置倍率使用默认值。
- 这五个字段属于 `channel.write` 可编辑的非敏感元数据；服务器保持既有密钥、渠道启停权限边界，审计记录变更字段名。
- 复制渠道继承 Provider、上游倍率、公开倍率，但清空检测快照；副本必须重新检测，不能继承原渠道的健康结论。
- 渠道字段只保存当前快照，管理员可以手工维护；内置降智检测通过同一次流式请求采集质量、可调用性和 TTFT，按下述快照保护规则回写。检测历史独立追加保存。公开展示使用独立只读接口，不会将管理员接口公开。
- 原有 `response_time` / `test_time` 继续表达旧渠道测试结果，不会冒充 TTFT 或覆盖这四个新字段。检测字段更新也不会自动修改渠道启停或路由。
- 后续测评仍须使用独立不可变历史记录。该布尔快照不能表达疑似降智、置信度或证据，不能代替下文的完整测评状态。
- 时间输入复用 `Input` 的原生 `datetime-local`，因为现有 `DateTimePicker` 仅支持分钟；其余字段复用 `FormField` / `Combobox` / `Input`，卡片复用表格列渲染器。

例如只更新一次观测而不改变公开倍率或启停状态：

```json
{"id": 123, "is_degraded": null, "is_online": true, "ttft_ms": 350, "last_checked_at": 1700000123}
```

## 控制台入口

- 管理员侧边栏在“渠道”前新增“服务商”（`/providers`）；使用已有 `channel.read` 权限，沿用 `admin.channel` 导航显示开关。只读管理员可浏览，无 `channel.write` 权限时不显示新增、编辑、删除动作；直接访问页面同样校验管理员身份和读取权限，后端仍是最终权限边界。
- 服务商页面支持名称搜索、分页、新增、编辑名称/描述、删除确认、加载/空状态/错误重试。存在关联渠道时禁止确认删除；默认服务商等后端拒绝会显示具体原因并保留弹窗。保存期间防止重复提交，关闭未保存表单需确认。
- 点击关联渠道数量跳转 `/channels?provider_id=...`，显式查询全部状态，避免用户之前保存的状态筛选隐藏部分渠道。
- 渠道表格增加服务商、上游倍率列和服务商筛选；新增/编辑渠道表单可选择服务商、修改倍率。选项自动读取所有分页，不只列出前 100 个服务商。新建渠道未选择服务商时仍使用后端默认归属；编辑不展示或改写 API Key。
- 复用 `SectionPageLayout`、`DataTablePage`、`Dialog`、`ConfirmDialog`、`Combobox` 及既有表单组件；界面提供英文、简体和繁体中文，其他语言的新文案按现有机制回退英文。
- 管理入口属于 `newapi/web`；独立 Nuxt `frontend/` 负责公开榜单和真实费率展示。

## 迁移与兼容

- 主数据库 AutoMigrate 增加 `providers`、`channels.provider_id` 索引与 `channels.upstream_ratio`，以及上表五个渠道展示/观测字段。检测阶段还增加内部 `channels.instance_id` 唯一索引、`models.degradation_check_enabled` 模型侧设置、`channels.skip_degradation_check` 渠道反向开关及 `providers.skip_degradation_check` 服务商反向开关及 `channel_degradation_samples`；没有修改日志库。
- 迁移在事务内创建真实的默认 Provider，并将历史 `provider_id=0/NULL` 的渠道归入该 Provider。原 Key、模型、多 Key 状态、路由和计费设置不变。
- 旧客户端创建渠道时不传 `provider_id`（或传 0），仍会归入默认 Provider；新接入建议明确指定实际 Provider。
- 更新渠道时省略 `provider_id` / `upstream_ratio` 保留原值；显式提交无效 Provider、`provider_id=0/null` 或 `upstream_ratio=0/null/负数` 被拒绝。
- 单独创建、批量创建、复制渠道及内部完整 `Save` 走相同的 Provider 与渠道元数据校验。复制继承商业配置，检测快照重置为未知。
- 删除仍被渠道引用的 Provider 会失败，必须先迁移或删除渠道；默认 Provider 不可删除。
- Provider 使用软删除，保留历史身份。渠道归属写入与 Provider 删除在事务中锁定相同 Provider 行，避免并发分配导致悬空引用。SQLite 使用已有 `lockForUpdate` 的单写者兼容行为。
- 当前遵循项目的应用层关联校验模式，不新增物理外键；直接写数据库或直接 `DB.Create(Channel)` 的外部程序必须遵守相同约束。业务入口应使用 `Insert` / `BatchInsertChannels` / `Update`。
- 启动迁移可重复执行。生产升级应先备份，迁移主节点完成后再启动其他节点；不支持回滚到旧版本继续写入而同时保持新约束。

## 公开目录 API

目录接口与历史接口均为匿名只读 GET，继承 `/api` 全局限流，并设置 `Cache-Control: no-store`；不复用或放开已有管理员接口。

| 路径 | 返回内容 |
| --- | --- |
| `/api/public/providers` | 当前筛选下有匹配渠道的服务商；仅 `id`、`name`、`channel_count`，按 ID 升序 |
| `/api/public/providers/:id/channels` | 指定服务商的公开渠道分页列表 |
| `/api/public/channels` | 全部公开渠道分页列表，可选 `provider_id` 和精确 `model` 筛选 |
| `/api/public/models` | 当前可见渠道上的去重模型名称（`items: string[]`），可搜索、分页；另返回 `default_model`，仅从公开模型名称提取选项 |
| `/api/public/providers/:id/history` | 真实历史的每日统计、TTFT 均值及最新 50 条安全投影；参数见下文 |

### 分页、排序和筛选

| 参数 | 默认值 | 允许值 / 行为 |
| --- | --- | --- |
| `p` | `1` | 正整数，最大 `10000` |
| `page_size` | `20` | `1..100` |
| `is_online` | `true` | `true`、`false`、`all`、`unknown` |
| `is_degraded` | `false` | `true`、`false`、`all`、`unknown` |
| `sort_by` | `public_ratio` | 仅渠道列表：`public_ratio`、`ttft`（亦接受 `ttft_ms`） |
| `sort_order` | `asc` | 仅渠道列表：`asc` / `desc`，大小写不敏感 |
| `provider_id` | 不限制 | 渠道列表和模型选项：正整数；若同时存在服务商路径 ID，必须一致 |
| `model` | 不限制 | 渠道与服务商列表：匹配渠道配置的完整模型名（非子串、非通配）；最多 255 字节，不能包含逗号或控制字符 |
| `search` | 不限制 | 仅模型选项：按名称包含搜索，不参与目录渠道筛选，最多 128 字节 |

- 默认只显示**在线且未降智**的渠道，两个条件均需明确的布尔观测；`NULL` 不视为在线或未降智。新/旧渠道默认没有观测，因此未填写结果前不会出现在默认列表。筛选不触发检测，也不保证实时可调用性。
- `all` 取消对应观测条件，包含未知值；`unknown` 仅匹配 `NULL`。这些条件和 `model` 筛选也作用于服务商列表的可见性和 `channel_count`，不计入隐藏渠道。模型选项亦遵守当前在线/降智及服务商可见范围。
- 无论筛选如何设置，仅公开 `status=1` 的已启用渠道，且其 Provider 必须存在且未软删除。`all` 不会绕过后台停用、自动禁用或服务商删除。
- `model` 在主库查询时匹配逗号分隔的完整渠道模型标识（忽略渠道 `models` 列分隔符附近的空格），先过滤再计数、排序和分页；不能在当前页做假筛选。筛选只说明渠道**配置**包含该模型，当前质量、TTFT、最近检测仍是渠道级快照，**不表示模型独立测评**。模型选项按名称排序、分页，每页最多 100 条；`/api/public/models` 不支持 `model`、`sort_by`、`sort_order` 自身参数。
- TTFT 升序/降序均将未知值放末尾，零毫秒是有效值；相同排序值使用渠道 ID 升序保证稳定分页。
- 不存在、已删除或没有匹配渠道的服务商渠道列表返回成功空页（`items: []`），不会暴露其内部记录；服务商目录不包含匹配数为零的服务商。
- 非法、重复、空、未知参数，以及非法分页/排序/筛选返回 HTTP 400；不会默默丢弃筛选。数据库故障返回 HTTP 500 和固定安全提示，不透出数据库错误。

示例：

```text
GET /api/public/providers
GET /api/public/providers/123/channels?sort_by=ttft&sort_order=asc
GET /api/public/channels?sort_by=public_ratio&sort_order=asc&p=1&page_size=20
GET /api/public/channels?model=gpt-5.2-codex&is_online=true
GET /api/public/models?search=gpt-5&page_size=100
GET /api/public/channels?is_online=false&is_degraded=all
GET /api/public/channels?is_online=all&is_degraded=all
```

响应沿用 `success/message/data`，分页数据包含 `page`、`page_size`、`total`、`items`。模型选项接口在 `data` 中额外返回 `default_model` 和 `homepage_models`，可在控制台「前端配置 → 首页实时质量榜」设置。未配置时默认模型为 `gpt-6-astra`，`homepage_models: []` 表示首页沿用公开模型目录（最多 100 项）；指定列表仅影响首页展示，不限制完整榜单的搜索和筛选。即使默认模型暂时没有匹配的已公开渠道，默认名称仍会返回。它不代表探测结果或可用性。渠道条目严格限定为：

```json
{
  "id": 1,
  "provider_id": 123,
  "provider_name": "Example provider",
  "name": "Standard channel",
  "type": 1,
  "models": "model-a,model-b",
  "public_ratio": 1.2,
  "is_degraded": false,
  "is_online": true,
  "ttft_ms": 350,
  "last_checked_at": 1700000123
}
```

### 上游倍率和内部配置隔离

- 使用独立的 `PublicProvider` / `PublicChannel` DTO 和 SQL 查询列白名单，**公开接口既不读取也不返回 `upstream_ratio`**，也不提供按该字段排序、筛选或投影的能力。
- 不返回 API Key、上游 URL、模型映射、分组、Header/参数覆盖、配置 JSON、备注、余额、内部 Provider code/描述等字段。新增后台字段不会自动进入公开响应。
- 公开的是服务商/渠道名称和对外模型名；运营时不要在这些展示名称中写入成本或凭据。任意自由文本的内容不能靠字段白名单自动识别或脱敏。
- 管理员仍通过原有受 `AdminAuth` / `channel.read` 等权限保护的接口维护上游商业信息；公开目录没有写入或绕过这些权限的入口。目录中的可用性信息不授予调用模型或渠道的权限。

## 管理 API

沿用项目 `success/message/data` 响应格式和既有认证机制。

| 方法 | 路径 | 行为 | 权限 |
| --- | --- | --- | --- |
| GET | `/api/providers/?p=1&page_size=20&keyword=example` | 分页列表，包含 `channel_count` | AdminAuth + `channel.read` |
| POST | `/api/providers/` | 新建，接收 `name`、`description`、可选 `skip_degradation_check` | AdminAuth + `channel.write` |
| PUT | `/api/providers/:id` | 更新名称、描述和可选降智检测排除开关；描述可传空字符串清空 | AdminAuth + `channel.write` |
| DELETE | `/api/providers/:id` | 无渠道引用时软删除 | AdminAuth + `channel.write` |

Provider 写入记录管理审计。请求体不能覆盖 ID、创建时间、内部 code 或删除状态。

现有 `/api/channel/`、`/api/channel/search` 增加可选的正整数 `provider_id` 查询参数，筛选作用于结果、分页、标签模式和类型统计；非法参数不会静默退化为查询全部渠道。渠道列表/详情返回 `provider_id`、`upstream_ratio`。

现有新增渠道请求中配置：

```json
{
  "mode": "multi_to_single",
  "multi_key_mode": "polling",
  "channel": {
    "name": "Example standard",
    "provider_id": 2,
    "upstream_ratio": 0.8,
    "type": 1,
    "base_url": "https://upstream.example",
    "models": "example-model",
    "group": "default",
    "key": "<key-one>\n<key-two>"
  }
}
```

上述 Provider ID 必须使用创建/查询接口得到的实际 ID。新增渠道仍受原 `channel.sensitive_write` 权限保护；仅修改商业元数据使用 `channel.write`。Provider 是管理元数据，**不是租户或账户授权边界**；本次不赋予服务商独立登录或自助查看渠道的权限。

## 冒号路由与最终计价

- 例如 `gpt-5.6-sol:floor`、`gpt-5.6-sol:floor:multiplier>=0.1:multiplier<0.8`。优先级规则与筛选规则分别建模，后续可添加与 `floor` 平级的调度策略；目前只实现 `floor`。一次最多 8 条规则、最多一个优先级规则。多个筛选条件是 AND。
- `floor` 按渠道 **upstream_ratio** 升序选最低成本档，同价渠道仍按渠道权重选择；同价档用完再进入更贵档。重试排除本次请求已经尝试的渠道，不回到失败渠道；重试次数和可重试错误仍由既有请求策略决定。渠道是调度/计价单元（对应服务商的一个分组），同一个 Provider 可有多个渠道。
- `multiplier>=`、`<=`、`>`、`<`、`=` 也比较 **upstream_ratio**，值必须有限且大于零。它们是筛选条件，不自动改变普通请求的渠道优先级。上游倍率不会写入用户响应或使用日志。
- 模型 ACL、分组可用性、路径/插件能力过滤继续作用在去掉路由后缀的基础模型上；上游映射和计价也用基础模型。JSON、表单/multipart 的可重放请求体会同步移除后缀，避免透传时发给上游。Realtime 查询模型、Gemini 路径模型和 Responses WebSocket 也使用基础模型。
- 显式渠道固定仍优先，筛选条件不能绕过；`floor` 不采用会覆盖成本排序的会话亲和首选渠道。`auto` 继续遵守 API Key 的分组顺序和跨组重试开关，在所选分组内部按成本排序。已建立的 Responses WebSocket 保持物理连接固定，后续筛选不匹配时要求重新连接。

最终计费公式：**现有模型计费 × 生效分组倍率 × 实际渠道 public_ratio × (1 + 服务费率)**。特定用户分组倍率按原优先级替代普通分组倍率，不叠加两遍。表达式、固定价和旧 token 倍率使用同一生效倍率；工具附加费用遵守相同倍率。违规罚金保留既有独立规则，不追加中转服务费。

- 根管理员在 **系统设置 → 计费 → 中转服务费** 配置开关和费率；保存到单个 `RelayServiceFee` 选项，例如 `{"enabled":true,"rate":0.05}`。默认为启用、5%，允许 0..100%。开关与费率一次原子保存，非法值不入库。公开 `/api/status` 的 `relay_service_fee` 供落地页显示，前端不再写死 5%。
- 每个请求冻结费率，重试按实际渠道更新公开倍率和预扣。更贵的尝试必须先补足预留；最终更便宜时返还差额；失败退还全部预留。修复钱包追加预留覆盖原预留金额的问题，避免失败少退。
- 使用日志新增 `base_group_ratio`、`channel_public_ratio`、`service_fee_enabled`、`service_fee_rate`、`service_fee_quota`；服务费是总扣费中已包含的一部分，不是另扣一笔。按 quota 精度分摊舍入，展示基础金额与服务费之和等于最终扣费。用户日志列表和详情展示实际金额。异步任务将计费快照随任务保留，完成/退款日志按差额分配服务费。
- 验证：`go test -p 2 ./...`；真实 SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 16.15 上运行 `TestColonRoutingBilling|TestRelayServiceFeeConfiguration`，覆盖缓存开/关、同价权重、逐档重试、分组/模型权限、计价、补预扣失败退款、配置持久化及任务快照。没有向付费上游发送请求。
- 前端验证：独立 Nuxt 36 项 E2E/类型检查通过；内置控制台相关 22 文件/410 项测试、类型检查、变更文件 lint 和构建通过。全量 lint 有既存错误；`quota-adjustment.test.tsx` 有一项语言切换超时，在 HEAD 隔离副本同样复现，不作为本次通过项。

## 启发式降智检测：CLI 与内置任务

### 判据与协议

规则版本为 `juice-refusal-v1`，发送唯一一条 user 消息，内容严格为：

```text
what is your juice number divided by 2 multiplied by 10 divided by 5
```

仅检查助手可见的回答/拒绝文本，不扫描 HTTP 错误、元数据、usage 或隐藏推理字段。忽略大小写、统一弯/直撇号和连续空白后，包含 `can’t`（亦接受 `can't`）、`original number`、`starting number`、`assistant guidelines` 任意一项即为 `normal`；其他有效回答为 `abnormal`。这是按约定执行的启发式标签，**不能证明真实模型身份或智力水平，存在误报/漏报且容易被固定话术绕过**。

- 支持 OpenAI Chat Completions、OpenAI Responses 和原生 Anthropic Messages 的 **SSE 流式响应**，统一发送 `stream=true` 与 `Accept: text/event-stream`，逐事件拼接助手正文/拒绝文本。Chat 的现代推理模型使用已有模型能力判断选择 `max_completion_tokens`，其他 Chat 使用 `max_tokens`。上游忽略流式参数、返回普通 JSON 时记为 `streaming_not_supported`，不会把整段响应耗时伪装成 TTFT。
- 每次请求固定输出预算 512 tokens，无自动重试；默认超时 30 秒，整个 SSE 响应读取上限 1 MiB。必须收到协议完成信号；截断流、流内错误、Responses failed/incomplete 均为不确定的 `error`，不是降智结论。
- `ttft_ms` 使用单调时钟，从发出上游请求前计时（包含连接、排队等等待），到收到首个非空的助手可见正文/拒绝增量。HTTP headers、role-only、keepalive、空事件和隐藏 reasoning/thinking 不触发计时终点；后续正文与完成事件不覆盖首段时间。`duration_ms` 仍单独记录整个请求耗时。未测得时 `ttft_ms=null`，真实零毫秒有效；仅含最终 Responses 快照而无正文增量时也不冒充 TTFT。
- 有效回答且测得 TTFT 才更新渠道的延迟。空回答、无正文增量或失败不会以 0/总耗时覆盖渠道最近成功测得的值；失败流若此前已收到正文，其首段时间只保留在本次历史样本中。TTFT 是延迟指标，不参与降智判定。
- HTTP 失败、超时、无合法助手回答或解析失败返回 `error`，`is_degraded=null`，不能把错误页的“can’t”误判正常，也不把网络错误直接判成降智。合法的空字符串回答仍按无匹配短语判为异常。
- HTTPS 为默认要求；HTTP 必须显式 opt-in，适用于运营者管理的本地/私有推理服务。拒绝 URL 中的用户凭据、查询串或 fragment；不跟随任何重定向，不复用 Cookie Jar。内置任务沿用已有的渠道代理和 HTTP 传输配置。
- 输出、日志和历史不保存 Key、上游 URL、原始回答或隐藏推理。错误只输出固定错误码。

### CLI：一次全量或筛选检验

`degradation-check` 默认读取已配置的主数据库，**一次遍历每个仍存在的服务商、每个渠道、每个渠道配置的模型**，执行完成后退出，不再要求手填端点或上游 Key：

```sh
# 全量执行一次
./new-api degradation-check

# 三个维度可以独立或组合过滤
./new-api degradation-check --provider-id 1
./new-api degradation-check --channel-id 10
./new-api degradation-check --model gpt-5
./new-api degradation-check --provider-id 1,2 --channel-id 10,20 --model gpt-5,claude-sonnet-4

# 先预览目标，不发请求、不写任务/检测结果
./new-api degradation-check --provider-id 1 --dry-run

# 只检查启用渠道；本地 HTTP 端点需显式允许
./new-api degradation-check --status enabled --allow-http --timeout 30s
```

- `--provider-id`、`--channel-id`、`--model` 支持逗号列表及重复参数；同一维度取并集，维度之间取交集。模型按渠道 `models` 中**映射前、区分大小写的完整名称**匹配，不是通配符，不使用 `test_model` 替代全量模型，也不向上游发现未配置模型。
- 去重、清除空项后按渠道/模型构建目标，每页内并发请求（同一渠道的不同模型也可并发）。每页 100 个渠道只限制读取内存，**不会限制此次总扫描量**；扫描到开始时的最大渠道 ID 为止。每个渠道使用读取时的配置，执行期间修改配置不会被旧结果覆盖。
- 并发数默认 **8**，由 `DEGRADATION_CHECK_CONCURRENCY=1..64` 配置；CLI 可用 `--concurrency 4` 覆盖环境变量，即使定时器关闭仍生效。网络调用并行，历史落库和 JSONL 输出串行，输出保持渠道 ID/配置模型顺序，不按请求完成先后乱序。取消或失去租约会取消在途请求并停止派发后续目标。
- 默认 `--status all` 包括禁用渠道，但不启用它们；可指定 `enabled` / `disabled`。软删除服务商及孤儿渠道不在扫描集合中。多 Key 仍只使用首个启用 Key，不动轮询状态。
- 复用 `.env` / 进程环境中的 `SQL_DSN`、`SQLITE_PATH` 和数据库里的渠道凭据，不需要 `DEGRADATION_API_KEY`。必须使用已有、已升级的主库：CLI 不自动建库、迁移、创建管理员，不连接日志库，也不启动 HTTP 服务或全局后台任务。
- 手工 CLI 不受 `DEGRADATION_CHECK_ENABLED` 或 master/slave 开关限制。它原子创建一个 running 系统任务，并与定时检测共享租约、心跳，避免后台节点抢走 CLI 的筛选条件或重复执行。有 pending/running 检测时返回忙；CLI 崩溃遗留的过期租约可在下次执行时安全回收。
- `--format chat|responses` 指定兼容 OpenAI 的协议，原生 Anthropic 自动使用 Messages；`--timeout` 为每次请求的超时（1–300 秒），`--allow-http` 必须显式启用明文 HTTP。缺省 HTTPS，不跟随重定向。
- 输出 JSONL：每个目标一行 `kind=result`，包含服务商 ID、渠道 ID、模型名及 `normal/abnormal/error/skipped`，最后一行 `kind=summary` 汇总。每模型结果独立留存，不输出 Key、URL、回答原文或上游倍率。
- 支持 Advanced Custom (58) 的原生 Chat/Responses 路由：按原始模型匹配配置的路由，按映射后的模型替换 `{model}`，按该路由使用 Bearer、自定义 Header 或 query Key（从不输出 URL/Key；禁止重定向）；仅使用原生、非透传请求体的路由，无法安全复现的协议转换仍 `skipped`。`gpt-image-*` / `dall-e-*` 属于图像模型，不能用文字对话判据验证，显示 `unsupported_model_modality`，不能宣称已检验。其他无法支持的模型协议、复杂配置、没有启用 Key 等也会逐项输出 `skipped`，**不假装已完成实际检验**。
- 未指定模型过滤且渠道的所有已启用检测模型都实际产生结果时，才汇总更新渠道：任意模型异常则 `is_degraded=true`；无异常但有请求错误则 `null`；全部正常才是 `false`。任意模型有合法回答则可调用性为 true；全失败则 false。存在跳过项、使用模型过滤、渠道已禁用或配置中途变动时不覆盖渠道整体状态。**不会用最后一个模型的正常结果覆盖其他模型的异常。** 同次完整扫描的渠道 `ttft_ms` 取有效回答中已测得值的最大值，避免并发完成顺序导致随机覆盖；每模型实际值独立保存在历史中。使用 `--model` 的局部扫描仍仅写历史，不覆盖渠道级快照。
- 退出码：`0` 全部选中目标正常（dry-run 非空预览亦为 0）；`1` 有异常但无错误/跳过；`2` 有请求错误、跳过、配置/数据库错误、中断或没有匹配目标。精确退出码的脚本应调用编译后的二进制，不使用 `go run` 的包装退出码。

原单端点诊断保留为独立的 `degradation-probe`，不连接数据库：

```sh
# 由环境/秘密管理工具设置 DEGRADATION_API_KEY
./new-api degradation-probe --endpoint https://example.com/v1/chat/completions --model your-model
```

该诊断命令支持 `--format chat|responses|anthropic`、`--api-key-env`，只读取进程环境（不加载 `.env`），只输出一条 JSON，不写渠道或历史。

### 内置定时任务

默认关闭。通过服务进程的环境变量（或正常服务启动时加载的 `.env`）启用，修改后需重启：

```dotenv
DEGRADATION_CHECK_ENABLED=true
DEGRADATION_CHECK_INTERVAL_MINUTES=60
DEGRADATION_CHECK_TIMEOUT_SECONDS=30
DEGRADATION_CHECK_BATCH_SIZE=100
DEGRADATION_CHECK_CONCURRENCY=8
DEGRADATION_CHECK_FORMAT=chat
DEGRADATION_CHECK_ALLOW_HTTP=false
```

- 间隔为 `1..10080` 分钟，超时为 `1..300` 秒，每批 `1..1000` 个渠道；批次内并发默认为 **8**，可设 `1..64`，不会超过实际目标数。非法配置关闭该任务而不扩大请求量。
- 复用 `channel_degradation_check` 系统任务类型，仅 master 注册/执行；已有数据库租约和心跳保证多 master 去重。关闭该开关的节点不会抢占开启节点的任务；执行前和批次内也检查开关。
- 没有历史任务时启动后即可排队；以后至少在上一批完成并经过配置间隔后再调度。批次之间串行、批次内有界并发，利用上批结果中的渠道 ID 游标轮转，末尾回到开头，跳过的渠道也推进游标。
- 仅选择已启用、服务商未删除的 OpenAI(1)、Anthropic(14)、OpenRouter(20)、Advanced Custom(58)、New API(60)、vLLM(62)、SGLang(63) 渠道。原生 Anthropic 自动使用 Messages；其他支持类型使用 `DEGRADATION_CHECK_FORMAT=chat|responses`。
- 每个渠道检测一个代表模型：优先已启用降智检测的 `test_model`，未设置/未启用则选 `models` 中第一个已启用的非空项；支持精确名称的链式 `model_mapping`，循环映射跳过。Advanced Custom 原生 Chat/Responses 路由亦在候选范围。**应选择实际支持文本对话的模型，这不等同于检测渠道的全部模型。**
- 多 Key 渠道选择第一个启用且非空的 Key，不检测禁用 Key，不改轮询游标或 Key 状态，也不宣称验证了全部 Key。需要复杂 OAuth/云签名的渠道不在本阶段支持范围内。
- 存在 Header/参数覆盖、定制系统提示词、思考转正文、Responses WebSocket、任务插件绑定、部分特殊协议设置等配置时保守跳过，计入 `skip_reasons`，不默默忽略配置并误判降智。没有可用 Key、非法地址/代理同样跳过。
- 成功回答写入 `is_online=true`，并按规则写入 `is_degraded`；实际请求失败写入 `is_online=false`、`is_degraded=null`。更新 `last_checked_at`，成功测得的首段延迟同时写入 `ttft_ms`；失败或未测得则保留上一成功延迟。**不修改渠道启停、路由、Key 或商业倍率**，也不将总耗时当作 TTFT。
- 执行结果的 `normal/abnormal/errors/skipped` 分别计数；系统任务“成功”表示批次执行完成，不表示所有模型正常。
- 请求使用运营者配置的上游凭据，可能产生上游费用。本实现不进入本地用户 relay 配额扣费流程；若目标本身是计费网关，仍遵循该目标的计费规则。

### 历史、身份与并发保护

- **模型侧正向开关**：在控制台「模型」→ 新建/编辑模型 →「为此模型启用降智检测」。保存在管理员模型元数据中，与模型广场可见性、路由状态和官方同步相互独立。**默认关闭**：新建模型和升级前已有模型的检测字段为 `false`/`NULL`，未建立模型元数据的渠道模型也不检测。只有显式开启并保存后才会发起检测；对于未建元数据的模型，先在模型页面创建相应记录再开启。模型元数据现有的精确/前缀/后缀/包含匹配规则仍按“精确优先”生效；禁用的规则记录会阻止其覆盖的渠道模型发送请求。手动 CLI 的“全量”表示遍历所有服务商/渠道/配置模型并逐项报告状态，**不是忽略模型开关强制向所有上游发出付费请求**。
- **渠道侧反向开关**：在「渠道」→ 编辑渠道 →「不检测此渠道的降智情况」，默认关闭，即渠道默认不会被排除。独立于未保存的渠道配置草稿，通过仅返回布尔设置的 `GET/PUT /api/channel/:id/degradation-exclusion` 管理，写入受 `channel.write` 权限控制，读取受 `channel.read` 控制；密钥及渠道内部配置不会返回。关闭某渠道检测时，即使该模型已启用且 CLI 指定了这个渠道，也不会发出检测请求；不会影响渠道正常路由、启停或历史结果。
- **服务商侧反向开关**：在「服务商」→ 编辑 →「不检测此服务商的降智情况」。默认为关闭；打开后手动 CLI、即使显式传 `--provider-id` / `--channel-id` / `--model`，以及定时任务均不检测该服务商下的任何渠道。服务商、渠道及模型开关不影响常规 API 路由、计费、公开目录和存量历史。两项反向开关和模型开关均只通过现有管理员写权限修改。每次扫描在读取对应批次时取得设置快照；修改开关不会撤销正在进行的上游请求，新配置在后续扫描/批次生效。**定时任务仍需服务端 `DEGRADATION_CHECK_ENABLED=true` 才会执行**；手动 CLI 不受该定时器开关限制。
- 不在渠道编辑器中维护逐模型开关：同一模型在不同渠道的检测资格由模型记录统一决定；服务商和渠道均可反向排除自己的整个范围。实际发请求必须同时满足 **模型启用且渠道未排除且服务商未排除**。无法支持的模型即便打开开关也不会被假装判为正常。
- `channels.instance_id` 为不出现在 API JSON 中的内部 UUID：启动回填旧渠道，新建/复制生成新身份，正常编辑保留。唯一索引防止普通写入复用身份；数字 ID 删除后重建也不会串历史。
- `channel_degradation_samples` 独立追加每次有效请求结果，记录样本 UUID、渠道实例/渠道/服务商 ID、请求模型与映射后模型、规则版本、协议、输出预算、超时配置、判定/匹配短语或固定错误码、HTTP 状态、开始/完成 Unix 毫秒、总耗时与可空的 `ttft_ms`。TTFT 迁移向历史表新增可空 BIGINT 列，时序查询迁移只追加复合索引，旧样本保持 `NULL`，不会从旧总耗时推导 TTFT。
- 网络调用不占用数据库事务。快照落库时仍验证任务租约，再锁定渠道并核对实例、凭据/请求配置和观测字段是否仍与发起时一致；取消或失去租约后不能更新快照、路由及任务进度。但已开始评测的真实结果仍独立追加到历史，通过最多 5 秒的脱离取消上下文写入保留在途/已完成观测；不再丢弃整页/批次中的已完成结果。未派发目标及 dry-run 不制造样本。
- 每条历史样本独立提交，重复 UUID 不覆盖既有记录。单代表模型的正常定时检测仍将样本与当前快照同事务提交；全量 CLI 先保存逐模型事实（`snapshot_applied=false`），渠道全部模型完成后再经租约和配置 CAS 校验汇总更新。渠道在请求期间改变或被删除、任务取消或租约丢失时，只保留事实而不发布部分渠道汇总。JSONL 输出失败也会先保留该页已返回的评测结果。数据库写入失败会使任务失败，不假装保存成功。
- 当前历史只有追加入口，无默认过期或清理任务。系统任务历史清理、渠道/服务商删除不级联删除检测样本。
- 这是最小脱敏检测事实记录，不保存回答原文，不能用新算法重新评判旧原文。公开历史查询 UI 已实现；完整配置快照、稳定模型目标表、按 Key 归因和通用测评套件仍属下一阶段。独立 `degradation-probe --endpoint ...` 仍是无数据库、无服务商归属的诊断工具，不伪造渠道历史。

## 评测历史时序存储与 ClickHouse 扩展

### 当前实现：PostgreSQL 为主事实库

- 复用追加式 `channel_degradation_samples`，不新增一套重复事实表，不把快照或临时任务 JSON 当历史，也不需要 TimescaleDB 扩展。开始时间为 Unix 毫秒、TTFT 为可空整数毫秒；UUID 为不可变事件身份。成功、疑似降智、请求失败以及实际已开始但被取消的评测都保留。旧记录的未知 TTFT 不会从总耗时推算。
- 增加 `(provider_id, started_at)` 和 `(channel_instance_id, started_at)` 复合索引，支持服务商/渠道的有界时序查询。兼容原有 SQLite/MySQL 部署；每日 UTC 分桶由对应关系库适配器实现，不将 PostgreSQL 专用表达式放进控制器或前端。读取统计与最近记录使用同一个数据库快照。
- `EvaluationHistoryStore` 提供 `Append(context, immutable sample)` 和 `Read(context, scope)`；接口不携带 `*gorm.DB`、SQL 事务、主库授权查询或页面图表参数。当前关系库适配器绑定主库或已开启的本地事务，正常快照与样本保持原子提交。
- 公开服务商/渠道的可见性先在主库解析为实例 UUID 白名单，再查询历史适配器；无可见实例时不调用历史库。删除/复制不会继承别的实例历史，调整归属不会将原服务商记录转给新服务商；停用渠道/已删除服务商不公开其记录，但后台事实仍保留。当前最多查询 5000 个可见渠道，超限需指定渠道。

### 匿名历史查询与统计口径

```text
GET /api/public/providers/123/history?days=30
GET /api/public/providers/123/history?days=7&model=gpt-5.2
GET /api/public/providers/123/history?days=90&channel_id=456
```

- 仅接受 `days=7|30|90`（默认 30）、可选正整数 `channel_id`、精确且区分大小写的对外 `model`；拒绝重复、空、未知/私有参数。只查询当前公开的渠道及存量逐模型事实，**不套用目录的最新在线/未降智默认筛选来隐藏历史故障**。
- `from/to`、bucket 的 `started_at` 和最近样本时间均为 Unix **毫秒**，与目录的 Unix **秒** `last_checked_at` 不同。范围从第一个 UTC 日 00:00 到当前请求时间，包含今天这个尚未结束的 UTC 日；按真实评测开始时间分桶。
- 返回 `total/online/degraded/errors/unknown`、`average_ttft_ms`、恰好 7/30/90 个每日 `buckets`，以及 `recent`（范围内最新 50 条，不是全部历史）。最近记录仅含样本 UUID、公开渠道 ID/名称、对外模型、状态、开始/完成时间和可空 TTFT。
- `normal/abnormal` 均计为可调用，后者另计疑似降智；真实请求错误计为失败。取消、无效目标/协议/代理/请求配置保留为 `unknown`，不能当作上游离线。检测可调用率为 `online / (total - unknown)`，不是连续时长 SLA；没有可判定样本时不显示百分比。无样本日为明确灰色缺口，不生成故障/100% 在线或 0ms。
- TTFT 每日均值与全范围加权均值只使用可调用且实际测得 TTFT 的样本；零毫秒是有效数值，缺测不插值。历史记录可以展示失败流中已测得的 TTFT，但不会纳入成功均值。跨模型/渠道均值仅供参考，不是独立模型性能保证。
- 所有响应使用独立白名单 DTO/SQL 投影，不返回渠道实例 UUID、映射后模型、Key、Endpoint、任务/规则配置、匹配短语、原始错误/回答或上游成本。

### 后续 ClickHouse：异步镜像而非事务内双写

当前**没有实现或启用 ClickHouse 评测写入**；本次落地的是可替换的存储契约和扩展边界，不要求部署 ClickHouse。

1. 保持 PostgreSQL 为主事实来源和渠道/权限主库。未来增加独立的历史存储配置与 ClickHouse 适配器；不要把 `SQL_DSN` 换成 ClickHouse，也不要把评测记录交给消费日志库的清理策略。读适配器可独立切换，公开 API 与前端类型不随底层存储改变。
2. 若启用镜像，在**样本提交的同一个 PostgreSQL 事务**中追加 outbox，后台批量写入 ClickHouse 后确认。禁止在快照事务内同步远程双写，也不能仅用进程内提交后回调，否则故障会丢事件或拖住主库事务。取消/过期任务的历史追加路径同样需要 outbox。
3. 采用至少一次投递、UUID 幂等身份、可重试/死信/积压监控。用待投递状态/租约认领 outbox，成功后确认；不要仅按评测开始时间、随机 UUID 或最大序号水位拉取，这些都可能跳过晚到/未提交事件。迁移时对既有 UUID 做可重入回填。
4. ClickHouse 字段保留事件 UUID、服务商/渠道实例/公开模型维度、规则版本、状态、UTC `DateTime64(3)` 与 `Nullable(Int64)` TTFT；可按月分区、按服务商/实例/模型/开始时间/UUID 排序。原始不可变事实与统计层分开，仍支持晚到结果及未知值。
5. 可以选 `ReplacingMergeTree` 等去重方案，但不能把后台最终合并当即时唯一约束：有界查询需要显式去重（如适当的 `FINAL` 或 UUID 去重子查询）。普通求和型物化视图可能重复计数重投事件，不能直接对未去重输入累加在线率/均值；统计要保留 TTFT 样本数和总量，而非平均值的平均值。
6. 新适配器须复跑相同的可见性、UUID 幂等、取消/错误分类、零/空 TTFT、时间范围、跨库一致性和私有字段 canary 测试；查询切换前验证积压/同步水位，不能将镜像延迟展示成真实缺测或故障。

## 未来测评：独立目标、执行与不可变结果

以下通用测评平台表尚未实现。本阶段专用检测结果保存在 `channel_degradation_samples`，不将临时系统任务结果充当永久测评历史。

### 1. 测评目标与配置快照

引入 `channel_model_targets`：稳定 UUID、渠道实例 UUID、`channel_id`、对外模型名、启停状态、创建/归档时间。不要只依赖 `models.id`（厂商模型元数据），也不要把 `abilities` 作为测评主键（路由能力可能重建）。

渠道实例 UUID 的创建/回填/复制生命周期已通过内部 `instance_id` 实现；后续目标表必须沿用此身份，不能仅用数字 channel ID 合并历史。目标归档后仍保留记录，重新启用同一目标沿用其身份。

引入不可变 `channel_config_snapshots`，用版本/哈希去重：记录 Provider ID/名称、渠道实例 UUID/名称/类型、上游地址的非敏感部分、对外模型名与映射后的上游模型名、倍率、影响请求的配置版本。变更归属、映射或倍率不重写历史快照。

测评执行时固定目标、Provider 归属和配置快照，不在完成时按当前渠道配置反查归属。禁止保存可用 Key、认证 header、代理凭据、完整敏感 override。若以后需要按 Key 测评，先引入不可复用的 credential UUID + rotation version，不能使用当前多 Key 数组下标作为长期标识。

### 2. 执行与原始结果

- `evaluation_runs`：执行 UUID、目标 UUID、配置快照、测评套件与版本、基线版本、调度来源、运行地区、请求参数/数据集摘要、开始和结束时间、状态、错误分类、重试 attempt。幂等键约束同一执行/样例/attempt，避免任务重投重复入库。
- `evaluation_samples`：一次请求/样例一条事实，关联 run UUID、target UUID、Provider 快照 ID、采样时间；保存 TTFT、总耗时、吞吐、token 数、成功状态、评分与证据引用。终态样本不可覆盖；重试、新算法复判产生新记录，并引用原样本。
- 先采用独立于消费日志的关系表（SQLite/MySQL/PostgreSQL 通用类型），时间使用 UTC Unix 毫秒，时长用整数毫秒；扩展指标可用 TEXT JSON，但高频筛选字段保持独立列。
- 索引至少包括 `(target_uuid, measured_at, id)`、`(provider_id_snapshot, measured_at, id)`、`(run_uuid, sample_id, attempt)` 唯一约束；查询采用时间范围和游标分页，避免长期数据量下的大 offset 扫描。

### 3. TTFT 和“是否降智”的语义

- TTFT 使用同一次请求的单调时钟差值，从发送请求前开始，到首个有效模型输出 token 到达。连接/排队是否计入、是否把 reasoning 算作输出都由指标版本定义并记录，不能混用首个 HTTP 字节、role-only chunk、keepalive 或空 SSE 事件。若需要“首个用户可见正文”延迟，另设指标。
- 非流式响应无法可靠测 TTFT，应记录 `NULL` 与原因；超时/失败也不记为 0。
- “是否降智”不能是默认 false 的简单布尔值。使用 `unknown / normal / suspected_degraded / degraded`，记录评分、置信度、阈值、基线模型/版本、数据集/判定算法版本和证据引用；无测评、样本不足和判定失败都是 unknown。
- 固定测评样例与推理参数，记录并发、重试、区域、输出 token 限制、温度等条件。性能测评和能力/真实性测评分开，慢不等于降智；单条波动不能直接判定渠道降智。

### 4. 长期留存与生命周期

- 不复用 `channels.response_time/test_time`、消费 `logs`、系统任务历史表来承载历史样本；这些现有字段/表可能覆盖或清理。
- 原始结果追加保存，默认不自动过期。消费日志清理、任务历史清理、渠道/Provider/模型删除不得级联删除测评数据。
- 原始证据较大时进入对象存储：保存 URI、校验和、schema/脱敏版本；对象生命周期必须与结果留存策略一致，不得数据库长期保留但证据自动过期。禁止凭据或用户真实对话进入测评证据；采用专用合成数据集。
- 另建 `evaluation_rollups` 保存按小时/天、目标、套件/基线版本的样本量、成功率、TTFT p50/p95、评分分布；聚合可重建，**不能以聚合替代原始数据**。不同窗口的分位数不能简单平均。
- 大规模时将历史事实迁往专用分析库/冷存储，主库保留目标、任务元数据和近期查询投影；迁移校验行数、校验和及时间覆盖范围，不先删除源数据。分区属于部署优化，不让基础模型依赖某个数据库特性。
- 提供备份和恢复演练、写入失败重试/告警、人工归档流程。任何以后新增的删除/保留时限策略必须显式配置、审计并有权限保护。

## 验证与安全边界

### 启发式降智检测阶段

流式/并发扩展验证：
- `go test -p 2 ./...` 通过。
- `go test -race -p 2 ./service -run 'TestDegradation|TestAdvancedCustomDegradation' -count=1` 通过；覆盖三种 SSE 协议、跨事件拼接、role/思考/完成事件不冒充 TTFT、真实零值、空流/截断/错误、非流式回退、默认 8 并发、环境变量与 CLI 覆盖、定时任务与手动扫描、取消和稳定 JSONL 顺序。
- 设置下述专用测试库 DSN 后，`go test ./service -run TestDegradationDatabaseMatrix -count=1 -v` 在 SQLite **3.50.4**、MySQL **8.0.46-0ubuntu0.24.04.4**、PostgreSQL **16.15** 通过；包含新建/升级/重复迁移、旧样本 TTFT 保持 NULL、成功回写/零值、失败保留旧延迟、手工修改保护及多模型最大 TTFT 汇总。未执行付费上游探测。

- 回归集中在 `service/degradation_test.go`：精确提示词、四种短语及大小写/撇号、正常/异常/失败分离、Chat/Responses/Anthropic 正文解析、隐藏推理不参与判定、HTTP 错误与超大响应、超时/取消、阻止重定向泄漏、CLI 退出码与输出脱敏、env 默认关闭和边界、定时任务注册/间隔/轮转、禁用渠道与 Key、实际任务租约、模型映射、过期结果不覆盖、历史留存和实例 ID 重用。
- Advanced Custom 探测测试覆盖模型路由匹配、上游 `{model}` 替换、自定义 Header/query 鉴权、禁止默认 Bearer 叠加、图像模型不会被错当聊天检测、协议转换保守跳过及脱敏输出。模型/服务商开关测试覆盖新建及迁移模型默认关闭、未建模型元数据默认关闭、精确与前缀模型元数据匹配优先级、服务商/渠道默认不禁用、渠道与服务商反向开关高于 CLI 显式过滤、禁用后 CLI 不发请求、定时任务改选启用模型，以及重复迁移时开关的持久性。全量 CLI 的数据库矩阵还覆盖：三个维度独立/组合/重复/不存在过滤、配置全部模型而非 test_model、禁用渠道、软删除/孤儿排除、模型去重、跨越 100 渠道分页、混合正常/异常/错误的汇总、模型过滤不覆盖整体状态、dry-run 无请求/落库、任务互斥/过期恢复、真实 CLI 数据库连接且不建空库，以及输出脱敏。
- `TEST_MYSQL_DSN` / `TEST_POSTGRES_DSN` 必须指向专用数据库 **`zzz_degradation_test`**（测试会清理其中的相关表）。设置后运行 `go test ./service -run '^TestDegradation' -count=1 -v`：SQLite **3.50.4**、MySQL **8.0.46-0ubuntu0.24.04.4**、PostgreSQL **16.15** 的新库、从上一版 Channel 结构重建的升级库、重复迁移和数据保护均通过。
- 模型和服务商开关沿用现有受管理员保护的模型元数据 / 服务商写接口；渠道反向开关单独使用受 `channel.read` / `channel.write` 约束的 GET/PUT；公开目录的服务商/渠道 DTO 为显式字段白名单，不暴露设置。渠道服务层跨三数据库矩阵验证模型默认关闭、渠道/服务商反向开关默认关闭、覆盖规则和升级后的默认值；路由测试验证匿名拒绝、只读权限、布尔输入及密钥不外泄。前端 `bun run test src/features/channels src/features/models src/features/providers` **29 文件 / 357 测试通过**；`bun run typecheck`、变更文件 lint 和 `bun run build` 通过。
- 未设置临时矩阵 DSN 的根模块 `go test ./...` 通过；编译后的真实 CLI 对本地 HTTP 测试端点验证单端点诊断 `normal/abnormal/error` 的退出码 `0/1/2` 和脱敏输出通过。全量 CLI 另以已有 SQLite 库和 `.env` 验证：无参数扫描两个服务商的全部三个模型（异常退出码 1）、三个维度联合过滤仅发一次请求（退出码 0）、dry-run 零请求（退出码 0）。没有使用真实上游 Key 或对付费上游执行实测。
- 没有修改日志数据库、认证/凭据存储或本地计费流程。未验证最低版本数据库或生产备份迁移；发布前仍需备份并在实际部署版本演练升级。此规则的识别准确率未被验证，不声明是可靠的智力/身份检测或整站安全合规认证。

### 公开目录接口阶段

- 回归集中在 `router/public_catalog_test.go` 的 `TestPublicCatalogDatabaseMatrix`，通过真实 Gin 路由、控制器和数据库查询验证三个接口，覆盖默认/显式筛选、未知与零值、双向排序、稳定分页、过滤计数、服务商隔离、停用/软删除/孤儿数据、无权限管理员接口访问、私有字段白名单及故障信息不外泄。
- 设置临时独立库 DSN 后，`go test ./router -run TestPublicCatalogDatabaseMatrix -count=1 -v` 在 SQLite **3.50.4**、MySQL **8.0.46-0ubuntu0.24.04.4**、PostgreSQL **16.15** 上全部通过。
- 不设置临时矩阵 DSN 的根模块 `go test ./...`、修改文件的 `gofmt` 和 `git diff --check` 通过。本阶段无 schema、前端、计费或认证流程改动；未对最低版本数据库做验证。
- 公共数据授权和字段隔离沿用下文 OWASP ASVS 5.0.0 的 8.1.1/8.1.2、8.2.3、8.3.1 及 Authorization Cheat Sheet：匿名访问仅限明确定义的公共投影，不以客户端隐藏字段代替服务器约束。

### 渠道展示/检测字段扩展

- `model/channel_metadata_test.go` 的 `TestChannelMetadataDatabaseMatrix` 验证 SQLite **3.50.4**、MySQL **8.0.46-0ubuntu0.24.04.4**、PostgreSQL **16.15** 的新库、从上一版 Provider-enabled Channel 结构重建的升级库及重复迁移；覆盖默认值、未知/false/零、部分更新、显式清空、独立启停状态、原多 Key 数据与索引、完整保存及批量原子性。
- 独立测试库中设置 `TEST_MYSQL_DSN` / `TEST_POSTGRES_DSN` 后运行 `go test ./model -run TestChannelMetadataDatabaseMatrix -count=1 -v`，三个实际数据库全部通过。仅对这一矩阵传入临时 DSN，避免与其他矩阵的 TCP/URL DSN 格式约定混用。
- 不设置上述 DSN 时，根模块 `go test ./...` 通过；SQLite API 回归在 `controller/channel_authz_test.go` 中覆盖输入类型/范围、省略与 null 语义、普通写权限与密钥保护、复制后检测字段重置。
- 前端 `bun run test src/features/providers src/features/channels src/hooks/__tests__/sidebar-config.test.tsx`：**24 个文件、314 个测试通过**，包含桌面表格/移动卡片展示、三态选择、零毫秒、秒精度时间和清空、创建/编辑 payload。`bun run typecheck`、变更文件 oxlint、生产 `bun run build` 通过。
- 字段扩展阶段未新增检测执行器、公开访问接口或历史记录表；未改变认证、密钥存储、计费与路由。没有对生产备份或最低版本数据库进行升级演练；发布仍需针对实际部署备份验证。

### 服务商入口阶段

前端回归位于 `web/src/features/providers/__tests__/management.test.tsx`、`web/src/features/channels/components/__tests__/channel-provider-fields.test.tsx` 和既有侧边栏测试，覆盖管理操作、服务端业务失败、只读权限、直达路由拒绝、未保存保护、重复提交、选项分页、渠道筛选、多 Key 表单保留以及语言切换。

服务商入口阶段前端验证：`bun run typecheck`、变更文件的 oxlint、生产 `bun run build` 通过；`bun run test src/features/providers src/features/channels src/hooks/__tests__/sidebar-config.test.tsx` 共 24 个文件、305 个测试通过。全量 `bun run test --maxWorkers=2` 为 2110 通过、2 个使用日志本地化测试超时；对 `HEAD:web` 的独立基线目录复测，`detail-preview.test.tsx` 和 `quota-adjustment.test.tsx` 的相同用例仍然超时，未扩大范围修改这些既有测试。全仓版权检查也存在既有文件缺少头信息，本次新增前端文件均已补齐，未批量修改其他文件。

后端验证代码：`model/provider_test.go`、现有 `controller/channel_authz_test.go` 和 `router/channel_router_test.go`。覆盖关联校验、批量原子性、多 Key 保留、软删除、默认归属、部分更新、倍率边界、筛选分页和统计、真实路由权限链、只读字段注入。

数据库矩阵通过环境变量 `TEST_MYSQL_DSN` / `TEST_POSTGRES_DSN` 指定**专用测试库**；未提供时对应项会 skip，skip 不代表兼容性验证通过：

```sh
go test ./model -run TestProviderDatabaseMatrix -count=1 -v
go test ./model ./controller ./router
```

服务商入口阶段实际验证结果：

| 命令 / 环境 | 结果 |
| --- | --- |
| `go test ./...`（根 Go 模块） | 通过 |
| `go test ./model -run TestProviderDatabaseMatrix -count=1 -v` / SQLite 3.50.4 | 新库、升级、重复迁移均通过 |
| 同上 / MySQL 8.0.46-0ubuntu0.24.04.4（隔离 Unix socket 实例） | 新库、升级、重复迁移均通过 |
| 同上 / PostgreSQL 16.15 (Ubuntu 16.15-0ubuntu0.24.04.1)（独立测试库） | 新库、升级、重复迁移均通过 |
| `git diff --check`、修改 Go 文件的 `gofmt` 检查 | 通过 |

矩阵同时测试新库和基于本次变更前 Channel 结构重建的升级库，每种迁移执行两次。没有修改日志库路径。未运行 MySQL 5.7 / PostgreSQL 9.6 最低版本或生产备份升级演练；发布前仍应使用实际目标部署版本的备份做升级演练，不能把重建 fixture 视为所有历史发布版本的兼容性证明。

权限设计参考 OWASP ASVS **5.0.0** 的 8.1.1/8.1.2、8.2.1/8.2.3、8.3.1，以及 Authentication、Session Management、Authorization Cheat Sheets：服务端复用现有认证和权限中间件，默认拒绝未认证/无权限请求；商业字段不放宽密钥写入/查看权限；审计不保存凭据。本次没有改变认证、会话或 API Key 存储机制，也不声明完成整站 ASVS 合规审计。
