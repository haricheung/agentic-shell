# artoo — 多智能体自动化系统

**版本**: 0.8
**状态**: 生产就绪
**日期**: 2026-02-27
**作者**: artoo 工程团队

[toc]

---

## 摘要

artoo 是一个用于在本地计算机上自主执行任务的多智能体系统。用户提交一条自然语言请求；artoo 将其分解为子任务，并行或按序执行，依据机器可验证的标准逐一校验结果，在结果不达标时进行重规划——全程无需人工干预。核心创新在于借鉴经典控制理论的双层嵌套控制回路：快速内回路（执行器 + 智能体验证器）实时处理每个子任务的纠正，而慢速外回路（目标梯度求解器）对完整任务结果计算损失函数并发出有数学依据的重规划指令。每次任务的经验被编码为梅格拉姆（Megram）并存储在 MKCT 认知记忆金字塔中，规划器在每次新任务前查询该金字塔，以充分利用过往成功经验并规避已知失败模式。

---

## 设计原则

### 1. 可观测消息总线

所有角色之间的通信均通过单一共享消息总线传递。任何角色不得直接调用另一个角色。这一约束带来两个结果：(a) 每条角色间消息都是可观测事件，因此审计员可以只读方式接入总线，无需对各个角色进行埋点即可检测边界违规、纠正风暴和收敛失败；(b) 角色完全解耦——只要遵守相同的消息协议，任何角色均可独立替换、升级或测试。

### 2. 双层嵌套控制回路

系统同时运行两个相互嵌套的反馈回路：

**快速回路**（毫秒级）在每个效应器智能体内部运行。执行器（R3）是被控对象；智能体验证器（R4a）是传感器和控制器。R4a 将 R3 的输出与每个子任务的标准进行评分，若结果不达标则向 R3 发送纠正信号。该回路最多重试 `maxRetries` 次，最终以 SubTaskOutcome 终止。

**中速回路**（秒到分钟级）在元智能体内部运行。效应器智能体集体构成被控对象；元验证器（R4b）是传感器；目标梯度求解器（R7）是控制器；规划器（R2）是执行机构。R4b 合并子任务结果，要么接受结果，要么向 R7 发送重规划请求。R7 计算损失函数 L(D, P, Ω) 及其梯度 ∇L，从六状态决策表中选择宏状态，并向 R2 发出规划指令。R2 在指令的硬约束下进行重规划。该回路最多运行 `maxReplans` 轮。

### 3. 认知记忆基底

原始任务经验被编码为梅格拉姆（Megram）——携带量级（f）、效价（σ）和衰减率（k）的原子元组。梅格拉姆以 (space, entity) 标签为键累积存储在 LevelDB 中。规划器在每次任务前查询两个卷积通道：注意力通道（无符号能量——关注哪里）和决策通道（有符号偏好——该做什么）。当超过显著性阈值时，梦境（Dreamer）后台引擎将梅格拉姆簇提升为永久有效的常识（C 级）标准操作程序（SOP），并通过信任破产机制降级过时知识。记忆层从不阻塞操作热路径——所有写入均为即发即忘。

---

## 系统架构

```
FAST LOOP (inside each Effector Agent)
┌─────────────────────────────────────────┐
│  decision  │  execution  │  correction  │
│  [SubTask] │  Executor   │  Agent-Val.  │
│    (R2)    │    (R3)     │    (R4a)     │
└─────────────────────────────────────────┘
         plant = R3 │ sensor+controller = R4a

MEDIUM LOOP (inside Metaagent)
┌────────────────────────────────────────────────────────────────────┐
│    decision     │     execution      │  sensor  │   controller     │
│  Planner (R2)  │  Effector Agents   │  R4b     │   GGS (R7)       │
│  [receives     │  (fast loops       │          │   [computes L,    │
│  PlanDirective │   running inside]  │          │    ∇L, directive] │
│  from GGS]     │                    │          │                    │
└────────────────────────────────────────────────────────────────────┘
    plant = Effectors │ sensor = R4b │ controller = GGS (R7) │ actuator = R2

AUDITOR (lateral — outside both loops)
┌──────────────────────────────────────────────────────────┐
│  Observes all inter-role messages via message bus        │
│  Reports anomalies to human operator                     │
│  Cannot instruct any agent; cannot be instructed by any  │
└──────────────────────────────────────────────────────────┘
```

---

## 架构约束：可观测消息总线

所有角色间通信必须通过共享消息总线传递，审计员可以只读观察者身份接入。不允许角色间直接点对点调用——每条消息必须是可路由的。总线是非阻塞的：慢速订阅者会以记录警告的方式丢弃消息，而不是对发布者施加背压。

---

## 角色索引

| ID | 角色 | 层级 | 回路位置 | 问责范围 |
|---|---|---|---|---|
| R1 | 感知器（Perceiver） | 入口点 | 参考信号 | 若任务被误解，此角色负责 |
| R2 | 规划器（Planner） | 元智能体 | 执行机构 | 若在有效执行的情况下目标仍未达成，此角色负责 |
| R3 | 执行器（Executor） | 效应器智能体 | 被控对象（快速回路） | 若可行的子任务未被正确执行，此角色负责 |
| R4a | 智能体验证器（Agent-Validator） | 效应器智能体 | 传感器 + 控制器（快速回路） | 若子任务结果与目标之间的差距未被解决或上报，此角色负责 |
| R4b | 元验证器（Meta-Validator） | 元智能体 | 传感器（中速回路） | 若合并结果在可信范围外被接受，或任务被静默放弃，此角色负责 |
| R5 | 共享记忆（Shared Memory） | 基础设施 | 认知基底 | 若有效数据丢失、损坏或被错误检索，此角色负责 |
| R6 | 审计员（Auditor） | 基础设施 | 侧向观察者 | 若系统性失败未被检测并上报给人类操作员，此角色负责 |
| R7 | 目标梯度求解器 / GGS（Goal Gradient Solver） | 元智能体 | 控制器（中速回路） | 若重规划方向错误、过于保守、过于激进，或未能逃脱局部最小值，此角色负责 |

