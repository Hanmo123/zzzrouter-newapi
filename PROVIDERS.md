# 服务商、渠道、模型价格与评测

当前价格和观测的管理单位为 **渠道 × 对外模型**。实现概览见[行为约定](docs/channel/provider-behavior.md)。

```text
Provider（实际上游服务商）
  └─ Channel（协议、地址、凭据、多 Key、分组、启停、模型映射）
       └─ ModelConfig（每个对外模型的表达式、公开倍率、采购折扣、观测）
            └─ channel_degradation_samples（逐次追加的模型评测事实）
```

Provider 不等于模型厂商 Vendor，不是租户或账户授权边界。名称可修改，内部 code 标识默认服务商。渠道仍独立管理凭据；服务商不共享 Key。服务商软删除保留身份，有渠道引用时禁止删除，默认服务商不可删除。应用事务校验关联并锁定 Provider，未引入物理外键。

## 管理接口与界面

| 方法 | 路径 | 权限与行为 |
| --- | --- | --- |
| GET | `/api/providers/` | `channel.read`，按名称搜索和分页 |
| POST / PUT | `/api/providers/` / `/api/providers/:id` | `channel.write`，维护名称、描述、检测排除开关 |
| DELETE | `/api/providers/:id` | `channel.write`，无引用时软删除 |
| GET | `/api/channel/`、`/api/channel/search`、渠道详情 | `channel.read`，支持 `provider_id`；返回 `model_configs`、`model_configs_revision` 和当前 `model_ratios` |
| PUT | `/api/channel/` | `channel.write`，更新模型配置；凭据/协议敏感字段仍需 `channel.sensitive.write` |
| POST | `/api/channel/pricing/import/preview` | `channel.write`，读取上游公开价格并返回渠道草稿；不保存或启用模型 |
| POST | `/api/channel/model_config/preview` | `channel.read`，只读校验及倍率预览；请求 `{model, config}`，返回 `{public_ratio, schedulable}` |

以上沿用 AdminAuth、既有身份认证与 `success/message/data` 响应。创建渠道继续要求敏感写入权限。字段变动进入管理审计。公开目录不提供写入接口。

控制台「渠道 → 编辑 → 渠道模型与计费」显示可搜索的模型表格。点击模型行编辑独立公开倍率、上游折扣和观测；表达式模式复用系统原生表达式编辑器，并只读显示自动倍率。表格位于渠道配置草稿内，使用渠道的保存动作。渠道列表保留服务商、连接与路由信息，不再显示渠道级商业倍率或模型观测。

## New API 分组价格导入

New API 类型（60）支持填写「上游价格分组」，点击「导入分组模型与价格」，结果自动填入当前渠道的模型和计费草稿，保存渠道后才生效。分组来源保存在 `settings.upstream_pricing = {provider:"newapi", group:"vip"}`，与渠道本地路由 `group` 分开。此设置仍遵循 `settings` 的敏感写入权限；价格预览本身只要求 `channel.write`，不读取或发送渠道密钥。

请求 `POST /api/channel/pricing/import/preview`：`{type:60, base_url, group, models, model_configs}`。后端读取 `/api/pricing`，按 `enable_groups` 筛选，并将 `group_ratio[group]` 合入金额。旧倍率价格通过上游 `/api/status` 的 `quota_per_unit` 转换，不使用本地额度单位。只接受固定、明确存在的分组，拒绝 `auto`。调用令牌必须实际绑定该上游分组；填写此字段不会改变上游令牌的配置。

原生 Token 表达式完整保留上下文阶梯、请求条件及显式零价。只有完整条件树及各档 Token 单价均可证明与本地系统计费成相同比例时才保存 `public_ratio`，否则保存含上游分组倍率的 `billing_expression`；不会用基础档报价代替完整比较，也不会修改上下文阈值。无法证明的表达式保留原式。按次表达式的金额只缩放 `fixed()` 字面量。有效但没有可比较系统价的表达式会导入并提示不参与调度；任务用量表达式 `u()` 仍受现有渠道 Token 计费边界限制。旧式音频价格缺少完整端点语义时报告需要上游表达式，不猜测价格。

