# Agent Infra 精简化落地方案（Kernel 重写版，主文档全问题闭环）

更新时间：2026-04-03  
适用目录：`/home/wandering/learn/agent/doc`  
文档级别：实施级（可落地、可验收、可回放）

---

## 0. 先说边界与承诺

### 0.1 本文目标

1. 在不降低主文档能力覆盖率的前提下，把系统从“正确但过重”重构为“最小可行且可扩展”的企业级 Agent Infra。
2. 给出完整 Kernel 内部实现，不停留在概念层。
3. 对主文档提出的全部问题给出逐项闭环：问题、机制、可验证证据、失败处理、回滚路径。
4. 保留长期扩展能力：多 Agent、自愈、异构调度、高级检索、策略与评测平台化。

### 0.2 信息来源与合规边界

1. 本文不使用、也不引用任何未授权泄露源码或非法获取信息。
2. 对 Claude Code 的比较仅基于公开官方文档与公开工程信息。
3. 对“2026 年先进实践”的吸收仅基于公开论文、标准、官方技术文档与公开工程经验。
4. 所有关键决策必须能回溯到“公开依据 + 本系统约束 + 可验证实验”。

### 0.3 为什么必须加这一节

问题：
1. 体系设计如果混入未授权来源，法律、合规、商业可持续性都不可控。
2. 企业级基础设施必须可审计，技术路线与依据来源也要可审计。

方案：
1. 固化“公开来源优先”制度。
2. 对每个关键设计输出“证据类型”标签：`official_doc`、`paper`、`internal_eval`、`incident_postmortem`。

优点：
1. 设计可复核。
2. 合规可落地。

缺点：
1. 可能失去部分“传闻型”细节。

依据：
1. 企业治理对可追溯与合法性要求高于“信息量”。

### 0.4 编码前不变量（不可简化）

1. 下列类型规则必须在编码前固化：系统不变量、发布门禁、运行时保护动作、值班 runbook。
2. 任一规则若仅有“方向描述”而无执行语义，视为未完成。
3. 代码评审必须验证：规则存在实现映射、指标有 owner、故障有自动动作与人工兜底。

### 0.5 代码设计六原则（落地顺序）

1. 先做编译产物，再做运行时执行。
2. 每个 Kernel 先定义不可变输入与结构化输出，再实现逻辑。
3. 门禁逻辑前置到 CI/CD，不把高风险检查推到线上兜底。
4. replay 从第一天就是一等公民，不做“上线后补 replay”。
5. 软策略必须绑定硬失败语义：`degrade/review_required/fail_closed/abort`。
6. 单 Agent 主路径先稳定，多 Agent 只保留接口并后置实现。

---

## 1. 精简设计的顶层原则（不能破）

### 1.1 三层不可外包真值

1. 执行真值：Run Kernel。
2. 裁决真值：Decision Kernel。
3. 证据真值：Evidence Kernel。

解释：
1. 可外包的是执行器，不是决策权与证据语义。
2. 一旦把决策或证据真值外包，平台身份就会坍塌。

### 1.2 两类可外包能力

1. 执行外包：调度、触发、模型推理、检索执行、通知投递、基础健康检测。
2. 存储托管：对象存储、消息队列、列式数仓、向量库、时序监控。

硬约束：
1. 外包系统不得产生最终 allow/deny/promote/block。
2. 外包系统写入的数据必须通过平台 canonical schema 落盘。

### 1.3 一类必须统一入口的动作

1. 会改变权限边界。
2. 会改变副作用边界。
3. 会改变资源公平边界。
4. 会改变发布风险边界。

这四类都必须经过 Decision Kernel 正式裁决。

### 1.4 统计系统与确定系统分治

1. 确定性链路：状态机、签名、幂等、快照、票据、权限匹配。
2. 统计性链路：LLM 生成、检索召回、评测打分、漂移检测。

治理要求：
1. 确定性链路要求强一致规则。
2. 统计性链路要求置信区间、样本覆盖、回放验证。
3. 禁止用统计指标替代确定性安全保障。

---

## 2. 为什么是“三 Kernel 最小解”，不是五花八门

### 2.1 反证法说明

去掉 Run Kernel 会怎样：
1. parked run 无法稳定恢复。
2. 版本冻结与回滚语义失效。
3. 工具幂等变成“约定而非系统保证”。

去掉 Decision Kernel 会怎样：
1. Policy、Approval、Scheduler、Release 分散裁决。
2. 最终谁说了算变成多头决策。
3. 跨域冲突只能靠人工排障。

去掉 Evidence Kernel 会怎样：
1. 事件能看见但无法形成可解释因果链。
2. replay 难复原，争议无法闭环。
3. 计费、审计、合规各说各话。

### 2.2 再加 Kernel 为什么不划算

问题：
1. 把 Context/Policy/Approval/Eval/Scheduler 都变成独立 Kernel，会把关键路径拉长。

方案：
1. 采用“3 Kernel + 领域引擎子域”的做法。
2. 子域通过统一契约在 Kernel 内组合，不额外增加跨服务跳数。

优点：
1. 保留语义边界。
2. 控制链路复杂度。

缺点：
1. 单 Kernel 内部实现复杂，需要清晰模块边界。

依据：
1. 高并发系统普遍遵循“语义边界清晰 + 网络边界克制”的设计法。

### 2.3 最小可验证不变量

1. `final_decider_uniqueness = 100%`。
2. `dispatch_ticket_enforced = 100%`。
3. `snapshot_resume_match >= 99.99%`。
4. `high_risk_decision_graph_completeness = 100%`。
5. `critical_false_allow = 0`。

---

## 3. 全问题清单映射（主文档问题必须闭环）

### 3.1 主问题组 A：架构与边界

问题 A1：上下文职责重叠（Context Compiler / Memory / RAG / Citation）。
1. 解决：Decision Kernel 内部强制单终裁器 `Context Compiler`。
2. 机制：Memory/RAG/Citation 只产生候选，不得终裁。
3. 验证：单 run 仅允许一个 `context.final_decision` node。

问题 A2：Workflow DSL 不足以支撑生产。
1. 解决：Run Kernel 强契约 DSL v2。
2. 机制：schema/effect/permission/approval/context boundary/observability 必填。
3. 验证：发布前静态校验与拓扑连通校验 100% 覆盖。

问题 A3：跨 Decider 隐式耦合链无法解释。
1. 解决：Evidence Kernel 维护 `Decision Causality Graph`。
2. 机制：节点与边语义标准化，必须可追溯 allow/deny 来源。
3. 验证：高风险 run 缺边一票否决。

问题 A4：系统过重，不易落地。
1. 解决：三 Kernel 合并高频治理路径，外包执行层。
2. 机制：调度/检索/模型作为 Adapter，不保留外部裁决权。
3. 验证：同步 hops 控制在目标范围。

### 3.2 主问题组 B：运行时正确性

问题 B1：工具等待时资源浪费与模型阻塞。
1. 解决：Run Kernel park/resume 分级等待。
2. 机制：A/B/C 工具类别 + continuation token。
3. 验证：长尾工具不占模型资源。

问题 B2：副作用未知结果处理不足。
1. 解决：Run Kernel + Decision Kernel 共治 unknown outcome。
2. 机制：探测优先、reconcile、before/after 证据。
3. 验证：unknown outcome reconciled rate。

问题 B3：错误传播模型缺失。
1. 解决：Evidence Kernel 落地 Error Propagation Graph（EPG）。
2. 机制：错误类型、传播边、遏制动作绑定。
3. 验证：amplification 被遏制时间 P95。

问题 B4：运行中版本兼容规则不清。
1. 解决：Run Kernel Execution Snapshot + Compatibility Policy。
2. 机制：STRICT/FORWARD_SAFE/MIGRATABLE。
3. 验证：parked run 恢复成功率与兼容违规率。

### 3.3 主问题组 C：治理与合规

问题 C1：Policy 方向正确但执行抽象。
1. 解决：Decision Kernel 内嵌 Policy as Code 执行器。
2. 机制：DSL -> IR -> 引擎，phase obligation 严格顺序。
3. 验证：policy replay 一致率。

问题 C2：审批域只是“节点”，不是业务域。
1. 解决：Decision Kernel 内审批子域独立建模。
2. 机制：路由、会签、升级、代理、证据包、恢复绑定。
3. 验证：审批超时升级达成率与恢复正确率。

问题 C3：Eval 不是一级系统。
1. 解决：Change Safety Engine（依附 Decision/Evidence 协作）。
2. 机制：Taxonomy、dataset version、grader 协议、gate。
3. 验证：坏变更阻断率、误伤率、回放一致率。

问题 C4：发布裁决缺证据融合。
1. 解决：release decision 必须融合 eval/policy/replay/incident/human。
2. 机制：单一指标不能直接 promote。
3. 验证：发布审计包完整率。

### 3.4 主问题组 D：性能与容量

问题 D1：SLO 有目标，缺容量推导。
1. 解决：容量模型绑定关键路径组件。
2. 机制：QPS -> CPU/Mem/IO/Storage/Cost 分解。
3. 验证：容量预测误差阈值管理。

问题 D2：Context Compiler 成为风险点。
1. 解决：Candidate Budget Gate + compile 预算 + 队列隔离。
2. 机制：源级限额、内存上限、降级配置。
3. 验证：候选爆发时 compile 仍可控。

问题 D3：调度与治理边界混乱。
1. 解决：调度执行外包，准入治理内生。
2. 机制：dispatch_ticket / execution_receipt 双向校验。
3. 验证：无票执行阻断率 100%。

问题 D4：故障节点隔离不体系化。
1. 解决：Health Adapter 上报 + Decision Kernel 统一隔离裁决。
2. 机制：健康信号标准化、隔离策略、恢复策略。
3. 验证：fault-to-isolation latency。

### 3.5 主问题组 E：数据面与可解释性

问题 E1：数据平面像“组件清单”，不是运行时科学。
1. 解决：Evidence Kernel 定义 canonical 数据域与一致性。
2. 机制：run_state/audit/replay/eval/ledger 分层与关系语义。
3. 验证：跨域对账差异率。

问题 E2：审计可见但不可解释。
1. 解决：Decision Causality Graph + root_cause_pack。
2. 机制：跨域因果边与 first_bad_node。
3. 验证：MTTI 下降。

问题 E3：DSAR 与回放/评测冲突。
1. 解决：数据保留分层 + 删除传播策略。
2. 机制：删明文留哈希索引、回放遮罩。
3. 验证：DSAR SLA 与合规审计通过率。

### 3.6 主问题组 F：可用性与开发者体验

问题 F1：平台强但开发者接入弱。
1. 解决：本地最小栈 + simulate + replay + dry-run。
2. 机制：统一 CLI/API 工具链。
3. 验证：首个 workflow 上线时间。

问题 F2：调试复杂度指数上升。
1. 解决：Evidence Kernel 提供 root-cause 一键包。
2. 机制：单 run 导出关键决策、日志、快照、回执。
3. 验证：跨系统手工拼日志次数下降。

问题 F3：新团队学习成本高。
1. 解决：Core/Extended/Experimental 三层启用模型。
2. 机制：默认只开 Core，渐进升级。
3. 验证：阶段门禁达成率。

---

## 4. 参考体系与对比原则（Claude 对比怎么做才严谨）

### 4.1 对比对象

1. Claude Code（终端 agent 编程体验与工具编排）。
2. Claude Agent SDK（headless 与应用接入能力）。

### 4.2 对比边界

1. 对比“能力结构”与“治理模型”，不是对比营销叙事。
2. 不比较未公开内部实现细节。
3. 不基于未授权泄露信息做推理。

### 4.3 对比结论先行

1. Claude 强在执行与开发体验层。
2. 本方案强在企业多租户治理、证据、发布门禁、可追责。
3. 两者可互补：Claude 作为执行能力接入，本平台保留裁决与证据真值。

---

## 5. 三 Kernel 总览（从系统图到职责图）

### 5.1 Run Kernel（执行真值）

解决的问题：
1. 长事务可恢复。
2. park/resume 稳定。
3. 幂等与副作用一致。
4. 运行中版本兼容。

最终输出：
1. run 状态迁移。
2. continuation token。
3. execution snapshot。
4. step execution record。

### 5.2 Decision Kernel（裁决真值）

解决的问题：
1. 谁允许执行。
2. 谁需要审批。
3. 谁可进入调度。
4. 谁可以发布。
5. 上下文如何终裁。

最终输出：
1. `allow/deny/require_approval/review_required`。
2. `admit/reject/preemptable`。
3. `promote/block`。
4. obligations。

### 5.3 Evidence Kernel（证据真值）

解决的问题：
1. 为什么这么决策。
2. 复盘和取证怎么做。
3. 成本、风险、合规如何归因。

最终输出：
1. decision logs。
2. decision causality graph。
3. root-cause pack。
4. replay manifest。
5. usage ledger。

### 5.4 三 Kernel 关系约束

1. Run 不直接读写 Decision 私有表。
2. Decision 不直接读写 Run 私有表。
3. Evidence 不替代 Run/Decision 的实时控制逻辑。
4. 只允许通过版本化 API 或事件契约交换数据。

---

## 6. Run Kernel 详细设计（实现级）

### 6.1 内部模块清单

1. `Run API Gateway`。
2. `State Machine Engine`。
3. `Snapshot Binder`。
4. `Continuation Manager`。
5. `Idempotency Guard`。
6. `Recovery Controller`。
7. `Compatibility Checker`。
8. `Step Executor Orchestrator`。
9. `Run Outbox`。
10. `Run Metrics Exporter`。

### 6.2 State Machine Engine

解决问题：
1. 防止非法状态跳转。
2. 防止并发推进冲突。

方案：
1. 状态集合：`CREATED`、`READY`、`RUNNING`、`PARKED`、`AWAITING_APPROVAL`、`FAILED`、`COMPLETED`、`ABORTED`。
2. 每次迁移要求 `expected_state_version`。
3. 不合法迁移立即拒绝并记录审计事件。

优点：
1. 状态一致性强。

缺点：
1. 调用方必须携带版本。

验证：
1. 非法迁移拒绝率应为 100%。

### 6.3 Snapshot Binder

解决问题：
1. 运行中版本漂移导致恢复错乱。

方案：
1. run 创建时冻结：workflow、policy bundle、model profile、dependency bundle、skill bundle set。
2. 生成 `snapshot_hash`。
3. resume 时强制校验。

优点：
1. 保证恢复语义一致。

缺点：
1. 变更即时生效能力受限。

验证：
1. snapshot mismatch 时恢复必须阻断。

### 6.4 Continuation Manager

解决问题：
1. park/resume token 混乱或重放风险。

方案：
1. token 分类：`park`、`approval`、`callback`。
2. token 仅存 hash，不存明文。
3. token 一次性消费与过期时间控制。

优点：
1. 安全与幂等兼顾。

缺点：
1. token 生命周期管理复杂。

验证：
1. 重放 token 拦截率 100%。

### 6.4.1 Progress Monotonicity Contract（进度单调性约束）

目的：
1. 防止 callback 晚到、重复 resume、并发 worker 竞争导致状态回退或乱序覆盖。

核心字段：
1. `step_seq_id`（严格递增）。
2. `current_seq_id`（run 当前已确认进度）。

运行时规则：
1. 任一步骤更新必须满足 `incoming_step_seq_id > current_seq_id`。
2. 不满足条件的更新一律拒绝并写 `progress_monotonicity_violation`。
3. continuation token 必须绑定 `run_id + step_id + step_seq_id`。
4. callback/resume 若 step_seq_id 不匹配当前预期，必须拒绝并进入审计。

收益：
1. 防状态回退。
2. 防重复副作用。
3. 提升 replay 与线上一致性。

### 6.4.2 Run 不可逆推进不变量（上线门禁）

不变量：
1. `step_seq_id` 只允许严格递增，不允许回退或平级覆盖。
2. 任一副作用步骤执行前必须满足 `decision_confirmed=true`（对应当前 `step_seq_id`）。
3. 发现 `pending_decision` 与执行状态不一致时，必须进入 `safeguard_hold`，不得继续推进。
4. 所有单调性违规必须写 `irreversible_progress_violation_event` 并触发 P1 告警。

门禁要求：
1. 发布前必须通过乱序 callback、duplicate resume、worker race 三类场景回归。
2. 任一场景出现状态回退或重复副作用，发布阻断。

### 6.5 Idempotency Guard

解决问题：
1. 重试导致写副作用重复执行。

方案：
1. 幂等主键：`run_id + step_id + step_version + idempotency_key`。
2. 命中后直接返回历史结果引用。

优点：
1. 减少重复写风险。

缺点：
1. 需要工具端配合幂等键传递。

验证：
1. duplicate side effect incident = 0（高风险路径）。

### 6.6 Recovery Controller

解决问题：
1. worker 崩溃与网络波动造成 run 卡死。

方案：
1. 周期扫描 stalled run。
2. 基于状态与最后事件重建执行上下文。
3. 对 callback 迟到进行去重校验。

优点：
1. 自愈能力增强。

缺点：
1. 扫描任务需要容量预算。

验证：
1. stuck run auto-recovered rate。

### 6.7 Compatibility Checker

解决问题：
1. 新版本发布后老 run 恢复不一致。

方案：
1. `STRICT`：必须原版本。
2. `FORWARD_SAFE`：允许新增非语义字段。
3. `MIGRATABLE`：必须有迁移器和审计标记。

优点：
1. 兼容策略可治理。

缺点：
1. 迁移器开发成本高。

验证：
1. 恢复失败原因可归因到具体兼容策略。

### 6.8 Step Executor Orchestrator

解决问题：
1. 不同步骤 effect 语义不同，执行策略应不同。

方案：
1. `pure`：可高并发重试。
2. `read`：可重试，需缓存策略。
3. `idempotent_write`：重试需幂等键。
4. `non_idempotent_write`：禁止自动重试。
5. `irreversible`：必须审批或双确认策略（按政策要求）。

优点：
1. 运行时与副作用语义一致。

缺点：
1. 流程定义要求更高。

验证：
1. effect mismatch 违规为 0。

### 6.9 Run Outbox

解决问题：
1. 主事务与事件发送不一致。

方案：
1. 本地事务写状态 + outbox。
2. 异步投递到事件总线。

优点：
1. 规避双写不一致。

缺点：
1. 需要 outbox 清理与重试机制。

验证：
1. outbox backlog 可控，丢失率 0。

### 6.10 Run 数据模型（SQL 草案）

```sql
CREATE TABLE run_instances (
  run_id                 TEXT PRIMARY KEY,
  tenant_id              TEXT NOT NULL,
  workflow_id            TEXT NOT NULL,
  workflow_version       TEXT NOT NULL,
  state                  TEXT NOT NULL,
  active_step_id         TEXT,
  state_version          BIGINT NOT NULL,
  risk_tier              TEXT NOT NULL,
  integrity_chain_version TEXT NOT NULL,
  last_step_hash         TEXT,
  run_integrity_root     TEXT,
  created_at             TIMESTAMPTZ NOT NULL,
  updated_at             TIMESTAMPTZ NOT NULL
);

CREATE TABLE run_snapshots (
  run_id                 TEXT PRIMARY KEY,
  policy_bundle_id       TEXT NOT NULL,
  model_profile_id       TEXT NOT NULL,
  dependency_bundle_id   TEXT NOT NULL,
  skill_bundle_set_hash  TEXT NOT NULL,
  context_policy_space_hash TEXT NOT NULL,
  snapshot_hash          TEXT NOT NULL,
  created_at             TIMESTAMPTZ NOT NULL
);

CREATE TABLE run_continuations (
  continuation_id        TEXT PRIMARY KEY,
  run_id                 TEXT NOT NULL,
  step_id                TEXT NOT NULL,
  token_type             TEXT NOT NULL,
  token_hash             TEXT NOT NULL,
  expires_at             TIMESTAMPTZ NOT NULL,
  status                 TEXT NOT NULL,
  created_at             TIMESTAMPTZ NOT NULL,
  consumed_at            TIMESTAMPTZ
);

CREATE TABLE run_idempotency (
  run_id                 TEXT NOT NULL,
  step_id                TEXT NOT NULL,
  step_version           TEXT NOT NULL,
  idempotency_key        TEXT NOT NULL,
  result_ref             TEXT NOT NULL,
  created_at             TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (run_id, step_id, step_version, idempotency_key)
);

CREATE TABLE run_step_integrity (
  run_id                 TEXT NOT NULL,
  step_id                TEXT NOT NULL,
  step_seq_id            BIGINT NOT NULL,
  previous_step_hash     TEXT,
  decision_hash          TEXT NOT NULL,
  input_hash             TEXT NOT NULL,
  output_hash            TEXT NOT NULL,
  step_hash              TEXT NOT NULL,
  created_at             TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (run_id, step_seq_id)
);
```

