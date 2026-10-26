# 聊天审核系统（演示）

一个单机、单进程、单页面的聊天审核演示系统：玩家在网页里发消息，服务端用
「归一化 + 视图 + 关键词/正则」判断违规等级，再结合近 7 天与终身的违规次数决定
禁言档位；管理员可以提前解除禁言、切换玩家身份、用 agent 提出语料库变更草案并逐条审批，
也可以在不提交的情况下用草稿语料库试跑一段文本。

设计文档见 `docs/`（`规格.md` 是可验收标准，`技术选型.md` 是理由，`adr/` 是难以逆转的决定），
术语以 `GLOSSARY.md` 为准。

## 跑起来

```bash
go run .                # 默认 http://localhost:8043 ，数据落在 ./chatmod.db
```

或：

```bash
go build ./cmd/server && ./server
```

浏览器打开页面后先建一个玩家，然后就能发消息。不需要 node、不需要 CGO。

### 环境变量

| 变量 | 默认 | 作用 |
| --- | --- | --- |
| `ADDR` | `:8043` | 监听地址 |
| `DB_PATH` | `chatmod.db` | SQLite 文件（表结构在启动时 `CREATE TABLE IF NOT EXISTS`） |
| `AGENT_NAME` | `opencode-go` | 中转站名字，只用于日志 |
| `AGENT_BASE_URL` | `https://opencode.ai/zen/go/v1` | OpenAI 兼容中转站地址（请求 `{base}/chat/completions`） |
| `AGENT_API_KEY` | 内置默认值 | 显式设为空则禁用 agent 并提示，其余功能完全可用 |
| `AGENT_MODEL` | `deepseek-v4.1-flash` | 模型名 |
| `AGENT_SESSION` | 进程启动时随机生成 | 作为 `x-opencode-session` 头发送（该中转站要求） |

## 界面

一页三块：

1. **玩家**：当前玩家选择器、玩家列表（名字 / 禁言剩余 / 7 天违规 / 终身违规）、
   输入框、发送、禁言状态与倒计时、提前解除。
2. **管理员 · 全局流**：所有玩家的消息与判定（命中规则、等级、处置、时长），
   被拒绝的消息也在里面并标明原因；提前解除记录同样出现在这里。
3. **管理员 · 语料库**：规则与映射的手工增删改、agent 草案审批、修订列表与回滚、
   试跑面板、只读阶梯。

页面上没有任何需要刷新才能看到的变化：判定、禁言生效/到期、语料库提交全部走 WebSocket。

## 验收

按 `docs/规格.md` §6 的 11 步人工脚本走一遍即可；程序侧对应关系：

- 第 2–6 步（1min → 1h → 1d → 3d → 永久）：发同一条 L1 规则消息，每次提前解除后再发，
  观察全局流里的时长与玩家列表里的 7 天/终身次数。
- 第 7–8 步（agent 藏头草案）：agent 默认已配置（opencode-go / deepseek-v4.1-flash），
  生成草案、勾选提交，然后用三行消息测试；把 `AGENT_API_KEY` 显式设为空可禁用 agent，
  改用手工新增 `view = acrostic` 的规则。
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