---

## R1 — 感知器（Perceiver）

**使命**：接收用户信号并以完整的保真度将其传入系统。R1 是接收器，而非解析器。它将自由格式自然语言转换为结构化 TaskSpec，不添加假设、不预设成功标准、不解释歧义。会话上下文允许后续输入参照先前轮次进行解析。

**回路位置**：参考信号生成器。位于两个控制回路的上游。

### 输入契约

- 来自用户的自由格式文本（REPL 或单次 CLI）
- 滚动会话历史：最近 5 条 `{input, result.Summary}` 对

### 输出契约

```json
TaskSpec {
  "task_id":     "short_snake_case_string",
  "intent":      "string — the user's goal, faithfully restated",
  "constraints": {
    "scope":    "string | null",
    "deadline": "ISO8601 | null"
  },
  "raw_input":   "string — verbatim user input"
}
```

### 技能

- 将自由格式文本转换为带有短 snake_case `task_id` 的 TaskSpec
- 通过 `buildSessionContext()` 将代词和指代（"再做一遍"、"那个文件"、"错了"）解析到会话历史
- 在不缩窄、扩展或重构的情况下，忠实保留用户意图
- 检测后续纠正并结合先前任务结果重新解释

### 不执行的操作

- 设置 `success_criteria`——标准是 R2 的职责
- 做出工具选择或提出执行策略
- 根据可行性评估修改用户意图
- 持久化任何状态（会话历史由入口点维护，而非 R1）

---

## R2 — 规划器（Planner）

**使命**：理解用户意图并掌握实现路径。R2 将 TaskSpec 转换为具体执行计划——一组带有标准、顺序约束和上下文的子任务。规划前，R2 查询 MKCT 记忆金字塔以充分利用过往成功并规避已知失败。重规划时，R2 在 GGS 注入的硬约束下运作。

**回路位置**：中速回路的执行机构。从 R7（GGS）接收规划指令并向调度器发出 SubTask[]。

### 输入契约

- 来自 R1 的 `TaskSpec`（初始规划）
- 来自 R7 的 `PlanDirective`（重规划轮次）
- 来自 R5 的 `[]SOPRecord` + `Potentials`（规划前的记忆校准）

### 输出契约

```json
{
  "task_criteria": ["string — assertion about the COMBINED output of all subtasks"],
  "subtasks": [
    {
      "subtask_id": "uuid",
      "sequence":   1,
      "intent":     "string",
      "context":    "string — everything the executor needs beyond the intent",
      "success_criteria": ["string — concrete checkable assertion about this subtask's output"]
    }
  ]
}
```

### 技能

- 规划前查询 R5：`QueryC(space, entity)` 获取 C 级 SOP；`QueryMK(space, entity)` 获取实时势能
- 推导记忆标签：`space = "intent:<first-3-words-underscored>"`，`entity = "env:local"`
- 应用记忆动作映射来校准计划：

| 记忆动作 | 提示词效果 |
|---|---|
| Exploit | 应当优先采用此方法 |
| Avoid | 绝对不得使用此方法 |
| Caution | 通过确认门 / 沙箱谨慎进行 |
| Ignore | 从提示词中省略 |

- 分配序号：相同序号 → 并行执行；不同序号 → 有序执行，先前组的输出注入下一组每个子任务的上下文
- 将 `task_criteria` 写为关于所有子任务**合并**输出的断言
- 将每个子任务的 `success_criteria` 写为具体可检验的断言（而非对意图的重述）
- 重规划时：遵守 `PlanDirective.blocked_tools`（不得出现在任何子任务中）和 `blocked_targets`（不得作为工具输入重复使用）
- 通过 `logReg.Open()` 开启任务日志

### 绝对不得设置（优先级顺序）

`memory Avoid SOPs` ∪ `GGS blocked_tools` ∪ `GGS blocked_targets`

### 不执行的操作

- 计算损失、梯度或选择宏状态（R7 的职责）
- 执行工具或调用外部服务（R3 的职责）
- 对照标准验证输出（R4a、R4b 的职责）
- 写入共享记忆（R7 是唯一写入者）

---

## R3 — 执行器（Executor）

**使命**：通过从工具优先级链中选择并调用最合适的工具来执行单个子任务，然后返回带有证据的结构化结果。R3 是快速回路的被控对象——它产生输出；R4a 判断该输出是否正确。

**回路位置**：快速回路的被控对象。从调度器接收 SubTask；向 R4a 返回 ExecutionResult。

### 输入契约

```json
SubTask {
  "subtask_id":       "uuid",
  "parent_task_id":   "uuid",
  "intent":           "string",
  "context":          "string — prior-step outputs, constraints, known paths",
  "success_criteria": ["string"],
  "sequence":         1
}
```

### 输出契约

```json
ExecutionResult {
  "subtask_id": "uuid",
  "status":     "completed | uncertain | failed",
  "output":     "string",
  "tool_calls": ["tool_name: input → firstN(output, 200)"]
}
```

### 工具优先级链

| 优先级 | 工具 | 使用场景 |
|---|---|---|
| 1 | `mdfind` | 个人文件搜索——macOS Spotlight 索引，100ms 以内。项目外的文件始终使用此工具。 |
| 2 | `glob` | 项目文件搜索——文件名模式，在项目根目录内递归搜索。 |
| 3 | `read_file` / `write_file` | 读取单个文件；将生成的输出写入 `~/artoo_workspace/`。 |
| 4 | `applescript` | 控制 macOS 应用（邮件、日历、提醒事项、音乐等）。 |
| 5 | `shortcuts` | 运行命名的 Apple 快捷指令（iCloud 同步）。 |
| 6 | `shell` | 通用 bash，用于计数、聚合或上述工具无法处理的操作。 |
| 7 | `search` | 网络搜索（默认 DuckDuckGo；设置 API 密钥时使用 Google Custom Search）。 |