### 6.11 Run API（OpenAPI 摘要）

1. `POST /v1/runs`。
2. `POST /v1/runs/{run_id}/advance`。
3. `POST /v1/runs/{run_id}/park`。
4. `POST /v1/runs/{run_id}/resume`。
5. `POST /v1/runs/{run_id}/abort`。
6. `GET /v1/runs/{run_id}`。


### 6.12 Run API 请求/响应契约（关键字段）

`POST /v1/runs`
1. 请求字段：`tenant_id`、`workflow_id`、`workflow_version`、`risk_tier`、`input_payload_ref`。
2. 响应字段：`run_id`、`snapshot_hash`、`context_policy_space_hash`、`initial_state`、`created_at`。
3. 失败行为：
- 工作流不存在 -> `404`。
- schema 不匹配 -> `422`。
- 依赖冻结失败 -> `409`。

`POST /v1/runs/{run_id}/advance`
1. 请求字段：`expected_state_version`、`decision_ref`、`execution_receipt_ref`、`incoming_step_seq_id`。
2. 响应字段：`new_state`、`next_action`、`state_version`、`step_hash`、`run_integrity_root`。
3. 幂等语义：相同 `run_id + state_version + decision_ref` 重放返回同结果。

`POST /v1/runs/{run_id}/resume`
1. 请求字段：`continuation_token`、`resume_reason`、`receipt_ref`、`incoming_step_seq_id`。
2. 响应字段：`resume_status`、`new_state`。
3. 失败行为：
- token 过期 -> `410`。
- token 已消费 -> `409`。
- snapshot mismatch -> `412`。

### 6.13 Run Kernel 资源预算

1. Run API 网关 P95：`<= 15ms`。
2. 状态机迁移 P95：`<= 12ms`。
3. continuation 校验 P95：`<= 8ms`。
4. 幂等查询 P95：`<= 10ms`。
5. 单租户并发活跃 run 默认上限：`20,000`。
6. 单租户 parked run 默认上限：`100,000`。
7. 恢复扫描周期：`10s`。
8. 恢复扫描单批上限：`2,000 runs`。

超预算退化：
1. 恢复扫描降级为优先队列模式。
2. 低风险 run 恢复频率降低。
3. 高风险审批恢复路径优先保障。

### 6.13.1 Liveness 保障（Run TTL + 强制终结）

`run_max_lifetime` 默认策略：
1. low：`1h`。
2. medium：`6h`。
3. high：`24h`。
4. critical：按租户合规配置（必须显式声明）。

超时处理规则：
1. 达到 TTL 且无活性进展：进入 `FORCE_REVIEW_REQUIRED`。
2. 达到 TTL 且触发安全风险或资源风险：进入 `FORCE_ABORT`。
3. critical 超时不得静默续命，必须写审计事件并触发值班告警。

僵尸 run 清理：
1. `zombie_run_sweeper` 每 5 分钟扫描一次。
2. 连续两次扫描无进展且超过阈值，执行强制终结策略。
3. 强制终结必须产出 `root_cause_pack`，用于后续回放与修复。

### 6.13.2 活性指标（必须监控）

1. `stuck_run_count`。
2. `run_ttl_breach_rate`。
3. `force_abort_rate`。
4. `force_review_required_rate`。
5. `zombie_run_cleanup_latency_p95`。

### 6.14 Run Kernel 扩展机制

扩展点 `StepType Plugin` 要求：
1. 注册元数据：`step_type_id`、`owner_team`、`version`。
2. 必须 schema：`input`、`output`。
3. 必须语义：`effect_type`、`retry_policy`、`timeout_policy`。
4. 必须处理器：`execute`、`simulate`、`replay`、`reconcile`。
5. 必须安全声明：`required_permissions`、`data_sensitivity`。
6. 必须可观测声明：`trace_tags`、`metric_keys`。

扩展验证流程：
1. 静态 lint。
2. sandbox simulate。
3. synthetic replay。
4. policy dry-run。
5. canary 发布。

扩展失败回滚：
1. run 不切换 snapshot。
2. 插件路由回退上一稳定版本。
3. 写入 failure evidence。

### 6.15 Run Kernel 设计证明（为什么成立）

问题：
1. 长事务系统的主要事故来自“恢复不一致”和“重复副作用”。

方案：
1. 状态机 + 快照冻结 + 幂等主键 + continuation token。

优点：
1. 把“恢复正确性”从经验变成系统约束。

缺点：
1. 早期实现复杂。

依据：
1. Durable workflow 与 outbox 工程模式成熟，且与现有目标一致。

### 6.16 Run Integrity Chain（全局一致性锚点）

目标：
1. 在 step 级不变量之外，提供 run 级完整性证明，防隐式漂移与篡改。

构建规则：
1. `step_hash = hash(previous_step_hash + decision_hash + input_hash + output_hash)`。
2. `previous_step_hash` 来自上一个已确认 `step_seq_id`。
3. 每次 step 提交成功后更新 `last_step_hash`。
4. run 完成时固化 `run_integrity_root`。

强约束：
1. `decision_hash` 必须来自 `decision_confirmed` 对应版本。
2. 缺失任一 hash 字段的 step 不得标记 completed。
3. replay 验证必须校验 `run_integrity_root`，不一致则进入 `integrity_mismatch_review`。
4. run 完成后必须把 `run_integrity_root` 锚定到 Evidence Integrity Guard。

用途：
1. run 级审计证明。
2. replay 一致性校验。
3. 事故取证中的“是否漂移”快速判定。

### 6.17 Intermediate Integrity Semantics（中间态完整性语义）

目标：
1. 统一未完成 run 的完整性语义，避免“完成态严格、失败态模糊”。

完整性根类型：
1. `completed_root`：run 正常完成后的最终根。
2. `partial_root`：`PARKED/AWAITING_APPROVAL` 等中间态根。
3. `reconciled_root`：unknown outcome 经 reconcile 后固化的根。
4. `review_hold_root`：`FORCE_REVIEW_REQUIRED` 时固化的根。
5. `aborted_root`：`FORCE_ABORT/ABORTED` 时固化的根。

状态与锚定规则：
1. `AWAITING_APPROVAL`：必须同步落 `partial_root` 到 Evidence 最小集。
2. `PARKED`：必须同步落 `partial_root`，resume 时校验连续性。
3. `RECONCILING`：可临时不更新最终根，但每次探测必须写中间 hash 事件。
4. `FORCE_REVIEW_REQUIRED`：必须同步落 `review_hold_root`。
5. `FORCE_ABORT/ABORTED`：必须同步落 `aborted_root` 并关闭推进。

取证与回放：
1. root-cause pack 默认使用最新已锚定 root。
2. replay 必须显式声明使用 `completed/partial/reconciled/review_hold/aborted` 哪一类 root。

---

## 7. Decision Kernel 详细设计（实现级）

### 7.1 内部模块清单

1. `Decision API Gateway`。
2. `Context Resolver Engine`。
3. `Feature Signal Verifier`。
4. `Policy Engine`。
5. `Approval Domain Engine`。
6. `Scheduler Admission Engine`。
7. `Quota & Fairness Engine`。
8. `Release Decision Engine`。
9. `Obligation Orchestrator`。
10. `Decision Outbox`。
11. `Decision Metrics Exporter`。

### 7.2 Decision Kernel 的单一职责

1. 不执行外部副作用。
2. 只做“裁决和约束生成”。
3. 所有输出必须可解释、可回放。

### 7.2.1 Decision Kernel 反膨胀硬边界（必须遵守）

1. 不做执行：不直接调用外部副作用工具，不直接下发物理调度命令。
2. 不做重型离线计算：不在 Kernel 内跑大规模回放、批评测、训练或特征离线构建。
3. 不做复杂特征生成：Kernel 仅消费预计算特征，不在请求路径构建重型特征。
4. 不做策略学习训练：策略学习与阈值训练在离线系统，Kernel 只消费已发布版本。
5. 不做多 Agent 重型合并计算：Kernel 只执行 merge 契约判定，不做复杂语义搜索式合并。

### 7.2.2 Decision Kernel 非目标清单（防超级中枢）

1. 非目标：策略学习平台。
2. 非目标：评测计算平台。
3. 非目标：检索特征工程平台。
4. 非目标：多 Agent 训练与优化平台。
5. 非目标：证据仓库分析平台。

### 7.2.3 Decision Kernel 复杂度门禁

1. 同步裁决路径禁止外部特征读取，只允许消费冻结输入与本地已验证缓存。
2. 同步裁决路径禁止调用离线仓库查询。
3. 决策超时预算固定：runtime 决策必须满足 P95 预算，不得因新功能放宽。
4. 新增决策维度必须通过“复杂度影响评估”，评估项包括增量时延、增量依赖数、失败模式新增数、回放复杂度增量。
5. 触发以下任一条件必须拆分为外部服务，不得继续塞入内核：新增同步外部依赖、决策 P95 增量大于 20ms、新增不可回放逻辑。

### 7.2.4 Decision Complexity Budget（DCU）执行器

目的：
1. 把“复杂度原则”变成运行时硬约束，防止 Decision Kernel 渐进过载。

`decision_cost_unit (DCU)` 组成：
1. `feature_reads_cost`：每次特征读取计分。
2. `rule_eval_cost`：每条规则评估计分。
3. `dependency_call_cost`：每个内部依赖查询计分（禁止同步外部依赖调用）。
4. `conflict_resolution_cost`：冲突求解复杂度计分。

预算策略（按 risk tier 配置）：
1. low：较低 DCU 上限，优先快路径。
2. medium：中等 DCU 上限。
3. high：允许更高 DCU，但必须可回放。
4. critical：允许最高 DCU，但必须满足强审计与强解释。

超预算行为：
1. low/medium：自动 `degrade` 到最小决策路径。
2. high：`review_required` 或降级到预定义安全策略集。
3. critical：`fail_closed`（若无法满足最小安全决策集）。

审计与观测：
1. 每次决策输出 `dcu_used`、`dcu_limit`、`dcu_component_breakdown`。
2. 超预算必须写 `decision_dcu_exceeded_event`。

### 7.2.5 Decision Input Freeze Layer（输入冻结层）

目的：
1. 消除 Decision 同步路径的隐式 RPC fan-out 风险，保证决策稳定与可回放。

冻结策略：
1. `Run -> Freeze Layer -> Decision`。
2. 冻结输入至少包含：context candidates snapshot、policy bundle snapshot、feature snapshot（如启用特征信号）、approval routing context snapshot（组织图快照引用）。
3. Decision 只消费冻结输入，不在同步路径主动拉取 RAG/Memory/外部特征系统。

强约束：
1. Decision 同步路径禁止 runtime fan-out 到 RAG/Memory/外部特征系统。
2. 若冻结输入缺失：low/medium 返回 `review_required`，high/critical 返回 `fail_closed`。
3. 每次决策必须输出 `frozen_input_hash`，用于 replay 一致性校验。

#### 7.2.5.1 Freeze Object Whitelist（硬白名单）

目标：
1. 防止实现阶段“能拿到什么就用什么”，导致 replay 与线上不一致。

必须冻结（mandatory frozen set）：
1. `context_candidates_snapshot_ref`。
2. `policy_bundle_snapshot_ref`。
3. `feature_snapshot_id`。
4. `approval_routing_snapshot_ref`。
5. `quota_snapshot_ref`。
6. `scheduler_admission_input_snapshot_ref`。

允许动态（dynamic allowed）：
1. 纯观测 trace tags。
2. 低风险 debug hints（不得进入裁决逻辑）。

禁止动态（dynamic forbidden）：
1. 任何影响 `allow/deny` 的字段。
2. 任何影响 `approval_required/approval_route` 的字段。
3. 任何影响 `admission/release` 的字段。

白名单矩阵（实现必须逐项校验）：

| category | fields |
|---|---|
| mandatory frozen | `context_candidates_snapshot_ref`、`policy_bundle_snapshot_ref`、`feature_snapshot_id`、`approval_routing_snapshot_ref`、`quota_snapshot_ref`、`scheduler_admission_input_snapshot_ref` |
| dynamic allowed | `trace_tags`（观测用途）、`debug_hints_low_risk` |
| dynamic forbidden | 所有影响 `allow/deny/approval/admission/release` 的字段 |

门禁：
1. `freeze_whitelist_coverage=100%` 才允许发布。
2. 命中 `freeze_forbidden_dynamic_field` 直接阻断 runtime（high/critical fail_closed）。

### 7.2.6 Decision Complexity Governance（制度化门禁）

目标：
1. 把 DCU 从“设计原则”升级为“发布与运行时双门禁”。

DCU 默认 profile（按 risk tier）：
1. low：`dcu_limit_low`（固定低阈值，优先快路径）。
2. medium：`dcu_limit_medium`（允许中等复杂度）。
3. high：`dcu_limit_high`（允许较高复杂度，但要求强审计）。
4. critical：`dcu_limit_critical`（最高阈值，必须可解释与可回放）。

统一错误码与审计字段：
1. `DECISION_DCU_EXCEEDED`：超预算拒绝/降级。
2. `DECISION_DCU_PROFILE_MISSING`：risk tier 未配置 DCU profile。
3. 必填审计：`dcu_used`、`dcu_limit`、`dcu_profile_id`、`dcu_reject_reason`。

新增决策维度准入流程（必须）：
1. 提交 `decision_change_rfc`，包含 `dcu_delta`、新增失败模式、回放样本覆盖。
2. 执行 shadow evaluate 与回放对比。
3. 平台 owner + 安全 reviewer 双签通过后才可进生产。

CI/CD 门禁：
1. `dcu_budget_coverage_rate=100%`（所有 risk tier 必须有 profile）。
2. `decision_p95_delta_ms` 不得超过阈值。
3. 新增依赖数超阈值自动阻断发布。

### 7.2.7 Feature Signal Contract（强制）

目标：
1. 防止 Decision 演化成“轻量特征推理层”，保持裁决可回放、可解释、可治理。

角色与职责：
1. `feature producer`：离线或准实时计算特征（不在 Decision 同步路径执行）。
2. `feature snapshot builder`：把可用特征冻结为 snapshot 并签名。
3. `Decision Kernel`：只消费冻结特征，不做 inline 特征计算。

强制字段（每个特征）：
1. `feature_snapshot_id`。
2. `feature_version`。
3. `freshness_ts`。
4. `producer_id`。
5. `feature_ttl_ms`。
6. `evidence_ref`（可回放证据引用）。

强约束：
1. Decision 同步路径禁止 inline 计算特征。
2. 未带 `feature_snapshot_id` 的特征不得参与 high/critical 决策。
3. 特征必须可通过 Evidence 回放还原（版本 + 值 + 新鲜度）。

stale 策略（按风险）：
1. low：允许使用 stale 特征，但必须写 `feature_stale_used_event`。
2. medium：允许有限 stale 窗口，超窗进入 `review_required`。
3. high：stale 触发 `review_required`。
4. critical：stale 触发 `fail_closed`。

SLA：
1. `feature_snapshot_ready_latency_p95` 必须在预算内。
2. `feature_stale_rate` 超阈值触发发布门禁与容量治理。

### 7.2.8 Feature Governance & Drift（一级故障域）

目标：
1. 把“坏特征/漂移特征/语义变更特征”作为独立故障域治理。

治理对象：
1. `feature producer owner`：每个特征必须有责任团队与值班人。
2. `feature schema`：字段变更必须声明兼容级别。
3. `feature distribution`：按窗口监控统计漂移。
4. `feature semantics`：语义变更必须伴随版本升级与变更记录。

版本语义：
1. major：语义变更或不兼容字段变更。
2. minor：向后兼容新增字段或阈值微调。
3. patch：实现修复且语义不变。

漂移与阈值：
1. `feature_distribution_drift_score` 超观察阈值进入 watch。
2. 超告警阈值触发 `feature_drift_alert`。
3. 超阻断阈值：high/critical 路径进入 `review_required` 或 `fail_closed`。

回滚与保留：
1. 特征版本必须支持 `feature_rollback` 到上一稳定版本。
2. `feature_snapshot_retention` 不得短于 replay 支持窗口。
3. snapshot 到期策略必须保证高风险取证窗口内可回放。

故障动作：
1. `bad_feature_detected`：low/medium -> review_required；high -> review_required + hold；critical -> fail_closed。
2. `semantic_change_without_version_bump`：立即阻断发布并触发 incident。

#### 7.2.8.1 Feature Dependency Graph（依赖图）

目标：
1. 支撑漂移归因、定点回滚和 replay 解释。

最小字段：
1. `feature_id`。
2. `upstream_feature_ids`。
3. `producer_version`。
4. `derivation_type`。
5. `critical_path_flag`。

强约束：
1. critical path feature 必须登记依赖图。
2. 未登记依赖图的 feature 不得用于 high/critical 决策。
3. feature rollback 必须支持“按依赖子图”回滚，不允许只做全量盲回滚。

### 7.2.9 Decision 实现不变量（代码级）

1. 纯函数风格：`Decision = f(frozen_input)`。
2. 输入全冻结：仅允许消费 freeze whitelist + 本地只读缓存。
3. 输出全结构化：固定 schema，不允许隐式 side-channel 输出。
4. 无隐式 I/O：禁止在决策函数内部进行未声明存取。
5. 无外部同步 fan-out：禁止同步调用 RAG/Memory/外部特征/外部策略系统。
6. 任何新增决策字段必须同时更新：契约 schema、replay 校验、门禁规则。

### 7.3 Context Resolver Engine

#### 7.3.1 解决问题

1. Context/Mem/RAG/Citation 职责重叠。
2. 候选爆发导致 compile 崩溃。
3. 上下文选择不可解释。

#### 7.3.2 方案

1. 预处理：`Candidate Budget Gate`。
2. 编译：`Context Compiler`。
3. 注入：`Skill Boundary Evaluator` + `Injection Assembler`。

#### 7.3.3 关键规则

1. 候选来源必须标注 `source_type`、`trust_level`、`freshness`、`cost`。
2. Compiler 是唯一 keep/drop/order/trim 决策者。
3. Skill 仅可修改允许字段，不能改权限与审批字段。
4. 注入结果必须附 `selection_rationale`。
5. Context 输出 schema 禁止包含 `allow/deny/risk/approval/effect` 决策字段。

#### 7.3.4 Candidate Budget Gate

输入：
1. retrieval candidates。
2. memory candidates。
3. citation anchors。
4. tenant budget。

处理：
1. 源级上限。
2. 类型级上限。
3. 风险级别上限。
4. token 预估门。

输出：
1. `candidate_set_filtered`。
2. `drop_reason_by_source`。

#### 7.3.5 Context Compiler

输入：
1. 过滤后候选。
2. task intent。
3. risk tier。
4. token budget。

处理步骤：
1. 去重。
2. 基础相关性排序。
3. 可信度与新鲜度重权。
4. 冲突解算。
5. token 裁剪。
6. 引用锚点补齐。

输出：
1. `compiled_context`。
2. `compiler_trace`。
3. `conflict_resolution_record`。

#### 7.3.6 Skill Boundary Evaluator

输入：
1. 原始 prompt/context。
2. skill patch。
3. policy constraints。

规则：
1. 不允许改 `required_permissions`。
2. 不允许改 `effect_type`。
3. 不允许改 `approval_policy_ref`。
4. 允许改风格、格式、提取模板、上下文组织。

输出：
1. `patch_applied` 或 `patch_rejected`。
2. 拒绝理由与字段级 diff。

#### 7.3.7 Context Resolver 预算

1. 候选数上限默认：`1000`。
2. compile CPU 上限：`1 vCPU-second`。
3. compile memory 上限：`512MB`。
4. compile P95（候选 <= 300）：`<= 220ms`。
5. compile P95（候选 <= 1000）：`<= 550ms`。

