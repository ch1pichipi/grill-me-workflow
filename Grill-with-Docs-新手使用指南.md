# Grill with Docs 新手使用指南

这份指南介绍 Matt Pocock 的 `grill-with-docs` 技能：先通过一轮有针对性的问答，把需求、设计和边界讲清楚；再把确认过的领域术语与重要技术决策记录到项目文档中。

> 它主要帮助你在开始实现前和 AI 对齐，不会因为你运行了它就自动完成整个功能开发。

## 1. 先理解它会做什么

`grill-with-docs` 可以理解为“深入追问 + 维护项目领域文档”的工作流。

1. 你描述想做的功能、计划或设计，即使描述还很粗略也可以。
2. AI 会围绕尚未明确的地方提问，逐步确认用户场景、规则、例外情况和取舍。
3. 当某个领域术语的含义被确认时，AI 会更新项目的 `GLOSSARY.md`。
4. 当一个重要技术决策满足 ADR 条件时，AI 会记录到 `docs/adr/`。
5. 访谈结束后，你可以检查形成的共识，再决定是否进入规格编写或开发。

举个例子：你说“玩家账号被封后不能再登录”，AI 可能继续追问：封禁是否有期限？玩家当前在线时要不要立即断开？封禁与注销是不是一回事？这些问题能在实现前暴露规则缺口，减少做完后才发现理解不一致的情况。

## 2. 安装（以 Claude Code 为例）

### 2.1 安装技能插件

在终端运行官方 README 给出的命令：

```bash
claude plugin install mattpocock-skills@claude-plugins-official
```

安装后，在 Claude Code 中使用 `/grill-with-docs`。该技能默认要求用户主动调用，不会仅仅因为你聊到一个功能就自行启动。

**不要把同一套技能重复安装两次。** 官方同时提供插件和可复制到项目中的技能文件；选一种方式即可。重复安装可能导致相同技能出现多份。

### 2.2 第一次在项目中使用前进行配置

进入要使用该工作流的项目，在 Claude Code 中运行：

```text
/setup-matt-pocock-skills
```

这个初始化技能会先查看项目当前情况，再与你确认配置，最后才写入文件。它主要确认：

- **Issue tracker（问题跟踪位置）**：例如 GitHub Issues、GitLab Issues、本地 Markdown 文件或你使用的其他系统。选择项目真正用来管理任务的地方。
- **Triage labels（分类标签）**：只有安装了 `triage` 技能时才会询问。没有安装就不需要配置。
- **Domain docs（领域文档位置）**：一般项目使用根目录的 `GLOSSARY.md` 和 `docs/adr/`。只有大型 monorepo 才需要考虑多个领域各自维护术语表。

初始化可能会在项目里生成或更新：

- `CLAUDE.md` 或 `AGENTS.md` 中的 `## Agent skills` 说明，让其他技能知道配置文档在哪里。
- `docs/agents/issue-tracker.md`：问题跟踪系统的约定。
- `docs/agents/domain.md`：领域文档的存放位置和使用规则。
- `docs/agents/triage-labels.md`：仅在需要配置 `triage` 时生成。

新手建议接受默认的 **单领域（single-context）** 布局。初始化会先展示发现结果和准备写入的内容；阅读确认后再让它继续。这个步骤是**每个项目做一次**，不是每次会话都要做。

## 3. 一次完整的使用流程

### 第一步：准备一个初始描述

不需要先写正式需求文档。简单说明目标、使用者和你目前知道的限制即可。例如：

```text
我想给游戏后端增加玩家封禁功能。管理员可以封禁玩家，封禁中的玩家不能登录。
请先帮我梳理需求和设计，不要开始改代码。
```

如果你已有草图、需求、代码或相关文档，也可以告诉 AI 去查看。不要把密钥、访问令牌等敏感信息贴进对话。

### 第二步：启动访谈

在 Claude Code 输入：

```text
/grill-with-docs
```

接着描述你希望讨论的计划或设计。也可以直接在一次消息中说明：

```text
/grill-with-docs 我想给游戏后端增加玩家封禁功能。先通过提问帮我把业务规则和设计边界理清；这一步先不要写实现代码。
```

### 第三步：认真回答问题

把它当成和同事做设计讨论：

- 按实际情况回答，不确定时直接说“不确定，请给我几个选项并说明区别”。
- 如果问题里的术语含义不清楚，先请 AI 用简单的话解释。
- 遇到你没考虑过的边界情况，可以一起讨论，不必急着选答案。
- 如果 AI 对项目术语的理解与你的理解不同，指出来并确认项目里应该采用哪个说法。
- 某个决定尚未确定时，不要为了让流程结束而假装它已经确定；记录为待确认事项。

AI 可能会追问规则之间的边界。例如，封禁是否自动到期、管理员能否解除封禁、重复封禁如何处理、已有在线会话是否失效。你的回答会决定讨论能否继续收敛。

### 第四步：检查文档变更

确认访谈结果后，检查 AI 是否更新了合适的文档：

- `GLOSSARY.md`：只记录本项目中特有的领域术语及其明确含义，不是代码实现说明，也不是需求草稿。
- `docs/adr/0001-...md`、`docs/adr/0002-...md` 等：记录少量重要且值得长期保留的技术决策。

如果访谈中没有新术语或符合条件的重要决策，相关文件可能不会被创建或修改，这是正常的；技能采用“确有内容再创建”的方式，不要求先造空文件。

### 第五步：决定下一步

访谈和文档沉淀不等于实现。完成后可以：