### 技能

- 为每个步骤选择优先级最高的适用工具
- 对工具输出应用 `headTail(result, 4000)`，使 LLM 在输出超过 4000 字符时能同时看到开头上下文和结尾结果
- 在每个 `tool_calls` 条目中追加 `→ firstN(output, 200)` 以给 R4a 提供具体证据
- 收到纠正信号时：重复格式约束，列出应避免的先前工具调用，采用不同方法
- 透明地将个人路径（`~/`、`/Users/`、`/Volumes/`）上的 `shell find` 重定向到 `mdfind`
- 从 `shell find` 命令中去除 `-maxdepth N` 标志

### 不执行的操作

- 对照成功标准评估自身输出（R4a 的职责）
- 自行发起重试——纠正回路是 R4a 的职责
- 生成虚假工具输出或在未实际调用工具的情况下假装工具已运行
- 写入共享记忆

---

## R4a — 智能体验证器（Agent-Validator）

**使命**：对照每个子任务的成功标准对 R3 的结果进行评分，并驱动快速纠正回路。R4a 既是传感器（检测差距），也是控制器（发出纠正指令）。纠正回路最多运行 `maxRetries = 2` 次，之后 R4a 给出最终裁决。

**回路位置**：快速回路的传感器 + 控制器。与每个执行器实例一一配对。

### 输入契约

- `SubTask`（标准、意图、上下文）
- 来自 R3 的 `ExecutionResult`（状态、输出、tool_calls）

### 输出契约

```json
SubTaskOutcome {
  "subtask_id":       "uuid",
  "parent_task_id":   "uuid",
  "status":           "matched | failed",
  "output":           "any",
  "failure_reason":   "string | null",
  "gap_trajectory":   [{"attempt": 1, "score": 0.5, "unmet_criteria": [...], "failure_class": "logical"}],
  "criteria_verdicts":[{"criterion": "...", "verdict": "pass|fail", "failure_class": "...", "evidence": "..."}],
  "tool_calls":       ["string"]
}
```

### 技能

- 基于 `tool_calls` 证据，将每个成功标准评分为 `met` 或 `unmet`
- 应用**证据接地规则**：`output` 是 R3 自述的文字——视为声明；`tool_calls` 是基本事实。若 `output` 声称成功但主要 `tool_call` 显示中断、错误或截断且无完成信号 → 矛盾 → 重试
- 主要操作失败后的事后验证（ls、find、stat）不构成操作成功的证明
- 将每次失败分类为 `logical`（方法错误）或 `environmental`（网络、权限、文件未找到）
- 向 R3 发送纠正信号以重试：包含 `what_was_wrong` 和 `what_to_do`
- 以下情况立即给出 `failed` 裁决（不重试）：`ExecutionResult.status == "failed"`、基础设施错误（超时、上下文取消、网络）
- 当真实搜索工具调用已执行时，空搜索结果返回 `matched`（缺席是有效答案）
- 将标准裁决和纠正记录到任务日志

### 不执行的操作

- 执行工具（R3 的职责）
- 跨多个子任务评估任务级标准（R4b 的职责）
- 写入共享记忆

---

## R4b — 元验证器（Meta-Validator）

**使命**：充当所有效应器智能体的汇聚门。R4b 收集每个 SubTaskOutcome，合并输出，并做出二元决策：接受任务结果（所有标准已满足）或向 GGS 发送重规划请求。R4b 是中速回路的传感器——它观察所有子任务结果的全貌，并将聚合差距呈现给控制器（R7）。

**回路位置**：中速回路的传感器。接收所有 SubTaskOutcome；向 R7 发布重规划请求或向 R7 发布 OutcomeSummary（接受路径）。

### 输入契约

- 当前任务的所有 `SubTaskOutcome` 消息（按序号顺序收集）
- `DispatchManifest.TaskCriteria`——关于合并输出的任务级断言

### 输出契约

接受路径：
```json
OutcomeSummary {
  "task_id":       "uuid",
  "merged_output": "any — concrete data, not a prose summary",
  "summary":       "string"
}
```

重规划路径：
```json
ReplanRequest {
  "task_id":        "uuid",
  "failed_outcomes": [SubTaskOutcome],
  "gap_summary":    "string"
}
```

### 技能

- 缓冲传入的 SubTaskOutcome，仅在每个序号组完整时才释放
- 将所有已匹配子任务的输出合并为单个 `merged_output`
- 使用 LLM 调用对照 `merged_output` 评估所有 `task_criteria`
- 若任何 `SubTaskOutcome.status == "failed"`：无需调用 LLM 即立即发送重规划请求
- 仅当所有 task_criteria 都满足时才接受；证据模糊时默认拒绝
- 强制执行 `maxReplans = 3`：超出时强制进入放弃路径
- 通过 `logReg.Close()` 关闭任务日志

### 不执行的操作

- 写入共享记忆——GGS 是所有路径上 R5 的唯一写入者
- 直接向 R2 发送规划指令——始终经由 R7（GGS）路由
- 重试单个子任务（这是 R4a 的职责）
- 计算损失或梯度（R7 的职责）

---

## R5 — 共享记忆（Shared Memory）

**使命**：充当系统持久的认知基底。R5 将经验以梅格拉姆的形式积累，通过梦境后台引擎将反复出现的模式提升为跨任务 SOP，并衰减过时知识——同时永不阻塞操作热路径。

**回路位置**：基础设施层。由 GGS（R7）写入；由规划器（R2）读取。

### MKCT 记忆金字塔