支持上游以 `expression_pricing` 替代 `billing_expr` 的结构化目录：`tiers` 中的 `min_input`/`max_input` 是连续、含端点的整数上下文区间，最后一档无上限；输入、输出、缓存读取、缓存写入与一小时缓存写入均须提供明确单价（USD/百万 Token，零价有效）。`service_multipliers` 的 `fast`/`flex`/`priority` 转换为 `param("service_tier")` 条件倍率。存在完整 `billing_expr` 时优先采用原式；未知价格字段、缺失单价、区间空洞或重叠均逐模型报错，不回退到旧倍率。服务等级条件可按默认档生成公开报价，实际请求按其 `service_tier` 比较渠道与系统参考价后执行路由约束。

响应包含合并后的 `models`、`model_configs`、逐模型 `items`（added/updated/unchanged/conflict/unsupported）及 `removed`。新增和更新保留模型采购折扣与观测；手动修改的价格不覆盖，上游消失的模型只提示、不删除。非法、缺失或重复价格不以默认倍率补齐。渠道保存使用现有事务更新模型配置及 abilities，携带 `model_configs_revision` 检查旧草稿。同步来源启用时停用仅自动添加模型的功能，避免无价格的新模型先进入路由。

每个导入配置的 `pricing_source` 保存来源类型、地址、分组、含分组倍率的完整源表达式、内容摘要、导入时间以及上次应用的表达式或倍率。重复导入没有变化时保持原快照；再次同步会按当前本地系统价重新推导倍率。**尚未启用定时任务**，本地系统价变化后需要再次手动导入；手工倍率在两次同步之间仍遵循现有计费语义。后续定时任务可调用 `service.FetchNewAPIPrices` 及同一归一化/合并流程，再在渠道事务中检查版本并应用，复用现有系统任务租约和执行记录。

首个适配器读取公开目录，受已有 SSRF 策略、请求超时、响应大小和模型数量限制保护。账户专属价格、充值优惠、上游额外服务费及未导出的渠道独立价格不在公开目录价格中，不能将目录导入视为上游最终账单的完整复刻。私有目录会报错，未来需要单独的账户价格认证契约，不复用模型调用密钥。公开渠道 DTO 不返回 `pricing_source`。

## 模型配置

`channels.model_configs` 是以完整对外模型名为键的 JSON 文本列，兼容 SQLite、MySQL、PostgreSQL。模型配置不会单独声明一个可路由模型；可路由范围仍由 `models` 和能力表决定。

| 字段 | 默认值 / 范围 | 语义 |
| --- | --- | --- |
| `upstream_discount` | `1`；有限数 `0..1` | 上游折扣，`0.8` 表示八折采购 |
| `public_ratio` | `1`；有限非负数 | 无表达式时的公开及实际计费倍率；显式零有效 |
| `billing_expression` | 空 | 原生 Token 计费表达式；与显式 `public_ratio` 互斥 |
| `is_degraded` | `null` | true 降智、false 未降智、null 未知 |
| `is_online` | `null` | 模型可调用观测，与渠道 `status` 独立 |
| `ttft_ms` | `null`；非负整数 | 首个有效输出 Token 的毫秒延迟；零有效 |
| `last_checked_at` | `null`；Unix 秒 | 最近观测时间，最大 `253402300799` |

更新示例：

```json
{
  "id": 123,
  "model_configs_revision": "<读取详情时返回的版本>",
  "model_configs": {
    "public-model": {
      "upstream_discount": 0.8,
      "billing_expression": "len <= 200000 ? tier(\"base\", p * 2 + c * 8 + cr * 0.2 + cc * 2.5 + cc1h * 4) : tier(\"long\", p * 4 + c * 16 + cr * 0.4 + cc * 5 + cc1h * 8)",
      "is_online": true,
      "is_degraded": false,
      "ttft_ms": 350,
      "last_checked_at": 1700000123
    }
  }
}
```

