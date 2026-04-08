# Agent Infra 项目说明（全流程原理与运行机制）

更新时间：2026-04-08  
适用对象：产品、研发、测试、SRE、治理与审计团队

---

## 1. 文档目的与适用范围

本文档用于说明 Agent Infra 在生产环境中的核心机制、职责边界与运行原理，重点覆盖以下问题：

1. 请求进入系统后，端到端链路如何推进。
2. 关键机制（裁决、双确认、调度、审批、取证）为何存在以及如何生效。
3. 预算与门禁在不同阶段的触发条件与处置动作。
4. 故障定位时应优先查看的证据对象与事件。

本文档定位为“项目说明文档”，用于跨角色协同与评审对齐；代码级实现细节请配合阅读：
[工程代码导读-从零到跑通.md](/home/wandering/learn/agent/doc/guides/工程代码导读-从零到跑通.md)

---

## 2. 系统目标

Agent Infra 的目标是建立可生产运行的 Agent 执行底座，而非单纯提升回答能力。系统围绕以下三项能力建设：

1. 决策可控：仅在满足策略、风险和输入可信条件时放行。
2. 执行可靠：保证状态迁移、资源使用和副作用执行可验证。
3. 证据可信：保证过程可解释、问题可回放、责任可审计。

---

## 3. 架构总览与职责边界

系统由三个核心内核组成：Decision Kernel、Run Kernel、Evidence Kernel。

## 3.1 Decision Kernel（裁决内核）

职责：

1. Runtime 裁决（allow/require_approval/review_required/fail_closed/deny）。
2. 调度准入评估与票据签发。
3. 执行凭据校验（ticket/receipt 绑定与资源上限）。
4. 审批域管理（case、decision、timeout、unlock 分发）。
5. obligations 生成与约束计划输出。

非职责：

1. 不执行工具副作用。
2. 不推进 Run 状态机。
3. 不承担证据平面长期归档。

## 3.2 Run Kernel（运行内核）

职责：

1. run 生命周期状态机推进。
2. step 单调性、幂等与 continuation 管理。
3. 执行前硬门禁（决策引用、凭据、约束执行）。
4. 活性保障（TTL、Zombie sweep、强制终结）。

非职责：

1. 不做策略裁决。
2. 不承担证据聚合与审计索引。

## 3.3 Evidence Kernel（证据内核）

职责：

1. 事件标准化（Canonical Event）。
2. 决策图、决策日志、账本、完整性链维护。
3. Replay Pack 与 Root Cause Pack 产出。
4. 保留策略、归档、DSAR 与导出能力。

非职责：

1. 不参与放行或阻断决策。
2. 不直接推进业务状态机。

---

## 4. 端到端流程（基于典型业务场景）

示例场景：客服 Agent 执行“订单退款并通知用户”。

1. 退款属于高风险写副作用。
2. 通知属于外部系统调用。

该场景需要完整经过“裁决 -> 准入 -> 执行 -> 审批（按需） -> 取证”链路。

## 4.1 流程阶段

1. 创建 Run：`POST /v1/runs`
2. Runtime 裁决：`POST /v1/decision/evaluate-runtime`
3. 双确认：`POST /v1/decision/confirm-run-advance`
4. 调度准入：`POST /v1/decision/evaluate-schedule-admission`
5. Run 推进：`POST /v1/runs/{run_id}/advance`
6. 审批闭环（按需）：`POST /v1/approval/cases` + `POST /v1/approval/cases/{case_id}/decision`
7. 证据汇聚：Decision/Run outbox -> Evidence ingest

---

## 5. 关键机制原理

## 5.1 Runtime 裁决机制

Runtime 裁决遵循“输入约束 -> 策略评估 -> 风险降级 -> 结构化输出”的顺序。

典型处理步骤：