```
[ UPWARD FLOW ]                                               [ DOWNWARD FLOW ]
         Async Consolidation                                  Degradation & Forgetting
         (Dreamer Engine)                                     (Time & Dissonance)

               ^                                                      |
               |              /:::::::::::::\                         |
               |             /    [ T ]      \                        |
  Immutable    |            /   THINKING      \                 Immutable
  Agent Laws   |           /___________________\                (No Demotion)
  k = 0.0      |          /                     \                     |
               |         /        [ C ]           \                   |
  Promotion    |        /     COMMON SENSE         \              Demotion
  M_att >=5.0  |       /   (SOPs & Constants)       \             M_dec < 0.0
  |M_dec|>=3.0 |      /________________________________\          k reverts to 0.05
  k = 0.0      |     /                                  \               |
               |    /            [ K ]                    \             v
  Clustering & |   /           KNOWLEDGE                   \       Time Decay
  Lazy Eval    |  / (Task Cache & Local Context)             \     g(Δt) = e^(-k*Δt)
  k > 0        | /______________________________________________\        |
               |/                                                \       v
  Generation   | /              [ M ]                             \  Garbage Collect
  GGS State    |/             MEGRAM                               \ M_att < 0.1
  f_i,σ_i,t_i  |/_(Atomic Events: t, s, ent, c, f, σ, k)__________\|(Hard DELETE)

  =============================================================================
  [             LEVELDB STORAGE (Append-Only Event Sourcing)                  ]
  =============================================================================
                                      |
                        QueryMK(space, entity)
              ┌─────────────────────────┴──────────────────────────┐
              │                                                     │
              ▼  Channel A                           ▼  Channel B
              Attention                              Decision
         Σ|fᵢ|·e^(−kᵢ·Δt)                  Σσᵢ·fᵢ·e^(−kᵢ·Δt)
         unsigned energy                    signed preference
              │                                                     │
              ▼ M_att                                    ▼ M_dec
              └─────────────────────────┬──────────────────────────┘
                                        ▼
                             M_dec ▲
                              +0.2 ┤  IGNORE │ EXPLOIT  ← SHOULD PREFER
                               0.0 ┤         │ CAUTION  ← sandbox / confirm
                              -0.2 ┤         │ AVOID    ← MUST NOT
                                   └─────────┴─────────────────────► M_att
                                             0.5
```

| 层级 | 名称 | 衰减 k | 描述 |
|---|---|---|---|
| M | 梅格拉姆（Megram） | 依量化矩阵 | 原始情节事实；创建时的默认层级 |
| K | 知识（Knowledge） | 同 M | 任务范围缓存；由梦境 GC 清理 |
| C | 常识（Common Sense） | 0.0（永久有效） | 提升的 SOP 或约束；由 LLM 从 M 簇中提炼 |
| T | 思维（Thinking） | 0.0（永久有效） | 系统人格和智能体法则；硬编码在系统提示词中 |

### 梅格拉姆基础元组

```
Megram = ⟨ID, Level, created_at, last_recalled_at, space, entity, content, state, f, sigma, k⟩
```

标签约定：
- **微事件**（动作状态）：`space="tool:<name>"`，`entity="path:<target>"` — 每个 blocked_target 一个梅格拉姆
- **宏事件**（终止状态）：`space="intent:<intent-slug>"`，`entity="env:local"` — 每个路由决策一个梅格拉姆

### GGS 量化矩阵

| 状态 | f | σ | k | 物理含义 |
|---|---|---|---|---|
| `abandon` | 0.95 | −1.0 | 0.05 | 创伤记忆；生成硬约束 |
| `accept` (D=0) | 0.90 | +1.0 | 0.05 | 完美黄金路径；强化为 SOP |
| `change_approach` | 0.85 | −1.0 | 0.05 | 反模式；工具类被列入黑名单 |
| `success` (D≤δ) | 0.80 | +1.0 | 0.05 | 最佳实践；规划器可直接复用 |
| `break_symmetry` | 0.75 | +1.0 | 0.05 | 突破点；倾向于在此点重试 |
| `change_path` | 0.30 | 0.0 | 0.2 | 死胡同；工具未受损；路径已规避 |
| `refine` | 0.10 | +0.5 | 0.5 | 肌肉记忆；快速 GC |

衰减常数：k=0.05 → 约 14 天半衰期；k=0.2 → 约 3.5 天；k=0.5 → 约 1.4 天。
C/T 级条目 k=0.0（永久有效，直至信任破产）。

### 双通道卷积势能

```
M_attention(space, entity) = Σ |f_i| · exp(−k_i · Δt_days)
M_decision(space, entity)  = Σ  σ_i · f_i · exp(−k_i · Δt_days)
```

| 条件 | 动作 |
|---|---|
| M_att < 0.5 | 忽略——历史不足以指导规划 |
| M_att ≥ 0.5 且 M_dec > +0.2 | 利用——应当优先采用此方法 |
| M_att ≥ 0.5 且 M_dec < −0.2 | 规避——绝对不得使用此方法 |
| M_att ≥ 0.5 且 \|M_dec\| ≤ 0.2 | 谨慎——通过确认门进行 |

### 梦境模块——离线巩固引擎（Dreamer — Offline Consolidation Engine）

以 5 分钟为间隔在后台 goroutine 中运行。永不阻塞操作热路径。

**MVP（v0.8）——向下流动（已激活）**：
- *物理遗忘*（Λ_gc）：M/K 级条目中实时 `M_attention < 0.1` → 从 LevelDB 中硬删除
- *信任破产*（Λ_demote）：C 级条目中实时 `M_decision < 0.0` → 撤销时间豁免；k 恢复为 0.05；降级至 K 级

