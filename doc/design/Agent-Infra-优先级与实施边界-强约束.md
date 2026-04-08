# Agent Infra 优先级与实施边界（强约束）

更新时间：2026-04-03
适用范围：`/home/wandering/learn/agent` 全部实现与发布流程

## 0. 文档性质

1. 本文是“范围裁决文档”，用于控制系统复杂度与交付顺序。
2. 本文使用 `MUST/SHOULD/MAY` 作为规范强度关键词：
3. `MUST`：必须遵守，不满足不得上线。
4. `SHOULD`：应优先遵守，偏离必须有书面理由与签字。
5. `MAY`：可选，不影响平台成立条件。

## 1. 核心判断

1. 系统过重的根因不是“模块数量多”，而是“首发范围未锁定”。
2. 平台成立条件与平台上限能力必须分离。
3. 任何“先进能力”不得抢占首发核心资源。
4. 执行层可外包，裁决层与证据语义真值不可外包。
5. Eval 是关键证据而非绝对真值，发布需证据融合裁决。

## 2. 三层优先级（必须遵守）

## 2.1 第一层：平台灵魂（MUST 自研且做硬）

以下能力必须作为平台核心能力实现，且其决策语义不得外包为黑盒拼接（执行引擎可托管）：
1. Durable workflow runtime
2. Tool safety runtime
3. Context Compiler
4. Policy gate minimal
5. Audit / execution snapshot / decision log
6. Decision Causality Graph（跨 decider 因果链）

硬规则：
1. 第一层能力必须在同一治理域内闭环：可回放、可审计、可阻断。
2. 第一层任一能力未达标，平台不得宣称“企业级 Agent Infra 可上线”。

## 2.2 第二层：必须有但可薄做（MUST 存在，SHOULD 轻量）

以下能力是企业化必要条件，但第一版必须压缩实现范围：
1. Approval domain
2. Eval gate minimum
3. Basic scheduler / trigger
4. Basic RAG
5. Dependency resolution / freeze
6. Skill injection minimal（签名 + 边界约束 + 审计）

硬规则：
1. 第二层第一版必须给出“最小实现”边界与不做清单。
2. 第二层禁止一次性豪华化（例如 advanced scheduler、advanced retrieval 同步首发）。

## 2.3 第三层：平台上限能力（MAY，禁止抢首发资源）

以下能力重要，但属于上限能力，不是平台成立条件：
1. Multi-agent advanced runtime
2. A2A interop
3. Graph retrieval / graph DB
4. DFS cache
5. Auto policy learning
6. JIT retrieval 高级链路
7. Heterogeneous scheduling 豪华版

硬规则：
1. 第三层能力必须在隔离环境验证后，按 feature gate 灰度进入生产。
2. 第三层能力不得阻塞第一层与第二层上线节奏。

## 3. 自研 / 薄做 / 外部能力接入策略

## 3.1 自研（MUST）

1. Runtime 编排与状态一致性
2. 工具副作用安全控制
3. 上下文最终裁决
4. 策略最小门禁与审计闭环
5. 决策因果图（节点/边/回放）闭环

## 3.2 薄做（SHOULD）

1. Scheduler 先做 `cron + dedupe + basic quota`
2. RAG 先做 L1（hybrid + rerank + citation）
3. Eval 先做关键门禁套件
4. Approval 先做模板+路由+恢复绑定
5. Skill 先做“注册 + 签名校验 + 禁止越权注入”

## 3.3 先接外部成熟能力（MAY）

1. 复杂告警与故障发现引擎
2. 图计算/图存储（在收益不明确前）
3. 跨平台 Agent 协议互操作网关

约束：
1. 外部能力接入必须经过适配层，不得直接进入核心裁决链。
2. 外部能力故障不得破坏第一层闭环，只允许性能退化。
3. 调度外包仅限执行层（placement/queue/autoscaling）；调度治理裁决必须留在平台。
4. Evidence 真值（snapshot/decision log/audit/ledger/decision graph）语义所有权必须在平台。

## 4. 交付顺序与阶段门禁

## 4.1 Phase 1（可上线）

MUST：
1. 第一层全部能力达标。
2. 第二层仅最小实现（薄做）。

MUST NOT：
1. 不得引入第三层能力进入生产主链路。

## 4.2 Phase 2（企业化完善）

1. 扩展第二层能力深度（但保持边界清晰）。
2. 完成 Eval/Approval/Scheduler 的运营闭环。
3. 若启用 `fast_path_eval`，仅允许低风险变更并强制后置 full shadow eval。

## 4.3 Phase 3（上限能力）

1. 第三层能力在证据充分后逐步灰度。
2. 任一高级能力上线需证明 `quality_gain` 与 `cost/latency` 可控。

## 5. 变更控制（防止范围失控）

1. 任何变更必须标记所属层级（L1/L2/L3）。
2. L3 变更若影响 L1 链路，默认拒绝。
3. 发布评审必须附：
4. `scope_classification_report`
5. `blast_radius_report`
6. `rollback_plan`

## 6. 验收判定

1. 满足第一层 + 第二层最小实现 = 可以称为“可上线 Agent Infra”。
2. 仅做了第三层能力但未满足第一层，不得通过架构验收。
3. 若交付与本文冲突，以本文为“范围仲裁真值”。

## 7. 实现语言治理（必须遵守）

## 7.1 主语言决策

1. Core 域主语言 `MUST` 为 `Go`。
2. Core 域定义：Workflow runtime、Tool safety、Context compile API、Policy eval API、Scheduler/Admission、Audit/Decision Log API。

## 7.2 语言边界

1. `TypeScript` `SHOULD` 用于前端与开发者体验层（Console/CLI/Portal）。
2. `Python` `MAY` 用于离线与实验链路（eval 数据加工、分析、实验检索），`MUST NOT` 成为生产主链路强依赖。

## 7.3 约束规则

1. `MUST`：跨语言交互统一通过版本化接口（gRPC/HTTP + OpenAPI/Proto），禁止隐式调用。
2. `MUST`：新增语言若影响 Core 域，必须提交 `benchmark + blast_radius + rollback_plan`。
3. `MUST NOT`：在 Phase 1 引入第二主语言实现 Core 同域重复能力。
4. `SHOULD`：代码评审与值班分工按“核心域单主语言”组织，避免 oncall 知识碎片化。