1. 输入归一化与冻结输入校验。
2. 风险层级与 effect 类型合法性校验。
3. Feature Signal Contract 校验（字段、生产者、时效、漂移、schema）。
4. DCU 决策复杂度预算校验。
5. Policy phase 评估与 obligations 合并。
6. 依赖可用性守卫（policy/approval 不可用时按风险降级）。
7. soft failure guard（限制重试循环）。

输出不是布尔值，而是结构化决策状态与理由集合，用于后续执行门禁与审计。

## 5.2 双确认机制（Decision 与 Run 对账）

双确认采用两段式提交语义：

1. Decision 先写 `pending_decision`。
2. Run 在推进前完成 owner-aware 对账，成功后推进为 `decision_confirmed`。

该机制解决以下一致性问题：

1. 决策结果与执行状态错位。
2. 乱序/晚到请求污染当前 step。
3. 跨 run/step/attempt 的错误借用。

关键绑定维度：

1. run_id
2. step_id
3. attempt_index
4. phase
5. owner_key

只有绑定完全一致，才允许进入执行阶段。

## 5.3 调度准入与票据机制

调度采取“先准入判定，后资源执行”的模型。

Decision 在准入通过后签发 DispatchTicket，票据绑定如下信息：

1. run_id
2. step_id
3. tenant_id
4. allowed_resources
5. expiry

Run 在执行前必须完成票据与回执校验，防止：

1. 超额资源使用。
2. 跨 run/step 串用票据。
3. 无依据执行。

## 5.4 执行前硬门禁机制（Run Advance）

Run 推进必须同时满足：

1. 状态机迁移合法。
2. step 序号单调递增。
3. 决策引用有效（confirmed + stable hash + binding 一致）。
4. receipt/ticket 有效（绑定一致且未超资源上限）。
5. obligations 已满足（例如 require_template、limit_param）。

任一条件不满足，均拒绝推进并记录证据事件。

## 5.5 审批闭环机制

当 Decision 输出 `require_approval` 时，系统进入审批域流程：

1. 创建审批 case。
2. 审批决策（approve/deny）。
3. 更新 pending 决策状态。
4. 分发 run unlock。
5. 分发失败触发 safeguard hold/force review。

审批必须与 run 解锁链路绑定，不能仅通过字段变更视为“已完成审批”。

## 5.6 证据化与取证机制

Decision/Run 仅输出 outbox 事件；Evidence 统一摄取并生成证据对象：

1. Canonical Event：标准化事件真值。
2. Decision Graph：因果路径。
3. Decision Log：裁决摘要视图。
4. Ledger：资源与成本账本。
5. Replay Pack / Root Cause Pack：复盘与回放工件。
6. Integrity/Anchor：完整性证明。

该机制确保系统不仅可运行，还可解释、可复盘、可审计。

## 5.7 活性保障与自愈机制

系统通过以下机制避免“长期卡死”：

1. Run TTL：超时转终态或复核。
2. Zombie sweep：无进展 run 回收。
3. Decision repair：pending 决策修复。
4. Approval hard-timeout：审批超时转安全终态。
5. Evidence backpressure：高压时降级证据，优先保障主链路。

---

## 6. 预算体系与触发动作

预算体系覆盖风险、稳定性与成本三个维度。

| 预算 | 所在层 | 触发条件 | 系统动作 | 取证意义 |
|---|---|---|---|---|
| DCU 决策复杂度预算 | Decision | 复杂度超阈值 | review/fail-closed | 证明拒绝具备规则依据 |
| Feature 时效预算 | Decision | stale/drift 超阈值 | 按风险层降级 | 证明输入可信度不足 |
| 调度配额预算 | Decision Admission | quota 不足或公平性命中 | deny/degrade | 证明资源仲裁依据 |
| Ticket 资源预算 | Run + Decision | used > allowed | 执行拒绝 | 证明阻断超额执行 |
| 重试预算 | Decision | soft failure 连续超限 | force review/failed | 证明防止无限重试 |
| 生命周期预算 | Run | TTL/Zombie 命中 | force abort/review | 证明系统可回收僵尸任务 |
| 证据写入预算 | Evidence | ingest 超限或背压升高 | drop/degrade（分级） | 证明高压下优先保关键证据 |
| Outbox 保留预算 | 三内核 | 事件过多/过旧 | trim + 指标 | 证明系统有界运行 |