**向上流动——巩固（延迟至 v0.9）**：
- 将具有相同 `(space, entity)` 标签对的梅格拉姆进行聚类
- `M_attention ≥ 5.0 且 M_decision ≥ 3.0` → 调用 LLM 提炼最佳实践 → 新梅格拉姆（Level=C，k=0.0）
- `M_attention ≥ 5.0 且 M_decision ≤ −3.0` → 调用 LLM 提炼约束 → 新梅格拉姆（Level=C，k=0.0）

### 存储：LevelDB

纯 Go 实现（syndtr/goleveldb），无 CGO。仅追加事件溯源。

键模式（单字符前缀 + `|` 分隔符）：
```
m|<id>                    → Megram JSON  (primary record)
x|<space>|<entity>|<id>   → ""           (inverted index for tag scan)
l|<level>|<id>            → ""           (level scan for Dreamer)
r|<id>                    → RFC3339      (last_recalled_at; updated on QueryC hits)
```

错误更正通过追加负 σ 梅格拉姆来实现，而非修改现有记录。

### MemoryService 接口

```go
type MemoryService interface {
    Write(m Megram)                                          // async, non-blocking; fire-and-forget
    QueryC(ctx, space, entity string) ([]SOPRecord, error)  // C-level SOPs; updates last_recalled_at
    QueryMK(ctx, space, entity string) (Potentials, error)  // live dual-channel convolution
    RecordNegativeFeedback(ctx, ruleID, content string)     // appends negative-σ Megram for stale SOP
    Close()                                                 // drains write queue; stops Dreamer
}
```

### 契约

| 方向 | 对应方 | 格式 |
|---|---|---|
| 接收写入 | 仅 GGS（R7） | 通过异步写入队列传递的 `Megram` |
| 提供 C 级读取 | 规划器（R2） | `[]SOPRecord` |
| 提供 M/K 级读取 | 规划器（R2） | `Potentials{Attention, Decision, Action}` |

### 不执行的操作

- 格式化提示词文本——R2 负责此项
- 阻塞 GGS 的热路径写入
- 接受来自 GGS 以外任何角色的写入
- 在热路径上运行 LLM 调用

---

## R6 — 审计员（Auditor）

**使命**：观察所有角色间通信并向人类操作员报告异常。R6 完全独立——它不能向任何智能体发出指令，也不能被任何智能体指令。其权威纯粹是认知性的：它看到一切，持久化所观察到的内容，并呈现操作员在单独观察各角色时无法发现的模式。

**回路位置**：侧向观察者。位于两个控制回路之外。以只读方式接入消息总线。

### 输入契约

- 通过只读接入（`bus.NewTap()`）获取的所有总线消息
- 来自人类操作员的 `MsgAuditQuery`（按需报告请求）

### 输出契约

```json
AuditReport {
  "trigger":              "periodic | on-demand",
  "window_start":         "ISO8601",
  "tasks_observed":       42,
  "total_corrections":    17,
  "gap_trends":           [{"task_id": "...", "trend": "improving"}],
  "boundary_violations":  ["description"],
  "drift_alerts":         ["description"],
  "anomalies":            ["description"],
  "tool_health": {
    "execution_failures":    3,
    "environmental_retries": 8,
    "logical_retries":       6
  }
}
```

### 技能

- 通过 `bus.NewTap()` 被动接入每条总线消息；将每条消息写入一个 JSONL `AuditEvent` 到 `~/.artoo/audit.jsonl`
- 按报告周期累积窗口统计：`tasksObserved`、`totalCorrections`、`gapTrends`、`boundaryViolations`、`driftAlerts`、`anomalies`
- 追踪 `ToolHealth`：将 `ExecutionResult.status == "failed"` 计为执行失败；将 `CorrectionSignal.FailureClass` 分类为环境重试与逻辑重试
- 检测 GGS 抖动：连续发出 `break_symmetry` 指令而 D 未减少 → `ggs_thrashing` 异常
- 检测边界违规：绕过总线的直接角色间消息
- 在 5 分钟周期定时器触发时及收到 `MsgAuditQuery` 时发布 `MsgAuditReport`
- 每次报告后重置窗口统计；通过 `~/.artoo/audit_stats.json` 跨重启持久化统计数据
- 在 3 秒内响应 `/audit` REPL 命令

### 不执行的操作

- 向任何角色发出指令
- 修改总线消息
- 以任何方式影响任务执行
- 接受来自任何角色的指令（审计查询仅来自人类操作员）

---

## R7 — 目标梯度求解器 / GGS（Goal Gradient Solver）

**使命**：将 R4b 的原始失败信号转化为针对 R2 的定向规划约束。若重规划方向错误——在收敛可行时过于保守、在精化足够时过于激进，或未能逃脱局部最小值——此角色负责。

**回路位置**：中速回路的控制器。位于 R4b（传感器）和 R2（执行机构）之间。

### 损失函数

```
L = α·D(I, R_t) + β_eff·P(R_t) + λ·Ω(C_t)

where:
  β_eff = β · (1 − Ω(C_t))   [process weight decays as budget exhausts]
```

**D(I, R_t) — 意图-结果距离** [0, 1]

衡量用户意图与当前结果之间的差距。从所有子任务的 `criteria_verdicts` 中聚合计算：

- `verifiable` 标准裁决为 `fail` → 对分子贡献 1.0
- `plausible` 标准裁决为 `fail` → 按轨迹一致性加权（k/N 次失败）
- `D = Σ(weighted_failures) / Σ(total_criteria)`

**P(R_t) — 过程不合理性** [0, 1]

衡量*方法*的错误程度，独立于结果是否错误：

```
P = logical_failures / (logical_failures + environmental_failures)
```

P 高 → 方法根本上是错误的（需要更换）。
P 低 → 方法本身合理，但环境阻止了执行（需要改变路径或参数）。

**Ω(C_t) — 资源成本** [0, 1]

同时捕获预算耗尽和实际用时：

