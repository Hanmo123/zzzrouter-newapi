# Provider、渠道与后续测评设计

## 本次落地范围

已实现后端数据模型、迁移、管理 API、渠道筛选与校验，以及 new-api 内置控制台的服务商入口。尚未实现测评任务、测评表或自动质量路由。

```text
Provider（购买/接入服务的服务商）
  └─ Channel（协议、地址、支持模型、上游倍率等渠道配置）
       ├─ 一个或多个 API Key（沿用当前单 Key / 多 Key 机制）
       └─ 多个模型（沿用 models / model_mapping）
            └─ 未来：多次独立测评及不可覆盖的历史结果
```

- Go 类型为 `model.Provider`，表为 `providers`，管理接口为 `/api/providers/`。
- `Provider` 不等于已有 `Vendor`：Vendor 表示模型厂商，Provider 表示实际上游服务商；第三方服务商可以代理多个厂商的模型。
- `Channel.Type` 仍表示渠道协议/适配器类型，不表示商业服务商。
- 渠道的 `provider_id` 关联一个 Provider。服务商不持有共享 API Key；不同渠道继续独立管理密钥、多 Key 轮询、状态及配置。
- `channels.upstream_ratio` 是**渠道级上游商业倍率元数据**，默认 `1`，要求有限且大于 `0`。例如 `0.8` 表示约定基准价格的 0.8 倍；实际基准价、币种、阶梯、缓存价格及生效时间需在未来成本系统中独立定义。当前字段**不参与用户扣费、结算或渠道路由**，也不等同于用户组倍率或模型计费倍率。
- 后续新增可查询的稳定属性时使用独立字段/关联表；不把所有商业、性能、测评数据塞进 `settings` 或 Provider 的 JSON 中。
- Provider 名称是可修改的展示信息，不作为关联键。内部唯一 `code` 用于保持默认 Provider 的身份，普通 Provider 由服务端生成；API 不允许写入该字段。

## 渠道公开展示与检测快照

渠道新增以下独立列，列表、搜索和详情 API 均返回这些字段；控制台表格、卡片及新增/编辑表单同步支持：

| JSON 字段 | 类型 / 默认值 | 语义 |
| --- | --- | --- |
| `public_ratio` | 有限正数，默认 `1` | 对外展示倍率，与 `upstream_ratio` 独立，不参与用户计费、上游结算或路由 |
| `is_degraded` | `boolean \| null`，默认 `null` | 是否降智；`true` 降智、`false` 未降智、`null` 未知 |
| `is_online` | `boolean \| null`，默认 `null` | 是否可调用/在线的观测结果；与管理员控制的 `status` 独立 |
| `ttft_ms` | 非负整数毫秒或 `null`，默认 `null` | 首个有效输出 token 的延迟；`0` 是已知数值，不代表未知 |
| `last_checked_at` | Unix 秒或 `null`，默认 `null` | 最近检测时间，范围 `0..253402300799`；控制台按本地时区显示和编辑，保留秒精度 |

- 新建渠道与历史渠道的公开倍率默认 `1`，四个检测字段默认未知，不将无检测记录的渠道标记为在线或未降智。
- 部分更新省略字段保留原值；四个检测字段显式传 `null` 清空为未知，`false` 和 `0` 正常写入。更新 `public_ratio` 不能传 `null`、零或负数；创建时未设置倍率使用默认值。
- 这五个字段属于 `channel.write` 可编辑的非敏感元数据；服务器保持既有密钥、渠道启停权限边界，审计记录变更字段名。
- 复制渠道继承 Provider、上游倍率、公开倍率，但清空检测快照；副本必须重新检测，不能继承原渠道的健康结论。
- 这里只保存当前快照，管理员可手工录入或经既有渠道 API 更新；**尚未新增自动检测、TTFT 采集、降智判定算法或公开渠道接口**。公开倍率为后续对外展示提供明确字段，不会将带凭据的管理员接口公开。
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
- 当前入口属于 `newapi/web`；独立 Nuxt `frontend/` 未修改。

## 迁移与兼容