---

## 7. 故障定位矩阵（现象 -> 动作 -> 证据）

| 现象 | 常见触发条件 | 系统动作 | 优先查看证据 |
|---|---|---|---|
| Runtime 被拒绝 | 合同字段缺失、stale/drift、DCU 超限 | review_required/fail_closed | reason_codes、contract 绑定字段、decision events |
| 有 allow 但 run 不推进 | 未 confirmed、owner mismatch、state/seq 冲突 | hold/reject | pending 状态、confirm 事件、run state_version |
| 无法取得 ticket | quota 不足、公平性/优先级限制 | deny/degrade | admission response、ticket 事件 |
| advance 被拒绝 | receipt 绑定错误、票据过期、资源超限 | RUN_EXECUTION_RECEIPT_REJECTED | run 拒绝事件、ticket/ref 绑定信息 |
| 审批后仍卡住 | unlock 分发失败、状态不匹配 | safeguard_hold/force review | approval decision 事件、unlock dispatch 状态、run 状态 |
| 任务长期未结束 | 回调缺失、循环失败、等待态僵死 | sweep 介入终结/清理 | sweep 事件、continuation 状态、step integrity |
| 证据查询不完整 | 背压、降级、保留期清理 | drop/degrade/retention | ingest counters、integrity verify、retention 结果 |

---

## 8. 控制面机制（治理维度）

## 8.1 Feature Signal Contract 控制面

目标是将“特征输入可信”治理为可发布、可追溯、可回放对象。

核心要素：

1. 作用域（org/workspace/project）。
2. 风险层与 phase 绑定。
3. 必填字段与约束集。
4. 版本与哈希 pinning。
5. scheduled activation 与生效记录。

## 8.2 Approval 组织治理

审批治理不仅关注系统可用，还关注组织可用。

关键指标：

1. active approver ratio
2. delegate freshness
3. override dependence
4. stale approver group ratio

## 8.3 Metric Enforcement Matrix

指标处置分级应明确：

1. observe only
2. alert
3. block release
4. block runtime

避免“有指标无动作”或“动作等级不一致”。

---

## 9. 阅读与落地建议

1. 先按 [工程代码导读-从零到跑通.md](/home/wandering/learn/agent/doc/guides/工程代码导读-从零到跑通.md) 跑通最小链路。
2. 对照本文第 5、6、7 章，建立“触发条件 -> 处置动作 -> 证据对象”的心智模型。
3. 再阅读三个 kernel 的测试用例，把原则映射为可执行断言。

---

## 10. 常见问题（FAQ）

1. 为什么不能收到 allow 就直接执行？
   - 因为生产环境必须通过双确认避免乱序、重试和跨对象误用。

2. 为什么 review_required 不能视为 allow？
   - review_required 表示需要人工复核，不是自动放行。

3. 为什么需要 ticket + receipt 双层机制？
   - ticket 定义可用资源上限，receipt证明实际资源使用，二者共同构成执行约束。

4. 为什么 Evidence 需要独立内核？
   - 证据平面生命周期与执行平面不同，需支持长期留存、审计与回放。

5. 为什么预算种类这么多？
   - 不同预算分别约束判定失控、执行失控、观测失控与成本失控。

---

## 11. 总结

Agent Infra 的核心价值在于形成三个闭环：

1. 决策闭环：输入可信 + 策略可解释 + 裁决可追溯。
2. 执行闭环：状态可控 + 资源可证 + 副作用可约束。
3. 证据闭环：过程可重建 + 问题可回放 + 责任可审计。

在该模型下，系统能够以工程化方式支撑 Agent 在企业生产环境中的长期稳定运行。
