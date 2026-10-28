# 聊天审核系统（演示）

一个单机、单进程、单页面的聊天审核演示系统：玩家在网页里发消息，服务端用
「归一化 + 视图 + 关键词/正则」判断违规等级，再结合近 7 天与终身的违规次数决定
禁言档位；管理员可以提前解除禁言、切换玩家身份、用 agent 提出语料库变更草案并逐条审批，
也可以在不提交的情况下用草稿语料库试跑一段文本。

设计文档见 `docs/`（`规格.md` 是可验收标准，`技术选型.md` 是理由，`adr/` 是难以逆转的决定），
术语以 `GLOSSARY.md` 为准。

## 跑起来

```bash
# 先注入 key（没有内置默认值；不注入则 agent 禁用，其余功能正常）
export AGENT_API_KEY='...'     # Windows PowerShell: $env:AGENT_API_KEY = '...'
go run .                       # 默认 http://localhost:8043 ，数据落在 ./chatmod.db
```

Windows 上可以直接从本机 pi 配置读 key（key 不进仓库、不落盘）：

```powershell
.\scripts\run-with-agent.ps1
```

或：

```bash
go build ./cmd/server && ./server
```

浏览器打开页面后先建一个玩家，然后就能发消息。不需要 node、不需要 CGO。

> 安全：仓库里不允许出现任何 key。旧 key 已经进过 Git 历史，必须先在控制台吊销，
> 再按 [`docs/密钥管理.md`](docs/密钥管理.md) 注入新 key 并清理历史。**改代码不能替代吊销。**

### 环境变量

| 变量 | 默认 | 作用 |
| --- | --- | --- |
| `ADDR` | `:8043` | 监听地址 |
| `DB_PATH` | `chatmod.db` | SQLite 文件（表结构在启动时 `CREATE TABLE IF NOT EXISTS`） |
| `AGENT_NAME` | `opencode-go` | 中转站名字，只用于日志 |
| `AGENT_BASE_URL` | `https://opencode.ai/zen/go/v1` | OpenAI 兼容中转站地址（请求 `{base}/chat/completions`） |
| `AGENT_API_KEY` | **无默认值** | 必须由环境变量注入；未设置/显式置空则禁用 agent 并提示，其余功能完全可用 |
| `AGENT_MODEL` | `deepseek-v4.1-flash` | 模型名 |
| `AGENT_SESSION` | 进程启动时随机生成 | 作为 `x-opencode-session` 头发送（该中转站要求） |
| `AGENT_MAX_TOKENS` | `16384` | 单次输出预算；这个模型会先输出很长的 reasoning，8k 预算常被思考吃光导致正文截断 |

## 界面

一页三块：

1. **玩家**：当前玩家选择器、玩家列表（名字 / 禁言剩余 / 7 天违规 / 终身违规）、
   输入框、发送、禁言状态与倒计时、提前解除。
2. **管理员 · 全局流**：所有玩家的消息与判定（命中规则、等级、处置、时长），
   被拒绝的消息也在里面并标明原因；提前解除记录同样出现在这里。
3. **管理员 · 语料库**：规则与映射的手工增删改、agent 草案审批、修订列表与回滚、
   试跑面板、只读阶梯。

生成草案时按钮会变成「生成中… Ns」并禁用（模型往返通常 8–30 秒），同时可以「取消等待」。
「取消等待」会真的发一条 `draft.cancel` 到服务端，中止在途 HTTP 请求并释放「进行中」闸门；
服务端自己也会拒绝第二条 `draft.request`（只回 `draft.busy`），不是只靠前端防重复。
等待超过 150 秒或连接中断时界面会自己取消并放弃等待，按钮一定回到可点状态，
不会卡在「生成中…」。模型往返在后台 goroutine 里跑，同一条连接上后发的聊天消息不会被它阻塞。
完成后草案区自动展开。每次往返的**请求体与响应体**会同时打到进程日志和页面上的
「agent 请求 / 响应日志」面板——因为模型调用发生在服务端，浏览器 devtools 的
Network 面板看不到它。输出被 `max_tokens` 截断时不会整批作废：已完整的建议保留下来，
状态行和日志会标明「只保留了前 N 条」。