超预算退化策略：
1. 进入 `minimal_context_profile`。
2. 丢弃低可信、低新鲜、低相关候选。
3. 高风险场景若证据不足，返回 `review_required`。

### 7.4 Policy Engine（执行级）

#### 7.4.1 解决问题

1. 策略散落代码、不可回放。
2. 冲突策略不统一。
3. obligation 执行顺序不清。

#### 7.4.2 方案

1. Policy DSL -> Policy IR -> 执行引擎。
2. phase-aware evaluation：`PRE_CONTEXT`、`PRE_TOOL`、`PRE_RESUME`、`PRE_RELEASE`。
3. obligation 采用确定顺序执行。

#### 7.4.3 obligation 顺序

同 phase 内顺序：
1. `limit_param`。
2. `attach_tag`。
3. `require_template`。
4. `emit_audit`。

跨 phase 冲突规则：
1. `PRE_TOOL` 决议优先于 `PRE_RESUME` 同字段放宽。
2. 审批返回 obligation 只能收紧不能放宽 deny。
3. fallback 不得覆盖 policy deny。

#### 7.4.4 fail 行为矩阵

1. 高风险 deny 规则缺失：`fail_closed`。
2. 低风险标签规则缺失：`fail_soft + emit_audit`。
3. 引擎不可用：
- critical/high：`fail_closed`。
- medium/low：`review_required`。

#### 7.4.5 policy bundle 生命周期

1. `draft`。
2. `signed`。
3. `canary`。
4. `active`。
5. `deprecated`。
6. `retired`。

每次发布必须包含：
1. bundle id。
2. signature。
3. schema version。
4. compatibility statement。

### 7.5 Approval Domain Engine

#### 7.5.1 解决问题

1. 审批流程复杂，单节点模型不够。
2. 审批与 run 恢复绑定容易出错。

#### 7.5.2 方案

1. 审批实体：`approval_case`。
2. 审批模式：`single`、`any_of`、`all_of`、`two_man_rule`。
3. 支持代理审批、升级审批、会签/或签。

#### 7.5.3 审批路由

输入：
1. tenant policy。
2. action risk。
3. resource scope。
4. compliance tags。

输出：
1. approver list。
2. mode。
3. sla。
4. escalation chain。

#### 7.5.4 审批证据包

必须字段：
1. request summary。
2. context hash。
3. model output summary。
4. risk assessment。
5. policy decision snapshot。
6. expected side effects。
7. rollback/compensation plan。

#### 7.5.5 审批恢复绑定

规则：
1. 审批通过只释放特定 continuation。
2. approval token 必须绑定 run_id + step_id + decision_node。
3. 审批超时必须触发升级或关闭。
4. 审批案例必须有 `approval_max_lifetime`，到期不得保持无限 `awaiting_approval` 状态。

#### 7.5.6 Approval Escalation Hard Deadline（强终止）

目的：
1. 审批链路必须有“终止机制”，防止系统级死锁。

规则：
1. 每个审批案例必须配置 `approval_hard_timeout`。
2. 到达 hard timeout 后不得继续 `awaiting_approval`。

超时后动作（按风险）：
1. low：`auto_deny`。
2. medium：`force_review_queue`。
3. high/critical：`fail_closed + alert`。

审计要求：
1. 写 `approval_hard_timeout_event`。
2. 记录升级链执行结果与最终裁决依据。

#### 7.5.7 审批路由漂移治理（组织侧强约束）

问题：
1. 审批系统可用不等于审批流程可用，组织结构漂移会导致“业务慢死锁”。

规则：
1. 每次审批路由决策必须记录 `approver_group_snapshot_id`。
2. 每日执行 `approval_route_replay`，检测组织结构变化后的路由偏差。
3. 发现 `approver_group_drift` 超阈值必须冻结高风险审批路由变更。

override 治理：
1. `approval override` 必须写 `override_reason_code` 与二次复核责任人。
2. 高频 override 触发 `override_anomaly_event`，进入治理审查。

#### 7.5.8 Approval Effective Latency（业务可用性 SLA）

问题：
1. 审批系统技术可用，不代表业务动作可及时解锁。

指标定义：
1. `approval_effective_latency = decision_request_ts -> business_action_unblocked_ts`。
2. 必须按 risk tier、租户、审批模式（single/any_of/all_of/two_man_rule）分桶监控。

SLA 规则：
1. low/medium：超 SLA 可触发受控 fallback（按策略 auto_deny 或 force_review_queue）。
2. high：超 SLA 必须升级路由并触发值班告警。
3. critical：超 SLA 默认 `fail_closed`，并要求平台 owner 介入。

治理动作：
1. 周期生成 `approval_bottleneck_report`（高延迟审批链路画像）。
2. 对持续超 SLA 的审批链自动给出路由优化建议（候选 approver group / 升级链调整）。

#### 7.5.9 Approval Organizational Health（组织可用性）

目标：
1. 把“审批组织是否真正可运行”纳入一级治理对象。

核心指标：
1. `active_approver_ratio`。
2. `delegate_freshness`。
3. `override_dependence_rate`。
4. `stale_approver_group_ratio`。
5. `route_to_no_action_cases`。

治理规则：
1. `active_approver_ratio` 低于阈值时，冻结高风险流程新路由发布。
2. delegate 过期率超阈值时，强制触发授权刷新任务。
3. `override_dependence_rate` 连续高位时，要求审批策略专项复盘。
4. `route_to_no_action_cases` 超阈值时，触发审批链重建。

#### 7.5.10 Override Authorization Model（权限与证据硬约束）

目标：
1. 防止 override 从“应急能力”退化成“常规捷径”。

必填字段：
1. `override_scope`。
2. `override_actor_type`。
3. `override_dual_control_required`。
4. `override_expiry`。
5. `override_followup_review_required`。
6. `override_reason_code`。
7. `override_evidence_ref`。

强约束：
1. high/critical 风险 override 默认双签（dual control）。
2. override 必须有过期时间，过期自动失效。
3. `override_followup_review_required=true` 的案例未复核不得关闭。
4. `override_dependence_rate` 超阈值触发流程治理，禁止继续扩容 override 权限。

runbook（P1/P0）：
1. 立即冻结异常 scope 的 override。
2. 导出最近窗口 override 证据包并复核 actor 合法性。
3. 按风险回滚到审批主流程并补充组织修复任务。

### 7.6 Scheduler Admission Engine

#### 7.6.1 解决问题

1. 调度可外包，但谁能调度必须平台判定。

#### 7.6.2 方案

1. admission 输入：任务属性、租户配额、优先级、资源约束。
2. 输出：`admit/reject/preemptable/queue_class`。
3. admit 才能签发 `dispatch_ticket`。

#### 7.6.3 dispatch_ticket 字段

1. `ticket_id`。
2. `run_id`。
3. `step_id`。
4. `tenant_id`。
5. `allowed_resources`。
6. `priority_class`。
7. `expires_at`。
8. `decision_hash`。

#### 7.6.4 execution_receipt 校验

1. receipt 必须对应有效 ticket。
2. receipt 报告资源使用不得越过 ticket 上限。
3. 过期 ticket 的 receipt 不得接收。

#### 7.6.5 VIP 与公平

1. VIP 池可独占保底资源。
2. 非 VIP 使用加权公平队列。
3. 突发抢占仅允许 preemptable 任务。
4. critical/high 风险任务不允许无证据抢占。

### 7.7 Quota & Fairness Engine

#### 7.7.1 解决问题

1. 多租户竞争导致噪声租户影响全局。

#### 7.7.2 方案

1. 配额维度：tokens、runs、concurrency、GPU minutes、egress。
2. 限流动作：soft throttle、hard throttle、queue downgrade。
3. 可观察：每次限流产生 decision event。

#### 7.7.3 关键指标

1. `tenant_throttle_rate`。
2. `vip_wait_p95_ms`。
3. `fairness_index`。

### 7.8 Release Decision Engine

#### 7.8.1 解决问题

1. Eval 不应成为唯一真值。
2. 发布决策需证据融合。

#### 7.8.2 证据输入

1. eval summary。
2. policy regression summary。
3. replay consistency summary。
4. incident trend。
5. human signoff。

#### 7.8.3 输出字段

1. `final_decision`。
2. `decision_confidence`。
3. `evidence_completeness`。
4. `blocking_reasons`。

#### 7.8.4 fast path 条件

1. risk tier 必须 low。
2. 变更范围限制在白名单。
3. 必须承诺后置 full eval 时限。

违反 fast path 条件：
1. 自动降级到 full gate。

### 7.9 Obligation Orchestrator

#### 7.9.1 解决问题

1. 多策略 obligation 执行顺序冲突。
2. obligation 与 workflow fallback 冲突。

#### 7.9.2 方案

1. obligation 执行按 phase 划分。
2. 同 phase 冲突按 priority + strictness。
3. deny 类 obligation 不可被 fallback 覆盖。

#### 7.9.3 合并算法（简化）

1. 收集 obligation 集合。
2. 按 `(phase, priority, strictness)` 排序。
3. 字段级冲突检查。
4. 生成可执行 obligation plan。
5. 写入冲突解释。

### 7.10 Decision API（OpenAPI 摘要）

1. `POST /v1/context/resolve`。
2. `POST /v1/decision/evaluate-runtime`。
3. `POST /v1/decision/evaluate-schedule-admission`。
4. `POST /v1/decision/evaluate-release`。
5. `POST /v1/approval/cases`。
6. `POST /v1/approval/cases/{case_id}/decision`。
7. `GET /v1/decision/{decision_id}`。

### 7.11 Decision 失败矩阵

1. Context compile timeout：
- low/medium -> `minimal_context_profile`。
- high/critical -> `review_required`。

2. Policy engine unavailable：
- low -> `review_required`。
- high/critical -> `deny`。

3. Approval router unavailable：
- 有审批要求 -> `deny + require_manual_review`。

4. Scheduler adapter unavailable：
- admission 可判但 execution pending。

5. Release evidence 缺失：
- high/critical -> `block`。

6. Feature snapshot stale 或缺失：
- low/medium -> `review_required`（可按策略允许短窗 stale）。
- high -> `review_required`。
- critical -> `fail_closed`。

7. Context decision boundary violation：
- low/medium -> `review_required + block_profile_publish`。
- high/critical -> `fail_closed + incident_alert`。

8. Feature drift/semantic mismatch：
- low/medium -> `review_required + feature_watch`。
- high -> `review_required + hold_feature_version`。
- critical -> `fail_closed + feature_rollback_candidate`。

### 7.11.1 审批系统不可用退化策略（防死锁）

当 `Approval Domain Engine` 或通知链路不可用时：
1. low：`auto_deny`。
2. medium：`review_required`。
3. high/critical：`fail_closed + alert`。

强约束：
1. 禁止在审批不可用时返回 `awaiting_approval` 并无限等待。
2. 所有不可用退化动作必须附 `approval_unavailable_decision_reason`。

### 7.11.2 Decision 软失败循环保护

`decision_retry_limit_per_step` 默认值：`3`。

循环检测条件：
1. 同一步骤连续命中 `review_required` 或同类软失败。
2. 连续重试后无新证据输入。

超过上限后的动作：
1. low/medium：`FAILED` 或 `FORCE_REVIEW_REQUIRED`（按策略）。
2. high/critical：`FORCE_REVIEW_REQUIRED`，禁止无限重试。

审计要求：
1. 写入 `decision_retry_exhausted_event`。
2. 记录 `first_failure_reason` 与 `last_failure_reason`。

### 7.12 Decision 资源预算

1. runtime decision P95：`<= 120ms`。
2. schedule admission P95：`<= 25ms`。
3. approval route P95：`<= 40ms`。
4. release decision P95：`<= 250ms`（不含离线 eval 计算）。

### 7.13 Decision 扩展机制

扩展点 `Decision Dimension Pack`：
1. `dimension_id`。
2. `phase`。
3. `input_schema`。
4. `output_schema`。
5. `conflict_priority`。
6. `fail_behavior`。
7. `explain_renderer`。

扩展点 `Execution Adapter Contract`：
1. `adapter_id`。
2. `ticket_schema_version`。
3. `receipt_schema_version`。
4. `health_signal_schema_version`。
5. `compatibility_level`。

扩展必须经过：
1. 语义兼容检查。
2. replay 回归。
3. canary。
4. gate。

### 7.14 Decision 设计证明（为什么成立）

问题：
1. 最终事故往往不是“执行失败”，而是“错误被允许执行”。

方案：
1. 把所有允许/拒绝动作统一收敛到 Decision Kernel。

优点：
1. 决策链可解释。
2. 规则冲突可治理。

缺点：
1. Decision 成为高价值组件，需要更高可靠性保障。

依据：
1. 企业生产事故复盘普遍显示，裁决分散是高频根因。

---

## 8. Evidence Kernel 详细设计（实现级）

### 8.1 内部模块清单

1. `Evidence API Gateway`。
2. `Evidence Ingestor`。
3. `Canonicalizer`。
4. `Integrity Guard`。
5. `Decision Graph Builder`。
6. `Replay Pack Builder`。
7. `Root Cause Pack Builder`。
8. `Ledger Aggregator`。
9. `Evidence Retention Manager`。
10. `Evidence Query Engine`。

### 8.1.1 Evidence Kernel 的实施现实（高投入但必要）

1. Evidence Kernel 是正确方向，但不是“低成本模块”。
2. 其难点来自数据治理、schema 演进、隐私合规、回放一致性。
3. 因此必须采用分阶段能力交付，而不是一次性全量落地。

### 8.1.2 Evidence MVP 分层（E0/E1/E2）

E0（Phase 1，必须）：
1. `Evidence Ingestor`。
2. `Canonicalizer`。
3. `decision_logs` 最小集。
4. `decision_graph` 最小关键路径（仅高风险必填）。
5. `usage_ledger` 最小可对账字段。

E1（Phase 2，企业化）：
1. `Replay Pack Builder`。
2. `Root Cause Pack Builder`。
3. retention 分层策略（hot/warm/cold）。
4. DSAR 基础删除传播。

E2（Phase 3，增强）：
1. 全量 `Integrity Guard`（哈希链与锚点增强）。
2. 高级查询 projection。
3. 跨域证据融合优化与治理自动化。

### 8.1.2A Evidence Value Tier（业务价值分层）

目的：
1. 将“技术分层”扩展为“业务价值分层”，避免 Evidence 成本黑洞。

Tier 定义：
1. Tier 0（critical audit）：必须同步落盘，不可丢，服务合规与关键争议取证。
2. Tier 1（debug essential）：可异步但必须最终可用，服务排障与回放核心路径。
3. Tier 2（analytics）：可延迟，可在背压时降级采样。
4. Tier 3（optional）：可丢弃或强采样，仅用于探索性分析。

写入策略：
1. Tier 0 永不降级到“可丢”。
2. Tier 1 在高压时允许延迟，但必须保留引用链。
3. Tier 2/3 在 backpressure 下按策略采样或丢弃。

成本护栏：
1. 设置 `evidence_cost_budget`（tenant/project/workflow 维度）。
2. 超预算时优先压缩 Tier 2/3，不影响 Tier 0。
3. 每次压缩或丢弃必须产出 `evidence_tier_drop_event`。

### 8.1.3 Evidence 降级策略（高压场景）

1. 常规路径优先写最小审计集，扩展证据异步补齐。
2. 当 ingest 压力过高时，低风险 run 降级为摘要证据。
3. 高风险 run 禁止降级关键证据字段。
4. 降级行为必须写 `evidence_degrade_event` 并可追溯。

`evidence_ingest_backpressure_levels`：
1. level1：丢弃 low-risk extended evidence，仅保留最小审计集。
2. level2：全租户仅保留 minimal audit + 关键决策图边，暂停扩展 pack 生成。
3. level3：暂停 non-critical runs 的新执行推进，优先保障 high/critical 与恢复链路。

升级条件（任一触发）：
1. ingest backlog 超过阈值窗口。
2. outbox 消费延迟超过阈值窗口。
3. 对象存储写入错误率超过阈值。

降级恢复条件：
1. backlog 回落到安全水位并持续稳定。
2. 错误率回落并通过完整性抽检。

### 8.1.4 Evidence Sampling Policy + Write Budget（强约束）

采样策略：
1. `sampling = f(risk_tier, tenant_profile, event_type, system_load)`。
2. 低风险 + 高负载：允许高比例采样或丢弃 Tier 2/3 事件。
3. critical/high 风险：关键审计事件必须 100% 保留。

每 run 写入预算：
1. `max_evidence_bytes_per_run`。
2. `max_events_per_run`。

超限行为：
1. `degrade`：降级为摘要事件。
2. `summarize`：聚合高频同类事件。
3. `stop_ingest_non_critical`：停止非关键证据入库。

强约束：
1. Tier 0 关键审计字段不受普通采样策略影响。
2. 预算超限动作必须写 `evidence_write_budget_exceeded_event`。

### 8.1.5 Evidence Governance Matrix（价值/合规/成本一体化）

目标：
1. 统一回答“保留什么、保留多久、能否采样、谁可改动”。

治理矩阵（执行规则）：
1. Tier 0（法务/合规必需）：100% 保留，不可采样，最长保留策略由合规域控制。
2. Tier 1（排障必需）：默认全量保留，可在高压场景降级为摘要。
3. Tier 2（分析优化）：允许按租户与负载采样。
4. Tier 3（可选扩展）：默认可采样或丢弃。

默认租户模板：
1. `regulatory_strict`：Tier 0/1 高保留，Tier 2 低采样。
2. `balanced_enterprise`：Tier 1 全量，Tier 2 按负载采样。
3. `cost_optimized`：Tier 2/3 高采样，Tier 0/1 不降级。

override 约束：
1. workflow 级 override 不得降低 Tier 0 保留要求。
2. 降低 Tier 1 保留窗口需合规审批并留证据。
3. override 必须写 `evidence_policy_override_event`。

### 8.1.6 Evidence 三路径分离（实现硬约束）

1. `minimum_audit_path`：高风险关键证据同步最小落盘。
2. `extended_debug_path`：扩展排障证据异步补齐。
3. `analysis_export_path`：分析导出路径独立队列与独立预算。

强约束：
1. 任何时候都优先保障 `minimum_audit_path`。
2. `extended_debug_path` 与 `analysis_export_path` 受背压时可降级，不得反压主执行链路。
3. 禁止把三路径混成“单表 + 重查询”实现。

### 8.2 Evidence Ingestor

解决问题：
1. 多系统日志格式不一致，难统一。

方案：
1. 接收 Run/Decision/Adapter 事件。
2. 强制转换成 canonical event schema。

优点：
1. 统一语义。

缺点：
1. schema 演进需要版本管理。

验证：
1. canonical conversion success rate。

### 8.3 Canonicalizer

解决问题：
1. 同类型事件字段不统一。

方案：
1. 统一字段：`run_id`、`tenant_id`、`event_type`、`event_ts`、`source`、`payload_hash`。
2. 所有字段有 schema version。

优点：
1. 查询与回放稳定。

缺点：
1. 早期接入成本更高。

验证：
1. schema compliance rate。

### 8.4 Integrity Guard

解决问题：
1. 审计证据防篡改。

方案：
1. 事件哈希链。
2. 批次签名。
3. WORM 存储锚点（可托管）。

优点：
1. 审计可信。

缺点：
1. 写入链路增加开销。

验证：
1. hash chain verify pass rate。

### 8.5 Decision Graph Builder

解决问题：
1. 有日志但无法解释最终决策。

方案：
1. 节点类型：context/policy/approval/schedule/release/tool。
2. 边类型：depends_on/influenced_by/overrides/blocks。
3. 必须能追溯最终决策的因果路径。

优点：
1. 可解释性提升。

缺点：
1. 节点边定义需要长期维护。

验证：
1. high risk graph completeness。

### 8.6 Replay Pack Builder

解决问题：
1. 故障无法稳定复现。

方案：
1. 组装 replay manifest：snapshot refs、input refs、decision refs、adapter receipts。
2. 标注可重放与不可重放字段。

优点：
1. 支持回归验证。

缺点：
1. 数据引用关系复杂。

验证：
1. replay success rate。

### 8.7 Root Cause Pack Builder

解决问题：
1. 调试跨系统回溯成本高。

方案：
1. 一键导出 root-cause 包。
2. 包含 first_bad_node、critical path、key evidences、timeline。

优点：
1. 缩短定位时间。