- 让 AI 总结已经确认的规则、仍未确定的问题和决策理由。
- 继续讨论尚未解决的设计分支。
- 在你准备好后，再明确要求 AI 把结论整理成规格、任务清单或开始实现。

不要把“访谈结束”误认为“代码已经验证完成”。实现阶段仍然需要检查代码、运行测试，并确认结果符合预期。

## 4. 两类文档分别写什么

### `GLOSSARY.md`：统一项目语言

词汇表让你和 AI 对项目里的关键概念使用同一套说法。例如项目里约定用“玩家（Player）”表示游戏角色主体，就不要在同一个语境里随意换成“用户”“账号”“客户”，除非这些概念确实不同。

词条通常包括：

- 推荐使用的标准术语。
- 一两句话以内的定义。
- 容易混淆或不建议使用的其他叫法（`Avoid`）。

词汇表应描述业务概念，不应堆放类名、数据库表结构或一般编程术语。技能通常在术语含义刚刚厘清时就更新词表，避免讨论完后遗漏。

### ADR：记录重要决策及其原因

ADR 是 Architecture Decision Record（架构决策记录）。官方规则是，以下三项**必须同时成立**，才值得提出 ADR：

1. **难以逆转**：未来推翻这个决定会有明显成本。
2. **没有背景会令人意外**：后来维护项目的人可能会问“为什么要这样做？”
3. **确实存在取舍**：当时有多个合理方案，并选择了其中一个。

例如，选择会造成较强锁定的数据库、服务之间采用事件通信而不是同步 HTTP、因合规限制不能采用某个平台，都可能值得记录。容易改、显而易见或没有替代方案的小决定通常不需要 ADR。

默认单领域项目的 ADR 放在 `docs/adr/`，按 `0001-简短主题.md` 的形式递增编号。ADR 可以很短：写清楚背景、决定和原因即可；不必为了格式而填充很多空章节。

## 5. 常见使用场景

适合在以下情况开始使用：

- 新功能还只有一句话，需要先弄清楚真正要解决的问题。
- 一个功能涉及多个角色、业务规则或异常情况。
- 你和 AI 对“账号”“角色”“订单”等词的含义可能不一致。
- 一个设计决定会影响多个模块，而且未来修改成本较高。
- 开始开发前，希望确认哪些内容已经定下、哪些仍待讨论。

不太适合只为一个明显、容易撤销的小改动启动长时间访谈，例如调整一段提示文案或修复一个已定位的拼写错误。简单任务直接告诉 AI 要改什么即可。

## 6. 常见问题

### 输入 `/grill-with-docs` 后找不到技能

确认已安装插件，重新打开 Claude Code 后再检查技能列表。如果你也通过 `npx skills` 或其他方法安装了同名技能，先避免重复安装，并检查当前会话能否识别 `/grill-with-docs`。

### AI 开始直接写代码，没有先提问

在初始描述里明确加上“请先访谈我”“先澄清需求与设计”“不要开始改代码”。如果需要，重新启动技能并说明当前阶段只讨论问题。

### 为什么没有生成 ADR 或词汇表？

这可能完全正常。词汇表只有出现已确认的领域术语时才需要更新；ADR 又有较高门槛，三个条件必须同时满足。不要为了让目录看起来完整而创建空文档。

### 我不知道问题的答案怎么办？

直接说“不确定”。请 AI 列出可选方案、各自影响和建议，再根据项目实际情况决定。不要把 AI 的建议误当成已经得到业务方确认的事实。

### 它会自动创建 GitHub Issue 或提交代码吗？

`grill-with-docs` 的核心是访谈和领域文档沉淀，不应假定它会替你发布 Issue、提交代码或完成实现。后续操作要明确说明，并在执行会影响远端或共享资源的操作前检查内容。

## 7. Claude Code 以外的安装方式

如果你使用的是其他编码代理，官方 README 还列出这些安装方式。按你实际使用的代理选择一种即可；具体命令可能会随官方仓库更新而变化：

```bash
# Codex
codex plugin marketplace add mattpocock/skills
codex plugin add mattpocock-skills@mattpocock

# GitHub Copilot CLI
copilot plugin marketplace add mattpocock/skills
copilot plugin install mattpocock-skills@mattpocock

# Gemini CLI：安装工程技能和效率技能
gemini skills install https://github.com/mattpocock/skills.git --path skills/engineering
gemini skills install https://github.com/mattpocock/skills.git --path skills/productivity

# Cursor、OpenCode、Devin、Windsurf、Amp、Pi 等
npx skills@latest add mattpocock/skills -a <agent>
```

这两条 Gemini CLI 命令分别安装工程技能和效率技能。除 Claude Code 插件外，其他代理的插件系统和技能调用方式可能不同，请以该代理当前版本的官方说明为准。上游 README 也说明了各方式的更新方式：Claude Code 插件默认自动更新；Codex 在启动时更新；Gemini 需要重新运行安装命令；`skills` 安装方式需要手动更新。

## 8. 官方资料

- 上游技能仓库与安装说明：[mattpocock/skills](https://github.com/mattpocock/skills)
- `grill-with-docs` 技能定义：[SKILL.md](https://github.com/mattpocock/skills/blob/main/skills/engineering/grill-with-docs/SKILL.md)
- 项目初始化技能：[setup-matt-pocock-skills](https://github.com/mattpocock/skills/tree/main/skills/engineering/setup-matt-pocock-skills)
- 领域建模与词汇表/ADR 规则：[domain-modeling](https://github.com/mattpocock/skills/tree/main/skills/engineering/domain-modeling)