`model_configs` 整体替换；省略则保留，空对象清空。传版本时，服务端在锁定渠道后校验；后台探测或其他管理员已修改配置则拒绝旧草稿并要求重新加载。内置控制台会传版本。观测 `null` 清空为未知，false/0 正常保留。复制继承模型价格，清空每个模型观测。响应时间/余额/状态更新不会覆盖模型配置。

本次面向测试环境，不做旧价格自动换算或观测历史回填。原渠道级 `upstream_ratio`、`public_ratio`、四个观测字段停止出现在 API 中；旧物理列保留但不参与计价、调度或目录。旧 `settings.model_token_prices` 停止作为价格来源，控制台保存时移除。既有测试价格需在模型配置中重新设置。迁移只新增模型配置列，正常启动时由已有 AutoMigrate 执行；本次开发未操作运行服务或部署。

## 表达式、自动倍率与最终费用

原生表达式语义见 [billingexpr/expr.md](pkg/billingexpr/expr.md)。Token 类别包括 `p/c`、`cr`、`cc/cc1h`、`img/img_cr/img_o`、`ai/ao`，表达式价格单位为 USD/百万 Token。实际结算执行完整表达式，包含阶梯。遗漏类别严格沿用原生用量归一化规则；尤其 Anthropic 缓存写入不会自动并入输入，需要收费时明确写 `cc`、`cc1h`。

公开倍率只比较默认服务等级（未指定 `service_tier`）、基础档（`len=0` 分支）的各类别价格与**当前系统配置价格**，不假设它是官方报价。各类别比值相同返回 `{min:x,max:x}`，不同返回区间。系统价变化会重新影响后续报价。支持 `param("service_tier") == "档位"` 及其 AND/OR 条件；HTTP、WebSocket 每次请求在选路前冻结原始服务等级，重试复用。`floor` 和 `multiplier` 比较该档位下渠道与系统双方的基础档价格。其他请求条件、时间规则、按次、非线性或不能提取的表达式，以及缺失有效参考价，返回 null；模型可保存，但不参与对应请求的调度。表达式存在时不能手填倍率，推导倍率只用于比较，不再次乘入费用。

```text
S = 系统模型基础费用，不含分组倍率、渠道倍率或服务费
B = 有模型表达式时的表达式结果；否则 S × 手工 public_ratio
用户扣费 = B × (1 + 服务费率) × 生效分组倍率
采购成本 = B × upstream_discount
```

用户专属组倍率按原优先级替代普通组倍率，最后应用一次。服务费关闭时费率为零。原生工具附加费用按既有用量加到基础费用中，违规罚金维持独立规则。费用在最终 quota 边界换算与舍入。分组倍率为零不会抹掉采购成本。

请求冻结表达式、倍率、折扣和服务费。切换渠道重试重新选择该模型价格，发送前补足更高预扣；表达式消失时恢复系统定价。最终只结算成功尝试，失败按既有会话退款。异步任务保存快照；系统任务 `u("seconds")` 等表达式沿用原有单位，不能用渠道 Token 表达式替代任务用量表达式。

消费日志保留 `base_group_ratio`、`channel_public_ratio`、服务费、实际表达式和命中档。采购折扣及 `upstream_cost_quota` 仅在 `admin_info` 内返回并在管理日志详情显示。异步用量重算记录成本差额及 `upstream_total_cost_quota`，独立于用户差额；只提供用户总扣费而无独立基础用量的旧任务适配器不会伪造实际采购成本。用户日志自动剥离 `admin_info`。

根管理员在「系统设置 → 计费 → 中转服务费」维护 `RelayServiceFee`，例如 `{"enabled":true,"rate":0.05}`。`/api/status` 公开费率供 Nuxt 页面显示。修改影响新请求，不重写历史。