缺点：
1. 需维护导出模板。

验证：
1. MTTI。

### 8.7.1 Root Cause Pack 分级导出（最小/完整）

`minimal_root_cause_pack`：
1. 目标：P0/P1 下快速导出关键因果路径。
2. 内容：first_bad_node、critical path、关键 decision/hash、最近失败事件。
3. 时延目标：`<=2s`（P95）。

`full_root_cause_pack`：
1. 目标：完整复盘与法务审计。
2. 内容：完整 decision graph、timeline、replay manifest、关联 evidence refs。
3. 时延目标：`<=30s`（P95）。

强约束：
1. high/critical 事故至少必须先导出 minimal 包。
2. full 包可异步补齐，但必须绑定同一 incident id。

### 8.8 Ledger Aggregator

解决问题：
1. token/cost 争议与对账困难。

方案：
1. 汇总模型、检索、调度、工具资源账本。
2. 账本维度：tenant/project/workflow/run/step。

优点：
1. 可运营、可计费、可治理。

缺点：
1. 账本字段需稳定管理。

验证：
1. ledger reconciliation mismatch rate。

### 8.9 Evidence 数据模型（SQL 草案）

```sql
CREATE TABLE decision_logs (
  decision_id            TEXT PRIMARY KEY,
  run_id                 TEXT NOT NULL,
  decision_type          TEXT NOT NULL,
  decision_value         TEXT NOT NULL,
  decision_confidence    DOUBLE PRECISION,
  evidence_completeness  DOUBLE PRECISION,
  rationale_ref          TEXT,
  created_at             TIMESTAMPTZ NOT NULL
);

CREATE TABLE decision_graph_nodes (
  node_id                TEXT PRIMARY KEY,
  run_id                 TEXT NOT NULL,
  node_type              TEXT NOT NULL,
  node_ref               TEXT NOT NULL,
  created_at             TIMESTAMPTZ NOT NULL
);

CREATE TABLE decision_graph_edges (
  edge_id                TEXT PRIMARY KEY,
  run_id                 TEXT NOT NULL,
  from_node_id           TEXT NOT NULL,
  to_node_id             TEXT NOT NULL,
  edge_type              TEXT NOT NULL,
  created_at             TIMESTAMPTZ NOT NULL
);

CREATE TABLE usage_ledger (
  ledger_id              TEXT PRIMARY KEY,
  run_id                 TEXT NOT NULL,
  tenant_id              TEXT NOT NULL,
  resource_type          TEXT NOT NULL,
  usage_amount           DOUBLE PRECISION NOT NULL,
  unit                   TEXT NOT NULL,
  cost_amount            DOUBLE PRECISION,
  created_at             TIMESTAMPTZ NOT NULL
);
```

### 8.10 Evidence API（OpenAPI 摘要）

1. `GET /v1/evidence/runs/{run_id}`。
2. `GET /v1/runs/{run_id}/decision-graph`。
3. `GET /v1/runs/{run_id}/root-cause-pack`。
4. `POST /v1/runs/{run_id}/replay`。
5. `GET /v1/ledger/runs/{run_id}`。

### 8.11 数据保留与归档

1. hot：7d。
2. warm：30d。
3. cold：archive。
4. critical evidence：按合规策略延长。

归档要求：
1. 保留 hash root。
2. 保留 schema version。
3. 保留 snapshot refs。

### 8.12 DSAR 删除传播策略

1. 明文可删。
2. 审计哈希保留用于完整性证明。
3. replay 在删除后必须进入脱敏模式。
4. eval 历史样本若含 PII，必须重标记与替换。

### 8.12.1 Deletion Consistency Window（删除一致性窗口治理）

目标：
1. 管理 DSAR 在 Evidence/Analysis/Replays 之间的传播时间窗口风险。

窗口规则：
1. `dsar_propagation_max_lag`：定义最大传播延迟窗口。
2. 超过窗口必须触发 `dsar_propagation_lag_alert`。
3. 未完成传播期间，受限 replay/export API 必须自动拒绝或返回脱敏替代结果。

API 行为约束：
1. `root-cause-pack` 遇到已删对象时返回占位符，不返回可逆引用。
2. `replay/export` 对未收敛删除请求返回 `DSAR_PENDING_PROPAGATION`。

### 8.13 Evidence 资源预算

1. 证据写入异步，不阻塞常规路径。
2. 高风险阻断证据必须同步落最小集。
3. 查询 P95：
- run evidence summary <= 120ms。
- decision graph <= 180ms。
- root cause pack generate <= 2s。

### 8.14 Evidence 扩展机制

扩展点 `Evidence Type Registry`：
1. `evidence_type_id`。
2. `schema_ref`。
3. `retention_policy`。
4. `integrity_policy`。
5. `pii_classification`。
6. `query_projection`。

扩展验证：
1. schema lint。
2. integrity verify。
3. replay compatibility。

### 8.15 Evidence 设计证明（为什么成立）

问题：
1. “能看日志”不等于“能解释决策”。

方案：
1. 把证据抽象为内核真值，而不是日志旁路。

优点：
1. 支撑审计、回放、发布门禁、计费争议。

缺点：
1. 需要专门数据治理。

依据：
1. 企业合规与高风险系统都要求证据链可验证。

---

## 9. Kernel 间交换契约（强约束）

### 9.1 Run -> Decision

请求：
1. `run_id`。
2. `step_id`。
3. `snapshot_hash`。
4. `risk_tier`。
5. `effect_type`。
6. `input_ref`。
7. `frozen_input_ref`。
8. `frozen_input_hash`。
9. `feature_snapshot_id`。
10. `feature_snapshot_freshness_ts`。
11. `context_policy_space_hash`。
12. `context_candidates_snapshot_ref`。
13. `policy_bundle_snapshot_ref`。
14. `approval_routing_snapshot_ref`。
15. `quota_snapshot_ref`。
16. `scheduler_admission_input_snapshot_ref`。

响应：
1. `decision_id`。
2. `decision`。
3. `obligations`。
4. `decision_node_id`。
5. `decision_hash`。

### 9.2 Decision -> Run

规则：
1. Run 仅接受带 `decision_id` 的推进请求。
2. obligation 若与当前 phase 不匹配，Run 必须拒绝执行。
3. Run 推进后必须更新 `step_hash`，并维护 `run_integrity_root`。

### 9.3 Run/Decision -> Evidence

事件最小字段：
1. `event_id`。
2. `event_type`。
3. `run_id`。
4. `source_component`。
5. `event_ts`。
6. `payload_ref`。
7. `payload_hash`。

### 9.4 版本兼容规则

1. `major`：不兼容，需迁移。
2. `minor`：向后兼容新增字段。
3. `patch`：实现修复不改语义。

### 9.5 禁止事项

1. 禁止跨 Kernel 直连数据库。
2. 禁止外部 adapter 回写决策字段。
3. 禁止未注册 schema 事件入证据域。

### 9.6 分布式双确认语义（Decision/Run）

为避免“Decision 已 allow 但 Run 未推进”：
1. `Decision evaluate` 先写入 `pending_decision`。
2. `Run advance` 成功后回写 `decision_confirmed`。
3. 仅 `decision_confirmed` 允许进入下一步副作用执行。

失败处理：
1. Decision 成功但 Run 失败：保持 `pending_decision`，触发重试或人工介入，不允许直接执行副作用。
2. Run 成功但 Decision 未确认：Run 进入 `safeguard_hold`，等待 Decision 对账确认。

### 9.6.1 pending_decision 修复协议（必须实现）

目标：
1. 防止 `pending_decision` 长期堆积，演化为隐藏卡死。

治理字段：
1. `pending_decision_ttl`。
2. `pending_decision_repair_worker_id`。
3. `pending_decision_repair_attempts`。

修复动作优先级：
1. `retry_confirm`。
2. `compare_run_state`（对账）。
3. `enter_safeguard_hold`。
4. `escalate_oncall`。

门禁与指标：
1. `pending_decision_age_p95` 必须受控。
2. 超过 TTL 未修复的 pending_decision 不得继续推进副作用。

runbook：
1. repair worker 扫描超龄 pending_decision。
2. 按优先级执行修复动作。
3. 持续失败进入值班升级与人工对账。

### 9.7 receipt 超时统一协议

`receipt_timeout_policy`：
1. T1：超时后主动 `probe adapter`。
2. T2：probe 失败则进入 `reconcile`。
3. T3：reconcile 超时触发 `escalate`（值班 + 审计）。

强约束：
1. 不允许无限等待 receipt。
2. receipt 超时必须产出标准事件：`receipt_timeout_event`。
3. 高风险任务 receipt 未决不得自动放行下一副作用步骤。

### 9.8 事件去重与回放边界

1. `event_id` 全局唯一（至少在租户域内全局唯一）。
2. 重放必须幂等：相同 `event_id` 再摄取不产生新副作用。
3. `run replay` 与 `evidence replay` 分离执行，避免互相污染。
4. replay 默认禁止真实外部副作用，除非显式批准的 sandbox 模式。

### 9.9 Error / Causality / Retry Taxonomy（统一字典）

目标：
1. 统一三 Kernel 的错误分类、因果边类型、可重试语义，降低 oncall 与回放歧义。

错误分类（error taxonomy）：
1. `INPUT_INVALID`（输入契约错误）。
2. `POLICY_DENY`（策略拒绝）。
3. `DEPENDENCY_UNAVAILABLE`（依赖不可用）。
4. `TIMEOUT`（超时）。
5. `STATE_CONFLICT`（状态冲突/并发冲突）。
6. `BUDGET_EXCEEDED`（预算超限）。
7. `SECURITY_VIOLATION`（安全边界违规）。

因果边分类（causality taxonomy）：
1. `depends_on`。
2. `influenced_by`。
3. `overrides`。
4. `compensates`。
5. `blocks`。

重试语义（retryability taxonomy）：
1. `RETRYABLE_IMMEDIATE`。
2. `RETRYABLE_BACKOFF`。
3. `REQUIRES_REVIEW`。
4. `NON_RETRYABLE`。

执行要求：
1. 所有关键事件必须携带 `error_class`、`causal_edge_class`、`retryability_class`。
2. 缺少统一字典字段的事件不得进入自动 replay 与自动治理。

---

## 10. 调度外包、治理内生（执行层解耦）

### 10.1 外包决策矩阵

可外包：
1. 物理调度。
2. 定时触发。
3. 扩缩容。
4. 节点探测。

不可外包：
1. 准入裁决。
2. 配额公平裁决。
3. 审批要求判断。
4. 发布门禁判断。

### 10.2 调度执行接口

`POST /internal/v1/scheduler-adapters/{adapter_id}/dispatch`
1. 输入：dispatch ticket。
2. 输出：accepted/rejected。

`POST /internal/v1/scheduler-adapters/{adapter_id}/receipt`
1. 输入：execution receipt。
2. 输出：receipt accepted/rejected。

`POST /internal/v1/scheduler-adapters/{adapter_id}/health-signal`
1. 输入：health signal。
2. 输出：signal accepted。

### 10.3 健康信号标准化

1. `node_cpu_stall`。
2. `node_nic_degraded`。
3. `gpu_ecc_error`。
4. `gpu_thermal_throttle`。
5. `disk_io_error`。
6. `kernel_panic_recent`。

每个信号必须包含：
1. `host_id`。
2. `severity`。
3. `confidence`。
4. `detected_at`。
5. `source_adapter`。

### 10.4 故障隔离策略

1. high severity + high confidence -> 立即隔离。
2. medium severity -> 降权调度并观察。
3. low severity -> 标记并持续监控。

恢复策略：
1. 通过健康回归阈值自动解除。
2. high risk 队列恢复需要二次确认策略。

### 10.5 为什么不把调度“全做内建”

问题：
1. 内建物理调度器成本高，且重复造轮子。

方案：
1. 保留 admission/治理。
2. 把执行排程交给成熟调度系统。

优点：
1. 降低实现复杂度。
2. 复用成熟生态。

缺点：
1. 需要 adapter 稳定性保障。

依据：
1. 多数平台在控制面与执行面分治后更容易扩展。

---

## 11. Context / Memory / RAG / Citation 一体化边界

### 11.1 四者职责边界

1. RAG：检索候选，不做终裁。
2. Memory：记忆候选，不做终裁。
3. Citation：引用锚点构建与充分性评估，不做终裁。
4. Context Compiler：唯一终裁。

### 11.2 为什么这样分

问题：
1. 多处排序会造成解释冲突和调优失控。

方案：
1. 把“候选生产”和“终裁”彻底分离。

优点：
1. 编译器成为唯一调优入口。

缺点：
1. 编译器压力增大，需预算和隔离。

### 11.3 Memory 状态机

状态：
1. `CANDIDATE`。
2. `ACTIVE`。
3. `SUSPECT`。
4. `REVOKED`。
5. `ARCHIVED`。

准入证据：
1. semantic memory 需多次一致证据。
2. procedural memory 需稳定成功回放证据。
3. policy memory 需官方政策来源与签名。

反证流程：
1. 命中反证 -> 进入 `SUSPECT`。
2. 二次确认失败 -> `REVOKED`。
3. 必要时写入 correction record。

### 11.4 RAG 分层（L1/L2/L3）

L1 必选：
1. 文档类型分块。
2. 混合检索。
3. rerank。
4. 去重。
5. top-k 动态裁剪。

L2 可选：
1. 查询改写。
2. JIT retrieval。
3. chunk cache。

L3 实验：
1. graph retrieval。
2. HyDE 等高级策略。
3. 自适应多路径检索。

### 11.5 Citation 充分性规则

1. 高风险输出必须有可追溯引用或显式“不足证据”声明。
2. 引用覆盖率不足时，Decision 可触发 `review_required`。
3. 引用冲突必须在 compiler trace 中记录。

### 11.6 上下文注入攻击防护

1. 非可信上下文不得提升工具权限。
2. 注入内容无法修改 policy/approval 字段。
3. 命中高危 injection pattern 时自动降级上下文并审计。

### 11.7 Context 链路失败矩阵

1. retrieval unavailable：使用 memory + cached context，标记降级。
2. memory unavailable：依赖检索与近期会话，标记降级。
3. compiler timeout：minimal context。
4. citation conflict high：review_required。

### 11.8 Context 子平台化治理（承认其复杂性）

1. Context 链路本质上是高复杂子平台，不是普通中间件。
2. 需要独立的性能治理、回归治理和容量治理制度。
3. 不把 Context 作为“可随意插策略”的实验区，所有新增策略必须过门禁。

### 11.9 Context 性能治理红线

1. compile 阶段不得调用非必要外部 RPC。
2. 候选数与 token 预算必须双阈值控制。
3. compile 队列与主交互队列隔离。
4. 任一优化策略引入后，必须通过 P95/P99 回归对比。
5. 当优化收益不稳定时，默认回退到 L1 必选路径。

### 11.10 Context 回归与调优纪律

1. 每次策略变更必须产出 `before/after` 评测报告。
2. 评测必须同时覆盖：质量、时延、成本、稳定性四维。
3. 任何导致解释性下降的策略不得上线。
4. context regression suite 必须包含冲突解算和注入防护样本。

### 11.11 Context Runtime Feedback Loop（自适应反馈环）

目标：
1. 把静态 budget 规则升级为运行时自适应策略，避免“受控但不自适应”。

实时输入信号：
1. `compile_latency_p95/p99`。
2. `citation_adequacy` 与 `hallucination_proxy`。
3. `context_cost_per_run`。
4. `tool_failure_correlated_with_context`。

动态调节动作：
1. 编译时延上升：自动降低候选上限与重排深度。
2. 幻觉代理指标上升：提高 citation 权重与可信源占比。
3. 成本上升：降低 token budget 并优先保留高价值上下文片段。
4. 质量下降：回退到上一个稳定 context policy profile。

保护边界：
1. 自适应动作仅允许在已批准策略空间内变更参数，不允许越权改策略语义。
2. critical/high 风险任务优先稳定性，禁止激进参数漂移。
3. 所有自适应动作必须写 `context_feedback_adjustment_event`。

### 11.12 Context Stickiness Mechanism（跨轮稳定性）

目标：
1. 保证多轮任务上下文选择的时间一致性，降低输出与参数漂移。

机制：
1. 维护 `previous_selected_context` 与 `sticky_candidates`。
2. 新一轮编译默认优先保留历史有效上下文片段。

移除 sticky 的条件：
1. 与新证据冲突。
2. 超过新鲜度阈值。
3. 被反证流程标记无效。
4. 明确被策略或审批约束排除。

审计字段：
1. `sticky_retained_count`。
2. `sticky_dropped_count`。
3. `sticky_drop_reasons`。

### 11.13 Context Compile Profile Canary + Auto Rollback

目标：
1. 防止 context 策略在生产中“悄悄变坏”。

发布机制：
1. 新 compile profile 必须先走 canary（低比例流量）。
2. canary 期间强制采集质量/时延/成本/稳定性四维差分。

自动回退条件（任一触发）：
1. `compile_latency_p95` 超过阈值。
2. `citation_adequacy` 下降超过阈值。
3. `context_cost_per_run` 超预算持续窗口。
4. `tool_failure_correlated_with_context` 异常上升。

回退动作：
1. 自动切回 `last_stable_compile_profile`。
2. 写 `context_profile_auto_rollback_event`。
3. 触发回归分析任务与责任人确认。

### 11.14 Context 非裁决边界（强约束）

原则：
1. Context 是“信息裁剪器”，不是“行为裁决器”。

Context 允许：
1. 候选排序。
2. 去重与裁剪。
3. 结构组织与引用锚点装配。

Context 禁止：
1. 输出 `allow/deny/require_approval` 裁决。
2. 修改 `risk_tier`。
3. 修改 `approval_required` 或审批路由字段。
4. 修改 `effect_type` 或权限字段。

越界处理：
1. 命中越界字段写入 `context_decision_boundary_violation_event`。
2. high/critical 风险直接阻断并进入 `review_required`。
3. 所有越界样本必须进入 regression suite。

接口约束：
1. Context 输出 schema 不得包含决策字段。
2. 若 Context 发现高风险证据，只能输出 `context_risk_signal`，由 Decision 统一裁决。
3. Context 产生的任何 signal 只能作为 Decision 输入特征，不得直接触发 Run 状态迁移。
4. 运行时禁止 `Context -> Run state change` 直连路径（门禁检查）。

### 11.15 Context Policy Space Contract（合法调参空间）

目标：
1. 把“已批准策略空间”从口头原则变成可冻结、可回放、可审计契约。

契约要素：
1. `policy_space_id`。
2. `policy_space_version`。
3. `policy_space_hash`。
4. 可调参数清单与上下界。
5. 参数联动约束（例如 A 升高时 B 必须在安全区间）。
6. 语义变更判定规则。

强约束：
1. 仅契约内参数允许自适应调节。
2. 参数越界必须阻断并写 `context_policy_space_violation_event`。
3. 命中“语义变更判定”必须走正式发布流程，不可当作普通调参。
4. `policy_space_hash` 必须进入 snapshot 与 evidence。

### 11.16 Context 实现不变量（代码级）

1. Context 输出对象禁止出现 run state 迁移字段。
2. Context 所有 signal 必须先进入 Decision，再由 Decision 形成 verdict。
3. `compile_profile`、`policy_space`、`selected_context` 三类对象必须可回放。
4. compile canary 与 rollback 接口必须首版即实现，禁止后补。
5. Context 团队不得通过参数空间绕过正式语义发布。

---

## 12. Workflow DSL v2（生产契约）

### 12.1 节点强制字段

1. `node_id`。
2. `node_type`。
3. `input_schema_ref`。
4. `output_schema_ref`。
5. `effect_type`。
6. `required_permissions`。
7. `approval_policy_ref`。
8. `retry_policy`。
9. `timeout_policy`。
10. `fallback_policy`。
11. `compensation_policy`。
12. `context_read_set`。
13. `context_write_set`。
14. `observability_tags`。

### 12.2 静态校验规则

1. schema 连通。
2. effect 与 policy 一致。
3. approval 引用存在。
4. compensation 可达。
5. context read/write 无越界。
6. retry 对 non-idempotent 不允许自动重试。
7. context write set 禁止写入决策命名空间（risk/approval/effect/permission）。

### 12.3 side effect 语义

1. `pure`。
2. `read`。
3. `idempotent_write`。
4. `non_idempotent_write`。
5. `irreversible`。