```
Ω = w₁·(replan_count / maxReplans) + w₂·(elapsed_ms / time_budget_ms)
```

默认权重：w₁ = 0.6，w₂ = 0.4。

### 梯度计算

```
∇L_t = L_t − L_{t−1}
```

GGS 跨轮次维护每个 task_id 的 `L_prev`。第一轮：`L_prev` 未定义 → `∇L = 0`。

### 宏状态决策表

24 格输入空间（2P × 2Ω × 2D × 3∇L）通过诊断级联折叠为 **6 个宏状态**：

```
优先级 1：Ω — 硬约束（我们能否继续？）
优先级 2：D — 目标距离（我们是否足够接近以接受？）
优先级 3：|∇L| 和 P — 动作选择（需要什么类型的变化？）
```

∇L *符号*降级为调节器——它影响宏状态内的紧迫性，但不决定宏状态本身。

#### 6 个宏状态

| # | 条件 | 宏状态 | 动作 |
|---|---|---|---|
| 1 | Ω ≥ θ | **abandon** | 预算耗尽——输出失败摘要 |
| 2 | Ω < θ，D ≤ δ | **success** | 足够接近——输出结果 |
| 3 | Ω < θ，D > δ，\|∇L\| < ε，P > ρ | **break_symmetry** | 停滞 + 方法错误——要求新颖工具类 |
| 4 | Ω < θ，D > δ，\|∇L\| ≥ ε，P > ρ | **change_approach** | 有信号 + 方法错误——切换方法 |
| 5 | Ω < θ，D > δ，\|∇L\| < ε，P ≤ ρ | **change_path** | 停滞 + 方法正确——更换目标 |
| 6 | Ω < θ，D > δ，\|∇L\| ≥ ε，P ≤ ρ | **refine** | 有信号 + 方法正确——收紧参数 |

共计：12 + 6 + 1 + 2 + 1 + 2 = **24 格**。完整且互不重叠。

#### 动作网格（Ω < θ，D > δ）

```
                    P ≤ ρ (environmental)     P > ρ (logical)
                  ┌────────────────────────┬────────────────────────┐
 |∇L| < ε        │     change_path        │    break_symmetry      │
 (plateau/stuck)  │     (1 cell)           │    (1 cell)            │
                  ├────────────────────────┼────────────────────────┤
 |∇L| ≥ ε        │     refine             │    change_approach     │
 (has signal)     │     (2 cells: ↑ or ↓)  │    (2 cells: ↑ or ↓)  │
                  └────────────────────────┴────────────────────────┘
```

#### 完整 24 格枚举

| # | ∇L | D | P | Ω | 宏状态 |
|---|---|---|---|---|---|
| 1 | < −ε | ≤ δ | ≤ ρ | < θ | success |
| 2 | < −ε | ≤ δ | > ρ | < θ | success |
| 3 | < −ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 4 | < −ε | ≤ δ | > ρ | ≥ θ | abandon |
| 5 | < −ε | > δ | ≤ ρ | < θ | refine |
| 6 | < −ε | > δ | > ρ | < θ | change_approach |
| 7 | < −ε | > δ | ≤ ρ | ≥ θ | abandon |
| 8 | < −ε | > δ | > ρ | ≥ θ | abandon |
| 9 | \|·\|< ε | ≤ δ | ≤ ρ | < θ | success |
| 10 | \|·\|< ε | ≤ δ | > ρ | < θ | success |
| 11 | \|·\|< ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 12 | \|·\|< ε | ≤ δ | > ρ | ≥ θ | abandon |
| 13 | \|·\|< ε | > δ | ≤ ρ | < θ | change_path |
| 14 | \|·\|< ε | > δ | > ρ | < θ | break_symmetry |
| 15 | \|·\|< ε | > δ | ≤ ρ | ≥ θ | abandon |
| 16 | \|·\|< ε | > δ | > ρ | ≥ θ | abandon |
| 17 | > ε | ≤ δ | ≤ ρ | < θ | success |
| 18 | > ε | ≤ δ | > ρ | < θ | success |
| 19 | > ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 20 | > ε | ≤ δ | > ρ | ≥ θ | abandon |
| 21 | > ε | > δ | ≤ ρ | < θ | refine |
| 22 | > ε | > δ | > ρ | < θ | change_approach |
| 23 | > ε | > δ | ≤ ρ | ≥ θ | abandon |
| 24 | > ε | > δ | > ρ | ≥ θ | abandon |

#### 微妙情形（第 6 格）

∇L < −ε（改善中），D > δ，P > ρ → **change_approach**。

损失在减少，但方法在逻辑上是错误的。这令人生疑——系统可能在幻觉式成功、攻击评估标准，或在错误的吸引域中收敛。正确的应对是不信任改善趋势并更换方法。未来梦境模块的向上巩固遍历将把这种模式识别为系统性评估偏差。

### 指令语义

**`abandon`** — Ω ≥ θ。预算耗尽。GGS 输出带有失败摘要的 `FinalResult`；不调用 R2。

**`success`** — Ω < θ，D ≤ δ。在收敛阈值内。GGS 输出带有合并输出的 `FinalResult`。v0.8 新增——v0.7 要求 D = 0。

**`break_symmetry`** — 停滞 + 逻辑错误。`blocked_tools`：失败子任务中的所有工具。

**`change_approach`** — 有信号 + 逻辑错误。`blocked_tools`：失败子任务中的工具。

**`change_path`** — 停滞 + 环境受阻。`blocked_targets`：累积的失败查询/路径。

**`refine`** — 有信号 + 环境受阻。`blocked_targets`：累积的失败查询/路径。

### ∇L 符号作为紧迫性调节器

| ∇L 符号 | 调节效果 |
|---|---|
| < −ε（改善中） | 紧迫性较低——当前轨迹有效；以更大余地执行指令 |
| > ε（恶化中） | 紧迫性较高——正在积极发散；积极执行指令 |