- 主数据库 AutoMigrate 增加 `providers`、`channels.provider_id` 索引与 `channels.upstream_ratio`，以及上表五个渠道展示/观测字段；没有修改日志库。
- 迁移在事务内创建真实的默认 Provider，并将历史 `provider_id=0/NULL` 的渠道归入该 Provider。原 Key、模型、多 Key 状态、路由和计费设置不变。
- 旧客户端创建渠道时不传 `provider_id`（或传 0），仍会归入默认 Provider；新接入建议明确指定实际 Provider。
- 更新渠道时省略 `provider_id` / `upstream_ratio` 保留原值；显式提交无效 Provider、`provider_id=0/null` 或 `upstream_ratio=0/null/负数` 被拒绝。
- 单独创建、批量创建、复制渠道及内部完整 `Save` 走相同的 Provider 与渠道元数据校验。复制继承商业配置，检测快照重置为未知。
- 删除仍被渠道引用的 Provider 会失败，必须先迁移或删除渠道；默认 Provider 不可删除。
- Provider 使用软删除，保留历史身份。渠道归属写入与 Provider 删除在事务中锁定相同 Provider 行，避免并发分配导致悬空引用。SQLite 使用已有 `lockForUpdate` 的单写者兼容行为。
- 当前遵循项目的应用层关联校验模式，不新增物理外键；直接写数据库或直接 `DB.Create(Channel)` 的外部程序必须遵守相同约束。业务入口应使用 `Insert` / `BatchInsertChannels` / `Update`。
- 启动迁移可重复执行。生产升级应先备份，迁移主节点完成后再启动其他节点；不支持回滚到旧版本继续写入而同时保持新约束。

## 管理 API

沿用项目 `success/message/data` 响应格式和既有认证机制。

| 方法 | 路径 | 行为 | 权限 |
| --- | --- | --- | --- |
| GET | `/api/providers/?p=1&page_size=20&keyword=example` | 分页列表，包含 `channel_count` | AdminAuth + `channel.read` |
| POST | `/api/providers/` | 新建，接收 `name`、`description` | AdminAuth + `channel.write` |
| PUT | `/api/providers/:id` | 更新名称与描述；描述可传空字符串清空 | AdminAuth + `channel.write` |
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

## 未来测评：独立目标、执行与不可变结果

以下是下一阶段的设计约束，**尚未创建这些表**。

### 1. 测评目标与配置快照

引入 `channel_model_targets`：稳定 UUID、渠道实例 UUID、`channel_id`、对外模型名、启停状态、创建/归档时间。不要只依赖 `models.id`（厂商模型元数据），也不要把 `abilities` 作为测评主键（路由能力可能重建）。

接入测评前必须补齐渠道实例 UUID 的创建/回填/复制生命周期：新建和复制得到新 UUID，渠道删除后 UUID 永不复用；手工指定、删除后重建数字 channel ID 不能合并两个实例的历史。目标归档后仍保留记录，重新启用同一目标沿用其身份。

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

### 渠道展示/检测字段扩展

- `model/channel_metadata_test.go` 的 `TestChannelMetadataDatabaseMatrix` 验证 SQLite **3.50.4**、MySQL **8.0.46-0ubuntu0.24.04.4**、PostgreSQL **16.15** 的新库、从上一版 Provider-enabled Channel 结构重建的升级库及重复迁移；覆盖默认值、未知/false/零、部分更新、显式清空、独立启停状态、原多 Key 数据与索引、完整保存及批量原子性。
- 独立测试库中设置 `TEST_MYSQL_DSN` / `TEST_POSTGRES_DSN` 后运行 `go test ./model -run TestChannelMetadataDatabaseMatrix -count=1 -v`，三个实际数据库全部通过。仅对这一矩阵传入临时 DSN，避免与其他矩阵的 TCP/URL DSN 格式约定混用。
- 不设置上述 DSN 时，根模块 `go test ./...` 通过；SQLite API 回归在 `controller/channel_authz_test.go` 中覆盖输入类型/范围、省略与 null 语义、普通写权限与密钥保护、复制后检测字段重置。
- 前端 `bun run test src/features/providers src/features/channels src/hooks/__tests__/sidebar-config.test.tsx`：**24 个文件、314 个测试通过**，包含桌面表格/移动卡片展示、三态选择、零毫秒、秒精度时间和清空、创建/编辑 payload。`bun run typecheck`、变更文件 oxlint、生产 `bun run build` 通过。
- 未新增检测执行器、公开访问接口或历史记录表；未改变认证、密钥存储、计费与路由。没有对生产备份或最低版本数据库进行升级演练；发布仍需针对实际部署备份验证。

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