每类语义对应：
1. 允许重试次数。
2. 是否必须审批。
3. 是否必须 snapshot before/after。

### 12.4 trigger 与重复任务

支持类型：
1. cron。
2. fixed interval。
3. business calendar。
4. external signal。

并发策略：
1. `forbid`。
2. `allow`。
3. `replace`。

去重键：
1. `trigger_id + scheduled_time + workflow_id + tenant_id`。

### 12.5 scheduler policy in DSL

1. `priority_class`。
2. `quota_profile`。
3. `placement_constraints`。
4. `preemptable`。
5. `deadline_ms`。

### 12.6 依赖继承模型

层次：
1. global defaults。
2. tenant profile。
3. project profile。
4. workflow overrides。
5. node overrides。

规则：
1. 低层可收紧，不可放宽敏感默认。
2. 最终解析结果必须 freeze 到 snapshot。

### 12.7 依赖变更影响面（blast radius）

每次依赖改动输出：
1. 影响 workflow 数。
2. 影响 node 数。
3. 影响风险级别分布。
4. 是否触发 mandatory replay。

### 12.8 DSL 示例（节选）

```yaml
workflow_id: invoice-agent-v2
version: 2.3.1
nodes:
  - node_id: parse_request
    node_type: llm_call
    input_schema_ref: schema://request.parse.input.v1
    output_schema_ref: schema://request.parse.output.v1
    effect_type: pure
    required_permissions: []
    approval_policy_ref: approval://none
    retry_policy:
      max_attempts: 2
      backoff_ms: [200, 800]
    timeout_policy:
      hard_timeout_ms: 5000
    fallback_policy: fallback://parse.default
    compensation_policy: compensation://none
    context_read_set: [session_summary, kb_policy]
    context_write_set: [parsed_intent]
    observability_tags:
      domain: invoice
      criticality: medium
```

### 12.9 编译产物契约（运行时只读）

原则：
1. 运行时不直接解释原始 DSL，只执行编译产物。

编译产物：
1. `compiled_workflow_plan`。
2. `compiled_policy_refs`。
3. `compiled_context_contract`。
4. `compiled_effect_map`。

强约束：
1. 产物必须带 `compiled_plan_hash` 并写入 snapshot。
2. replay 必须基于相同编译产物版本执行。
3. 原始 DSL 变更但未重新编译，禁止发布。
4. 运行时读取到 raw DSL 时必须拒绝执行并告警。


## 13. 工具调用运行时（等待、休眠、资源退让）

### 13.1 工具调用分级

A 类工具（短调用）：
1. 预期 `<= 500ms`。
2. 同步等待，不 park。
3. 用于读查询、轻计算。

B 类工具（中调用）：
1. 预期 `500ms ~ 5s`。
2. 异步调用 + 可选短等待窗口。
3. 超出窗口 park run。

C 类工具（长调用/回调）：
1. 预期 `> 5s` 或外部系统回调。
2. 强制 park。
3. 通过 callback continuation 恢复。

### 13.2 模型资源策略

1. 模型推理资源不与工具等待绑定。
2. 进入 B/C 类超时区间后，释放模型会话资源。
3. 对会话状态采用可恢复上下文引用而非保持占用。

### 13.3 Worker 资源策略

1. park 后释放执行 worker。
2. resume 时从队列重新分配 worker。
3. worker 不持久化 run 私有状态。

### 13.4 工具副作用四件套

1. 幂等键：写调用必须带 `idempotency_key`。
2. before/after snapshot：高风险写必须保留。
3. unknown outcome reconcile：超时后先探测后重放。
4. side-effect audit：请求/响应 hash + 关键参数日志。

### 13.5 unknown outcome 处理协议

场景：
1. 客户端超时，但外部系统可能已成功。

处理顺序：
1. `probe` 查询外部状态。
2. 若已成功：写 `reconciled_success`。
3. 若未成功：按 effect_type 进行重试/人工介入。
4. 若无法判定：进入 `review_required`。

### 13.6 工具参数策略约束

1. 参数白名单与类型校验。
2. 敏感字段上限限制（金额、数量、权限范围）。
3. 不符合策略时拒绝执行并记审计。

### 13.7 工具审批策略

1. read：默认无需审批。
2. idempotent_write：按阈值策略审批。
3. non_idempotent_write：默认审批。
4. irreversible：强制审批 + 双确认。

### 13.8 工具重试策略

1. read：指数退避可重试。
2. idempotent_write：带幂等键可重试。
3. non_idempotent_write：默认不自动重试。
4. irreversible：禁止自动重试。

### 13.9 工具调用可观测字段

1. `tool_name`。
2. `effect_type`。
3. `idempotency_key`。
4. `attempt_index`。
5. `outcome`。
6. `latency_ms`。
7. `unknown_outcome_flag`。
8. `reconcile_status`。

### 13.10 工具治理的可验证指标

1. `duplicate_side_effect_incident`。
2. `unknown_outcome_reconciled_rate`。
3. `tool_call_p95_ms`。
4. `tool_policy_reject_rate`。
5. `irreversible_without_approval_incident`。

### 13.11 Tool Semantic Contract（外部系统语义契约层）

目的：
1. 不同外部系统一致性语义不同，统一重试/探测逻辑会失效。

每个 tool 必须声明：
1. `consistency_model`：`strong | eventual | async_confirm`。
2. `visibility_delay_ms`：结果可见延迟窗口。
3. `reconcile_strategy`：`probe_first | event_confirm | manual_reconcile`。
4. `retry_policy_by_semantic`：按语义单独定义重试行为。

运行时应用：
1. strong：可快速判定成功/失败，重试策略保守。
2. eventual：优先 probe + 延迟确认，避免过早判定失败。
3. async_confirm：必须等待外部确认事件或回执，不得直接当作失败重试。

强约束：
1. 未声明 semantic contract 的工具不得进入生产白名单。
2. reconcile 引擎必须按 contract 驱动，而非统一默认逻辑。

### 13.12 Tool 接入门禁（Contract-First）

上线前必须满足：
1. `consistency_model`、`visibility_delay_ms`、`reconcile_strategy` 完整声明。
2. contract test 通过（含 unknown outcome 场景）。
3. approval 策略映射完成（可写操作必须具备策略定义）。
4. 安全审计通过（参数约束 + 凭证作用域校验）。

阻断条件：
1. 缺任一 contract 字段。
2. contract test 不通过。
3. 审批映射缺失但含不可逆副作用。

### 13.13 Probe/Reconcile 成本预算（防自拖垮）

预算字段：
1. `probe_cost_budget_per_tool`。
2. `reconcile_attempt_budget`。

强约束：
1. unknown outcome 高发时不得无限 probe/reconcile。
2. 预算耗尽后必须进入 `review_required` 或受控降级。
3. high/critical 工具预算耗尽必须触发值班告警与策略收紧。

观测指标：
1. `probe_budget_exhausted_rate`。
2. `reconcile_budget_exhausted_rate`。

---

## 14. Eval 作为一级系统（实施级）

### 14.1 Eval 控制面模块

1. `Dataset Registry`。
2. `Benchmark Orchestrator`。
3. `Grader Hub`。
4. `Replay Runner`。
5. `Metric Pipeline`。
6. `Gatekeeper`。
7. `Eval Reporter`。

### 14.2 Eval taxonomy

1. `task_success`。
2. `tool_correctness`。
3. `groundedness`。
4. `citation_adequacy`。
5. `policy_regression`。
6. `memory_helpfulness`。
7. `memory_harm`。
8. `handoff_quality`。
9. `cost_quality_pareto`。

### 14.3 benchmark 分层

L0：合约与静态校验。
1. schema 兼容。
2. policy 语法。
3. workflow lint。

L1：离线 deterministic replay。
1. 固定输入固定快照回放。
2. 检查可确定行为一致性。

L2：离线 stochastic eval。
1. 随机采样。
2. 多次运行估计区间。

L3：影子流量评测。
1. 线上影子不影响生产。

L4：灰度上线评测。
1. 小流量真实业务验证。

L5：全量上线持续监控。
1. 漂移监控与回归触发。

### 14.4 grader 协议

grader 接口：
1. 输入：`sample_id`、`prediction`、`context_refs`、`ground_truth(optional)`。
2. 输出：`score`、`label`、`confidence`、`explanation`。
3. 失败：`ungraded` + fallback grader。

grader 类型：
1. rule-based。
2. model-based。
3. human-based。
4. hybrid。

### 14.5 auto/human 混合规则

1. 高风险样本必须包含人审比例下限。
2. 自动 grader 与人工 grader 分歧过大触发仲裁。
3. 人工仲裁结果进入 dataset 修订流程。

### 14.6 dataset 版本与演进

1. 数据集必须语义版本号。
2. 字段演进必须兼容声明。
3. 样本来源标签必须可追溯。
4. 污染或漂移可紧急冻结数据集。

### 14.7 replay 一致性协议

1. replay 必须绑定 snapshot。
2. 非确定字段须标注容忍区间。
3. 行为不一致要输出 first divergence node。

### 14.8 metric pipeline

实时流：
1. 关键风险指标。
2. gate 必需指标。

批处理：
1. 成本与长期质量趋势。
2. 复杂质量评估汇总。

### 14.9 gate 规则

1. critical/high 失败不能跳过。
2. medium/low 可走 review_required 流程。
3. fast path 仅低风险且白名单变更。

### 14.10 Eval 不是真值的制度化

1. Eval 提供证据，不直接等于发布真值。
2. 发布由 Release Decision Engine 融合证据裁决。
3. 任何“单指标直放行”都视为违规。

### 14.11 Eval 运营制度

1. grader 上线需要双人评审。
2. dataset 漂移有冻结责任人。
3. 人工分歧有仲裁角色。
4. replay mismatch 有 oncall 接管流程。

### 14.12 Eval 关键指标

1. `regression_block_rate`。
2. `false_block_rate`。
3. `sample_coverage`。
4. `grader_disagreement_rate`。
5. `replay_mismatch_rate`。
6. `cost_per_eval_suite`。

---

## 15. 多 Agent 平台能力（协议层 + 调度层）

### 15.1 默认策略

1. 单 Agent 默认。
2. 多 Agent 必须证明 ROI。
3. 多 Agent 属于可选能力，不强制首发。

### 15.1.1 多 Agent 后置强锁定（Phase 3 Only）

1. Phase 1 与 Phase 2 禁止默认启用多 Agent。
2. 仅允许在隔离租户、隔离 workflow 做小流量实验。
3. 进入生产前必须同时满足 handoff ROI 达标、merge conflict 在可控阈值内、调试与回放链路完整、值班手册与回滚手册完备。
4. 任一门槛不满足，自动回退单 Agent 路径。

### 15.1.2 Core 路径永不反向依赖（硬约束）

1. single-agent 主路径不得依赖 multi-agent registry/merge/branch timeline。
2. multi-agent 子系统故障时，系统必须可无损降级到 single-agent。
3. 若检测到 single-agent 路径引用 multi-agent 组件，发布直接阻断。

### 15.2 多 Agent 协议层

1. agent identity registry。
2. capability registry。
3. handoff contract。
4. delegation policy。
5. scoped memory contract。

### 15.3 角色模型

1. `coordinator`。
2. `specialist`。
3. `reviewer`。
4. `arbiter`。

### 15.4 handoff contract 字段

1. `handoff_id`。
2. `from_agent`。
3. `to_agent`。
4. `task_spec`。
5. `context_scope_ref`。
6. `budget_ref`。
7. `deadline`。
8. `expected_output_schema`。

### 15.5 调度层核心问题

1. 是否值得 handoff。
2. 选哪个 specialist。
3. 并行分支多少。
4. 哪些分支该提前终止。
5. 分支结果如何合并。

### 15.6 coordinator 调度策略

1. 基于任务类型与历史表现做 handoff 价值估计。
2. 预算不足时优先单 Agent 路径。
3. 高风险任务优先 reviewer 引入。

### 15.7 specialist 选择机制

1. 能力匹配分。
2. 历史质量分。
3. 成本时延分。
4. 当前负载分。

综合分最高者优先。

### 15.8 parallel fan-out 管理

1. 最大分支数按 risk 与 budget 约束。
2. 分支 token 上限与时延上限独立配置。
3. 超预算分支优先取消低收益分支。

### 15.9 branch 终止规则

1. 命中全局 deadline。
2. 质量上限已达且边际收益低。
3. 被更高优先结果覆盖。

### 15.10 merge contract

字段分三类：
1. deterministic fields（必须一致，冲突即失败）。
2. mergeable fields（按策略合并）。
3. non-deterministic annotations（保留多版本供审阅）。

冲突策略：
1. policy-sensitive 字段由 arbiter 决策。
2. action 字段冲突默认 deny。
3. 文本建议冲突可并存但需置信标签。

### 15.10.1 Action Merge Semantics（副作用语义）

规则：
1. merge 只能合并建议，不得直接形成可执行副作用动作。
2. 所有可执行 action 字段在 merge 后必须重新进入 Decision 裁决。
3. action conflict 默认 `review_required` 或 `coordinator_replan`，禁止隐式执行。
4. 若 merge 输出含多动作候选，必须显式标注 `action_conflict_set` 并进入审计。

### 15.11 多 Agent 可观测

1. `trace_id` + `run_id` + `agent_id`。
2. `handoff_latency_ms`。
3. `handoff_success_rate`。
4. `merge_conflict_rate`。
5. `branch_cancel_rate`。

### 15.12 多 Agent 启用门槛

1. 单 Agent baseline 不达标。
2. 评测证明多 Agent ROI > 阈值。
3. merge conflict 可控。

### 15.13 Multi-Agent Debugging Primitive（调试原语）

目标：
1. 解决多 Agent 状态爆炸与调试爆炸问题，支持快速定位 first bad agent/action。

核心对象：`agent_execution_timeline`
1. `run_id`、`trace_id`、`agent_id`、`agent_role`。
2. 每次 handoff 的输入摘要与输出摘要。
3. 每次 handoff 的决策依据（why this handoff）。
4. 每个 agent 的 context snapshot hash。
5. merge 输入/输出 diff 与冲突裁决记录。
6. 分支终止原因（deadline/budget/override/quality）。

调试 API：
1. `GET /v1/runs/{run_id}/multi-agent/timeline`。
2. `GET /v1/runs/{run_id}/multi-agent/merge-diff`。
3. `GET /v1/runs/{run_id}/multi-agent/first-bad-agent`。

强约束：
1. 多 Agent 生产启用必须具备 timeline 完整性。
2. 缺失关键 timeline 字段的运行不得作为上线通过依据。

### 15.14 Multi-Agent Explosion Guard（防爆保护）

目标：
1. 防止无限 handoff、分支爆炸、跨 agent 环路导致系统失控。

硬约束：
1. `max_handoff_depth`：限制单 run handoff 深度。
2. `max_total_branches_per_run`：限制总分支数。
3. `cross_agent_cycle_detection`：检测 `A -> B -> A` 等循环。

触发动作：
1. 超深度：`collapse_to_coordinator` 或 `abort`（按风险策略）。
2. 超分支预算：优先取消低收益分支。
3. 命中环路：立即中断循环路径并写审计事件。

审计字段：
1. `handoff_depth_used`。
2. `branch_count_used`。
3. `cycle_detected`。

---

## 16. Observability -> Governance -> Learning 自动闭环

### 16.1 闭环目标

1. 不只是看见问题。
2. 能自动触发治理动作。
3. 能把失败变成评测样本与知识补洞任务。

### 16.2 事件分类

1. runtime failure events。
2. policy violation events。
3. approval delay events。
4. retrieval miss events。
5. cost spike events。
6. quality regression events。
7. feature drift events。
8. integrity mismatch events。
9. approval organizational health events。
10. dsar propagation lag events。

### 16.3 自动动作矩阵

事件：retrieval miss 高频。
1. 动作：加入 knowledge gap backlog。
2. 动作：生成 synthetic eval cases。
3. 动作：触发索引优化任务。

事件：policy false deny 上升。
1. 动作：shadow evaluate 新策略。
2. 动作：触发策略误伤回放。

事件：cost 超预算。
1. 动作：路由降级。
2. 动作：限制高成本模型使用范围。

事件：tool unknown outcome 增长。
1. 动作：提高 reconcile 探测频率。
2. 动作：临时收紧重试策略。

事件：feature stale rate 上升。
1. 动作：切换到更新鲜 snapshot 源。
2. 动作：对 high/critical 决策自动收紧到 review/fail_closed。

事件：feature distribution drift 超阈值。
1. 动作：冻结相关特征版本发布。
2. 动作：触发 feature rollback 候选评估与灰度回退。

事件：context decision boundary violation。
1. 动作：阻断违规 profile 发布。
2. 动作：自动生成 regression case 并加入门禁套件。

事件：analysis plane backlog 激增。
1. 动作：限流 analysis 导出任务。
2. 动作：保护 evidence ingest 主链路优先级。

事件：approval organizational health 恶化。
1. 动作：冻结高风险审批路由变更。
2. 动作：触发 approver group 修复任务与值班升级。

事件：run integrity mismatch。
1. 动作：阻断该 run 后续副作用步骤。
2. 动作：触发 root-cause pack 强制导出与人工审查。

事件：pending_decision aging。
1. 动作：触发 repair worker 优先队列。
2. 动作：超 TTL 自动升级值班并收敛到 safeguard_hold/review。

事件：override dependence rate 上升。
1. 动作：冻结对应 scope 的 override 扩展。
2. 动作：触发审批流程适配复盘。

### 16.4 自动动作边界

1. 自动动作只能收紧，不可自动放宽高风险策略。
2. 自动动作不得直接 promote release。
3. 自动动作必须写审计证据。

### 16.5 闭环执行主体

1. 触发：Evidence/Metric pipeline。
2. 决策：Decision Kernel。
3. 执行：Run tasks 或 adapter commands。
4. 留痕：Evidence Kernel。

### 16.6 Agent-Native 一级指标（新增）

计划稳定性：
1. `plan_revision_count_per_run`。
2. `plan_churn_rate`。
3. `late_plan_flip_rate`。

工具选择正确性：
1. `tool_selection_precision`。
2. `tool_selection_regret_rate`。
3. `tool_overcall_rate`。
4. `tool_undercall_rate`。

参数推断质量：
1. `tool_param_fill_rate`。
2. `tool_param_correction_rate`。
3. `tool_param_reject_by_field`。
4. `sensitive_param_near_limit_rate`。

上下文切换抖动：
1. `context_set_jaccard_delta`。
2. `context_anchor_flip_rate`。
3. `context_source_mix_shift`。
4. `context_recovered_after_drop_rate`。

证据依赖质量：
1. `unsupported_assertion_rate`。
2. `evidence_conflict_exposure_rate`。
3. `decision_without_high_trust_evidence_rate`。
4. `stale_evidence_usage_rate`。

反思/回退/自修复：
1. `self_correction_rate`。
2. `self_correction_success_rate`。
3. `same_error_repeat_rate`。
4. `replan_after_failure_rate`。
5. `replan_success_rate`。

人工介入依赖度：
1. `review_required_rate_by_workflow`。
2. `human_rescue_rate`。
3. `approval_dependency_rate`。
4. `manual_override_after_agent_failure_rate`。
5. `human_followup_needed_rate`。

决策可解释性质量：
1. `decision_explainability_score`。
2. `first_bad_node_identifiable_rate`。
3. `missing_causal_edge_rate`。
4. `unresolved_blocking_reason_rate`。

自治度：
1. `autonomous_completion_rate`。
2. `autonomous_write_rate`。
3. `supervised_completion_rate`。
4. `blocked_by_policy_rate`。
5. `blocked_by_missing_confidence_rate`。

多轮任务收敛：
1. `steps_to_completion_p50/p95`。
2. `repeated_step_pattern_rate`。
3. `loop_escape_rate`。
4. `goal_convergence_score`。
5. `unfinished_but_nonfailed_rate`。

多 Agent 专属：
1. `handoff_regret_rate`。
2. `specialist_misroute_rate`。
3. `merge_reopen_rate`。
4. `parallel_branch_useful_ratio`。
5. `agent_disagreement_resolution_time`。

价值密度：
1. `cost_per_successful_outcome`。
2. `tokens_per_successful_outcome`。
3. `tool_calls_per_successful_outcome`。
4. `human_minutes_saved_per_run`。
5. `effective_value_density`。