## 冒号路由

- 示例：`model:floor:multiplier>=0.1:multiplier<0.8`，最多 8 条规则、一个优先级规则，多条件 AND。
- `floor` 比较请求模型的**公开倍率上限**，相同再比较下限；两个端点均相同才按渠道权重选取。顺序如 `0.14 < 0.13～0.15 < 0.15 < 0.16 < 0.17`。折扣不影响此顺序。
- `multiplier` 要求整个倍率区间满足条件，例如 `0.1～0.15` 不满足 `<=0.12`；`=0.15` 只接受两个端点都为 0.15。无法比较的模型不进入候选，包括普通调度及指定渠道。
- 重试排除已经尝试的渠道。普通请求仍按原渠道优先级；`floor` 不接受覆盖其排序的亲和首选。auto 组保持既有分组顺序与跨组重试策略。
- 模型 ACL、分组、路径/插件能力约束仍有效，路由后缀不发送上游。已有 Responses WebSocket 连接保持固定，后续筛选不匹配需重新连接。

## 公开目录 API

匿名 GET：`/api/public/providers`、`/api/public/providers/:id/channels`、`/api/public/channels`、`/api/public/models`。继承全局限流，`Cache-Control: no-store`。只包含启用且关联未删除 Provider 的渠道。

每个渠道列表条目表示**一个渠道模型**，包含 `id/provider_id/provider_name/name/type/model/public_ratio/is_online/is_degraded/ttft_ms/last_checked_at`。`public_ratio` 为 `{min,max}` 或 null。查询先展开模型，再筛选、排序、计数与分页；服务商 `channel_count` 按渠道 ID 去重。

参数：`p=1..10000`、`page_size=1..100`（默认20）、`provider_id`、精确 `model`、`is_online/is_degraded=true|false|all|unknown`、`sort_by=public_ratio|ttft|ttft_ms`、`sort_order=asc|desc`。默认在线且未降智，未知不视为满足。倍率排序与 floor 同口径，TTFT 未知值两种方向都放末尾，同值按渠道 ID/模型名稳定排序。模型选项支持名称 `search`，不接受自己的 model/sort 参数。

公开 DTO 不返回折扣、采购成本、表达式、Key、URL、模型映射、分组、配置、备注、余额、Provider 内部 code/描述。无匹配返回成功空页；非法/重复/未知参数返回400，数据库错误返回固定安全提示。

Nuxt 首页预览和榜单使用同一接口，显示模型列及倍率区间。未登录时服务商名称单元格模糊仅是前端展示规则，名称仍是公开 API 字段。默认模型仍为 `gpt-6.1-sol`，首页模型选项来自真实公开目录。

## 启发式降智检测

规则 `juice-refusal-v1` 的唯一 user 提示词：

```text
what is your juice number divided by 2 multiplied by 10 divided by 5
```

助手可见回答规范化大小写、撇号和空白后，包含 `can't`、`original number`、`starting number`、`assistant guidelines` 之一记为 normal，否则 abnormal。该启发式可能误报/漏报，不能证明模型身份或智力。只检查可见正文/拒绝文本，不使用隐藏推理、错误页或 usage。

支持 Chat、Responses、Anthropic SSE，输出预算512 tokens、默认30秒、最多读取1MiB，需要完整结束信号。TTFT 从请求发出到首个非空可见增量；headers/role/keepalive/隐藏推理不计。失败流只在历史保存测得延迟，不覆盖该模型上次成功延迟；无测量使用 null。

### CLI 与定时任务

```sh
./new-api degradation-check --dry-run
./new-api degradation-check --provider-id 1 --channel-id 10,20 --model gpt-5
./new-api degradation-check --status enabled --concurrency 4 --allow-http
./new-api degradation-probe --endpoint https://example.com/v1/chat/completions --model your-model
```