### 第二定律熔断开关

连续两轮重规划恶化（两轮 `∇L > ε`）→ 强制 **abandon**，无论 Ω 如何。系统正在积极发散，再多预算也无济于事。

### 动态绝对不得注入

- **`blocked_tools`**（逻辑失败）：来自失败子任务的工具名称。R2 不得使用这些工具进行规划。
- **`blocked_targets`**（环境失败）：失败的具体输入。在每个任务的所有重规划轮次中累积。
- 合并绝对不得集合 = 记忆绝对不得 ∪ `blocked_tools` ∪ `blocked_targets`

### 记忆写入（GGS 是 R5 的唯一写入者）

- **动作状态**（change_path、refine、change_approach、break_symmetry）：为 `blocked_targets` 中的每个条目写入一个梅格拉姆；标签 = `(tool:<name>, path:<target>)`
- **终止状态**（accept、success、abandon）：写入一个带有标签 `(intent:<task-intent-slug>, env:local)` 的梅格拉姆
- 所有写入均为即发即忘（向 R5 写入 goroutine 的非阻塞通道发送）

### 契约

```json
PlanDirective {
  "task_id":         "string",
  "loss":            { "D": "float", "P": "float", "Omega": "float", "L": "float" },
  "prev_directive":  "string",
  "directive":       "refine | change_path | change_approach | break_symmetry",
  "blocked_tools":   ["string"],
  "blocked_targets": ["string"],
  "failed_criterion":"string",
  "failure_class":   "logical | environmental | mixed",
  "budget_pressure": "float",
  "grad_l":          "float",
  "rationale":       "string"
}

FinalResult {
  "task_id":        "string",
  "summary":        "string",
  "output":         "any",
  "loss":           { "D": "float", "P": "float", "Omega": "float", "L": "float" },
  "grad_l":         "float",
  "replans":        "integer",
  "prev_directive": "string",
  "directive":      "accept | success | abandon"
}
```

### 不执行的操作

- 直接生成子任务或修改计划（R2 的职责）
- 观察单个工具调用（R4a 的职责）
- 合并或验证输出（R4b 的职责）
- 覆盖汇聚门（R4b 的职责）

---

## 交互图

```
                 ┌─────────────────── MESSAGE BUS ──────────────────────────┐
                 │  (all inter-role messages pass through here)              │
                 │                              ┌──── R6 Auditor ──────┐    │
                 │                              │  (read-only tap)      │    │
                 │                              └──────────┬───────────┘    │
                 └─────────────────────────────────────────│────────────────┘
                                                           │ AuditReport
                                                           ▼
                                                    Human Operator

User
 │ free text
 ▼
[R1] ──TaskSpec──► [R2 Planner] ◄──────────────────────────── PlanDirective ── [R7 GGS]
                    │     ▲                                         ▲      │
        ┌───────────┤     └─── []SOPRecord, Potentials ◄── [R5] ───┤      │
        │  memory   │                                               │      │ Megram writes
        │  calibrate│                                               │      │ (async, fire-and-forget)
        │  plan     │                                               │      ├──► FinalResult
        │           │                           [R4b] ──ReplanReq──┘      │    (success/abandon → User)
        │  SubTask[]│                              ▲                       │
        │           │                              │ SubTaskOutcome[]      │
        └───────────┴──► [R3 × N] ──► [R4a × N] ──┘                      │
                                                                           │
                          OutcomeSummary (all matched) ────────────────────┘
                          → GGS accept path → FinalResult → User
```

---

## 关键设计决策（v0.8）

| 决策 | 理由 |
|---|---|
| GGS 决策表：24 格 → 通过诊断级联折叠为 6 个宏状态 | v0.7 以 ∇L 符号作为主要拆分依据。这是错误的：∇L 符号将方法质量与轨迹噪声混为一谈。新的级联——先 Ω，再 D，再（|∇L|，P）——产生更清晰、正交的决策 |
| `success` 宏状态：D ≤ δ → 接受，无需要求 D = 0 | 要求所有标准通过才接受会在噪声级差距上浪费预算。D ≤ δ 意味着结果在收敛阈值内；继续重规划是浪费 |
| ∇L 符号从状态决定因素降级为紧迫性调节器 | 在逻辑错误方法（P > ρ）下损失改善令人生疑——可能表明幻觉、标准博弈或在错误吸引域中收敛。系统不应盲目信任改善趋势 |
| \|∇L\|（量级）成为有意义的拆分：有信号 vs 平台期 | 系统是否拥有任何方向性信息比它朝哪个方向移动更重要。有信号 → 可以适应；无信号 → 必须逃脱 |
| P 阈值参数化为 ρ | 为梦境模块引导调优做准备：ρ 将根据 MKCT 金字塔中的历史失败模式按任务类型可调 |
| R5 共享记忆重新设计：MKCT 金字塔 + 双通道卷积 | 关键词扫描 JSON 存储无法支持跨任务 SOP 提升、衰减加权规避，以及方法级与路径级失败的结构化区分 |
| GGS 是 R5 的唯一写入者 | R4b 之前在接受/失败时写入 MemoryEntry，绕过了 GGS 的可观测性。将写入统一通过 GGS 确保每次记忆写入都与损失计算配对 |
| R2 从 R5 接收结构化数据（而非格式化文本） | 记忆即文本格式化器使记忆层无法独立于 R2 进行测试，并违反了数据服务原则 |

---

## 关键不变量