优先落地 8 个（首批）：
1. `plan_revision_count_per_run`。
2. `tool_selection_regret_rate`。
3. `tool_param_correction_rate`。
4. `context_set_jaccard_delta`。
5. `unsupported_assertion_rate`。
6. `self_correction_success_rate`。
7. `autonomous_completion_rate`。
8. `steps_to_completion_p95`。

### 16.7 Metric Enforcement Matrix（门禁层级表）

执行层级定义：
1. `observe only`：记录趋势，不自动动作。
2. `alert`：触发告警与值班关注。
3. `block release`：阻断发布与配置推广。
4. `block runtime`：触发运行时保护（降级/阻断/收敛）。

| metric name | observe threshold | alert threshold | block release threshold | block runtime threshold | owner |
|---|---|---|---|---|---|
| `replay_mismatch_rate` | >0.5% | >1% | >2% | >5% | Eval Steward |
| `feature_distribution_drift_score` | >0.2 | >0.35 | >0.5 | >0.7 (high/critical) | Feature Owner |
| `feature_stale_rate` | >2% | >5% | >8% | >12% (critical path) | Feature Owner |
| `compile_latency_p95` | >baseline+10% | >baseline+20% | >baseline+30% | >baseline+50% (critical path) | Context Owner |
| `context_set_jaccard_delta` | >0.35 | >0.5 | >0.65 | >0.8 (critical) | Context Owner |
| `unsupported_assertion_rate` | >1% | >2% | >4% | >6% (high/critical) | Context + Policy |
| `tool_selection_regret_rate` | >3% | >6% | >9% | >12% | Tool Runtime Owner |
| `tool_param_correction_rate` | >5% | >8% | >12% | >18% | Tool Runtime Owner |
| `self_correction_success_rate` | <80% | <70% | <60% | <50% | Agent Runtime Owner |
| `autonomous_completion_rate` | <target-5% | <target-10% | <target-15% | <target-25% | Product + Runtime |
| `unfinished_but_nonfailed_rate` | >5% | >10% | >15% | >25% | Workflow Owner |
| `steps_to_completion_p95` | >baseline+10% | >baseline+20% | >baseline+30% | >baseline+50% | Workflow Owner |
| `pending_decision_age_p95` | >ttl*0.5 | >ttl*0.75 | >ttl | >ttl*1.5 | Run + Decision Owner |
| `approval_effective_latency_p95` | >SLA*1.1 | >SLA*1.25 | >SLA*1.5 | >SLA*2 | Approval Owner |
| `approval_override_dependence_rate` | >3% | >5% | >8% | >12% | Approval Owner |
| `active_approver_ratio` | <85% | <75% | <65% | <50% (high risk routes) | Approval Owner |
| `evidence_backlog_level` | L1 持续 | L2 持续 | L3 触发 | L3+关键链路受压 | Evidence Owner |
| `evidence_backpressure_level` | level1 持续 | level2 持续 | level3 触发 | level3+主链路受压 | Evidence Owner |
| `run_integrity_mismatch_rate` | >0 | >0.1% | >0.2% | >0.5% | Run Owner |
| `decision_dcu_exceeded_rate` | >2% | >5% | >8% | >12% (critical) | Decision Owner |

规则：
1. 任一指标进入 `block release` 必须冻结对应域发布。
2. 任一指标进入 `block runtime` 必须执行运行时保护动作并启动值班流程。
3. 所有阈值需按 tenant/risk/profile 可配置，但默认基线不得放宽 critical 路径。

---

## 17. Runtime Data Architecture（落盘与一致性）

### 17.1 逻辑数据域

1. `run_state_store`。
2. `decision_store`。
3. `audit_evidence_store`。
4. `event_log_store`。
5. `replay_eval_store`。
6. `kb_raw_store`。
7. `kb_index_store`。
8. `memory_store`。
9. `usage_ledger_store`。

### 17.2 每域作用

1. run_state：运行时真值状态。
2. decision：裁决结果与 obligation。
3. audit_evidence：审计与取证。
4. event_log：时序事件回放源。
5. replay_eval：评测与回放数据。
6. kb_raw：知识原文。
7. kb_index：检索索引。
8. memory：记忆图谱。
9. usage_ledger：成本与资源账本。

### 17.3 物理建议

1. run_state/decision：Postgres。
2. event log：Kafka/Pulsar。
3. audit/manifest：对象存储 + 元数据索引。
4. replay/eval：列式仓库。
5. kb_raw：对象存储。
6. kb_index：向量库 + 搜索引擎。

### 17.4 对象存储 vs 分布式文件（知识库）

结论：
1. 原文真值用对象存储。
2. 分布式文件仅用于缓存或特定批处理中间产物。

原因：
1. 对象存储更适合不可变版本化与低成本归档。
2. DFS 适合高吞吐临时处理，但不适合当真值主存。

### 17.5 一致性模型

1. run_state 与 decision 强一致。
2. event/evidence 最终一致（通过 outbox 保证不丢）。
3. replay/eval 可延迟一致。

### 17.6 artifact GC

1. 引用计数 + 时间策略。
2. critical evidence 不受普通 GC 影响。
3. GC 前必须确保无活跃 replay 引用。

### 17.7 cache 分层

1. hot cache：短期热点上下文。
2. warm cache：中期检索结果。
3. cold cache：归档缓存。

失效策略：
1. 版本变更触发强失效。
2. policy 变更触发相关上下文失效。

### 17.8 事务边界

1. run_state 与 outbox 同事务。
2. decision 与 obligation 同事务。
3. evidence 异步摄取但可校验完整性。

### 17.9 outbox 背压隔离（活性保护）

1. outbox 与主状态推进通道隔离队列，防止证据积压直接拖慢 run 推进。
2. 当 evidence 消费滞后时，优先保证状态推进最小事件写入。
3. 达到 backpressure level2 以上时，自动暂停非关键扩展证据生成任务。
4. 达到 backpressure level3 时，限制 non-critical 新 run 入场，保留高风险主链路活性。

### 17.10 多区域、数据驻留与 Cell 架构

核心能力：
1. `region-aware tenancy`：租户绑定区域或多区域策略。
2. `data residency`：按法规域约束数据驻留边界（例如 EU/US/APAC）。
3. `cell-based architecture`：按 cell 隔离故障域与爆炸半径。
4. `cross-region DR`：明确 RPO/RTO 目标并演练。
5. `per-region policy/key/evidence`：按区域隔离策略包、密钥材料、证据保留。

数据面规则：
1. 默认不跨区域移动敏感原始数据。
2. 跨区域复制需显式策略许可与审计。
3. 回放与评测数据按驻留策略执行脱敏与隔离。

Cell 运行规则：
1. 调度准入在 cell 内优先完成。
2. cell 异常时仅影响本 cell，禁止跨 cell 扩散故障。
3. 跨 cell 故障切换必须经过 Decision 裁决与审计记录。

### 17.11 Evidence Plane vs Analysis Plane 分离（防爆炸半径）

目标：
1. 防止 eval/replay/analytics 负载反向拖垮证据主链路。

Evidence Plane（强一致、主链路）：
1. `audit_evidence_store`。
2. `decision_graph`。
3. `minimal_replay_pack`。
4. 合规与取证必需索引。

Analysis Plane（最终一致、离线优先）：
1. `eval datasets`。
2. `analytics/optimization marts`。
3. `full replay materialization`。
4. 运营分析与实验特征衍生。

交换规则：
1. Evidence -> Analysis 通过异步导出管道。
2. Analysis 侧失败不得阻断 Evidence 摄取。
3. Analysis 查询不得直接压主 Evidence 索引。

DSAR/保留策略：
1. Evidence Plane 执行法务必需删除与保留策略真值。
2. Analysis Plane 接收删除 tombstone 并异步收敛。
3. DSAR 传播延迟必须可观测并有超时告警。

### 17.11.1 Evidence Export Data Product Contract（导出协议硬约束）

导出必填字段：
1. `export_snapshot_id`。
2. `export_schema_version`。
3. `redaction_policy_id`。
4. `evidence_tier_filter`。
5. `dsar_watermark`。

强约束：
1. 导出是受治理数据产品，不是原始复制。
2. 未携带 `dsar_watermark` 的导出请求一律拒绝。
3. Analysis 不得反向作为 Evidence 真值输入。

---

## 18. API 设计（边界、版本、解耦）

### 18.1 API 分层

1. external tenant API。
2. internal control API。
3. adapter API。
4. admin governance API。

### 18.2 external API 最小集

1. `POST /v1/runs`。
2. `GET /v1/runs/{run_id}`。
3. `GET /v1/runs/{run_id}/events`。
4. `GET /v1/runs/{run_id}/evidence`。
5. `GET /v1/usage/summary`。
6. `GET /v1/dsar/requests/{request_id}/status`。

### 18.3 internal API 最小集

1. `/v1/decision/freeze-input`。
2. `/v1/context/resolve`。
3. `/v1/decision/evaluate-runtime`。
4. `/v1/decision/evaluate-schedule-admission`。
5. `/v1/decision/evaluate-release`。
6. `/v1/evidence/ingest`。
7. `/v1/features/snapshots/resolve`（返回 `feature_snapshot_id` + metadata）。
8. `/v1/analysis/export-evidence`（Evidence -> Analysis 异步导出）。
9. `/v1/features/drift/report`。
10. `/v1/features/rollback`。
11. `/v1/features/dependency-graph`。
12. `/v1/context/policy-space/validate`。
13. `/v1/approval/org-health`。
14. `/v1/metrics/enforcement-matrix`。
15. `/v1/decision/pending-decisions/repair`。

### 18.3.1 `/v1/analysis/export-evidence` 请求契约

请求必填：
1. `export_snapshot_id`。
2. `export_schema_version`。
3. `redaction_policy_id`。
4. `evidence_tier_filter`。
5. `dsar_watermark`。

失败行为：
1. 缺任一字段 -> `422`。
2. `dsar_watermark` 过旧或未收敛 -> `DSAR_PENDING_PROPAGATION`。
3. tier 越权请求 -> `403`。

### 18.4 adapter API 约束

1. 仅允许 ticket dispatch / receipt / health signal。
2. 不允许 adapter 调用 release 决策接口。
3. 所有 adapter 调用必须签名。

### 18.5 版本兼容

1. header：`X-API-Version`。
2. 支持 `N` 与 `N-1`。
3. major 升级需迁移窗口与回放验证。

### 18.6 token 统计与对账 API

1. `GET /v1/usage/tokens?tenant_id=&period=`。
2. `GET /v1/usage/cost?tenant_id=&period=`。
3. `GET /v1/usage/reconciliation?run_id=`。

字段：
1. prompt tokens。
2. completion tokens。
3. cache hit tokens。
4. retrieval tokens。
5. tool call cost。
6. scheduler resource cost。

### 18.7 调试 API

1. `POST /v1/runs/{run_id}/simulate`。
2. `POST /v1/runs/{run_id}/replay`。
3. `GET /v1/runs/{run_id}/root-cause-pack`。
4. `POST /v1/policy/dry-run`。
5. `POST /v1/context/compile-debug`。
6. `GET /v1/runs/{run_id}/integrity-verify`。

### 18.8 API 易用性要求

1. 所有错误码必须带机器可解析原因。
2. 所有高风险拒绝必须可解释。
3. 所有异步任务必须有查询状态 API。
4. 删除传播未收敛必须返回标准错误：`DSAR_PENDING_PROPAGATION`。

### 18.9 值班应急 API（必须具备）

1. `POST /v1/runs/{run_id}/force-terminate`
- 用途：终结僵尸 run 或不可恢复 run。
- 约束：必须记录 `force_terminate_reason` 与审批/签字信息（按风险）。

2. `POST /v1/approval/cases/{case_id}/override`
- 用途：审批系统异常时执行受控人工覆盖。
- 约束：仅授权角色可用，必须携带 `override_scope`、`override_actor_type`、`override_dual_control_required`、`override_expiry`、`override_followup_review_required`，并留审计证据。

3. `POST /v1/policy/emergency-disable`
- 用途：对指定策略包执行紧急熔断（仅限临时窗口）。
- 约束：critical/high 默认不允许全局禁用 deny 规则，需双签。

4. `POST /v1/adapters/{adapter_id}/isolate`
- 用途：隔离故障 adapter/节点池。
- 约束：隔离动作必须有自动恢复策略或人工恢复步骤。

5. `POST /v1/releases/rollback`
- 用途：执行一键回滚（policy/workflow/model/adapter）。
- 约束：必须携带 `rollback_operation_id`、`scope`、`reason`，并支持 dry-run。

### 18.10 组织级控制面 API（Org Control Plane）

资源层次（四级）：
1. `organization`。
2. `workspace`。
3. `project`。
4. `environment`（`dev/staging/prod`）。

核心能力：
1. 组织级资源树与继承关系管理。
2. 组织级 RBAC + ABAC + 委派管理员。
3. 跨团队共享（受控）与只读审计角色。
4. 环境级策略与数据边界隔离。

关键 API：
1. `POST /v1/orgs`。
2. `POST /v1/orgs/{org_id}/workspaces`。
3. `POST /v1/workspaces/{workspace_id}/projects`。
4. `POST /v1/projects/{project_id}/environments`。
5. `POST /v1/orgs/{org_id}/iam/roles`。
6. `POST /v1/orgs/{org_id}/iam/bindings`。
7. `POST /v1/orgs/{org_id}/iam/delegated-admins`。
8. `GET /v1/orgs/{org_id}/audit-readonly-access`。
9. `GET /v1/orgs/{org_id}/effective-policy`。
10. `GET /v1/projects/{project_id}/effective-entitlements`。

### 18.11 身份目录与同步 API（SSO/SCIM）

核心能力：
1. OIDC 登录联邦。
2. SAML 企业单点登录。
3. SCIM 用户与组同步。
4. 组织角色映射与最小权限默认。

关键 API：
1. `POST /v1/orgs/{org_id}/identity-providers/oidc`。
2. `POST /v1/orgs/{org_id}/identity-providers/saml`。
3. `POST /v1/orgs/{org_id}/scim/config`。
4. `POST /v1/orgs/{org_id}/scim/sync`。
5. `POST /v1/orgs/{org_id}/iam/role-mappings`。

### 18.12 Connector / Tool 生态控制面 API

核心能力：
1. connector registry / marketplace。
2. MCP / connector gateway。
3. tool certification pipeline。
4. tool contract testing。
5. connector versioning + deprecation policy。
6. per-connector SLO/health/cost 可观测。

关键 API：
1. `POST /v1/connectors/registry`。
2. `POST /v1/connectors/{connector_id}/versions`。
3. `POST /v1/connectors/{connector_id}/certify`。
4. `POST /v1/connectors/{connector_id}/contract-test`。
5. `POST /v1/connectors/{connector_id}/deprecate`。
6. `GET /v1/connectors/{connector_id}/health`。
7. `GET /v1/connectors/{connector_id}/slo`。
8. `GET /v1/connectors/{connector_id}/cost`。
9. `POST /v1/connectors/{connector_id}/mcp-negotiate`。
10. `GET /v1/connectors/{connector_id}/blast-radius-report`。

### 18.13 Cloud Extension Interface Contracts（契约先行，能力后置）

原则：
1. 当前阶段先冻结接口契约，不强制首发全量实现。
2. 新增云平台能力必须复用现有三 Kernel 真值边界，不得绕过审计链。

18.13.1 Org / IAM / Environment：
1. `POST /v1/orgs`。
2. `POST /v1/orgs/{org_id}/workspaces`。
3. `POST /v1/workspaces/{workspace_id}/projects`。
4. `POST /v1/projects/{project_id}/environments`。
5. `POST /v1/orgs/{org_id}/iam/roles`。
6. `POST /v1/orgs/{org_id}/iam/bindings`。
7. `GET /v1/orgs/{org_id}/effective-policy`。
8. `GET /v1/projects/{project_id}/effective-entitlements`。

18.13.2 Identity / Delegated Auth / Secret Scope：
1. `POST /v1/auth/delegations`。
2. `GET /v1/auth/delegations/{delegation_id}`。
3. `POST /v1/auth/consents`。
4. `POST /v1/secrets/scopes`。
5. `POST /v1/secrets/rotate`。
6. `POST /v1/secrets/break-glass`。
7. `POST /v1/secrets/providers/register`。
8. `POST /v1/auth/act-as-policy/dry-run`。

18.13.3 Connector / Marketplace / MCP：
1. `POST /v1/connectors`。
2. `POST /v1/connectors/{connector_id}/versions`。
3. `POST /v1/connectors/{connector_id}/contract-test`。
4. `POST /v1/connectors/{connector_id}/certify`。
5. `POST /v1/connectors/{connector_id}/deprecate`。
6. `POST /v1/connectors/{connector_id}/mcp-negotiate`。
7. `GET /v1/connectors/{connector_id}/health`。
8. `GET /v1/connectors/{connector_id}/blast-radius-report`。

18.13.4 Region / Cell / Residency：
1. `POST /v1/tenants/{tenant_id}/residency-policy`。
2. `POST /v1/cells/register`。
3. `GET /v1/cells/{cell_id}/health`。
4. `POST /v1/cells/{cell_id}/drill`。
5. `POST /v1/scheduler/regional-admission`。
6. `GET /v1/tenants/{tenant_id}/effective-region-plan`。
7. `POST /v1/cells/{cell_id}/failover-plan/validate`。

18.13.5 Provider / Model Control Plane：
1. `POST /v1/providers/register`。
2. `POST /v1/model-profiles`。
3. `POST /v1/model-profiles/{profile_id}/rollout`。
4. `POST /v1/model-profiles/{profile_id}/rollback`。
5. `GET /v1/model-profiles/{profile_id}/slo`。
6. `GET /v1/model-profiles/{profile_id}/cost-quality-report`。

18.13.6 Managed Sandbox / Code Execution：
1. `POST /v1/sandboxes`。
2. `POST /v1/sandboxes/{sandbox_id}/execute`。
3. `POST /v1/sandboxes/{sandbox_id}/artifacts/upload`。
4. `GET /v1/sandboxes/{sandbox_id}/artifacts`。
5. `POST /v1/sandboxes/{sandbox_id}/terminate`。
6. `GET /v1/sandboxes/{sandbox_id}/lineage`。

18.13.7 Feature Signal Control Plane：
1. `POST /v1/features/definitions`。
2. `POST /v1/features/{feature_id}/versions`。
3. `POST /v1/features/snapshots/build`。
4. `GET /v1/features/snapshots/{snapshot_id}`。
5. `POST /v1/features/snapshots/validate-freshness`。
6. `GET /v1/features/snapshots/{snapshot_id}/evidence`。
7. `GET /v1/features/{feature_id}/drift-report`。
8. `POST /v1/features/{feature_id}/rollback`。
9. `GET /v1/features/{feature_id}/dependency-graph`。

约束：
1. 所有 feature API 必须绑定 org/workspace/project scope，并记录审计证据。
2. snapshot 必须签名并可追溯到 `feature_version` 与 `producer_id`。
3. freshness 校验失败时必须返回标准错误码并触发风险分级动作。

18.13.8 Context Policy Space Control Plane：
1. `POST /v1/context/policy-spaces`。
2. `POST /v1/context/policy-spaces/{space_id}/versions`。
3. `POST /v1/context/policy-spaces/{space_id}/validate`。
4. `GET /v1/context/policy-spaces/{space_id}/history`。

18.13.9 Approval Organizational Health Control Plane：
1. `GET /v1/approval/org-health`。
2. `POST /v1/approval/org-health/remediation`。
3. `GET /v1/approval/org-health/reports`。

18.13.10 Metrics Enforcement Control Plane：
1. `GET /v1/metrics/enforcement-matrix`。
2. `POST /v1/metrics/enforcement-matrix/validate`。
3. `POST /v1/metrics/enforcement-matrix/publish`。

---

## 19. 高可用、高并发、低时延（容量闭环）

### 19.1 可用性目标

1. 控制面可用性：`>=99.95%`。
2. 执行核心链路：`>=99.9%`。
3. evidence 查询：`>=99.9%`。

### 19.2 交互链路预算