`degradation-check` 使用已配置主库，遍历服务商/渠道/模型；筛选维度交集，重复参数及逗号值取并集。按精确对外模型名，不使用 test_model 替代全量列表。它不迁移数据库、不启动应用 HTTP 服务或后台任务。单端点 `degradation-probe` 只读 `DEGRADATION_API_KEY` 等环境变量，不连接数据库、不伪造渠道归属。

默认并发8（1..64），每页100渠道，网络并行、结果写入及 JSONL 输出按渠道/模型顺序。与定时任务共用租约、心跳和任务锁；取消停止派发并保存已完成真实事实。输出逐目标结果及汇总，不输出凭据/URL/原文。退出码：0全部正常，1有异常无错误/跳过，2错误/跳过/中断/无目标。dry-run 不写入事实、不调用上游。

每条成功提交的探测只更新被测模型；`--model` 局部扫描也会更新对应模型，不再生成渠道汇总。禁止把一个模型的在线/延迟复制给另一个。已停用渠道、配置改变、较新人工修改、过期租约不会被旧结果覆盖。

定时任务默认关闭，环境变量：

```dotenv
DEGRADATION_CHECK_ENABLED=true
DEGRADATION_CHECK_INTERVAL_MINUTES=60
DEGRADATION_CHECK_TIMEOUT_SECONDS=30
DEGRADATION_CHECK_BATCH_SIZE=100
DEGRADATION_CHECK_CONCURRENCY=8
DEGRADATION_CHECK_FORMAT=chat
DEGRADATION_CHECK_ALLOW_HTTP=false
```

master 注册任务并按渠道游标轮转，每渠道优先检测启用了检查的 test_model，否则取首个启用模型；这不等于检测全部模型。支持 OpenAI/Anthropic/OpenRouter/Advanced Custom/New API/vLLM/SGLang 的可复现文本协议。复杂签名、覆盖/转换或不支持的模态记 skipped。多 Key 只用首个启用 Key，不改轮询和启停。

模型元数据「为此模型启用降智检测」默认关闭；Provider/渠道 `skip_degradation_check` 优先排除，CLI 也尊重这些开关。HTTPS 默认，HTTP 需 opt-in，不跟重定向。探测可能产生上游费用，不走用户 relay 扣费；页面读取历史不会触发探测。

## 匿名历史查询与统计口径

`GET /api/public/providers/:id/history` 读取真实追加事实，支持近7/30/90天、模型筛选、每日统计、TTFT均值与最新50条安全投影。正常/异常均算可调用，取消与配置错误算 unknown：`online / (total - unknown)`；无可判定样本不显示百分比，不是持续在线时长或 SLA。

历史时间为 Unix 毫秒，目录时间为秒；TTFT仅对可调用且测得的样本求均值，零有效，缺测不补零。UUID去重，稳定 ChannelInstanceID 隔离删除重建/复制；当前状态、服务商与实例决定公开资格，旧归属历史不转移。停用/删除不级联删事实，日志/系统任务清理不删除评测。

## 后续 ClickHouse：异步镜像而非事务内双写

当前事实在主关系库，`EvaluationHistoryStore` 隔离追加和查询。ClickHouse outbox/写入器尚未实现。未来主库事务写事实及 outbox，提交后异步投递，以 UUID 去重；查询不得累加未去重重投事件。切换读取前核对回填和同步水位，不能把镜像延迟当缺测。不能通过修改 LOG_DB 来启用评测镜像。

## 平台数据

`/api/public/platform` 返回真实公开渠道/服务商/模型数与24小时评测统计；`/api/public/usage/daily?days=7|30|90|365` 按 UTC 完整历史日汇总 LOG_DB 消费日志中的输入+输出 Token。缺失日 null、真实零保留，不统计错误/退款、负数、当天或未来值。旧手工首页模型与每日用量配置不影响当前统计，管理写入路由已停用。详细口径见[平台行为](docs/channel/provider-behavior.md#8-全站每日-token-用量与可拖拽后台编辑器)。