| 不变量 | 执行方 |
|---|---|
| SubTask ID 是 Go 运行时分配的 UUID，从不由 LLM 生成 | 调度器 |
| TaskSpec 不携带 success_criteria——R2 推导所有标准 | R1 提示词；R2 规划器提示词 |
| task_criteria 以普通字符串形式存在于 DispatchManifest；R4b 从那里读取 | R2 包装器输出；R4b 代码 |
| R4b 的推理能力必须 ≥ R2 | 模型选择策略 |
| 证据模糊时 R4b 默认拒绝 | R4b LLM 提示词 |
| 当任何 SubTaskOutcome.status == "failed" 时不调用 R4b LLM | R4b 代码门 |
| R4b 向 R7 发送重规划请求，从不直接发送给 R2 | R4b 代码 |
| GGS 计算损失和梯度；R2 不自主指导重规划 | R7 拥有 PlanDirective |
| R2 计划不能重用 PlanDirective 中 `blocked_tools` 里的工具 | R2 计划验证器 |
| GGS 在 Ω ≥ θ 时发出 `abandon`，无论其他信号 | R7 决策表 |
| GGS 在 Ω < θ 且 D ≤ δ 时发出 `success`，无论 P 和 ∇L | R7 决策表 |
| `blocked_targets` 在同一任务的所有重规划轮次中累积 | R7 `triedTargets` 映射 |
| GGS 是所有路径（accept、success、abandon）上 `FinalResult` 的唯一发射者 | R7 代码 |
| `FinalResult.Directive` 始终为 `accept`、`success`、`abandon` 之一 | R7 代码 |
| GGS 是 R5 共享记忆的唯一写入者 | R7 代码；R4b 不再写入 MemoryEntry |
| 动作状态下每个 blocked_target 对应一个梅格拉姆 | R7 写入路径 |
| 梅格拉姆写入为即发即忘；GGS 从不阻塞在记忆 I/O 上 | R5 异步写入队列 |
| 记忆返回结构化数据；R2 格式化为提示词 | R5 接口契约 |
| C 级梅格拉姆 k=0.0，直至信任破产 | R5 梦境引擎 |
| 调用 C 级 SOP 会更新 last_recalled_at | R5 QueryC 实现 |
| `PlanDirective.PrevDirective` 在第一轮为 `init` | R7 `prevDirective` 映射 |
| 第二定律熔断开关：连续 2 轮恶化 → 强制放弃 | R7 `worseningCount` |

---

## 损失超参数（v0.8 默认值）

| 参数 | 符号 | 默认值 | 含义 |
|---|---|---|---|
| 距离权重 | α | 0.6 | 意图-结果距离 D 的权重 |
| 过程权重 | β | 0.3 | 过程不合理性 P 的权重（自适应缩放前） |
| 资源权重 | λ | 0.4 | 资源成本 Ω 的权重 |
| Ω 重规划子权重 | w₁ | 0.6 | 来自重规划计数的 Ω 比例 |
| Ω 时间子权重 | w₂ | 0.4 | 来自已用时间的 Ω 比例 |
| 平台阈值 | ε | 0.1 | \|∇L\| 低于此值 → 无方向信号 |
| 收敛阈值 | δ | 0.3 | D 低于此值 → 接受为成功 |
| P 阈值 | ρ | 0.5 | P 高于此值 → 逻辑失败；低于此值 → 环境失败 |
| 放弃阈值 | θ | 0.8 | Ω 高于此值 → 放弃 |
| 时间预算 | time_budget_ms | 300,000 | 每个任务 5 分钟 |
| 最大重规划次数 | maxReplans | 3 | 用于 Ω 重规划子计算 |
| 第二定律熔断阈值 | — | 2 | 强制放弃前的连续恶化轮数 |

---

## 问责映射

| 失败模式 | 责任角色 |
|---|---|
| 用户原始意图未被忠实传入 TaskSpec | R1 — 感知器 |
| 模糊意图被误解；task_criteria 错误 | R2 — 规划器 |
| 尽管子任务执行有效，目标仍未达成 | R2 — 规划器 |
| 放弃时：仅输出失败消息，未提供任何部分结果 | R2 — 规划器 |
| 可行的子任务未被正确执行 | R3 — 执行器 |
| 子任务输出与目标之间的差距未被解决或上报 | R4a — 智能体验证器 |
| 失败的子任务被接受为成功；合并结果未通过 task_criteria | R4b — 元验证器 |
| 重规划方向错误；局部最小值未被逃脱；预算判断有误 | R7 — 目标梯度求解器 |
| 有效经验数据丢失、损坏或被错误检索 | R5 — 共享记忆 |
| 系统性失败未被检测并上报给操作员 | R6 — 审计员 |

---

## 路线图

### v0.9 — 计划中

| 组件 | 所需工作 |
|---|---|
| GGS 超参数调优 | 基于审计员会话数据对 α、β、λ、w₁、w₂、ε、δ、ρ、θ 进行经验性校准 |
| ∇L 符号紧迫性调节 | 每个宏状态的紧迫性调整的具体实现 |
| 结构化标准模式 | `{criterion, mode}` 对象区分 `verifiable` 与 `plausible`；影响 D 计算权重 |
| R2 放弃时的优雅失败 | 由 LLM 生成的部分结果 + 下一步建议（目前仅为代码模板） |
| 梦境模块向上巩固 | LLM 从 M 级簇中提炼 C 级 SOP/约束；FinalResult 触发的安定延迟 |

### 第二阶段 — 研究

| 组件 | 描述 |
|---|---|
| T 层缓慢演化机制 | 允许系统人格/价值观从高置信度 C 级巩固中更新 |
| 梦境模式迁移引擎（Dreamer Schema Transfer Engine） | 对高分梅格拉姆进行语义因式分解；生成假设梅格拉姆以发明新颖工具组合 |
| 梦境认知失调梅格拉姆 | 在软覆写时生成失调梅格拉姆（高 f，负 σ）；在夜间梦境周期中粉碎过时 SOP 的可信度 |
| 多智能体协调 | 多个 GGS 实例共享单一 R5；跨智能体 SOP 提升 |