1. run create + snapshot：`<= 80ms`。
2. context resolve：`<= 220ms`（常规）。
3. model first token：`<= 1200ms`。
4. runtime decision：`<= 120ms`。
5. response assembly：`<= 150ms`。

### 19.3 容量模型

输入：
1. runs per minute。
2. 平均 steps/run。
3. 平均 model calls。
4. 工具调用比例。

输出：
1. Run Kernel CPU/Mem。
2. Decision Kernel CPU/Mem。
3. Evidence ingest QPS。
4. 存储 IO 与成本。
5. feature snapshot 供给 QPS 与 stale 率。
6. Evidence -> Analysis 导出吞吐与积压窗口。

### 19.4 扩缩容策略

1. Run/Decision 无状态服务水平扩容。
2. Evidence ingest 按事件积压自动扩容。
3. adapter worker 按队列长度扩容。
4. feature snapshot builder 按 stale 率与请求延迟双信号扩容。
5. analysis export worker 独立扩容，不与 evidence ingest 共享关键配额。

### 19.5 模型热机策略

1. 维护 warm pool。
2. 按时段预测负载预热。
3. 冷启动超过阈值触发路由降级。

### 19.6 park 规模治理

1. parked run 分层：短停/中停/长停。
2. 长停 run 存储转冷并保留恢复索引。
3. 超限时按风险和 SLA 排序恢复。

### 19.7 调度算法（精简而可用）

1. admission：quota + risk + priority。
2. queue：VIP 保底 + WFQ 公平。
3. preemption：只针对 preemptable。
4. isolation：故障节点自动隔离。

### 19.8 性能退化策略

1. 先降级高级检索策略。
2. 再降级低风险 context budget。
3. 最后限制低优先级任务并保持高风险路径可用。

### 19.9 变更与回滚 Runbook（生产操作级）

#### 19.9.1 一键回滚对象

1. `policy_bundle` 回滚。
2. `workflow_version` 回滚。
3. `model_profile` 回滚。
4. `adapter_version` 回滚。

原子性要求：
1. 同一次回滚操作必须产出统一 `rollback_operation_id`。
2. 多对象回滚需按预定义顺序事务化切换（失败即整体回退）。

#### 19.9.2 自动回滚触发条件

任一条件满足可触发候选回滚：
1. `regression_block_rate > threshold`。
2. `replay_mismatch_rate > threshold`。
3. `incident_spike_detected = true`。
4. `critical_false_allow_incident > 0`。

触发后动作：
1. 进入 `rollback_candidate` 状态。
2. 由 Release Decision 执行最终 `rollback_execute` 或 `manual_review`。

#### 19.9.3 回滚后 run 处理策略

1. `new run`：使用回滚后版本。
2. `running run`：继续旧 snapshot，不强制切换。
3. `parked run`：resume 时执行 compatibility 校验，不通过则进入 review/abort 流程。
4. `failed run`：按 replay 策略进入回放或人工分析。

#### 19.9.4 灰度回滚范围

1. tenant 级回滚。
2. workflow 级回滚。
3. risk tier 级回滚。

原则：
1. 默认优先最小爆炸半径回滚，不默认全量回滚。

#### 19.9.5 Rollback GameDay（制度化演练）

演练频率：
1. 至少每月一次全链路回滚演练。
2. 重大版本发布前必须加做一次专项演练。

演练范围：
1. policy/workflow/model/adapter 四类对象至少覆盖两类联动回滚。
2. tenant 级与 workflow 级灰度回滚都必须演练。

产物要求：
1. `rollback_rehearsal_report`（触发、执行、耗时、失败点、改进项）。
2. `rollback_success_rate` 与 `rollback_mttd/mttr` 指标。
3. 未达标时自动冻结下一次高风险发布窗口。

### 19.10 成本保护与预算控制（控制策略）

#### 19.10.1 自动成本保护

当 `tenant_cost > budget`：
1. `throttle`。
2. `downgrade model profile`。
3. `restrict context budget`。
4. 必要时限制高成本工具调用。

#### 19.10.2 单 run 成本护栏

1. `max_cost_per_run`（按租户/流程可配置）。
2. `max_tokens_per_run`（prompt/completion 分项上限）。
3. 超过上限默认进入 `review_required` 或 `abort`（按风险策略）。

#### 19.10.3 成本保护审计

1. 每次护栏触发必须写 `cost_guard_event`。
2. 记录触发前后路由与预算参数。
3. 高风险任务成本限制动作需保留人工 override 记录。

#### 19.10.4 资源池隔离（生产稳定性要求）

隔离原则：
1. `production runs` 与 `replay/eval/backfill` 至少逻辑隔离。
2. 推荐资源池隔离：独立队列、独立并发配额、独立预算。

强约束：
1. replay/eval 不得挤占生产高优先级配额。
2. 成本护栏必须区分生产成本与治理成本（eval/replay）账本。

### 19.11 Liveness SLO（防卡死）

1. `run_stuck_over_30m_rate`：低于阈值。
2. `approval_case_stuck_over_sla_rate`：低于阈值。
3. `decision_retry_exhausted_rate`：持续监控并触发治理。
4. `receipt_timeout_unresolved_rate`：低于阈值。
5. `zombie_run_count`：持续下降并可在窗口内清零。
6. `approval_effective_latency_p95`：按 risk tier 达标。
7. `pending_decision_age_p95`：低于 `pending_decision_ttl`。

### 19.12 区域化调度准入与容灾目标

区域化准入：
1. `regional_scheduler_admission`：调度必须优先满足租户驻留区域。
2. 跨区域执行需满足策略许可与风险阈值。
3. 驻留冲突时默认拒绝并返回可解释原因。

容灾目标：
1. 每个服务域定义 RPO/RTO。
2. 跨区域故障切换有演练周期和达标门槛。
3. 容灾切换后仍需保持 ticket/receipt 与审计链完整。

### 19.13 审批业务可用性治理（Business SLA）

核心目标：
1. 审批链路不仅“技术可用”，还要满足业务动作解锁时效。

指标：
1. `approval_effective_latency_p50/p95`。
2. `approval_unblocked_within_sla_rate`。
3. `approval_timeout_fallback_rate`（按风险分桶）。
4. `approval_override_dependence_rate`。
5. `active_approver_ratio`。

策略：
1. 低风险流程优先自动 fallback，防止业务长期阻塞。
2. 高风险流程优先安全收敛（review/fail_closed），并要求人工介入。
3. 连续违约链路进入审批路由治理队列并冻结变更。

---

## 20. 安全、合规、风险控制

### 20.1 身份与凭证

1. 服务间 mTLS。
2. workload identity。
3. 短期凭证轮转。

### 20.2 数据安全

1. PII 标注与最小化。
2. 敏感字段脱敏存储。
3. 传输与存储加密。

### 20.3 Prompt Injection 风险控制

1. 上下文分层可信度。
2. 非可信上下文不可触发权限提升。
3. 高危 pattern 命中 -> 自动降级 + 审计。

### 20.4 工具安全沙箱

1. 工具执行容器隔离。
2. 网络出口最小权限。
3. 高风险工具命令白名单。

### 20.5 合规与审计

1. 所有关键决策必须可回放。
2. 审批记录可归档检索。
3. 发布证据包可导出。

### 20.6 代理身份与委托授权治理（Delegated Auth）

核心问题：
1. agent 调用第三方系统时“代表谁”不明确。
2. 缺少用户同意、授权范围、到期与撤销治理。

治理能力：
1. `delegated_auth_principal`：每次调用绑定代理身份主体。
2. `consent_token`：显式同意令牌，含 scope、有效期、撤销状态。
3. `act_as_policy`：定义 agent 可代表用户执行的动作边界。
4. 所有代理调用必须写审计：who/when/scope/target。

### 20.7 连接器密钥与凭证治理

secret scope 分层：
1. organization scope。
2. workspace scope。
3. project scope。
4. workflow scope。

关键能力：
1. 自动轮转（rotation policy）。
2. 紧急通道（break-glass）与双签审批。
3. BYO credentials（客户自带凭证）。
4. customer-managed secret store 对接（例如外部 KMS/Secret Manager）。
5. per-tool permission grant UI 对应的后端授权模型。

强约束：
1. 明文密钥不得落日志与证据正文。
2. 密钥读取必须最小权限与短期令牌。
3. break-glass 操作必须有过期回收与全量审计。

---

## 21. 开发者体验（DX）与团队可执行性

### 21.1 本地最小栈

1. run/decision/evidence 三服务。
2. 本地 mock adapter。
3. 本地 dataset + replay。

### 21.2 开发工作流

1. 写 DSL。
2. 跑 lint。
3. 跑 simulate。
4. 跑 replay suite。
5. 提交评审。
6. canary。

### 21.3 CI 门禁

1. schema compatibility。
2. policy bundle signature。
3. eval baseline。
4. replay consistency。
5. security lint。
6. freeze whitelist coverage。
7. compiled artifact consistency（`compiled_plan_hash`）。
8. single-agent path 无 multi-agent 反向依赖。

### 21.4 可视化调试

1. run timeline。
2. decision graph viewer。
3. obligation inspector。
4. root-cause pack 下载。

### 21.5 团队角色

1. platform owner。
2. policy steward。
3. eval steward。
4. oncall engineer。
5. security reviewer。

### 21.6 运行制度

1. 变更必须附 hypothesis + evidence。
2. 关键策略变更必须双人审查。
3. fast path 需事后 full eval 补跑。

### 21.7 Oncall 职责矩阵（上线必备）

1. `Run Oncall`：处理 stuck run、恢复失败、TTL 终结策略异常。
2. `Decision Oncall`：处理 policy/approval/admission 异常与循环软失败。
3. `Evidence Oncall`：处理 ingest 积压、图谱缺边、replay 失败。
4. `Platform Owner`：执行发布/回滚最终裁决与跨域协调。

职责边界：
1. 单域故障由对应 oncall 首责处理。
2. 跨域故障由 Platform Owner 统一指挥并指定主责域。

### 21.8 告警分级策略

P0（立即唤醒）：
1. `critical_false_allow`。
2. `dispatch_without_ticket`。
3. `snapshot_mismatch_bypass`。

P1（高优先级处理）：
1. `approval_stuck_over_sla`。
2. `replay_mismatch_spike`。
3. `decision_retry_exhausted_spike`。
4. `approval_effective_latency_sla_breach`。
5. `feature_drift_alert`。
6. `approval_org_health_degraded`。
7. `pending_decision_ttl_breach`。

P2（工作时段处理）：
1. `evidence_backlog_high`。
2. `context_compile_degrade`。
3. `cost_guard_frequent_trigger`。
4. `dsar_propagation_lag_alert`。
5. `approval_override_dependence_high`。

### 21.9 值班处置工具链与 runbook

必备操作：
1. `run force terminate`。
2. `approval override`。
3. `policy emergency disable`。
4. `adapter isolation manual trigger`。

runbook 最小结构：
1. 触发条件。
2. 立即遏制动作。
3. 回滚或恢复动作。
4. 证据采集清单。
5. 复盘与回归样本沉淀步骤。

值班增强要求：
1. 每个告警必须附 `playbook_link` 与 `owner_team`。
2. 支持一键导出 `root-cause-pack`（run 级与 tenant 级）。
3. `run force terminate` 在 high/critical 风险下必须走审批流并留双签。
4. `pending_decision_ttl_breach` 必须触发 repair worker 立即扫描与升级动作。
5. `approval_override_dependence_high` 必须触发 override scope 冻结与流程复核。

### 21.10 Tenant 自服务控制台（平台产品面）

目标：
1. 让企业租户可自服务配置、审计、排障、对账，而非平台团队代运维。

控制台模块：
1. tenant console 首页（健康、SLO、成本、告警）。
2. workflow/policy/skill/connector 自服务配置页。
3. usage/billing/SLO dashboard。
4. approval operations console（待审、超时、升级、代理审批）。
5. replay/root-cause self-service 面板。
6. quota/budget/alert subscription 配置页。
7. change history / release history 审计时间线。

自服务边界：
1. 默认只暴露租户权限范围内资源。
2. 高风险操作需要审批或双确认。
3. 所有控制台操作必须落审计证据。

---

## 22. 与 Claude Code / Agent SDK 的结构化对比（公开信息）

### 22.1 对比方法声明

1. 仅使用官方公开文档能力。
2. 不使用未授权泄露内容。
3. 对比是结构能力，不是实现细节猜测。

### 22.2 能力维度矩阵

1. 目标定位：
- Claude：开发者协作与执行效率。
- 本方案：企业级运行时治理与证据闭环。

2. 编排粒度：
- Claude：会话/任务导向。
- 本方案：run/step/phase/obligation/ticket。

3. 裁决中心：
- Claude：权限与会话控制。
- 本方案：Decision Kernel 统一准入与发布裁决。

4. 调度治理：
- Claude：公开重点不在多租户调度治理。
- 本方案：admission + quota + VIP + isolation 内生。

5. 证据体系：
- Claude：具备日志和工具生态。
- 本方案：Evidence Kernel 原生 causality + replay + ledger。

6. 发布门禁：
- Claude：可通过外部流程实现。
- 本方案：Eval + Release Decision 内建融合。

### 22.3 我们借鉴 Claude 的点

1. 扩展体验（skills/hooks/subagent 风格）。
2. headless/SDK 接入体验。
3. 权限模式显式化。

### 22.4 我们需要保持差异化的点

1. 企业多租户治理。
2. 审计可追责证据链。
3. 发布门禁制度化。
4. 运行中兼容与长期可运营。

### 22.5 仍需补足的短板（相对先进平台）

1. 多 Agent 调度学习器需要持续训练与反馈。
2. 成本-质量在线优化需要更成熟。
3. 本地 DX 工具链仍要继续打磨。
4. 自动化运营制度需要团队长期投入。

---

## 23. 分阶段实施计划（可执行，不冒进）

### 23.1 Phase 1（8-10 周）

目标：可上线最小系统。
1. Run Kernel 主链路。
2. Decision Kernel runtime/context/schedule admission 最小集。
3. Evidence Kernel E0：ingest + canonical + 最小 decision graph + 最小 ledger。
4. 调度 adapter + ticket/receipt。
5. Org 控制面最小层次：organization/workspace/project/environment。
6. Tenant Console 最小版：run 查询、审批待办、基础用量。
7. 强约束首批：Decision Input Freeze、step_seq_id 单调性、approval_hard_timeout、Run Integrity Chain、Intermediate Integrity Semantics、Context 非裁决边界、Decision 纯函数实现不变量。

验收：
1. 基础可用性达标。
2. 高风险决策可追溯。
3. 无票执行阻断 100%。

### 23.2 Phase 2（6-8 周）

目标：治理与质量闭环。
1. Approval Domain 完整化。
2. Eval Control Plane 最小闭环。
3. compiler 资源退化策略完整化。
4. Evidence Kernel E1：root-cause pack 与 replay 完整化。
5. SSO/SAML/OIDC/SCIM 集成与组织 IAM 完整化。
6. Connector Registry + contract test + certification 流程。
7. 凭证治理：secret scope、rotation、break-glass、BYO secret。
8. Tool Semantic Contract 全量接入与 contract lint。
9. Evidence Sampling + per-run write budget 落地。
10. Context Stickiness + Runtime Feedback Loop 稳定化。
11. Feature Signal Contract 落地（snapshot/version/freshness/evidence_ref）。
12. Evidence Plane / Analysis Plane 分离与异步导出。
13. approval_effective_latency 指标治理上线。
14. Feature Governance & Drift 落地（owner/schema/drift/rollback）。
15. Context Policy Space Contract 落地（参数空间哈希化）。
16. Deletion Consistency Window 治理落地（DSAR 窗口门禁）。
17. Approval Organizational Health 指标与治理动作上线。
18. Metric Enforcement Matrix 落地到告警与发布门禁。
19. Freeze Object Whitelist 与请求契约固定字段落地。
20. pending_decision repair worker + TTL 机制上线。
21. Override Authorization Model（dual control/expiry/followup）上线。
22. Feature Dependency Graph（critical path）落地。
23. DSL 编译产物运行时（compiled artifacts）落地。
24. Tool probe/reconcile 预算门禁落地。
25. Evidence Export Data Product Contract 落地。
26. Evidence 三路径分离（minimum/debug/analysis）实现落地。
27. Context 实现不变量（含 canary/rollback 接口）完成。

验收：
1. 发布门禁稳定运行。
2. 关键事故可复盘可回放。
3. 指标层级执行一致（observe/alert/block release/block runtime）。

### 23.3 Phase 3（8-12 周）

目标：高级能力。
1. 多 Agent 调度层。
2. 高级检索可选能力。
3. 自动治理与成本优化增强。
4. Evidence Kernel E2 增强能力与跨区域容灾演练。
5. region-aware tenancy + data residency + cell 架构。
6. regional scheduler admission 与跨区域 DR 达标。
7. Tenant Console 完整自服务（计费、回放、发布历史、权限授权面板）。
8. Multi-Agent Explosion Guard（depth/branch/cycle）生产化。

验收：
1. ROI 证明。
2. 不破坏核心 SLO。

### 23.4 Phase 锁定与提前启用例外机制

1. 默认锁定：多 Agent 仅在 Phase 3 可生产启用。
2. 提前启用必须满足例外审批：平台 owner 批准、安全与合规签字、oncall 负载评估通过、可一键回退单 Agent。
3. 未通过例外审批，任何提前启用均视为发布违规。

### 23.5 首发非验收项冻结（防范围失控）

以下能力默认不作为首发验收项：
1. multi-agent advanced merge optimization。
2. graph retrieval / HyDE / adaptive multi-path 全量策略。
3. 自动策略学习与自动阈值调优。
4. Evidence E2 高级查询优化。

规则：
1. 非验收项可在实验域验证，但不得阻塞 Core 首发。
2. 非验收项若要提前纳入首发，必须提交 ROI 与风险评估并走变更审批。

---

## 24. 主文档问题闭环清单（逐项对照）

### 24.1 Blocker 闭环

Blocker 1：Policy/Eval 可执行规范不足。
1. 对应：第 7、14、18 章。
2. 证据：字段契约、phase、失败行为、幂等语义明确。

Blocker 2：Context Compiler 单点风险。
1. 对应：第 7.3、7.11、19.8 章。
2. 证据：candidate gate、预算、退化、fail policy。

Blocker 3：路线过于乐观。
1. 对应：第 23 章。
2. 证据：三阶段拆分与明确验收。

### 24.2 High 闭环

High 1：平台过载风险。
1. 对应：第 1、2、5、23 章。
2. 证据：三 Kernel 最小解 + 外包执行层。

High 2：多 Agent 合并协议不足。
1. 对应：第 15.10 章。
2. 证据：merge contract 字段分层与冲突策略。

High 3：容量模型不闭环。
1. 对应：第 19 章。
2. 证据：QPS 到资源到成本映射。

High 4：DX 弱。
1. 对应：第 18、21 章。
2. 证据：simulate/replay/dry-run/root-cause。

### 24.3 Medium 闭环

Medium 1：RAG 研究化。
1. 对应：第 11.4 章。
2. 证据：L1/L2/L3 分层。

Medium 2：Scheduler 过强。
1. 对应：第 10、19.7 章。
2. 证据：执行外包 + 治理内生。

Medium 3：数据成本风险。
1. 对应：第 17.3、17.4、8.11 章。
2. 证据：分层存储与保留策略。

### 24.4 新增风险闭环

风险：跨 Decider 隐式耦合。
1. 对应：第 8.5、16、24。
2. 证据：decision graph 必填。

风险：Eval 成瓶颈。
1. 对应：第 14.9、14.10。
2. 证据：fast path + full eval 回补。

风险：确定性假设过强。
1. 对应：第 1.4、14.10。
2. 证据：统计与确定性分治。

风险：关键路径过长。
1. 对应：第 2、19.2。
2. 证据：三 Kernel 合并与 hops 预算。

风险：Decision Kernel 过强中枢。
1. 对应：第 7.2.1、7.2.2、7.2.3。
2. 证据：非目标清单 + 复杂度门禁 + 外部化约束。

风险：Evidence Kernel 投入过重导致实施失败。
1. 对应：第 8.1.1、8.1.2、8.1.3。
2. 证据：E0/E1/E2 分层交付与降级策略。

风险：Context 链路有序但仍偏重。
1. 对应：第 11.8、11.9、11.10。
2. 证据：子平台治理红线与回归纪律。