页面上没有任何需要刷新才能看到的变化：判定、禁言生效/到期、语料库提交全部走 WebSocket。
每个连接的待发队列区分两类事件：本次请求的直接回包（`result` / `draft` / `error` / `ack`）
永不静默丢弃（队列满时背压，超时才断开）；广播事件积压时服务端主动断开，
浏览器自动重连并拿到 `bootstrap` 全量状态重新同步，避免界面漏事件却以为是最新的。

玩家列表同理：一条消息只推**变动的那一行**（`players.changed`），整份 `players` 只出现在
`bootstrap`、建/改/删玩家（100ms 合并）与重连。所以每条消息的服务端成本与玩家数无关，
代价是非发送者的两个计数可能停在旧值——它们只是展示，档位永远用判定时的现算值
（ADR 0004）。

## 验收

按 `docs/规格.md` §6 的 11 步人工脚本走一遍即可；程序侧对应关系：

- 第 2–6 步（1min → 1h → 1d → 3d → 永久）：发同一条 L1 规则消息，每次提前解除后再发，
  观察全局流里的时长与玩家列表里的 7 天/终身次数。
- 第 7–8 步（agent 藏头草案）：先按 [`docs/密钥管理.md`](docs/密钥管理.md) 注入
  `AGENT_API_KEY`（模型默认 opencode-go / deepseek-v4.1-flash），生成草案、勾选提交，
  然后用三行消息测试；不注入或显式置空 key 即禁用 agent，改用手工新增
  `view = acrostic` 的规则。
- 第 9 步（试跑）：试跑面板里贴正常聊天 + 广告，只命中广告，且不产生消息/判定。
- 第 10 步（软删除 + 同名重建）：玩家列表里删除 alice 再创建 alice，计数从 0 开始，
  旧判定仍留在库里（重启后全局流仍能回看）。
- 第 11 步（回滚）：修订列表里点「回滚到这一版」，回滚本身也是一条需要原因的修订。

## 代码结构

```
main.go / cmd/server/          入口（`go run .`）
internal/app/                  接线：环境变量、DB、drafter、HTTP/WS
internal/moderation/           纯函数：归一化、视图抽取、等级计算
internal/punishment/           纯函数：档位公式、永久门槛、只读阶梯
internal/corpus/               语料库：内存快照编译/匹配、变更操作的校验与应用
internal/store/                SQLite 持久化（schema.sql 内嵌）
internal/drafter/              agent 边界：Request/Response、Anthropic 实现、fake
internal/httpapi/              编排层（service.go）、WS 协议、全局流、静态页面
internal/httpapi/web/index.html  单页前端（原生 JS，go:embed 进二进制）
```

关键不变量落在哪：

| 不变量 | 位置 |
| --- | --- |
| 映射按 `from` 长度降序、等长按 id | `moderation.SortedMappings` |
| 藏头视图按换行 + 中文标点取首字 | `moderation.Acrostic` |
| 等级取最大值、多规则 +1、封顶 5 | `moderation.Severity` |
| `rung = clamp(severity + 7天 - 2, 0, 3)` | `punishment.Rung` / `punishment.Decide` |
| 终身 ≥ 5 独立触发永久 | `punishment.Decide` |
| 单条消息只用一个快照版本 | `httpapi.Service.Send` 里的 `snap := s.snap.Load()` |
| 判定存规则快照，不存 id 引用 | `store.judgments.hits` + `moderation.Hit` |
| 至多一条活跃禁言 | `Service.applyMuteLocked` + `ux_mutes_one_active` 唯一索引 |
| 惰性到期 + 到期主动推送 | `Service.resolveMuteLocked` / `scheduleLocked` |
| agent 永不写库 | `drafter.Drafter` 只返回 `[]corpus.Operation`，写入只经 `Service.CommitOps` |
| 提交即追加修订（含快照） | `Service.CommitOps` / `Service.Rollback` |

## 测试

```bash
go test ./...
```

- `internal/moderation`：归一化顺序、视图抽取、等级计算（表驱动）。
- `internal/punishment`：逐行覆盖规格 3.5 的断言表、封顶、永久优先（表驱动）。
- `internal/corpus`：匹配管线 + 变更校验的每一条校验项（含「命中不了自己的 examples」）。
- `internal/drafter`：模型输出的结构校验（缺 severity、非法 JSON、Markdown 围栏容错）。
- `internal/httpapi`：发送/禁言/计数/试跑/修订/草案的服务层测试，
  以及一条端到端冒烟（临时 SQLite → 起 httptest → 连 WS → 发违规消息 → 断言判定与禁言
  → 断言禁言期间再发被拒）。

## 压力测试

`scripts/loadtest-chatmod.mjs` 模拟 N 个玩家各发 M 条消息（最后一条是违禁消息），
只依赖 Node 22+（自带 WebSocket），不需要安装依赖：

```bash
# 玩家先自己建好；不够时加 --create-missing 让脚本补建
node scripts/loadtest-chatmod.mjs --players=10 --messages=20 --base=http://127.0.0.1:8043
```

输出消息总数/接受/拒绝、违禁命中数、总耗时与吞吐，并把
**服务端单条处理耗时**（`result.judgeMs`，不含排队与网络）和**端到端往返**分开报
（p50/p95/p99/max）。另有一行**玩家状态流量**（全量/增量各多少份、每消息每连接多少字节）：
拿 10 玩家与 50 玩家两档对比，它和 `judgeMs` 都不该随玩家数增长。
明细写入 `loadtest-chatmod-<时间>.json`。

### agent 异步 / 单飞 / 取消验证

`scripts/loadtest-agent-async.mjs` 自带一个 OpenAI 兼容的 mock 模型，**不花真实额度**，
会自动编译临时二进制、用临时 SQLite 起服务，然后断言：

1. 模型 5 秒往返期间，同一连接上的 20 条聊天不被阻塞；
2. 模型返回前再发 `draft.request` 只收到 `draft.busy`，模型调用次数不增加；
3. `draft.cancel` 真的中止服务端 HTTP 请求，取消后可以立即重发；
4. N 个玩家并发发消息，自己的 `result` 一条不丢、不超时。

```bash
node scripts/loadtest-agent-async.mjs
node scripts/loadtest-agent-async.mjs --players=12 --messages=30 --delay=3000
```

常用参数：`--delay`（mock 往返毫秒）、`--players`、`--messages`、
`--chat-p95` / `--chat-max`（阶段 1 阈值）、`--stress-p95` / `--stress-max`（阶段 4 阈值）。
任一条不满足会以非零码退出并打印服务端日志。

### 真实 agent 验证

`scripts/verify-real-agent.mjs` 不使用 mock，会从本机
`C:\Users\xuzongqi\.pi\agent\models.json`（或 `--models` 指定）读取 key，
自动起临时服务并产生约 **3 次真实模型调用**：

```bash
node scripts/verify-real-agent.mjs

# 也可以连一个已经启动、已注入 AGENT_API_KEY 的服务：
node scripts/verify-real-agent.mjs --base=http://127.0.0.1:8043
```

断言与 mock 脚本一致：真实模型往返期间聊天不被阻塞、第二次 `draft.request` 只回
`draft.busy` 且没有第二次模型调用、`draft.cancel` 会中止真实 HTTP 请求、取消后可立即重发、
并发聊天 result 不丢。脚本会打印每次真实往返的耗时与返回建议数；失败时以非零码退出并
打印服务端最后 80 行日志。

对照组 `pi-chat-content-moderation-system` 的等价脚本是该仓库的
`scripts/loadtest-pi-chat.mjs`，字段命名与本脚本一致，便于并排看。