风险：多 Agent 过早进入主路径。
1. 对应：第 15.1.1、23.4。
2. 证据：Phase 3 锁定 + 例外审批机制。

风险：系统活性不足导致僵尸 run 积压。
1. 对应：第 6.13.1、6.13.2、19.11。
2. 证据：Run TTL + 强制终结 + liveness SLO。

风险：审批系统不可用导致全链路死锁。
1. 对应：第 7.11.1。
2. 证据：审批不可用退化矩阵与告警。

风险：Decision 软失败无限循环。
1. 对应：第 7.11.2。
2. 证据：decision retry 上限与强制终结策略。

风险：Evidence 背压反向拖垮主链路。
1. 对应：第 8.1.3、17.9。
2. 证据：backpressure 分级与 outbox 隔离。

风险：回滚只能“设计可行”但“操作不可行”。
1. 对应：第 18.9、19.9。
2. 证据：一键回滚 API + 自动触发条件 + run 状态处置规则。

风险：跨 Kernel 极端不一致导致重复执行或卡死。
1. 对应：第 9.6、9.7、9.8。
2. 证据：Decision/Run 双确认 + receipt 超时统一协议 + replay 边界。

风险：值班可观测但不可处置。
1. 对应：第 21.7、21.8、21.9。
2. 证据：职责矩阵 + 分级告警 + 应急 API 工具链。

风险：成本可观测但不可控。
1. 对应：第 19.10。
2. 证据：自动成本保护 + per-run 成本与 token 上限。

风险：Decision Kernel 复杂度缓慢失控（功能蠕变）。
1. 对应：第 7.2.4。
2. 证据：DCU 预算执行器 + 超预算强退化。

风险：Evidence 成本黑洞（价值与成本失衡）。
1. 对应：第 8.1.2A、8.1.3。
2. 证据：Evidence Value Tier + 分级降级与采样。

风险：Context 仅静态受控、缺少运行时自适应。
1. 对应：第 11.11。
2. 证据：Runtime Feedback Loop + 自动参数调节审计。

风险：多 Agent 调试不可操作导致故障无法定位。
1. 对应：第 15.13。
2. 证据：agent_execution_timeline + merge diff + first-bad-agent。

风险：缺少组织级控制面导致平台无法企业自服务。
1. 对应：第 18.10、18.11、21.10。
2. 证据：四级资源层次 + IAM/SCIM + Tenant Console。

风险：连接器凭证治理不足导致越权与密钥风险。
1. 对应：第 20.6、20.7、18.12。
2. 证据：delegated auth + secret scope + certification 流程。

风险：缺少连接器生态层导致接入规模化失败。
1. 对应：第 18.12。
2. 证据：registry/marketplace + contract testing + deprecation policy。

风险：缺少多区域与驻留能力导致云平台不可落地。
1. 对应：第 17.10、19.12。
2. 证据：region-aware tenancy + cell + RPO/RTO + regional admission。

风险：缺少租户产品化面板导致平台仍偏内部化。
1. 对应：第 21.10。
2. 证据：自服务控制台模块与审计边界。

风险：Decision 运行时隐式依赖膨胀拖死主链路。
1. 对应：第 7.2.5。
2. 证据：Decision Input Freeze Layer + 禁止同步 fan-out。

风险：长事务乱序更新导致状态回退与副作用重复。
1. 对应：第 6.4.1。
2. 证据：Progress Monotonicity Contract + step_seq_id。

风险：Evidence 高负载写入导致 outbox 与主链路受压。
1. 对应：第 8.1.4、17.9。
2. 证据：Sampling Policy + Write Budget + 背压隔离。

风险：Context 多轮漂移导致结果不稳定。
1. 对应：第 11.12。
2. 证据：Context Stickiness Mechanism。

风险：工具系统跨外部一致性语义失配。
1. 对应：第 13.11。
2. 证据：Tool Semantic Contract + 语义驱动 reconcile。

风险：审批链路仅有 SLA 无硬终止导致卡死。
1. 对应：第 7.5.6。
2. 证据：approval_hard_timeout + 强终止动作。

风险：多 Agent handoff/分支/环路爆炸。
1. 对应：第 15.14。
2. 证据：depth/branch/cycle 三重硬约束。

风险：错误码/因果边/重试语义不统一导致自动治理失效。
1. 对应：第 9.9。
2. 证据：Error/Causality/Retry Taxonomy 统一字典。

风险：回滚仅“可设计”但无演练导致真实事故回滚失败。
1. 对应：第 19.9.5。
2. 证据：Rollback GameDay + 演练报告与发布冻结规则。

风险：审批组织漂移导致业务慢死锁。
1. 对应：第 7.5.7。
2. 证据：approval_route_replay + drift 检测 + override 异常治理。

风险：Decision 特征语义膨胀导致回放不可复现。
1. 对应：第 7.2.7。
2. 证据：Feature Signal Contract + snapshot/version/freshness/evidence_ref。

风险：Decision 代码实现偏离纯函数风格导致隐式耦合扩张。
1. 对应：第 7.2.9。
2. 证据：纯函数实现不变量 + 同步 fan-out 禁止门禁。

风险：Context 子系统承担隐式裁决导致策略绕过。
1. 对应：第 11.14、12.2。
2. 证据：Context 非裁决边界 + DSL 静态禁写决策命名空间。

风险：Evidence 与分析负载耦合导致主链路受压。
1. 对应：第 17.11、18.3。
2. 证据：Evidence Plane / Analysis Plane 分离 + 异步导出。

风险：Evidence 实现路径混杂导致证据域反拖主链路。
1. 对应：第 8.1.6。
2. 证据：minimum/debug/analysis 三路径分离与压测隔离。

风险：仅有 step 级不变量，缺 run 级完整性锚点。
1. 对应：第 6.16。
2. 证据：Run Integrity Chain + run_integrity_root 校验。

风险：审批技术可用但业务动作仍超时阻塞。
1. 对应：第 7.5.8、19.13。
2. 证据：approval_effective_latency 指标 + 按风险 fallback 规则。

风险：特征漂移未被及时识别，造成“可解释但错误”的决策。
1. 对应：第 7.2.8、16.7。
2. 证据：Feature Governance & Drift + 指标门禁矩阵。

风险：Context 自适应越过合法策略空间，形成隐式语义变更。
1. 对应：第 11.15。
2. 证据：Context Policy Space Contract + policy_space_hash。

风险：中间态 run 完整性语义不清导致失败态取证不稳定。
1. 对应：第 6.17。
2. 证据：Intermediate Integrity Semantics + root 类型化锚定。

风险：DSAR 删除传播窗口未收敛导致 replay/export 泄露路径。
1. 对应：第 8.12.1、18.8。
2. 证据：Deletion Consistency Window + `DSAR_PENDING_PROPAGATION`。

风险：审批组织可用性下降导致“路由成功但无人处理”。
1. 对应：第 7.5.9、19.13。
2. 证据：Approval Organizational Health 指标与治理动作。

风险：多 Agent merge 直接形成副作用执行权。
1. 对应：第 15.10.1。
2. 证据：Action Merge Semantics + merge 后再裁决。

风险：指标有观测无门禁，无法形成一致执行动作。
1. 对应：第 16.7。
2. 证据：Metric Enforcement Matrix（observe/alert/block release/block runtime）。

风险：冻结输入集合实现漂移导致 replay 对不上。
1. 对应：第 7.2.5.1、9.1。
2. 证据：Freeze Object Whitelist + 请求契约固定字段。

风险：pending_decision 无修复协议导致隐性堆积与卡死。
1. 对应：第 9.6.1、16.7。
2. 证据：TTL + repair worker + `pending_decision_age_p95` 门禁。

风险：Context signal 被上层直接用作状态迁移触发器。
1. 对应：第 11.14。
2. 证据：Context->Decision->Run 单向约束 + 直连路径门禁。

风险：override 权限泛化，审计可解释性失效。
1. 对应：第 7.5.10、18.9。
2. 证据：override 字段硬约束 + dual control + followup review。

风险：feature 依赖不可见导致漂移根因不可归因。
1. 对应：第 7.2.8.1、18.3。
2. 证据：Feature Dependency Graph + critical path 强制登记。

风险：Evidence 导出协议弱化，analysis 与 evidence 语义混淆。
1. 对应：第 17.11.1、18.3.1。
2. 证据：导出数据产品契约 + dsar watermark 强约束。

风险：single-agent 主路径反向依赖 multi-agent 组件。
1. 对应：第 15.1.2。
2. 证据：Core 路径永不反向依赖门禁。

风险：运行时解释原始 DSL 导致线上行为漂移。
1. 对应：第 12.9。
2. 证据：compiled artifacts + compiled_plan_hash。

风险：probe/reconcile 自身成本拖垮系统。
1. 对应：第 13.13、16.7。
2. 证据：工具探测预算 + 耗尽后强制收敛。

风险：root-cause 导出过重导致 P0 时不可用。
1. 对应：第 8.7.1、25.2。
2. 证据：minimal/full 分级导出与时延目标。

---

## 25. 验收与证明（不是口头）

### 25.1 结构验收

1. final decider 唯一性检查。
2. ticket-required 执行检查。
3. decision graph 完整性检查。

### 25.2 性能验收

1. 无审批路径 P95。
2. 审批路径恢复时延。
3. compile 超预算退化行为。
4. approval_effective_latency 按风险分桶达标。
5. analysis plane 压测下 evidence ingest P95 不退化超阈值。
6. `minimal_root_cause_pack` 导出 P95 <= 2s。
7. `full_root_cause_pack` 导出 P95 <= 30s。
8. `pending_decision_age_p95` 受控在 TTL 门限内。

### 25.3 正确性验收

1. snapshot mismatch 阻断。
2. unknown outcome reconcile。
3. policy obligation 跨 phase 冲突可解释。
4. Decision/Run 双确认在网络抖动场景下保持一致。
5. event 去重与 replay 边界符合副作用隔离要求。
6. DCU 超预算时决策退化或阻断行为符合策略。
7. multi-agent timeline 可完整还原 handoff 与 merge 决策链。
8. Decision 冻结输入哈希一致性与 replay 一致性通过。
9. run 进度单调性（step_seq_id）在乱序回调场景下不被破坏。
10. Tool Semantic Contract 对应的 retry/probe/reconcile 行为一致。
11. approval_hard_timeout 到期后不出现无限等待状态。
12. 多 Agent depth/branch/cycle 触发时可正确遏制。
13. Error/Causality/Retry 字段在关键事件中覆盖率 100%。
14. Decision DCU profile 覆盖率 100%，超限错误码一致。
15. Feature Signal Contract 覆盖率 100%（snapshot/version/freshness/evidence_ref）。
16. Run Integrity Chain 在回放中可重建并与 `run_integrity_root` 一致。
17. Context 输出不包含裁决字段，越界样本被阻断并可审计。
18. Context Policy Space Contract 生效：`policy_space_hash` 可在 snapshot/evidence 回放还原。
19. Intermediate Integrity Semantics 生效：`partial/reconciled/review_hold/aborted` 根可验证。
20. 多 Agent action merge 不直接触发副作用，必须二次进入 Decision。
21. Freeze Object Whitelist 覆盖率 100%，禁止动态字段无法进入裁决。
22. pending_decision 修复协议生效：TTL 超限自动修复/升级。
23. Context signal 无法直接触发 Run 状态迁移。
24. override 请求字段完整性与 dual-control 规则校验通过。
25. DSL 运行时只执行 compiled artifacts（raw DSL 不可直执）。
26. Tool probe/reconcile 预算耗尽后行为符合收敛策略。
27. Evidence 导出必须携带 `export_snapshot_id/export_schema_version/redaction_policy_id/evidence_tier_filter/dsar_watermark`。
28. single-agent 主路径无 multi-agent 反向依赖。
29. Decision 代码路径符合纯函数实现不变量（无隐式 I/O、无同步外部 fan-out）。
30. Evidence 三路径分离实现存在并通过压测隔离验证。
31. Context 实现不变量通过（signal 不越界、canary/rollback 接口可用）。

### 25.4 运营验收

1. token/cost 对账一致性。
2. DSAR 删除传播。
3. root-cause 包完整导出。
4. stuck run 可在 TTL 窗口内自动收敛。
5. approval 不可用时系统可退化且不死锁。
6. 组织资源层次与 IAM 继承关系校验通过。
7. SSO/SCIM 用户与组同步一致性达标。
8. 凭证轮转与 break-glass 演练通过。
9. connector contract test 与 certification 通过率达标。
10. 区域驻留策略抽检无越界。
11. tenant console 关键自服务路径可用。
12. evidence value tier 采样/降级策略在压力测试下符合成本与合规边界。
13. context feedback loop 在质量/时延/成本三维上达到目标改善。
14. evidence per-run 写预算在压测中稳定生效（bytes/events 双阈值）。
15. Decision 同步路径无未授权 fan-out 依赖调用。
16. rollback GameDay 按制度执行并达成成功率阈值。
17. approval route drift 检测与修复流程可用。
18. 生产与 replay/eval/backfill 的资源隔离策略生效。
19. approval_effective_latency 按 risk tier 达标或触发受控 fallback。
20. Evidence Plane 与 Analysis Plane 解耦后，analysis 负载不影响 evidence ingest SLO。
21. DSAR 删除传播在 `dsar_propagation_max_lag` 内收敛，超时自动告警与接口拒绝生效。
22. Approval Organizational Health 指标达标或触发治理冻结策略。
23. Metric Enforcement Matrix 与告警/门禁系统配置一致，owner 责任域明确。
24. `approval_override_dependence_rate` 受控并低于门禁阈值。
25. high/critical feature 的 dependency graph 覆盖率 100%。

### 25.5 故障演练场景

1. adapter 回执伪造。
2. policy engine 短时不可用。
3. compiler 候选爆发。
4. approval 超时积压。
5. replay mismatch 爆发。
6. approval 系统整体不可用。
7. decision review_required 循环重试。
8. evidence ingest/backlog 激增。
9. 自动回滚触发与灰度回滚执行。
10. 单 run 成本超限触发护栏。
11. approval route drift 导致审批异常路由。
12. taxonomy 缺失字段导致自动 replay 阻断。
13. feature snapshot 过期导致高风险裁决降级/阻断。
14. context 输出越界写决策字段并被门禁拦截。
15. analysis query 洪峰下 evidence ingest 保持 SLA。
16. feature distribution drift 超阈值触发回滚候选与发布冻结。
17. DSAR 传播超窗口时 replay/export 自动拒绝。
18. approval route 正常但组织健康恶化（无人处理）场景演练。
19. intermediate integrity root 不一致导致回放失败场景演练。
20. merge action conflict 触发 review/replan 而非直接执行。
21. pending_decision TTL 超限后的 repair worker 修复与升级演练。
22. override 权限滥用与 dual-control 阻断演练。
23. context signal 直连状态迁移尝试被门禁拦截演练。
24. raw DSL 变更未编译产物刷新时发布阻断演练。
25. evidence export 缺少 `dsar_watermark` 被拒绝演练。
26. tool probe/reconcile 预算耗尽后的收敛与告警演练。
27. single-agent 路径误依赖 multi-agent 组件的发布阻断演练。

每个场景必须产出：
1. 演练报告。
2. 遏制动作。
3. 改进任务。

---

## 26. Top Failure Modes（实施优先级）

| priority | failure mode | impact | detection | automatic action | manual runbook |
|---|---|---|---|---|---|
| 1 | Decision input freeze mismatch | 高风险决策不可解释或误判 | `frozen_input_hash` mismatch | block runtime + fail_closed（critical） | 冻结发布，回放差异样本，恢复稳定 snapshot |
| 2 | step monotonicity violation | 状态回退、重复副作用 | `progress_monotonicity_violation` / `irreversible_progress_violation_event` | 阻断推进 + safeguard_hold | 对账 step_seq，人工修复 run 状态后再恢复 |
| 3 | unknown outcome unresolved | 外部写操作结果不确定，可能重复写 | `receipt_timeout_unresolved_rate` | 自动 probe/reconcile/escalate | 执行人工对账与补偿流程 |
| 4 | context profile regression | 输出漂移、工具参数漂移 | canary 指标退化、`context_profile_auto_rollback_event` | 自动回滚到稳定 profile | 复盘策略差异并修复 regression 套件 |
| 5 | feature snapshot stale at critical path | 高风险决策依据过期 | `feature_stale_rate` + risk tier | critical 路径 fail_closed | 回滚特征版本并修复 producer |
| 6 | approval organizational failure | 技术可用但无人真正审批 | `active_approver_ratio` 下降、`route_to_no_action_cases` 上升 | 冻结高风险路由变更 + 升级告警 | 重建 approver group / delegate / 值班覆盖 |
| 7 | evidence backlog level3 | 证据链阻塞并反压主链路 | `evidence_backlog_level` | 降级非关键证据 + 限流 non-critical runs | 扩容 ingest 与排查存储/总线瓶颈 |
| 8 | pending_decision unrepaired | 决策已出但运行无法确认，形成隐性卡死 | `pending_decision_age_p95` 超阈值 | repair worker 自动修复/对账/升级 | 执行 pending 决策对账与 run 手工收敛 |
| 9 | run integrity mismatch | 取证与回放不可信 | `run_integrity_mismatch_rate` > 0 | 阻断后续副作用 + 强制 root-cause 导出 | 校验 hash 链、修复中间态根并重放 |
| 10 | multi-agent branch explosion | 成本与时延爆炸、可解释性下降 | depth/branch/cycle 指标触发 | 自动裁枝/中断环路/回退单 Agent | 降级到单 Agent 并复盘 handoff 策略 |

---

## 27. 为什么这版是“最小精简，不能再减”

### 27.1 如果再减一个 Kernel

1. 去 Run：长事务不可控。
2. 去 Decision：裁决多头化。
3. 去 Evidence：可审计性失真。

### 27.2 如果再把核心能力拆更多服务

1. 跳数增加。
2. 延迟增加。
3. 故障域扩大。
4. 调试复杂度指数上升。

### 27.3 如果把治理也外包

1. 平台丧失身份。
2. 合规与审计失去真值。
3. 发布与准入无法闭环。

结论：
1. 三 Kernel + 外包执行 + 统一证据链是当前最小且可持续的企业解。

---

## 28. 公开参考（与本文直接相关）

1. Claude Code Overview  
https://docs.anthropic.com/en/docs/claude-code/overview
2. Claude Code Subagents  
https://docs.anthropic.com/en/docs/claude-code/sub-agents
3. Claude Code Hooks  
https://docs.anthropic.com/en/docs/claude-code/hooks
4. Claude Code Settings  
https://docs.anthropic.com/en/docs/claude-code/settings
5. Claude Code Security  
https://docs.anthropic.com/en/docs/claude-code/security
6. Claude Agent SDK Overview  
https://platform.claude.com/docs/en/agent-sdk/overview
7. Claude Agent SDK Headless  
https://platform.claude.com/docs/en/agent-sdk/headless
8. Claude Agent SDK Permissions  
https://platform.claude.com/docs/en/agent-sdk/permissions
9. Temporal Worker Deployments  
https://docs.temporal.io/production-deployment/worker-deployments
10. Temporal Worker Versioning  
https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning
11. OPA Rego Policy Language  
https://www.openpolicyagent.org/docs/policy-language
12. OpenTelemetry GenAI Agent Spans  
https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-agent-spans/
13. Kubernetes Pod Priority and Preemption  
https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/
14. Kubernetes Node Health  
https://kubernetes.io/docs/tasks/debug/debug-cluster/monitor-node-health/
15. Kubernetes Taints and Tolerations  
https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/
16. Kubernetes CronJob  
https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/
17. OWASP Top 10 for LLM Applications  
https://genai.owasp.org/llm-top-10/
18. NIST AI RMF Generative AI Profile  
https://www.nist.gov/publications/artificial-intelligence-risk-management-framework-generative-artificial-intelligence
19. OpenAI Evaluation Best Practices  
https://developers.openai.com/api/docs/guides/evaluation-best-practices
20. OpenAI Retrieval Guide  
https://developers.openai.com/api/docs/guides/retrieval
21. Anthropic Building Effective Agents  
https://www.anthropic.com/engineering/building-effective-agents
22. Anthropic Effective Context Engineering  
https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
23. Anthropic Demystifying Evals  
https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents
