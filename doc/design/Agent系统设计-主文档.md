# Agent Infra 主文档（2026 全量去重版）

更新时间：2026-04-03

## 0. 文档约定

1. 本文是唯一主规范，覆盖架构、运行时、治理、评测、发布与运维。
2. 每个关键决策按统一结构描述：`问题`、`方案`、`优点`、`缺点`、`依据`。
3. 重复规则：同一能力只定义一次，其他章节只引用，不重复解释。
4. 平台目标：企业级、多租户、高可用、高并发、低时延、可审计、可回滚。
5. 执行级子规范：
6. `agent/doc/README.md`（文档索引与优先级）。
7. `agent/doc/specs/policy-spec.md`（Policy 实施规范，字段/时机/失败/幂等）。
8. `agent/doc/specs/eval-spec.md`（Eval 实施规范，grader/数据集/回放/指标管线）。
9. `agent/doc/specs/skills-spec.md`（Skill 注入实施规范，字段/时机/失败/幂等）。
10. `agent/doc/design/Agent-Infra-优先级与实施边界-强约束.md`（范围裁决规范，必须遵守）。
11. `agent/doc/governance/团队开发守则.md`（团队协作与发布守则，必须遵守）。
12. 子规范与主文档冲突时：字段契约与失败语义以子规范为准，架构边界以主文档为准。
13. 若主文档与“强约束文档”冲突：实施范围与阶段优先级以强约束文档为准。

---

## 1. 目标、非目标与输入假设

## 1.1 目标

1. 可用性：控制面 `>= 99.95%`，执行面核心链路 `>= 99.9%`。
2. 性能：交互任务 `P95 <= 6s`，首 token `P95 <= 1.2s`。
3. 并发：峰值 `5,000 runs/min`，可线性扩容。
4. 可靠性：副作用重复执行事故 `0`，高风险动作越权事故 `0`。
5. 可运营：token/成本/质量/风险可归因到租户、项目、工作流版本、步骤。

## 1.2 非目标

1. 不追求“无边界全自动自治”。
2. 不允许绕过策略与审批执行高风险动作。
3. 不把多 Agent 作为默认路径，必须由评测收益证明。

## 1.3 输入假设

1. 业务构成：交互任务 60%，异步任务 40%。
2. 单 run：6-12 次模型调用，1-5 次工具调用。
3. 存在长事务：审批、外部回调、人工补充信息。
4. 存在高合规租户：审计、驻留、DSAR 删除传播。

---

## 2. 当前业务痛点（驱动设计）

1. 上下文链路职责重叠，导致多处排序、裁剪、冲突解算，结果不可解释。
2. Workflow DSL 过轻，难以发布前静态校验，容易线上暴雷。
3. 工具调用只考虑调度成功率，未完整覆盖副作用安全与未知结果处理。
4. 审批只当节点，缺少独立审批域（路由、SLA、升级、会签、归档）。
5. 多 Agent 没有平台化运行时调度，只有“策略建议”，缺预算与冲突治理。
6. 数据面偏“组件清单”，未形成 run state / audit / replay / eval 的一致性体系。
7. 观测与治理脱节，事件只能“看见问题”，不能自动学习与收敛。
8. Eval 还未成为一级系统，无法稳定阻断坏变更。
9. 发布后运行中实例（parked/pending approval）与新版本兼容规则不清晰。

---

## 3. 关键决策登记簿（问题-方案-优缺点-依据）

### D01 执行引擎采用 Durable Workflow（Temporal）

问题：Agent run 跨多轮推理、工具、审批、回调，普通任务队列难保证可恢复与幂等。

方案：使用 Temporal 编排 run 生命周期，Worker 无状态化，状态外置。

优点：
1. 崩溃后可从上次已提交状态恢复。
2. 天然支持长事务、重试、补偿、超时。
3. 便于版本化与运行中实例兼容治理。

缺点：
1. 引入工作流建模与可重放约束。
2. 团队学习成本较高。

依据：Temporal 生产部署与 Worker Versioning 文档 [E10][E11]。

### D02 Worker 无状态，状态统一外置

问题：有状态 Worker 在扩缩容、故障切换、跨 AZ 时一致性差。

方案：Worker 仅执行业务逻辑；run 状态、事件、审计、记忆全部存储在数据平面。

优点：
1. 弹性扩容简单。
2. 故障恢复路径单一。
3. 降低进程内状态漂移风险。

缺点：
1. 对外部存储和网络依赖更高。

依据：Durable execution 最佳实践 [E10]。

### D03 工具等待采用 park/resume，而非“模型占用等待”

问题：工具延迟高时若保持模型同步等待，会占用昂贵计算资源并拖慢系统。

方案：
1. `A类`（<=500ms）同步等待。
2. `B类`（500ms-5s）异步派发后 park run，释放 Worker。
3. `C类`（>5s 或外部回调）强制异步与 callback。

优点：
1. 释放执行资源，提升吞吐与成本效率。
2. 长尾工具不阻塞主链路。

缺点：
1. 需要 continuation token、幂等键、恢复点管理。

依据：Temporal 长流程实践与工程延迟模型 [E10][E11]。

### D04 DSL 升级为强契约（Schema + Side Effect + 权限 + 可观测）

问题：轻量 DSL 无法做发布前强校验，节点契约易断裂。

方案：节点强制声明 `input/output schema`、`effect_type`、`required_permissions`、`approval_policy_ref`、`compensation_ref`、`context read/write set`、`observability_tags`。

优点：
1. 可做发布前静态验证。
2. 运行时可做越权阻断。
3. 评测覆盖与回归可自动化。

缺点：
1. 初期建模成本上升。

依据：企业工作流与策略工程实践，结合 Agent 生产事故模式。

### D05 Context Compiler 成为唯一上下文决策者

问题：RAG/Memory/Citation/Context 多处做筛选与裁剪，解释性与可维护性差。

方案：只允许 Context Compiler 做最终排序、裁剪、冲突仲裁和注入组织；其余组件只产出候选。

优点：
1. 去重排序与裁剪逻辑。
2. 选择理由可审计。
3. 调优入口单一。

缺点：
1. Context Compiler 会成为关键路径组件，需高性能设计。

依据：Context engineering 实践与“最小高信号上下文”原则 [E8]。

### D06 Token Budget 使用动态策略而非固定配方

问题：固定比例预算在不同任务类型下波动大，质量/成本不稳定。

方案：按任务类别给初始预算，运行时根据检索质量、失败画像、风险等级、模型窗口与成本实时调整。

优点：
1. 质量与时延更平衡。
2. 预算策略可在线学习。

缺点：
1. 需要实时特征与反馈环。

依据：OpenAI/Anthropic 上下文管理与评测循环建议 [E2][E8]。

### D07 Memory 采用“证据驱动写入 + 可反证状态机”

问题：长期运行会积累错误记忆，污染后续决策。

方案：对不同记忆类型设置差异化准入证据；状态机 `CANDIDATE -> ACTIVE -> SUSPECT -> REVOKED -> ARCHIVED`；支持反证降级与纠错替换。

优点：
1. 降低错误记忆长期污染。
2. 记忆可信度可追踪。

缺点：
1. 审核与回归链路更复杂。

依据：代理记忆/反思类研究与工程实践 [P3][P4][P7]。

### D08 RAG 采用“文档类型特征化”而非统一切分

问题：政策、API、SOP、表格语料混切会导致召回与引用质量下降。

方案：按文档类型定义摄取与切分策略；检索采用混合召回 + 重排 + 去重 + budget 裁剪。

优点：
1. 提升命中率与可引用性。
2. 降低“检索到了但不可用”的比例。

缺点：
1. 文档治理工作量上升。

依据：RAG 系列研究与检索评测方法 [P8][P9][P10][P11][P12][P13]。

### D09 工具副作用治理优先级提升为一级能力

问题：写操作超时后“结果未知”是生产事故高发点。

方案：
1. 工具分类：`read` / `idempotent_write` / `non_idempotent_write` / `irreversible`。
2. 写操作必须带 `idempotency_key`。
3. 未知结果先探测再 reconcile，禁止盲目重试。
4. 外部写记录 before/after snapshot 与 request/response hash。

优点：
1. 显著降低重复执行与脏写。
2. 审计取证完整。

缺点：
1. 开发与存储成本增加。

依据：工具调用基准与分布式一致性模式 [P6][P7][P14][E12]。

### D10 Policy as Code（双层：业务 DSL + 执行引擎）

问题：策略写在代码分散且不可回放，难审计、难迁移。

方案：
1. 业务层：可读 DSL（策略产品化）。
2. 执行层：Rego/CEL 引擎（可测试、可回放、可版本化）。
3. 实施细节下沉到 `policy-spec.md`，主文档仅保留架构边界与治理约束。

优点：
1. 决策一致、可解释、可审计。
2. 支持 shadow evaluate 和误伤回放。

缺点：
1. 需要策略工程体系与 lint/test 流程。

依据：OPA/Rego 官方实践 [E15]。

### D11 审批作为独立业务域（HITL Domain）

问题：仅把审批做成节点无法支撑企业级流程。

方案：独立审批域，包含路由、会签/或签、代理审批、SLA 升级、证据包、归档检索，并与 run 恢复强绑定。

优点：
1. 审批流程可治理。
2. 不与编排逻辑耦合。

缺点：
1. 系统复杂度增加。

依据：企业合规流程要求与高风险动作治理实践。

### D12 Eval 升级为平台一级系统

问题：无系统化评测会导致“改一处坏一片”。

方案：建立 Eval Control Plane，按 L0-L5 分层评测并作为发布门禁。
补充：grader 协议、dataset 版本、replay 一致性、metric batch/streaming 管线由 `eval-spec.md` 统一定义。

优点：
1. 变更可量化、可阻断。
2. 支持持续回归与漂移监控。

缺点：
1. 数据与评测基础设施投入较大。

依据：OpenAI eval best practices + Anthropic eval 方法学 [E2][E9]。

### D13 多 Agent 采用“收益证明后启用”

问题：多 Agent 容易放大成本、时延和失控风险。

方案：默认单 Agent；仅当 `quality_gain` 显著且通过回归门禁时开启多 Agent。

优点：
1. 避免无效复杂化。
2. 让架构演进可证据化。

缺点：
1. 前期可能损失部分并行收益。

依据：OpenAI/Anthropic 对“先简单后复杂”的一致建议 [E1][E6]。

### D14 多 Agent 需要独立调度层

问题：仅有 handoff 协议不够，缺运行时预算与冲突治理。

方案：增加 `Coordinator Scheduler`、`Specialist Selector`、`Budget Broker`、`Branch Arbiter`、`Merge Resolver`。

优点：
1. 控制 fan-out 成本。
2. 统一冲突合并与提前终止。

缺点：
1. 调度策略调参成本高。

依据：Anthropic 多 Agent 工程复盘 + OTel agent spans 语义 [E7][E14]。

### D15 数据平面采用 Agent-native 分层

问题：数据仅按组件拆分，无法支持大规模回放、取证、计费争议处理。

方案：分离 `Run State`、`Event Log`、`Audit Store`、`Usage Ledger`、`Eval Warehouse`、`Artifact Canonical Store`、`Memory/Evidence Graph`。

优点：
1. 数据职责清晰。
2. 回放、审计、计费、DSAR 路径明确。

缺点：
1. 跨域一致性治理更复杂。

依据：分布式可靠消息与审计架构实践 [E12]。

### D16 一致性采用“事务状态 + Outbox + 幂等消费”

问题：状态写入和事件发布双写不一致。

方案：状态事务提交后写 outbox，事件异步可靠投递，消费端按 `event_id + step_version` 幂等。

优点：
1. 避免双写不一致。
2. 支持 replay 与追责。

缺点：
1. 事件最终一致，需处理延迟可见。

依据：Transactional Outbox 模式 [E12]。

### D17 发布兼容采用 Execution Snapshot + Compatibility Policy

问题：运行中实例在新版本发布后恢复易混配。

方案：每个 run 固定 `workflow/prompt/policy/tool_schema/model_profile/context_compiler` 版本；恢复时按兼容策略判定。

优点：
1. 避免新旧配置混用。
2. 审计闭环完整。

缺点：
1. 版本矩阵管理复杂。

依据：Temporal versioning 与长流程升级建议 [E11]。

### D18 观测必须闭环到治理与学习

问题：只有监控告警，无法自动收敛质量问题。

方案：建立 `Detect -> Diagnose -> Act -> Assess -> Learn` 闭环；异常自动触发 replay、策略收紧、知识补档、synthetic eval 生成。

优点：
1. 质量改进速度提升。
2. 减少人工巡检负担。

缺点：
1. 自动动作需要严格边界控制。

依据：OpenAI/Anthropic 持续评测与生产反馈闭环实践 [E2][E9]。

### D19 外部开放 Token/成本治理 API

问题：企业无法精细预算管理与成本归因。

方案：按调用采集 tokens/cost/latency/status，开放 usage 与 quota API，并做账单对账。

优点：
1. 成本透明可控。
2. 支持租户级配额治理。

缺点：
1. 计量链路需高精度与抗争议能力。

依据：OpenAI usage 字段与 prompt caching 成本优化机制 [E4][E5]。

### D20 协议互操作：MCP 为工具协议，A2A 为跨 Agent 协议（可选）

问题：单一框架会形成厂商锁定，跨团队 Agent 难互通。

方案：
1. 工具接入统一 MCP。
2. 跨平台 Agent 协作通过 A2A 网关（可选能力）。

优点：
1. 降低集成成本。
2. 提升生态互操作性。

缺点：
1. 协议仍在演进，需版本隔离与适配层。

依据：MCP 规范、A2A 规范与 LF 项目化进展 [E13][E16][E17]。

### D21 定时与重复任务必须做成平台一级 Trigger 能力

问题：用外部 Cron 或脚本触发 run，容易出现漏触发、重复触发、越权执行、审计断裂。

方案：建设 `Trigger & Scheduler Domain`，统一支持 `one_time`、`cron`、`interval`、`calendar_rrule`、`event_window` 触发，并内置 `misfire`、`concurrency`、`pause/resume`、`backfill`、`dedupe`、`quota`。

优点：
1. 调度、风控、审计、配额统一治理。
2. 定时与重复任务可回放、可追责、可限流。
3. 与审批和策略引擎天然联动，避免“定时绕过风控”。

缺点：
1. 调度器需要高可用与时钟一致性治理。
2. `misfire/backfill` 策略配置复杂度上升。

依据：Kubernetes CronJob 在并发策略、时区、missed-run 治理上的工程经验 [E20]，以及 Durable Workflow 的恢复模型 [E10]。

### D22 调度采用“分层准入 + 多队列公平调度 + VIP 预留池”

问题：同一平台同时要满足 VIP 低时延 SLA 与普通租户公平性，若只做全局 FIFO，会出现 VIP 抖动和普通租户被饿死两类问题。

方案：
1. 分层准入：`global -> tier -> tenant -> user` 四级令牌桶。
2. 队列分层：`vip_dedicated_pool` + `shared_pool`。
3. 共享池调度：加权 Deficit Round Robin（WDRR）+ aging 防饿死。
4. 抢占规则：优先抢占 `pending` 低优先级任务；运行中仅在安全边界（step/park）可抢占。
5. 借还策略：VIP 空闲容量可借给共享池，VIP 回潮时按宽限窗口回收。

优点：
1. VIP SLA 可保障且可解释。
2. 普通租户保持可预期公平性。
3. 算法可观测、可调参、可灰度。

缺点：
1. 调度参数（权重、量子、宽限）需要持续调优。
2. 抢占与回收策略实现复杂度较高。

依据：Kubernetes Priority/Preemption 与 API Priority and Fairness 的生产机制 [E21][E22]，结合 CronJob 并发策略 [E20]。

### D23 检索采用“混合召回 + 分层重排 + JIT 探索”的双路径架构

问题：知识库规模增长后，单一路径向量检索会出现召回不稳、时延抖动和上下文噪声增大，导致模型“找不到真正需要的信息”。

方案：
1. 快速路径：metadata 过滤 + hybrid retrieval（关键词/稀疏 + 稠密向量）+ 轻量重排。
2. 深度路径：当快速路径低置信时，触发 query rewrite（含 HyDE 类策略）和 agentic/JIT 探索检索。
3. 最终由 Context Compiler 做跨源去重、冲突仲裁、预算裁剪和注入编排。
4. 采用检索缓存（query/result/passage）与分层索引（hot/warm/cold）控制时延与成本。

优点：
1. 在高吞吐场景保持低时延，在复杂问题场景保持召回质量。
2. 知识库扩容后稳定性更好，不依赖单一检索器。
3. 检索链路可观测、可评测、可回放，便于持续优化。

缺点：
1. 管线复杂度增加，需要严格的调参和门禁评测。
2. 双路径切换阈值不当会带来额外成本。

依据：HyDE、ColBERTv2、REPLUG、Lost-in-the-Middle、LightRAG 等论文 [P17][P18][P19][P20][P21]，以及 Anthropic/OpenAI 与混合检索工程文档 [E8][E23][E25][E26]。

### D24 机器故障治理采用“外部检测器 + 平台标准健康接口 + 调度隔离闭环”

问题：网卡慢卡、GPU 异常、节点故障等检测算法变化快；若检测与调度强耦合，后续升级会牵一发而动全身。

方案：
1. 故障发现解耦：外部程序（NPD/DCGM/自研探针）负责检测，平台只消费标准 `HealthSignal`。
2. 平台内部统一：Health Normalizer -> Isolation Controller -> Scheduler。
3. 机器状态机：`HEALTHY -> DEGRADED -> DRAINING -> ISOLATED -> RECOVERING`。
4. 调度感知健康：候选节点必须满足资源约束与健康阈值；故障节点自动摘流。
5. 契约优先：所有交换接口版本化，升级优先适配层，不改核心调度器。

优点：
1. 检测器可独立替换，平台核心稳定。
2. 故障治理路径统一，可审计、可回放。
3. 支持 CPU/NIC/GPU/磁盘等多类型故障扩展。

缺点：
1. 需要维护健康信号标准和兼容策略。
2. 状态机与回收策略调优成本较高。

依据：Kubernetes Node Health、Taints/Tolerations、Priority/Fairness，以及 GPU 健康监控实践 [E21][E22][E27][E28][E29][E30]。

### D25 依赖服务采用“分层继承 + 解析快照”机制

问题：同一能力在租户/项目/工作流/节点多层重复配置，易出现版本漂移、权限放宽和恢复不一致。

方案：
1. 分层继承：`platform -> tenant -> project -> workflow -> node`。
2. 受限覆写：仅允许白名单字段覆写，权限与策略绑定只能收紧。
3. 编译解析：发布时生成 `ResolvedDependencyGraph` 与 `dependency_bundle_id`。
4. 运行冻结：run 生命周期内固定依赖快照，恢复时禁止隐式替换。
5. 回放审计：依赖解析结果可回放、可签名、可追责。

优点：
1. 大幅降低重复配置和人为漂移。
2. 故障排查可定位到“实际绑定快照”而非“当前配置”。
3. 支持依赖配置灰度与可控回滚。

缺点：
1. 需要维护依赖 profile 与版本治理流程。
2. 解析器与兼容校验需要持续维护。

依据：Kubernetes 分层配置与准入控制、Priority/Fairness 与生产签名发布治理实践 [E21][E22][E30]。

### D26 知识库文档存储采用“对象存储主真值 + 分布式文件可选缓存”

问题：知识库文档既要低成本海量存储、版本追溯、跨地域复制，也要支持高吞吐预处理与批量索引；单一存储形态难同时最优。

方案：
1. 原文真值：对象存储（Object Storage）作为唯一 canonical source（带版本、生命周期、跨区复制）。
2. 加速层：分布式文件系统（如并行文件或集群文件）仅作临时解压、OCR、中间分片缓存。
3. 索引层：向量/关键词/图索引独立存储，不直接把 DFS 当知识真值。
4. 引用一致性：所有 chunk 必须可回溯到 `object_uri + version_id + content_hash`。
5. 恢复策略：DFS 丢失只影响吞吐，不影响数据正确性；可由对象存储重建。

优点：
1. 对象存储在成本、耐久、版本化和生命周期治理上更适合知识库长期留存。
2. 分布式文件层可吸收高并发预处理 I/O，降低索引构建抖动。
3. 真值与加速层解耦后，容灾与审计路径更清晰。

缺点：
1. 需要维护对象存储与缓存层的一致性和失效策略。
2. 预处理链路增加一次对象->缓存的搬运成本。

依据：企业检索与数据湖工程实践（对象存储作为 canonical，计算/缓存层解耦），结合本平台 claim-check 与可回放要求 [E23][E24]。

### D27 平台分层采用“Core / Extended / Experimental”

问题：将 runtime、scheduler、policy、eval、infra 编排全部作为同一首发范围会导致 blast radius 过大。

方案：
1. Core（必须）：Workflow runtime、Tool safety、Context compiler、Policy minimal、Audit。
2. Extended（可插拔）：Approval Domain、Scheduler advanced、Eval control plane、KB retrieval optimization。
3. Experimental（隔离）：Multi-agent advanced、JIT/graph retrieval、自动策略学习。
4. 上线顺序强制：Core -> Extended -> Experimental，不允许跨层直上生产。
5. 分层落地与发布裁决由 `agent/doc/design/Agent-Infra-优先级与实施边界-强约束.md` 统一约束。

优点：
1. 降低首发复杂度与系统耦合风险。
2. 便于分层回滚和责任隔离。

缺点：
1. 需要维护分层 feature gate 与兼容矩阵。

依据：大型平台分层发布与风险隔离实践（灰度、可插拔、实验域隔离）。

### D28 引入 Decision Causality Graph（跨 Decider 因果绑定）

问题：Context/Policy/Approval/Resume 分别可审计，但跨系统只有隐式数据耦合，出现争议时难回答“为什么最终允许或拒绝”。

方案：
1. 每个 run 强制生成 `decision_graph_id`。
2. 各 decider 输出标准化 `decision_node`（输入指纹、规则版本、结论、理由、时间）。
3. 统一维护 `depends_on|influenced_by|overrides` 三类边，形成可回放因果链。
4. 高风险 run 缺失关键因果边时，禁止放行或恢复。

优点：
1. 把“可审计”提升为“可解释”。
2. 跨域回放和责任归因更直接。
3. 为误判复盘和自动治理提供统一图结构输入。

缺点：
1. 事件量和存储成本上升。
2. 需要定义跨系统节点与边的稳定语义。

依据：生产审计与因果追踪工程实践（trace + decision lineage 融合）。

### D29 Context Compiler 前置 Candidate Budget Gate

问题：仅在 compiler 内限制 `candidate_hard_cap` 不足以抑制上游候选爆发，RAG/Memory/Tool 并发高峰会拖垮 compile 链路。

方案：
1. 在 Context Compiler 前新增 `Candidate Budget Gate`。
2. 先做“源级配额 + 风险分层 + 租户配额”预裁剪，再进入 compiler。
3. Gate 超预算时执行硬退化：低权威源先丢弃，保证系统策略与高权威证据优先。

优点：
1. 将过载拦截前移，降低 compile 崩溃概率。
2. 资源预算更可控，尾延迟更稳定。

缺点：
1. 需要维护源级配额策略与动态阈值。

依据：高并发检索系统的 admission control 与多源预算治理实践。

### D30 引入 fast_path_eval（仅低风险）

问题：全量发布都走完整 eval gate 时，低风险改动也可能被评测队列拖慢，影响交付效率。

方案：
1. 新增 `fast_path_eval`，仅允许低风险改动进入。
2. 快速路径仍保留 policy gate 与最小 eval 集，不允许绕过关键安全门禁。
3. 发布后必须在限定窗口内完成 full shadow eval；失败自动冻结与回滚。

优点：
1. 降低低风险变更等待时间。
2. 不牺牲高风险场景的安全边界。

缺点：
1. 需要严格定义“低风险变更”与自动回滚条件。

依据：分级门禁与渐进发布（fast gate + shadow full gate）工程模式。

### D31 Skill 注入采用“注册制 + 策略约束 + 可回放”

问题：临时拼接 prompt/tool 规则不可治理，易出现能力漂移、权限边界不清和复现困难。

方案：
1. 建立 `Skill Registry` 与签名 `skill_bundle`。
2. Skill 注入仅通过 `Skill Injection Engine`，并受 Policy/Approval 与上下文边界约束。
3. 每次注入写入 decision graph 与审计证据，支持按版本回放。

优点：
1. Skill 能力可治理、可审计、可灰度。
2. 避免“野生提示词”绕过系统边界。

缺点：
1. 需要维护 skill 生命周期与兼容矩阵。

依据：企业化 Prompt/Tool 模板治理与策略收敛实践。

### D32 平台采用三层外包边界（Decision / Execution / Evidence）

问题：把“能跑起来”与“谁有裁决权”混在一起，会导致平台身份弱化，出现合规与审计失真风险。

方案：
1. Decision Plane（不可外包）：`allow/deny/require_approval`、调度准入裁决、上下文保留/裁剪终裁、发布门禁、skill 注入放行。
2. Execution Plane（可外包）：执行调度、队列、触发器、资源编排、模型 serving、检索执行、通知投递等。
3. Evidence Plane（语义不可外包）：`decision log/audit snapshot/usage ledger/replay input/causality graph` 的语义真值必须由平台掌握；底层存储可托管。

优点：
1. 保持平台控制权与审计闭环。
2. 可复用外部基础设施能力而不丢失治理能力。

缺点：
1. 需要维护决策层与执行层之间的契约与回放一致性。

依据：企业合规与平台治理实践中“执行可托管、裁决不可托管”的通用模式。

### D33 调度外包仅限执行层，治理裁决必须内生

问题：调度组件可替换，但调度治理（VIP/配额/风险阻断/审批恢复）若外包会导致策略漂移与责任不可追溯。

方案：
1. 可外包：队列系统、任务调度器、定时触发器、资源编排器、节点放置器、扩缩容控制器。
2. 不可外包：`schedule_admit/reject`、VIP 池资格、抢占资格、高风险阻断、parked run 恢复资格、审批缺失阻断。
3. 外部调度器仅接收平台签发的 `dispatch_ticket`，无票不得执行。

优点：
1. 调度执行能力可弹性替换。
2. 治理规则与审计链条保持统一真值。

缺点：
1. 需要 ticket 生命周期与幂等校验机制。

依据：K8s/队列系统作为执行器 + 平台控制面裁决的分层实践。

### D34 系统设计必须区分“可控性”与“正确性”

问题：Policy/Eval/Compiler 能提高可控性，但不等于天然正确；若表述不克制，容易造成错误预期。

方案：
1. 文档与门禁统一声明：`可控性改进 != 正确性保证`。
2. 关键模块必须有“反证/失效/回放纠偏”机制：Policy 误伤回放、Compiler 回归、Eval 误判仲裁。
3. 高风险链路默认 fail-closed，并要求人工复核通道可达。

优点：
1. 预期管理更真实，治理动作更稳健。
2. 便于组织层建立持续校准机制。

缺点：
1. 增加运营与复盘负担。

依据：生产 Agent 系统普遍经验：控制与正确性需分层治理。

### D35 确定性边界与统计边界必须分离

问题：把 LLM/检索/API 的统计行为当成完全确定系统治理，会导致错误指标承诺与误判。

方案：
1. 仅对“可确定环节”施加确定性约束：DSL->IR 编译、策略冲突求解、快照绑定、幂等键计算。
2. 对“统计环节”采用概率约束：LLM 输出、检索召回、外部 API 可用性、回放一致性。
3. 所有指标必须标注 `deterministic|stochastic` 属性，并定义不同门禁语义。

优点：
1. 指标含义与系统物理现实一致。
2. 降低“虚假确定性”导致的误治理风险。

缺点：
1. 需要双轨指标与双轨门禁配置。

依据：线上 AI 系统工程共识：确定性模块与统计模块需分治。

### D36 Eval 不是“真值”，而是“证据系统”

问题：若把 eval 指标当最终真值，容易把 sampling/proxy metric 的局限掩盖成“已证明正确”。

方案：
1. 发布裁决改为“证据裁决”：`Eval + Policy Regression + Replay + Incident Trend + Human Signoff` 联合决策。
2. Eval 输出必须携带不确定性与样本覆盖声明：`confidence_interval + coverage_report + bias_report`。
3. 对高风险变更，Eval 必须与非 Eval 证据（回放一致性、审计样本）交叉验证。

优点：
1. 避免“指标替代正确性”。
2. 发布决策更稳健，解释性更强。

缺点：
1. 决策流程更复杂，节奏变慢。

依据：评测方法学中“proxy metric 仅是证据，不是事实”原则。

### D37 错误传播与遏制模型必须显式定义

问题：仅定义 fail_closed/require_review/fallback，无法描述错误如何跨链路传播与放大。

方案：
1. 建立 Error Propagation Graph（EPG），跟踪 `source_error -> propagated_error -> containment_action`。
2. 定义错误类型与传播边：`context_error`、`policy_false_allow/deny`、`approval_stale`、`eval_miss`、`tool_unknown_outcome`。
3. 为每类错误定义遏制边界（containment boundary）与最大传播层级（max_hops）。

优点：
1. 可量化“局部错误如何变成系统事故”。
2. 便于自动化遏制与复盘。

缺点：
1. 需要维护错误分类体系与传播规则。

依据：复杂分布式系统的错误预算与故障传播治理实践。

### D38 跨域耦合必须显式注册，不得仅靠事件隐式耦合

问题：Context/Policy/Approval/Eval/Skill 之间是强语义耦合，仅靠事件不能代表真正解耦。

方案：
1. 建立 Coupling Registry，登记每个跨域依赖的输入输出契约、时序约束、失败语义。
2. 每次跨域变更必须产出 `coupling_impact_report`，未通过不得发布。
3. 高频耦合链路强制做端到端回放回归（而非单模块回归）。

优点：
1. 把隐式耦合转为可治理资产。
2. 降低“局部改动全局抖动”的风险。

缺点：
1. 增加变更评审与回归成本。

依据：平台化工程中“接口治理优先于模块边界叙事”的实践。

---

## 4. 总体架构

## 4.1 架构平面

1. 接入平面：API Gateway、AuthN/AuthZ、Rate Limit、SSE/WebSocket。
2. 控制平面：Workflow Registry、Agent Registry、Skill Registry、Policy Registry、Release Manager、Approval Domain、Trigger & Scheduler Domain、Resource Health & Placement Domain、Quota/Billing。
3. 执行平面：Orchestrator、Run Worker、Tool Worker、Approval Worker。
4. 模型平面：Model Gateway（路由/降级/预算/缓存）、Provider Adapters。
5. 上下文平面：Candidate Budget Gate、Context Compiler、Skill Injection Engine、RAG Service、Memory Service、Citation Service。
6. 数据平面：Run State、Event Log、Audit Store、Artifacts、Usage Ledger、Eval Warehouse、Feature Store、Vector Store、Schedule Store、Trigger Log、Node Registry、Health Signal Store、Decision Causality Graph Store、Skill Bundle Store。
7. 治理平面：OTel、Agent Event Hub、Self-heal Engine、Eval Gatekeeper、Release Decision Engine、Policy Replay、Decision Graph Analyzer。

## 4.2 主链路（交互任务）

1. `POST /runs` 创建 run，写入快照版本。
2. Orchestrator 调度步骤并创建 `decision_graph_id`。
3. Candidate Budget Gate 先执行源级预算与配额裁剪。
4. Context Compiler 聚合候选并生成最终注入上下文（写 `context.compile` 因果节点）。
5. Skill Injection Engine 按 `skill_bundle + policy` 注入技能片段（写 `skill.inject` 因果节点）。
6. Model Gateway 发起推理（写 `model.inference` 因果节点）。
7. 若触发工具/动作：先做 Policy Eval（写 `policy.eval` 因果节点）。
8. 若策略要求审批：挂起至审批域返回 `approval_token`（写 `approval.decision` 因果节点）。
9. 若触发工具：按 D03 策略同步或 park；resume 时写 `run.resume` 因果节点并重建上下文增量。
10. 完成后写事件、审计证据、使用量与 `decision_graph_snapshot`。

## 4.3 主链路（定时/重复任务）

1. Scheduler 到点产出 `trigger.fired` 事件。
2. Trigger Executor 做幂等去重与配额检查。
3. 通过后调用 `POST /runs`（写入 `trigger_id/scheduled_time`）。
4. run 进入与交互任务同一执行链路（策略、审批、工具治理不变）。
5. 若 miss 或失败，按 `misfire_policy` 和 `retry_policy` 处理，并写 `schedule.*` 事件。

## 4.4 主链路（调度与限流）

1. Admission Controller 执行四级令牌桶检查（global/tier/tenant/user）。
2. Queue Selector 按 `tier + execution_pool` 分流至对应队列。
3. Dispatcher 在 pool 内按 WDRR 选择可执行任务。
4. 若 VIP 队列超阈值，触发 pending 抢占与容量回收。
5. 所有调度决策写入 `scheduler.decision_log`，用于审计与回放。

## 4.5 主链路（故障发现与隔离）

1. 外部检测器通过标准接口上报 `HealthSignal`（节点、设备、故障码、置信度、TTL）。
2. Health Normalizer 做去重、归一化、阈值判定与签名校验。
3. Isolation Controller 更新节点状态机并执行动作（cordon、drain、isolate、recover）。
4. Scheduler 仅消费节点状态与健康分，不直接依赖检测器实现细节。
5. 全流程写入 `infra.*` 事件与 `scheduler.decision_log`。

## 4.6 最终裁决归属矩阵（Single Decider）

1. 上下文注入：`Context Compiler` 最终裁决；RAG/Memory/Citation 仅供候选。
2. 权限与动作放行：`Policy Engine` 最终裁决；审批不能覆盖 deny。
3. 是否需要人工审批：`Approval Policy` 最终裁决；执行层只消费审批结论。
4. 调度准入与排队：`Scheduler` 最终裁决；业务 workflow 不能绕过配额。
5. 节点可调度性：`Resource Health & Placement` 最终裁决；检测器仅上报信号。
6. 发布是否放行：`Release Decision Engine` 最终裁决；`Eval Gatekeeper` 为必选证据输入（critical/high 不可跳过）。
7. 成本超限后的限流：`Quota/Billing Policy` 最终裁决；模型网关只执行降级路由。

硬规则：
1. 任一决策类型只允许一个 `final_decider`。
2. 非最终裁决模块不得直接改写最终结果，只能提交候选或建议。
3. 每次裁决必须产出 `decision_log`（含输入摘要、规则版本、结论、理由）。
4. 每次最终裁决必须产出 `decision_node_id` 并绑定到 `decision_graph_id`。
5. 高风险 run 在 `policy.eval -> approval.decision -> run.resume` 任一关键因果边缺失时，必须阻断执行。

## 4.7 可选能力启用边界（Feature Gate）

1. `A2A`：默认关闭；仅在跨平台协作需求明确且通过安全评审后开启。
2. `Graph DB`：默认关闭；仅在图查询收益经 Eval 证据证明后开启。
3. `DFS Cache for KB`：默认关闭；仅在 ingestion 吞吐瓶颈确认后开启，且不得作为 canonical。
4. `Multi-Agent`：默认关闭；仅当 `HandoffROI > 0` 且回归门禁通过后开启。
5. `Heterogeneous Scheduling`：默认关闭；仅在多硬件池并存且 SLO 受影响时开启。

统一要求：
1. 所有可选能力必须有 `feature_flag_id`、`owner`、`enable_scope`、`rollback_plan`。
2. 可选能力开启必须经 `dry-run + canary + gate pass` 三步，不允许直接全量。
3. 可选能力关闭不得影响 canonical 数据正确性，只允许影响性能或成本。

## 4.8 Core / Extended / Experimental 模块分层

Core（首发必须）：
1. Orchestrator + Worker runtime
2. Tool safety（幂等/未知结果/reconcile）
3. Context Compiler（唯一裁决）
4. Policy minimal（deny/allow/approval gate）
5. Audit + Execution Snapshot

Extended（第二阶段）：
1. Approval Domain 完整化
2. Scheduler advanced（WDRR/VIP/异构）
3. Eval Control Plane + Gatekeeper
4. Knowledge Base API + 检索优化

Experimental（隔离实验）：
1. Multi-agent advanced topology
2. JIT/graph retrieval
3. 自动策略学习与自动阈值调优

硬规则：
1. Experimental 不得直接依赖生产写路径。
2. Extended 功能失败不得拖垮 Core 主链路。
3. 每层必须有独立 kill switch。

## 4.9 实现语言选型（主语言 + 边界）

问题：
1. 平台核心链路同时要求高并发、低时延、强可观测、可维护，若语言过多会显著增加运维与治理成本。
2. 若把动态语言放到执行主链路，P95/P99 抖动与资源密度风险会放大。

方案：
1. 主语言：`Go`（控制面与执行面核心服务统一使用）。
2. `Go` 覆盖范围：Orchestrator/Worker、Scheduler、Policy Eval API、Model Gateway、Admission、Health/Placement、核心 API。
3. `TypeScript` 覆盖范围：控制台前端、运维/开发者 CLI、非关键路径管理服务（可选）。
4. `Python` 覆盖范围：离线/异步链路（评测样本加工、数据分析、实验型检索策略），不得进入主链路强依赖。

优点：
1. `Go` 在并发 IO、内存开销、部署密度、可观测生态方面适合 Agent Infra 主链路。
2. 以单主语言实现核心域，可降低 oncall、代码评审、故障排查与人员切换成本。
3. 保留 `Python` 边界后，既能保证主链路稳定，也不牺牲离线算法迭代效率。

缺点：
1. 团队需投入 Go 工程规范与性能调优能力建设。
2. 跨语言边界需要统一 IDL、版本管理和兼容测试。

依据：
1. 本平台首要目标是“可上线、可运维、可扩容”，而不是“语言多样性最大化”。
2. 与 D27 分层一致：Core 先稳，再在 Extended/Experimental 做语言级优化实验。

## 4.10 Decision Causality Graph（跨 Decider 因果链）

目标：显式表达 `Context -> Model -> Policy -> Approval -> Resume` 的决策因果，而不是仅靠隐式数据耦合。

最小结构：
```json
{
  "decision_graph_id": "dg_01",
  "nodes": [
    {"node_id": "n1", "type": "context.compile"},
    {"node_id": "n2", "type": "model.inference"},
    {"node_id": "n3", "type": "policy.eval"},
    {"node_id": "n4", "type": "approval.decision"},
    {"node_id": "n5", "type": "run.resume"}
  ],
  "edges": [
    {"from": "n1", "to": "n2", "kind": "depends_on"},
    {"from": "n2", "to": "n3", "kind": "influenced_by"},
    {"from": "n3", "to": "n4", "kind": "depends_on"},
    {"from": "n4", "to": "n5", "kind": "overrides"}
  ]
}
```

强规则：
1. 每个 `final_decider` 事件必须携带：`decision_graph_id + decision_node_id + parent_node_ids`。
2. `deny/require_approval` 等覆盖型结论必须写 `overrides` 边，禁止隐式覆盖。
3. 高风险 run 缺失关键因果节点时，默认 `fail_closed`。
4. Replay 必须可按图回放并还原最终决策路径。

## 4.11 Skill 注入架构边界

1. `Skill Registry`：管理 skill 元数据、签名、版本、适用范围与 owner。
2. `Skill Bundle Store`：存储可注入内容（prompt 片段、tool allowlist、retrieval profile、memory policy、eval tags）。
3. `Skill Injection Engine`：在 `PRE_CONTEXT_COMPILE/PRE_MODEL/PRE_TOOL/PRE_RESUME` 执行注入。

强规则：
1. Skill 注入不得改变 final decider 归属，不能绕过 Policy/Approval。
2. 非签名或越权 skill 必须被阻断并写 `skill.blocked_by_policy` 事件。
3. Skill 注入结果必须进入 decision graph 与审计证据包。

## 4.12 三层平面与外包边界（平台身份）

### Decision Plane（不可外包）

1. `Policy/Approval/Eval/Scheduler admission/Context final keep-drop` 等最终裁决。
2. 输出必须进入 `decision_log + decision_graph`。
3. 外部系统只能提供建议，不得直接产出最终放行结论。

### Execution Plane（可部分外包）

1. 可外包执行：队列、调度器、定时触发器、资源编排、模型 serving、检索执行、通知投递。
2. 外包执行必须通过平台签发 `dispatch_ticket` 执行，并回传 `execution_receipt`。
3. 外部执行失败只允许影响吞吐/时延，不得绕过平台裁决链。

### Evidence Plane（语义真值不可外包）

1. 真值对象：`execution_snapshot/decision_log/audit_evidence/usage_ledger/replay_input/decision_graph`。
2. 底层存储可托管；语义模型、校验逻辑、签名与可回放协议必须由平台掌控。
3. 任一争议裁决以 Evidence Plane 真值为准，不以执行器本地日志为准。

## 4.13 调度外包模型（执行外包，治理内生）

可外包执行侧：
1. 物理调度（placement、autoscaling、queue consume）。
2. 到点触发（cron/schedule service）。
3. 健康检测（NPD/DCGM/云监控/探针）。

平台内生治理侧：
1. `schedule_admit/reject`。
2. VIP 池资格与抢占资格。
3. 高风险任务阻断与审批缺失阻断。
4. parked run 恢复资格判定。

交换契约：
1. 平台 -> 外部执行器：`dispatch_ticket`（含风险级别、权限边界、过期时间、签名）。
2. 外部执行器 -> 平台：`execution_receipt`（含执行状态、资源信息、证据引用、幂等键）。
3. 平台收到 receipt 后必须做 `ticket-receipt` 一致性校验，不一致则阻断并审计。

## 4.14 Coupling Registry（跨域耦合注册表）

目标：把 Context/Skill/Policy/Approval/Eval 之间的强耦合显式化，避免“事件可见但依赖不可见”。

注册项最小字段：
1. `coupling_id`、`producer_domain`、`consumer_domain`。
2. `input_contract_ref`、`output_contract_ref`、`timing_constraint`。
3. `failure_semantics`（fail_closed/require_review/fail_soft）。
4. `owner`、`blast_radius_scope`、`rollback_plan_ref`。

强规则：
1. 未登记耦合不得进入生产发布。
2. 变更任一耦合契约必须产出 `coupling_impact_report`。
3. `critical/high` 耦合链路变更必须跑端到端回放回归。

---

## 5. Workflow / DSL v2（生产级）

## 5.1 节点类型

1. `llm`
2. `tool`
3. `retrieval`
4. `condition`
5. `loop`
6. `approval`
7. `callback`
8. `delay`
9. `compensation`
10. `end`

## 5.2 节点强制字段

1. `input_schema`
2. `output_schema`
3. `effect_type`：`pure|read|write|external_write|irreversible`
4. `required_permissions`
5. `approval_policy_ref`（高风险必填）
6. `compensation_ref`（副作用节点必填）
7. `idempotency_required`
8. `context_read_set`
9. `context_write_set`
10. `observability_tags`
11. `timeout_ms`
12. `retry_policy`
13. `fallback_policy`
14. `skill_profile_ref`（需技能增强时必填）
15. `skill_injection_policy_ref`（高风险节点必填）

## 5.3 发布前静态校验

1. Schema 连通性。
2. 权限越界。
3. 审批绑定完整性。
4. 补偿路径可达性。
5. Context 读写边界冲突。
6. 观测标签覆盖率。
7. 策略引用与版本锁定合法性。
8. skill 引用合法性（存在性、签名有效性、作用域匹配）。
9. skill 注入策略边界（是否越权修改 tool_scope/approval 约束）。

## 5.4 Side Effect 语义约束

1. `pure/read`：允许自动重试。
2. `write`：必须幂等或带 reconcile 方案。
3. `external_write/irreversible`：必须审批策略与证据快照。

## 5.5 Trigger 定义（定时 / 定时重复）

触发器为 workflow 顶层字段 `triggers[]`，支持以下模式：
1. `one_time`：一次性触发（`run_at`）。
2. `cron`：Cron 表达式触发（`schedule`）。
3. `interval`：固定间隔触发（`every_sec` + `anchor_time`）。
4. `calendar_rrule`：日历规则触发（`rrule`）。
5. `event_window`：事件到达且命中时间窗时触发（`event_filter + window`）。

字段定义（必填）：
1. `timezone`（IANA 时区）。
2. `misfire_policy`：`skip|fire_once|backfill_window`。
3. `concurrency_policy`：`allow|forbid|replace`。
4. `dedupe_key_template`（例如 `tenant:workflow:scheduled_time`）。
5. `enabled`。
6. `tier_class`：`vip|pro|standard|batch`。
7. `execution_pool`：`vip_dedicated|shared|batch_only`。

字段定义（条件必填）：
1. `run_at`（`one_time`）。
2. `schedule`（`cron`）。
3. `every_sec + anchor_time`（`interval`）。
4. `rrule`（`calendar_rrule`）。
5. `event_filter + window`（`event_window`）。
6. `backfill_window_sec`（当 `misfire_policy=backfill_window`）。

字段定义（可选）：
1. `start_at` / `end_at`。
2. `max_concurrency`（默认继承 tier 配额）。
3. `jitter_sec`（默认 `0`）。
4. `retry_policy`（默认使用系统模板）。
5. `pause_reason`（仅 `enabled=false` 时建议填写）。
6. `priority_class`（默认 `0`）。
7. `rate_limit_profile`（默认继承租户模板）。
8. `sla_fire_delay_ms`（默认继承系统 SLO）。

分阶段实现约束：
1. Phase 1 仅启用 `cron + dedupe + basic quota`。
2. `one_time/interval/rrule/event_window` 与 backfill 高级能力在 Phase 2 后逐步启用。

## 5.6 Trigger 静态校验

1. `timezone` 合法性校验。
2. `cron/rrule` 语法校验。
3. `end_at > start_at`。
4. `misfire_policy` 与 `backfill_window_sec` 组合合法。
5. `concurrency_policy` 与 `max_concurrency` 组合合法。
6. 重复触发窗口与 `dedupe_key_template` 冲突检查。
7. 高风险工作流的 schedule 必须绑定审批策略。
8. `tier_class` 与租户套餐匹配校验（普通租户不可声明 `vip`）。
9. `execution_pool=vip_dedicated` 仅允许具备 VIP 配置的租户。

## 5.7 SchedulerPolicy（调度策略对象）

```yaml
policy_id: sched.default.v3
global_qps_limit: 2000
tiers:
  vip:
    weight: 8
    reserved_concurrency: 200
    burst_qps: 300
  pro:
    weight: 3
    reserved_concurrency: 0
    burst_qps: 150
  standard:
    weight: 1
    reserved_concurrency: 0
    burst_qps: 80
  batch:
    weight: 1
    max_lag_sec: 1800
preemption:
  allow_pending_preempt: true
  allow_running_preempt_at_safe_point: true
  grace_sec: 20
aging:
  enable: true
  promote_after_sec: 60
heterogeneous:
  enabled: true
  resource_classes:
    - class_id: gpu.h100.80g
      selectors:
        accelerator_type: nvidia_h100
        min_gpu_memory_gb: 80
        min_nic_bandwidth_gbps: 100
      weight: 4
      reserved_concurrency: 40
    - class_id: gpu.l40s.48g
      selectors:
        accelerator_type: nvidia_l40s
        min_gpu_memory_gb: 48
      weight: 2
      reserved_concurrency: 20
  fallback_order:
    - gpu.h100.80g
    - gpu.l40s.48g
    - cpu.highmem
  cross_class_preemption:
    allow: false
    safe_boundary_only: true
```

## 5.8 执行约束与放置策略（Execution Constraints）

工作流或 schedule 可声明：
1. `resource_profile`：`cpu_only|gpu_required|network_sensitive|io_sensitive`。
2. `min_gpu_memory_gb`、`gpu_arch`、`allow_mig`。
3. `min_nic_bandwidth_gbps`、`max_network_jitter_ms`。
4. `fault_tolerance_class`：`strict|standard|best_effort`。
5. `placement_policy_ref`：引用放置策略（如“禁止 DEGRADED 节点”）。
6. `accelerator_type`：如 `nvidia_h100|nvidia_l40s|amd_mi300x|none`。
7. `interconnect_pref`：`nvlink|pcie|ethernet|any`。
8. `resource_class`：绑定 `heterogeneous.resource_classes[].class_id`。
9. `topology_hint`：`same_zone|same_rack|spread`。
10. `fallback_resource_classes[]`：异构降级候选（有序）。

静态校验：
1. `resource_profile` 与租户套餐匹配。
2. `fault_tolerance_class=strict` 不允许 `best_effort` 池。
3. 高风险 workflow 不能绑定 `best_effort` GPU 节点池。
4. `resource_class` 必须存在于 scheduler policy 的异构类定义。
5. `fallback_resource_classes[]` 不得包含比主类更高权限的资源池。

## 5.9 DSL v2 语法与能力上限（实施边界）

```ebnf
workflow      = header, version, nodes, edges, triggers?, scheduler_policy?, constraints? ;
node          = node_id, node_type, input_schema, output_schema, effect_type, permission_ref, timeout, retry, fallback ;
edge          = from_node, to_node, condition? ;
retry         = max_attempts, backoff_ms, jitter_ms ;
fallback      = none | node_ref | compensation_ref ;
triggers      = trigger, { trigger } ;
```

硬约束：
1. 图必须无隐式环；仅 `loop` 节点允许显式环，且必须声明 `max_iterations`。
2. `external_write|irreversible` 节点必须同时声明 `approval_policy_ref + compensation_ref + idempotency_required`。
3. `context_write_set` 仅允许写入声明字段，未声明字段写入一律拒绝。
4. 任一节点未声明 `observability_tags` 视为构建失败。
5. 节点间 schema 断裂禁止发布（禁止运行时“猜字段”）。

## 5.10 发布前验证流水线（DoD）

1. `dsl-lint`：语法、字段、引用完整性。
2. `schema-link-check`：节点输入输出契约连通。
3. `permission-check`：权限边界与策略映射校验。
4. `side-effect-check`：副作用节点的审批/补偿/幂等三件套校验。
5. `simulation-check`：至少通过 `happy_path + failure_path + timeout_path` 三类仿真。
6. 任一步骤失败即禁止进入发布审批。

### 5.10.1 Skill 注入声明（可编排）

```yaml
skill_injection:
  skill_profile_ref: "skill.customer_support.v3"
  phases:
    - PRE_CONTEXT_COMPILE
    - PRE_MODEL
  mode: "strict" # strict|best_effort
  allow_override_fields:
    - "llm.temperature"
  deny_override_fields:
    - "required_permissions"
    - "approval_policy_ref"
    - "effect_type"
```

执行规则：
1. `strict` 模式下 skill 注入失败即阻断当前节点；`best_effort` 仅在低风险节点可用。
2. Skill 允许覆盖字段必须在白名单内，黑名单字段禁止被 skill 修改。
3. Skill 注入结果必须写入 `decision_graph_id` 和 `audit_evidence_ref`。

## 5.11 依赖服务继承模型（Dependency Service Inheritance）

目标：在不复制大量配置的前提下，保证依赖服务绑定“可继承、可覆写、可审计、可回放”。

继承层级（由低到高）：
1. `platform_default`（平台基线）。
2. `tenant_profile`（租户基线）。
3. `project_profile`（项目基线）。
4. `workflow_declared_dependencies`（工作流声明）。
5. `node_override`（节点级覆写，最小粒度）。

依赖对象最小字段：
1. `service_id`
2. `service_type`（`llm_provider|tool_backend|vector_store|kb_connector|approval_adapter`）
3. `endpoint_ref`
4. `version_pin`
5. `auth_binding_ref`
6. `policy_profile_ref`
7. `qos_profile_ref`
8. `timeout_ms/retry_policy`

继承规则：
1. 缺省继承：上层未声明字段自动沿用下层最近值。
2. 白名单覆写：仅允许覆写 `endpoint_ref/version_pin/qos_profile_ref/timeout_ms/retry_policy`。
3. 禁止覆写：`auth_binding_ref` 与 `policy_profile_ref` 只能“收紧”，不能放宽。
4. 版本约束：上层可从 `^x.y` 收紧为 `x.y.z`，不得放宽主版本范围。
5. 冲突处理：同层冲突按 `priority`，同优先级按 `updated_at`，仍冲突则发布失败。

## 5.12 依赖解析与发布校验（可执行）

解析产物：`ResolvedDependencyGraph (RDG)`，必须写入发布制品并参与 Execution Snapshot。

编译期检查：
1. `service_id` 唯一性。
2. 引用完整性（`endpoint_ref/auth_binding_ref/policy_profile_ref` 必须存在）。
3. 版本兼容性（禁止跨主版本不兼容组合）。
4. 传递依赖环检测（有环直接拒绝发布）。
5. 权限边界校验（节点声明权限不得高于依赖服务策略上限）。

运行期规则：
1. run 启动时只读取 RDG 快照，不读取“最新配置”。
2. parked/pending run 恢复必须使用同一 `dependency_bundle_id`。
3. 配置热更新仅影响新 run；旧 run 需通过 `FORWARD_SAFE/MIGRATABLE` 才可升级。
4. 每次依赖解析写 `dependency.resolved` 事件，包含 `dependency_binding_hash`。

## 5.13 依赖变更影响面分析（Blast Radius）

1. 任一 `tenant_profile/project_profile` 变更必须先跑 impact analysis。
2. 分析输出至少包含：
3. `affected_workflows_count`
4. `affected_nodes_count`
5. `affected_risk_tier_distribution`
6. `affected_active_runs_count`
7. `requires_manual_review`（当高风险受影响超阈值）

计算规则：
1. 影响面 = 所有继承链命中该 profile 且解析结果变化的 workflow/node 集合。
2. 仅文本变更但解析结果不变，不计入 blast radius。
3. 高风险 workflow 受影响时，强制进入人工审核并限制灰度比例。

发布强约束：
1. 无 `blast_radius_report` 不得发布依赖变更。
2. 报告中 `affected_high_risk_nodes > threshold` 时，禁止全量，只允许 canary。

---

## 6. 工具调用运行时：等待、休眠、资源退让

## 6.1 决策规则

```text
if est_latency <= 500ms and effect_type in {pure, read}: sync wait
elif est_latency <= 5s: async dispatch + park
else: callback mode + park
```

## 6.2 模型与 Worker 资源策略

1. 工具等待期间不保留推理会话占位，不“空转等待模型”。
2. run park 后释放 Worker 执行槽位。
3. 恢复时基于 continuation token 重新构建下一轮上下文。
4. 仅保留最小恢复状态：`run_id/step_id/tool_call_id/idempotency_key/step_version`。

## 6.3 park/resume 状态机

```text
RUNNING -> TOOL_DISPATCHED -> PARKED -> TOOL_DONE -> RESUMED -> COMPLETED/FAILED
```

## 6.4 未知结果（超时但可能已成功）

1. 禁止直接重试非幂等写。
2. 先探测目标系统状态（read-after-write）。
3. 未确定时进入 reconcile 队列。
4. reconcile 超阈值后转人工。

## 6.5 副作用证据

对 `external_write/irreversible` 强制落盘：
1. before snapshot
2. request hash
3. response hash
4. after snapshot
5. compensation plan

---

## 7. Context Management v2（边界清晰）

## 7.1 组件边界

### Candidate Budget Gate（编译前准入）

职责：候选入闸准入、源级配额裁剪、租户预算约束、强退化触发。

禁区：不做最终排序、不做冲突仲裁、不做上下文注入。

### Context Compiler（唯一决策者）

职责：候选聚合、权限过滤、排序、裁剪、冲突终裁、注入编排、理由日志。

禁区：不做索引维护、不写长期记忆、不输出引用展示。

### RAG Service

职责：文档摄取、切分、索引、检索、重排，输出候选证据。

禁区：不做最终注入决策。

### Memory Service

职责：记忆生命周期、写入准入、反证降级、纠错替换，输出记忆候选。

禁区：不做最终排序，不做权限判定。

### Citation Service

职责：引用句柄映射、证据链结构化、面向用户/审计的引用输出。

禁区：不参与排序裁剪，不做记忆写入。

### Skill Injection Engine（技能注入执行器）

职责：按 `skill_bundle + policy` 在指定 phase 注入技能片段并留痕。

禁区：不得修改 `required_permissions/effect_type/approval_policy_ref`，不得充当 final decider。

## 7.2 候选统一契约

```json
{
  "candidate_id": "uuid",
  "source": "session|rag|memory|tool",
  "source_ref": "doc:12#chunk:3",
  "tenant_id": "t_001",
  "content": "...",
  "token_estimate": 140,
  "relevance_score": 0.82,
  "authority_score": 0.90,
  "freshness_ts": "2026-04-02T10:00:00Z",
  "confidence": 0.88,
  "security_level": "internal",
  "policy_tags": ["finance"],
  "citation_handle": "cit_abc",
  "budget_group": "rag",
  "is_mandatory": false
}
```

## 7.3 动态 Token Budget

目标函数：

```text
maximize Utility = a*Quality - b*Latency - c*TokenCost - d*Risk
```

动态信号：
1. 检索命中质量。
2. 任务复杂度。
3. 历史失败类型。
4. 模型窗口与成本约束。
5. 风险等级。

裁剪顺序：低可信 -> 低相关 -> 过期 -> 冗余 -> 长尾低收益。

硬规则：
1. 系统安全策略上下文不裁剪。
2. 每次裁剪写 `context.trimmed` 事件与理由。

## 7.4 冲突仲裁矩阵

1. 全局安全策略 > 租户策略（租户只可收紧）。
2. 特例规则 > 通用规则（必须命中作用域和有效期）。
3. 同权威源：新版本 > 旧版本。
4. 多源冲突：`authority + freshness + consistency_vote` 联合打分。
5. 无法裁决：输出不确定标记并触发澄清/人工。

## 7.5 Prompt Injection 防护（能力边界化）

1. 数据与指令通道分离：不可信文本不得进入 instruction channel。
2. 能力防火墙：工具权限由策略引擎裁决，不由上下文文本裁决。
3. 高风险动作必须满足：`policy allow + approval token`。
4. 非可信上下文最多影响“建议内容”，不得直接放宽 `required_permissions`、`effect_type`、`approval_policy_ref`。

## 7.6 Context Compiler 工程纪律（可复现、可回放、可回归）

1. 编译输入固定：同一 `run_id/step_id` 的候选集合、策略快照、预算配置、模型配置必须可版本化追溯。
2. 编译输出固定：输出 `compile_fingerprint`（候选哈希 + 规则版本 + 裁剪轨迹哈希）。
3. 决策分层：关键决策逻辑必须 deterministic；允许非确定性的步骤仅限“低风险摘要增强”，且必须标记 `nondeterministic=true`。
4. 回放接口：`POST /v1/context/compile/replay`，输入 `compile_fingerprint` 后必须产出同等裁剪结果（deterministic 区）。
5. 画像落盘：每次编译写 `compiler.profile`，至少包含 `candidate_count`、`trim_ratio`、`conflict_count`、`compile_ms`、`token_alloc_by_source`。
6. 回归套件：每次编译器发布必须跑 `golden_compile_suite`，要求 `deterministic_diff_rate <= 0.1%`（仅统计 deterministic 区）。

## 7.7 Context Compiler 系统级保障（P0 防线）

## 7.7.1 deterministic boundary（细化）

1. 必须 deterministic：
2. 候选过滤、排序、裁剪、冲突仲裁、权限过滤、注入顺序。
3. 可 non-deterministic（受限）：
4. 仅低风险摘要压缩与同分候选 tie-break，且必须写 `nondeterministic_reason`。

## 7.7.2 compiler 版本语义

1. `major`：不兼容变更（候选契约、冲突规则、裁剪语义改变）。
2. `minor`：兼容且 deterministic-preserving 的优化。
3. `patch`：缺陷修复，不改变语义输出。

```yaml
compiler_versioning:
  major: incompatible
  minor: deterministic-preserving
  patch: bugfix-no-semantic-change
```

## 7.7.3 多版本共存策略

1. 新 run 走最新可用 compiler 版本。
2. 运行中 run 固定 `context_compiler_version`。
3. canary 可并存 `N` 个版本，但同一 run 仅允许一个版本。

## 7.7.4 compiler rollback 机制

1. 触发条件：`compile_error_rate`、`deterministic_diff_rate`、`context_regression_rate` 任一超阈值。
2. 回滚动作：立即切回上一个稳定版本并冻结新版本流量。
3. 回滚后要求自动触发 replay 对比与根因分析。

## 7.7.5 compiler failure policy

```yaml
compiler_failure_policy:
  - fallback_to_previous_version
  - degrade_to_minimal_context
  - block_execution_high_risk
# 等价语义：block_execution (high risk)
```

执行规则：
1. 低风险：`degrade_to_minimal_context` 可继续执行并打不确定标记。
2. 中风险：优先回退上版本，失败则 `require_review`。
3. 高风险：无法得到可信编译结果时必须 `block_execution`。

## 7.8 Context Compiler 资源预算与退化策略

## 7.8.1 单次 compile 预算

1. 候选上限（硬上限）：`candidate_hard_cap = 1000`。
2. 预算上限（软预算）：`candidate_soft_cap = 500`（超过即触发预裁剪）。
3. CPU 预算：`compile_cpu_p95 <= 600ms`，硬超时 `1200ms`。
4. 内存预算：单次 compile 工作内存 `<= 512MB`，硬上限 `768MB`。
5. Token 预算：注入前上下文 token 必须满足步骤预算，不得因 compile 超发。

## 7.8.2 超预算硬退化

1. 候选超软预算：先执行“权限过滤 -> 高风险保留 -> 去重 -> 低可信裁剪”。
2. 候选超硬上限：直接截断到 `candidate_hard_cap`，并写 `context.compile_over_cap` 事件。
3. CPU 接近硬超时：跳过非关键增强（摘要压缩、次级 rerank）进入最小可用编译路径。
4. 内存超阈值：触发 `degrade_to_minimal_context`，仅保留安全策略 + 高权威证据 + 最近会话窗口。
5. 高风险任务在硬退化后仍不满足最小可信上下文时：`block_execution_high_risk`。

## 7.8.3 compile 队列隔离

1. `compile_queue` 与主执行 `run_queue` 物理隔离（独立 worker 池）。
2. 资源配额：compile 池默认占执行集群 `20%-30%`，不得抢占主链路最低保障容量。
3. 优先级：高风险 compile 请求优先；低风险请求在拥塞时允许排队降级。
4. 过载策略：当 `compile_queue_lag` 超阈值时，先降级低风险 compile，再削减 replay/eval compile 任务。
5. 禁止策略：不得用 replay/eval compile 作业挤占在线交互 compile 的保底配额。

## 7.9 Candidate Budget Gate（编译前预算闸门）

目标：在进入 compiler 前完成候选 admission control，避免多源候选同时爆发拖垮 compile。

预算模型：
1. 全局硬上限：`candidate_ingress_hard_cap = 1200`（入闸后强截断）。
2. 编译入口上限：`compiler_input_cap = 500`（交给 compiler 的目标规模）。
3. 源级默认上限：
4. `session <= 120`
5. `rag <= 260`
6. `memory <= 120`
7. `tool <= 60`
8. `system/policy <= 40`（保留槽位，不参与普通竞争）

入闸顺序：
1. 权限过滤（不满足 security/policy 的候选先剔除）。
2. 源级限额裁剪（按 `source_cap` 和租户配额裁剪）。
3. 风险重排（高风险任务提高权威源配额、压缩低权威源）。
4. 去重与新鲜度裁剪（`is_mandatory=true` 候选不可在该步丢弃）。
5. 输出 `compiler_input_cap` 规模候选集。

过载退化：
1. 超 `candidate_ingress_hard_cap`：丢弃低权威低相关尾部候选并写 `context.candidate_dropped`。
2. 低风险任务可启用 aggressive trim；高风险任务必须保留 `policy/system + high_authority` 最小集合。
3. Gate 异常时：低风险允许降级到最小上下文；高风险 `fail_closed`。

可观测与审计：
1. 每次入闸产出 `candidate_gate_profile`：`ingress_count`、`dropped_by_source`、`kept_by_source`、`gate_ms`。
2. 入闸决策必须写入 `decision_graph` 节点 `context.candidate_gate` 并与后续 `context.compile` 建边。

## 7.10 上下文前链路风险治理（Gate -> Compiler -> Skill）

前链路定义：
1. `Candidate Budget Gate -> Context Compiler -> Skill Injection -> Model`。

已知风险：
1. 前链路模块增多后，性能优化与故障定位复杂度上升。
2. 任一环节失败都可能导致高风险路径 `fail_closed` 频率上升。
3. 多模块版本联动易形成隐式回归。

治理规则：
1. 前链路发布必须提供端到端 `stage_latency_breakdown`（gate/compile/skill/model_prefill）。
2. 前链路每个 stage 必须具备独立降级策略与独立 kill switch。
3. 高风险链路若前链路失败率异常，不允许通过“放宽策略”降压，只允许容量扩展或回滚。
4. 前链路回放必须固定：`candidate_gate_profile + compile_fingerprint + skill_bundle_set_hash`。

---

## 8. Memory v2（可写、可证伪、可纠错）

## 8.1 类型

1. Working
2. Episodic
3. Semantic
4. Procedural
5. Policy

## 8.2 准入证据（按类型差异化）

1. Episodic：任务质量达标且无关键违规。
2. Semantic：至少一个高可信来源证据。
3. Procedural：多次复现成功并通过回归。
4. Policy：必须经治理流程批准。

## 8.3 状态机

```text
CANDIDATE -> ACTIVE -> SUSPECT -> REVOKED -> ARCHIVED
```

## 8.4 反证与纠错流程

1. 触发：新证据冲突、连续错误归因、来源失效。
2. 处置：先降级 `SUSPECT`，避免继续污染。
3. 复核：回归通过恢复 `ACTIVE`，否则 `REVOKED`。
4. 修复：生成替代记忆候选并回归验证后替换。

---

## 9. RAG v2（业务特征化）

## 9.1 文档类型策略

1. 政策：按章-条-款切分。
2. API：按 endpoint/参数/错误码切分。
3. SOP：按步骤与决策点切分。
4. 表格：先结构化抽取后索引。
5. FAQ：按问答对与意图标签切分。

## 9.2 检索流程

1. 混合召回（向量 + 关键词）。
2. 重排（cross-encoder 或规则）。
3. 语义去重。
4. top-k + budget 裁剪。

## 9.3 低置信与高风险联动

1. 高风险建议无证据时：拒绝执行或降级为人工确认。
2. 低置信回答必须输出引用不足标识。

## 9.4 知识缺口闭环

1. 记录 `rag.no_hit` 与低置信画像。
2. 进入 Knowledge Gap Backlog。
3. 补档后触发增量索引。
4. 跟踪补档前后命中率与答复质量变化。

## 9.5 高效检索执行路径（在线）

1. Query Planner：先判定任务类型（事实问答/流程问答/代码问答/报表分析）。
2. Scope Filter：按租户、权限、文档属性先做硬过滤，缩小候选空间。
3. 快速路径：hybrid retrieval（BM25/稀疏 + 向量）召回 top-N。
4. 分层重排：先轻量 rank，再对前 K 条做高精度 rerank。
5. 低置信兜底：触发 query rewrite（多查询/HyDE）与 JIT 检索。
6. 结果合并：使用 RRF 或加权融合去除单一路径偏置。
7. Context Compiler 最终打包：去重、冲突消解、预算裁剪、引用绑定。

## 9.6 知识库摄取与索引（离线）

1. Canonicalization：文档统一转为规范文本与结构化块（标题、段落、表格、字段）。
2. 按文档类型切分：policy/API/SOP/table/FAQ 使用差异化 chunk 模板。
3. 元数据抽取：租户、领域、版本、生效时间、权限标签、置信来源。
4. 多索引写入：keyword/sparse、dense vector、可选 graph index 并行构建。
5. 增量更新：仅重建受影响分片，保留版本快照可回滚。
6. 批处理优先：高吞吐批量入库，降低写入争用和索引抖动。
7. 原文来源固定：chunk 必须可追溯到 `object_uri + version_id + content_hash`，禁止“仅缓存无原文”入索引。

## 9.7 检索效率与上下文质量优化

1. 位置策略：关键证据优先放在上下文头尾，缓解“中间遗忘”。
2. 多样性约束：同义冗余文段做聚类去重，提升 token 利用率。
3. 缓存分层：query cache、embedding cache、passage cache，按 TTL 与热度淘汰。
4. 动态 K：根据问题复杂度与预算动态决定召回深度。
5. 延迟预算：检索链路与重排链路分别设 `P95` 预算，超时自动降级轻量策略。

## 9.8 失败模式与降级

1. `no_hit`：触发 query rewrite 与知识缺口工单。
2. `low_confidence`：降级回答粒度并要求引用或人工确认。
3. `retrieval_timeout`：降级为快速路径结果 + 明确不确定性标记。
4. `index_staleness`：命中过期索引时强制拉取最新版本或拒绝高风险动作。

## 9.9 当前实现优先级（从现在可落地到高级）

1. L1（必选）：`hybrid retrieval + metadata filter + rerank + 引用绑定 + no_hit/low_confidence 事件闭环`。
2. L2（可选）：`query rewrite`、动态 `K`、检索缓存、文档类型分桶阈值。
3. L3（实验）：JIT 检索工具链、graph/层次检索、自动阈值调优、HyDE 变体。

强制启用规则：
1. 未完成 L1，禁止上线任何 L2/L3 特性。
2. L2 启用必须通过 `quality_gain` 与 `latency_budget` 双门禁。
3. L3 仅允许在实验租户或 canary scope，禁止直上全量。
4. L3 默认不参与高风险动作决策路径。

## 9.10 知识库存储选型（对象 vs 分布式文件）

1. 主存储：对象存储（必须）。
2. 可选缓存：分布式文件系统（仅预处理/批量索引中间态）。
3. 禁止模式：仅把分布式文件系统作为唯一真值源。
4. 适用规则：
5. 文档留存、审计、版本回放、跨区容灾 -> 走对象存储。
6. 高吞吐 OCR、批量解析、向量化临时中间文件 -> 可落分布式文件缓存。
7. 失效策略：DFS 缓存失效可重建；对象存储不可替代。

---

## 10. Citation Service（引用充分性）

1. 关键结论必须绑定 `citation_handle`。
2. 输出层支持“强制引用模式”。
3. 审计视图保留 claim->evidence 映射。
4. 指标：`claim_coverage`、`unsupported_claim_rate`。

---

## 11. Policy as Code（实施规范）

实施级引用：
1. 详细执行规范见 [policy-spec.md](/home/wandering/learn/agent/doc/specs/policy-spec.md)。
2. 主文档负责架构边界；字段语义、时机、失败行为、幂等语义由子规范定义。

## 11.1 双层模型

1. 业务层 DSL：治理人员可审查、可签署、可回滚。
2. 执行层引擎：Rego/CEL（或等价表达式引擎），负责低时延判定。
3. 规则：业务 DSL 只能编译到受限执行子集，禁止“运行时任意代码”。

## 11.2 Policy DSL v1 语法边界

```ebnf
policy      = header, scope, effect, priority, when, obligations?, exceptions?, validity ;
scope       = "global" | "tenant" | "project" | "workflow" | "step" ;
effect      = "allow" | "deny" | "require_approval" | "require_review" ;
when        = expr ;
expr        = term, { ("&&" | "||"), term } ;
term        = field, op, value | "in_set(", field, ",", set_id, ")" ;
op          = "==" | "!=" | ">" | ">=" | "<" | "<=" | "matches" ;
obligations = obligation, { ",", obligation } ;
obligation  = "attach_tag" | "require_template" | "limit_param" | "emit_audit" ;
validity    = "effective_at", "expires_at" ;
```

硬限制：
1. 表达式深度 `<= 8`，单条规则条件项 `<= 32`。
2. 禁止循环、递归、外部网络调用、动态 `eval`。
3. 参数约束仅允许白名单字段（如 `amount`、`currency`、`tool.name`、`risk_score`）。
4. 每条 policy 必须声明 `owner`、`reviewer_group`、`ticket_ref`。

## 11.3 决策与冲突求解

执行顺序：
1. 取有效规则：按 `scope + time + status=active` 过滤。
2. 条件求值：对每条规则产出 `matched=true/false`。
3. 冲突求解：`deny > require_approval > require_review > allow`。
4. 同 effect 冲突：小作用域优先，再按 `priority`，再按 `updated_at`。
5. 产出决策：`decision + obligations + matched_rule_ids + trace_id`。

## 11.4 Obligation 执行时机

1. `PRE_DISPATCH`：run 创建后、节点执行前（例如风控标签注入）。
2. `PRE_TOOL`：工具调用前（例如参数收敛、模板审批校验）。
3. `POST_TOOL`：工具返回后（例如副作用证据归档）。
4. `PRE_RESUME`：park 恢复前（例如审批 token 二次校验）。
5. obligation 必须幂等，失败行为受 `fail_mode` 控制（见 11.5）。

## 11.5 fail-closed / fail-soft 判定矩阵

1. 条件：`risk_tier=critical|high` 且 `effect_type in {write, external_write, irreversible}` -> `fail_closed`。
2. 条件：`risk_tier=medium` 且读写混合 -> `require_review` + `audit_required`。
3. 条件：`risk_tier=low` 且 `effect_type in {pure, read}` -> `fail_soft`（降级执行）+ 强审计。
4. 策略超时/异常默认不允许提权：只能“拒绝或收紧”，不能“放行”。

## 11.6 Policy Bundle（发布、签名、回放）

Bundle 结构：
1. `bundle_id`
2. `dsl_version`
3. `compiler_version`
4. `policies[]`
5. `sha256`
6. `signature`
7. `created_by/created_at`

发布流程：
1. `lint -> unit_test -> shadow_eval -> canary_eval -> promote`。
2. `shadow_diff_rate > threshold` 禁止 promote。
3. 所有 bundle 必须可下载并可回放：`POST /v1/policies/bundles/{id}:replay`。
4. 运行中 run 使用 Execution Snapshot 绑定 bundle，禁止隐式切换。

## 11.7 性能预算与退化策略

1. Inline eval：`P95 <= 10ms`，`P99 <= 20ms`。
2. Remote eval：`P95 <= 50ms`，`P99 <= 120ms`。
3. 缓存键：`tenant + policy_bundle_id + action + resource_class`。
4. 缓存未命中或引擎抖动时，按 11.5 矩阵退化，不允许绕过策略判定。

## 11.8 迁移与误伤治理

1. 必跑 shadow evaluate：新旧决策并行对比 `>=7` 天或 `>=100k` 样本。
2. 差异分析必须分桶：租户、动作类型、风险等级、工具类型。
3. 关键误伤（高价值合法请求被拒）超阈值时自动回滚至上一个 bundle。
4. 每次迁移必须生成 `policy_change_evidence_pack`（差异、风险、回滚路径、签字记录）。

---

## 12. 审批域（HITL Domain）

## 12.1 域模型

1. 审批模板
2. 审批路由（角色、组织、值班）
3. 会签/或签/n-of-m
4. 代理审批
5. SLA 与超时升级
6. 证据包归档检索

## 12.2 审批证据包

1. run/step/tenant
2. 风险评分与命中策略
3. 拟执行动作与副作用评估
4. before snapshot
5. 引用证据
6. 推荐与替代动作

## 12.3 恢复绑定

1. 审批通过签发 `approval_token`。
2. resume 必须校验 `approval_token + step_version`。
3. 防止审批结论与执行上下文错绑。

## 12.4 定时任务审批策略

1. 高风险 schedule 支持两种模式：`per_run_approval`、`pre_approval_with_ttl`。
2. `pre_approval_with_ttl` 到期后自动降级为 `paused`。
3. 修改 schedule 的关键字段（触发频率、作用范围、风险等级）必须重新审批。

## 12.5 Policy 与 Approval 优先级边界

1. `policy deny` 为最高优先级，审批通过也不得放行。
2. `policy require_approval` 时，未获得有效 `approval_token` 必须阻断执行。
3. `approval_token` 仅证明“人工同意”，不等于“策略放行”，执行前仍需再次 policy eval。
4. 审批系统不得直接修改 `required_permissions/effect_type`，仅能返回 `approve|reject|request_changes`。
5. 任何“审批通过但策略拒绝”的案例必须落 `policy_approval_conflict` 事件并进入治理复盘。

---

## 13. 多 Agent 平台能力与调度层

## 13.1 平台能力（协议层）

1. Agent Identity Registry：`agent_id/type/version/trust_level/policy_profile`。
2. Capability Registry：`capabilities/tool_scope/data_scope/risk_level`。
3. Handoff Contract：`intent/output_schema/constraints/trace_context`。
4. Shared Memory Scope：Private/Team/Tenant/Global-RO。
5. Delegation Policy：限制委派深度、跨域、跨租户。

## 13.2 调度层（运行时层）

组件：
1. Coordinator Scheduler
2. Specialist Selector
3. Budget Broker
4. Branch Arbiter
5. Merge Resolver

决策函数：

```text
HandoffScore = wq*ExpectedQualityGain - wl*LatencyOverhead - wc*TokenCost - wr*RiskPenalty
```

触发条件：
1. `HandoffScore > 0`
2. 预算满足
3. 策略许可

调度硬规则：
1. `max_parallel_handoff` 按 workflow 与 tenant 双重限制。
2. 单 run 的 `branch_token_budget` 与 `branch_latency_budget_ms` 不得突破父 run 预算上限。
3. Coordinator 必须在每次 handoff 前写 `handoff.decision`（含输入特征、预测收益、预算快照）。

## 13.3 并行与预算治理

1. `max_branch_count`
2. `max_branch_tokens`
3. `max_branch_latency_ms`
4. `max_branch_cost_usd`

提前终止条件：
1. 达到目标置信度
2. 连续无增益
3. 超预算
4. 风险升级

## 13.4 冲突合并

1. 输入：`branch_outputs[]`，每项包含 `schema_valid`、`confidence`、`evidence_refs`、`policy_flags`。
2. Step 1：先过滤 `schema_invalid` 或 `policy_blocked` 分支。
3. Step 2：结构化字段按 `field_merge_strategy` 合并（`override|max_confidence|majority_vote`）。
4. Step 3：文本冲突按“证据覆盖率 > 置信度 > 新鲜度”排序；仍冲突则交 reviewer agent。
5. Step 4：高风险动作若存在冲突，强制进入审批，不允许自动执行。
6. 产出：`merge_result + merge_report`，必须可回放。

## 13.5 Specialist Selector 学习与更新

1. 选择目标：在满足策略与预算前提下最大化 `ExpectedQualityGain`。
2. 特征：任务类型、历史成功率、延迟分布、成本分布、失败类型、租户偏好。
3. 更新策略：每日离线重训 + 在线多臂老虎机微调，保留 `champion/challenger` 双模型。
4. 安全阈值：挑战者上线需满足 `quality_non_inferior` 且 `risk_not_worse`。

## 13.6 分支取消与结果归一

1. 取消触发：任一分支达到“可接受质量阈值”且其余分支边际收益为负。
2. 取消触发：全局预算耗尽。
3. 取消触发：风险升级或策略收紧。
4. 取消语义：优先 `cancel_pending`，运行中分支仅在安全边界取消。
5. 归一规则：被取消分支保留部分证据但不得作为最终可执行动作依据。

## 13.7 可观测与追踪

1. 每个 branch 必须继承主 trace，并新增 `branch_id`。
2. handoff、merge、cancel、budget 事件必须落 Event Log。
3. 提供 `GET /v1/runs/{run_id}/multi-agent-trace` 一键回放。

## 13.8 互操作（可选）

1. MCP：工具与资源接入标准。
2. A2A：跨平台 agent 协作标准。
3. 平台提供 Protocol Gateway 做版本与安全隔离。
4. A2A 默认关闭，启用需受 `feature_flag` 与跨域策略约束。
5. 互操作层不得绕过本地 Policy/Approval，外部 agent 返回仅作候选输入。

## 13.9 Merge Contract（严格语义）

```yaml
merge_contract:
  deterministic_fields:
    - action.type
    - action.target
    - action.parameters
    - risk_tier
  non_deterministic_fields:
    - rationale_text
    - optional_suggestions
  conflict_policy:
    action.type: deny_on_conflict
    action.target: require_review
    action.parameters: strict_intersection
    rationale_text: best_evidence_wins
```

强规则：
1. 动作字段冲突优先按 `conflict_policy` 处理，不允许文本覆盖动作。
2. `partial_success` 合并必须保留 `failed_branches[]` 与失败原因，不得静默丢弃。
3. 合并必须满足 causal consistency：仅允许合并同一父任务版本和同一输入快照分支。
4. 不满足因果一致性的分支必须丢弃并写 `merge.causal_mismatch` 事件。

---

## 14. Agent-native Runtime Data Architecture

## 14.1 逻辑数据域

1. Run State Store：事务态。
2. Immutable Event Log：不可变执行事件。
3. Audit Evidence Store：审计证据。
4. Policy Decision Log：策略判定轨迹。
5. Decision Causality Graph Store：跨 decider 因果图。
6. Context Cache：hot/warm/cold。
7. Retrieval Feature Store：检索特征。
8. Eval Warehouse：评测仓。
9. Billing Ledger：计量账本。
10. Memory Graph + Evidence Graph。
11. Skill Bundle Store：技能包与签名元数据。
12. Artifact Canonical Store：大对象规范存储。
13. Schedule Store：触发器配置与状态。
14. Trigger Log：触发历史、miss、回填与去重记录。
15. Node Registry：节点清单、能力标签、可用区与实例画像。
16. Health Signal Store：健康信号、故障证据、状态变更历史。

## 14.2 每个域是否必要

1. Run State Store 必要：恢复与当前执行真值来源；消费者为 Orchestrator/Resume Worker。
2. Immutable Event Log 必要：replay、调试、闭环学习；消费者为 Replay/Eval/Self-heal。
3. Audit Evidence Store 必要：合规取证与责任追溯；消费者为审计与法务。
4. Policy Decision Log 必要：解释“为何放行/拒绝”；消费者为风控复盘与误伤分析。
5. Decision Causality Graph Store 必要：解释跨 decider 最终结论；消费者为审计、复盘与自动治理。
6. Context Cache 必要：低时延上下文拼装；消费者为 Context Compiler。
7. Retrieval Feature Store 必要：检索调参与回归；消费者为 Retrieval Planner/Rerank Tuner。
8. Eval Warehouse 必要：发布门禁与漂移治理；消费者为 Gatekeeper。
9. Billing Ledger 必要：预算、计费、争议对账；消费者为运营与财务。
10. Memory Graph + Evidence Graph 必要：记忆可证伪、可纠错；消费者为 Memory Service。
11. Skill Bundle Store 必要：skill 注入可追溯与可回滚；消费者为 Skill Injection Engine。
12. Artifact Canonical Store 必要：大 payload 规范存储与 claim-check；消费者为 Worker/审计。
13. Schedule Store 必要：触发器真值与状态恢复；消费者为 Scheduler。
14. Trigger Log 必要：漏触发/重复触发分析与回填审计；消费者为调度治理。
15. Node Registry 必要：放置决策和容量画像；消费者为 Placement/Scheduler。
16. Health Signal Store 必要：故障隔离、灰度回流依据；消费者为 Isolation Controller/Scheduler。

## 14.3 物理落盘建议

1. PostgreSQL：run/policy/approval 元数据。
2. Redis：hot cache、短状态、锁。
3. Object Storage：artifacts/snapshots。
4. Kafka：event log。
5. ClickHouse：event/usage/eval 分析。
6. Vector DB：检索索引。
7. Graph DB（可选）：记忆证据图查询。
8. Decision Graph Store 默认采用 PostgreSQL（事务一致）+ ClickHouse（分析查询）。
9. Skill Bundle Store 默认采用 Object Storage（bundle）+ PostgreSQL（索引与签名元数据）。
10. Schedule Store 默认采用 PostgreSQL（强一致 + 索引查询）。
11. Trigger Log 默认采用 ClickHouse（高吞吐分析）+ Kafka（事件流）。
12. Node Registry 默认采用 PostgreSQL（强一致）。
13. Health Signal Store 默认采用 Kafka + ClickHouse（高吞吐时序分析）。
14. Knowledge Base 原文默认采用 Object Storage（canonical + versioned + lifecycle）。
15. 分布式文件系统仅用于 ingestion scratch space 与批处理缓存，不做 canonical。
16. 索引重建必须从 Object Storage 拉取原文，不依赖 DFS 缓存命中。

## 14.3.1 必选与可选边界

1. 必选域：`PostgreSQL + Redis + Object Storage + Kafka + Vector DB`。
2. 条件必选：`ClickHouse`（当需大规模事件/计费分析时必须启用）。
3. 可选域：`Graph DB`、`DFS Cache`（仅在收益证据成立时启用）。
4. 禁止将可选域提升为真值源；真值仍由 `Run State/Object Storage/Policy Bundle` 承担。

## 14.4 Claim-Check

1. 大 payload 不入事务表和主事件体。
2. 统一传递 `artifact_ref + hash + schema_version`。

## 14.5 数据来源治理矩阵（决策依据全覆盖）

每个数据来源必须定义以下元数据并纳入审计：
1. `source_owner`（谁对正确性负责）。
2. `decision_consumers`（哪些决策依赖该源）。
3. `freshness_sla`（可接受时效）。
4. `quality_checks`（完整性/一致性/异常值校验）。
5. `fallback_mode`（该源不可用时如何退化）。

默认要求：
1. 用于高风险决策的数据源必须具备 `lineage_id + schema_version + checksum`。
2. 缺失 lineage 的数据不得用于 `write/external_write/irreversible` 决策。
3. `freshness_sla` 违约时自动降级：高风险 fail-closed，中低风险 require-review。
4. 每次关键决策写入 `decision_input_manifest`，记录所用数据源版本与时间戳。

## 14.6 知识库文档存储一致性规则

1. `document_id` 的真值记录在元数据表，必须绑定 `object_uri/version_id/content_hash`。
2. DFS 缓存块必须携带 `source_hash`，不匹配时直接失效重拉。
3. 索引构建任务必须校验 `index_input_hash == object_content_hash`。
4. 对象存储版本回滚后，必须触发受影响索引分片重建。
5. DSAR 删除以对象存储版本为主驱动，向索引与缓存传播 tombstone。

---

## 15. 一致性模型与生命周期

## 15.1 一致性级别

1. Run State：强一致。
2. Event Log：至少一次投递 + 幂等消费。
3. Cache：最终一致。
4. Audit：写后不可变。
5. Memory Graph：准入流程内最终一致。
6. 审计归属边界：`Audit Evidence Store` 是合规真值，`Event Log` 仅作运行时事件分析。
7. 争议处理边界：账单/合规争议以 `Audit + Ledger` 为准，不以 `Event Log` 单独裁决。

## 15.2 Outbox 模式

1. 状态事务提交。
2. outbox 可靠发布事件。
3. 消费端用 `event_id + step_version` 幂等。

## 15.3 Replay 语义

1. 可 replay：纯读节点、幂等写节点。
2. 禁 replay：unknown outcome 的非幂等写节点。

## 15.4 Artifact GC

1. 引用计数 + 保留期双条件。
2. 命中 legal hold 禁止回收。

## 15.5 DSAR 删除传播

1. 事务态删除/匿名化。
2. 对象存储删除 artifact。
3. 向量索引删除 embedding。
4. 记忆节点 tombstone。
5. eval 样本替换或移除。
6. 生成删除完成证明。

## 15.6 缓存失效与重建

1. Hot：短 TTL + 事件失效。
2. Warm：定时重建 + 按需刷新。
3. Cold：归档只读，回放时 hydrate。

## 15.7 Memory / Evidence 写入事务边界

1. `memory_node` 与其 `evidence_edge` 必须同事务提交，禁止“只写记忆不写证据”。
2. 事务失败时两者都回滚，不允许半提交。
3. 跨存储写入采用 Outbox 补偿，最终状态由 `memory_commit_id` 对齐。

## 15.8 Audit 防篡改

1. Audit 记录采用哈希链：`hash_i = H(hash_{i-1} + record_i)`。
2. 每日生成锚点摘要写入只读存储。
3. 回放与审计查询必须校验哈希链完整性，失败即标记证据无效。

## 15.9 DSAR 对 Replay/Eval 的影响边界

1. 被 DSAR 删除的数据不得再进入可识别回放。
2. Eval 历史允许保留聚合统计，不得保留可识别原文。
3. 回放任务遇到 DSAR tombstone 必须终止并输出“证据受限”状态。

## 15.10 数据保留分层策略（成本边界）

```yaml
data_retention_policy:
  hot:
    window: 7d
    stores: [run_state, hot_cache, recent_events]
  warm:
    window: 30d
    stores: [event_log, usage_ledger, eval_intermediate]
  cold:
    window: archive
    stores: [audit_evidence, release_evidence_pack, policy_bundle]
```

执行规则：
1. hot 过期数据自动降到 warm，不允许直接删除审计相关证据。
2. warm 到期按策略归档或聚合保留，删除必须满足合规规则。
3. cold 层删除需 legal/retention 双重校验并写审计事件。

## 15.11 Decision Graph 一致性语义

1. `decision_node` 写入与对应 decider 的 `decision_log` 必须同事务提交。
2. `decision_edge` 允许最终一致写入，但高风险 run 在终态前必须完成关键边闭合。
3. 关键边集合：`context.compile -> model.inference -> policy.eval -> approval.decision -> run.resume`。
4. run 进入终态前执行 `decision_graph_validate`；失败则标记 `graph_incomplete` 并触发阻断/复盘策略。
5. Replay 默认按 `decision_graph_snapshot` 校验路径一致性，不一致则降级为“证据受限”。

## 15.12 Error Propagation & Containment Model（错误传播与遏制）

错误类型（最小集）：
1. `context_error`：候选污染、裁剪失真、冲突误判。
2. `model_error`：幻觉、格式偏移、工具意图误判。
3. `policy_error`：false_allow、false_deny、obligation mismatch。
4. `approval_error`：stale token、路由错误、超时误升级。
5. `eval_error`：sampling bias、grader drift、replay mismatch。
6. `tool_error`：unknown_outcome、side_effect_uncertain。

传播图规则：
1. 每次关键错误必须记录 `error_node_id + upstream_error_node_ids`。
2. 错误传播边分三类：`causal`、`amplified_by`、`masked_by`。
3. 每类错误定义 `max_propagation_hops`，超限即触发强制遏制动作。

遏制动作模板：
1. `context_error`：降级到最小上下文 + require_review（高风险）。
2. `policy_false_allow`：立即 block 相关变更并回滚最近策略 bundle。
3. `policy_false_deny`：进入人工仲裁并回放校正，禁止审批绕过。
4. `eval_error`：冻结该评测套件门禁能力，回退到保守门禁配置。
5. `tool_unknown_outcome`：禁止自动重试写，进入 reconcile 队列。

可观测指标：
1. `error_propagation_depth_p95`。
2. `error_containment_time_p95`。
3. `amplified_error_rate`（被放大错误占比）。

---

## 16. Agent 可观测事件标准

## 16.1 事件族

1. 规划：`plan.generated`、`plan.revised`
2. 上下文：`context.candidate_gated`、`context.compiled`、`context.trimmed`、`context.conflict_detected`
3. 记忆：`memory.promoted`、`memory.suspected`、`memory.revoked`
4. 检索：`rag.retrieval_no_hit`、`rag.rerank_done`
5. 工具：`tool.call_started`、`tool.call_unknown_outcome`、`tool.call_reconciled`
6. 审批：`approval.requested`、`approval.timeout`、`approval.resolved`
7. 安全：`prompt_injection.detected`、`policy.violation`
8. 技能：`skill.bundle_resolved`、`skill.injected`、`skill.blocked_by_policy`
9. 决策因果：`decision.node_emitted`、`decision.edge_linked`、`decision.graph_finalized`
10. 多 Agent：`handoff.started`、`handoff.invalid_schema`、`handoff.loop_detected`
11. 自愈：`selfheal.triggered`、`selfheal.action_applied`、`selfheal.rolled_back`
12. 调度：`schedule.created`、`schedule.fired`、`schedule.misfired`、`schedule.duplicate_trigger`、`schedule.paused`、`schedule.backfilled`
13. 调度治理：`schedule.admission_rejected`、`schedule.throttled`、`schedule.preempted`、`schedule.starvation_promoted`
14. 基础设施：`infra.node_degraded`、`infra.node_isolated`、`infra.node_recovered`、`infra.gpu_fault_detected`、`infra.nic_degraded`
15. 错误传播：`error.detected`、`error.propagated`、`error.contained`、`error.amplified`

## 16.2 统一字段

1. `event_id`
2. `event_name`
3. `timestamp`
4. `tenant_id`
5. `run_id`
6. `step_id`
7. `trace_id`
8. `workflow_version`
9. `severity`
10. `decision_graph_id`
11. `decision_node_id`
12. `parent_decision_node_ids`
13. `error_node_id`
14. `upstream_error_node_ids`
15. `payload`

## 16.3 OTel 对齐策略

1. 对齐 OTel GenAI spans，但将 schema 版本显式写入事件。
2. 由于 GenAI 语义约定仍在发展阶段，平台侧做版本兼容层。
3. `decision_graph_id` 与 span link 关联，保证跨 decider 因果路径可追踪。

---

## 17. Observability -> Governance -> Learning 自动闭环

## 17.1 流程

1. Detect：异常检测。
2. Diagnose：根因分类。
3. Act：执行治理动作。
4. Assess：验证效果。
5. Learn：生成评测样本与策略提案。

## 17.2 触发矩阵（核心）

1. `tool.call_unknown_outcome` -> reconcile 队列，禁止自动重试写。
2. `context.conflict_detected` 高频 -> 收紧冲突阈值 + 生成冲突回归集。
3. `rag.retrieval_no_hit` 高频 -> 知识缺口 backlog。
4. `prompt_injection.detected` -> 收紧 tool_scope + 风险升级。
5. `approval.timeout` 激增 -> 审批路由扩容与升级策略。
6. `token_cost_anomaly` -> 模型降级路由 + 限流低优先级任务。
7. `handoff.invalid_schema` -> 回退单 Agent + handoff 回归套件。
8. `schedule.misfired` 激增 -> 自动放大调度副本并收紧 backfill 窗口。
9. `schedule.duplicate_trigger` -> 收紧 dedupe 策略并触发幂等回归套件。
10. `schedule.throttled` 某租户持续高位 -> 自动下发 tenant 限流策略并通知运营。
11. `schedule.starvation_promoted` 激增 -> 提升低 tier aging 权重并触发公平性回归评测。
12. `schedule.preempted` 异常升高 -> 收紧抢占阈值并回退到“仅 pending 抢占”。
13. `infra.gpu_fault_detected` 激增 -> 自动隔离故障节点并切换到健康 GPU 池。
14. `infra.nic_degraded` 激增 -> 将 `network_sensitive` 工作流迁移到高带宽池并收紧 NIC 阈值。
15. `infra.node_recovered` 持续稳定 -> 进入灰度回流而非立即全量放流。
16. `decision.graph_finalized` 缺边率升高 -> 阻断高风险放行并触发因果链完整性回归套件。
17. `error.amplified` 升高 -> 触发跨域遏制策略（冻结变更 + 缩小流量 + 强制复盘）。
18. `error.contained` 失败率升高 -> 升级人工值班并禁止自动化放行动作。

## 17.3 自动动作边界

1. 自动动作必须可撤销。
2. 自动动作不能提权，只能收紧。
3. 高风险动作不自动放行。

## 17.4 闭环执行主体边界

1. `Self-heal Engine` 仅能执行预定义治理动作模板，不得执行任意脚本。
2. `Policy` 相关自动动作仅允许“收紧或回退”，禁止自动放宽策略。
3. `Release` 相关自动动作仅允许 `block/rollback/canary_hold`，不得自动 promote。
4. `Data` 相关自动动作（删除/压缩）必须经过 legal/retention 规则校验。
5. 任一自动动作都必须记录 `actor=system` 与 `action_template_id`，便于审计追责。

---

## 18. Eval 作为一级系统

实施级引用：
1. 详细执行规范见 [eval-spec.md](/home/wandering/learn/agent/doc/specs/eval-spec.md)。
2. 主文档负责治理与门禁边界；grader 协议、数据集演进、回放一致性、指标管线由子规范定义。

## 18.1 Eval Control Plane

1. Eval Registry
2. Dataset Registry
3. Grader Registry
4. Eval Runner
5. Eval Warehouse
6. Drift Monitor
7. Replay Factory
8. Synthetic Case Generator
9. Gatekeeper

## 18.2 Eval Taxonomy

1. L0 Unit Eval
2. L1 Component Eval
3. L2 Workflow Eval
4. L3 Agentic Eval
5. L4 Safety Eval
6. L5 Economic Eval

## 18.3 Benchmark 分层

1. golden_set
2. production_shadow_set
3. adversarial_set
4. long_tail_set
5. canary_set
6. synthetic_growth_set

## 18.4 关键评测协议

### 工具正确性

1. TSA：工具选择准确率
2. AFS：参数准确度
3. EOC：执行结果正确性
4. SVPR：副作用验证通过率

### Handoff 质量

1. Delegation Precision/Recall
2. Handoff ROI
3. Invalid Handoff Rate
4. Loop Handoff Rate

### Groundedness / Citation

1. Claim Coverage
2. Citation Precision
3. Citation Recall
4. Unsupported Claim Rate

### Policy Regression

1. Allow/Block 混淆矩阵
2. Critical False Allow（0 容忍）
3. False Block Rate
4. Policy Eval Latency

### Memory 价值

1. Memory On/Off 成功率差值
2. Memory Harm Rate
3. Freshness Impact

### Cost-Quality Pareto

1. 新方案必须在 Pareto 前沿或给出可审证偏离理由。
2. 被基线全面支配的方案禁止上线。

### 检索质量与效率

1. `Recall@k`、`nDCG@k`、`MRR@k`（按文档类型分桶）。
2. `No-hit Rate` 与 `Low-confidence Rate`。
3. `Retrieval Latency P50/P95`、`Rerank Latency P95`。
4. `Unsupported Claim Rate` 与 `Citation Coverage` 联动评测。
5. `Retrieval Cost per Run`（含向量检索、重排、query rewrite 开销）。

## 18.5 自动触发评测

1. 失败事件自动进入 replay。
2. 高频异常自动生成 synthetic cases。
3. policy 变更强制跑回归套件。

## 18.6 发布门禁

1. 变更类型映射到必跑评测套件。
2. 任一关键套件未达标即阻断发布。
3. 例外发布必须有风险签字与回滚计划。
4. 每次发布产出 Eval Evidence Pack。

## 18.7 指标严格定义（可计算协议）

样本单位：
1. 工具评测样本：`(task, expected_tool, expected_args, oracle_outcome)`。
2. handoff 样本：`(task, baseline_single, candidate_multi, budgets)`。
3. 引用样本：`(claim_set, evidence_set, gold_links)`。
4. 记忆样本：`(memory_on_result, memory_off_result, harm_label)`。

核心公式：
1. `TSA = correct_tool_selected / tool_required_cases`。
2. `AFS = exact_arg_match_cases / tool_called_cases`。
3. `EOC = oracle_equivalent_cases / executable_cases`。
4. `HandoffROI = median((Q_multi - Q_single) - w_latency*delta_latency - w_cost*delta_cost - w_risk*delta_risk)`。
5. `MemoryHarmRate = harmful_with_memory_cases / memory_used_cases`。
6. `CitationAdequacy = supported_critical_claims / critical_claims`。

统计规则：
1. 每个关键指标必须给出置信区间（Wilson 下界），门禁基于下界而非点估计。
2. 任一套件样本数小于 `n_min` 仅允许“观察态”，不得作为放行证据。

## 18.8 Auto/Human Grader 混合协议

1. 自动 grader 覆盖全量样本；人工 grader 覆盖分层抽样样本。
2. 抽样分层至少含：租户 tier、风险等级、任务类型、失败类型。
3. 人工复核比例：高风险 `>=20%`，中风险 `>=10%`，低风险 `>=5%`。
4. 人工一致性要求：`CohenKappa >= 0.75`；不达标则冻结该套件放行能力。
5. Auto/Human 分歧率超阈值时，自动 grader 降级为“参考分”，发布门禁改用人工主导。

## 18.9 线上样本去偏与回放

1. 线上评测采用分层加权采样，避免大租户或高频路径淹没长尾风险。
2. 回放优先级：`critical_false_allow > irreversible_side_effect > handoff_loop > no_hit_high_risk`。
3. `failure -> eval case` 必须保留原始快照（workflow/policy/model/context 版本）。
4. 同一根因去重后进入 `regression_suite`，防止噪声样本堆积。

## 18.10 阈值制度与阻断规则

1. 关键安全指标（如 `Critical False Allow`）零容忍，单次命中即阻断发布。
2. 非关键指标采用分层阈值：`critical > high > medium > low`。
3. 若新方案未在 Cost-Quality Pareto 前沿，必须附可审证偏离理由并获治理签字。
4. 任一评测结果缺失证据包（数据集版本、grader 版本、脚本哈希）视为无效结果。

## 18.11 默认门禁阈值模板（v1）

1. 最小样本量：`critical>=2000`、`high>=1000`、`medium>=500`、`low>=200`。
2. 工具选择（TSA 下界）：`critical/high>=0.995`、`medium>=0.98`、`low>=0.95`。
3. 参数准确（AFS 下界）：`critical/high>=0.99`、`medium>=0.97`、`low>=0.94`。
4. 执行结果（EOC 下界）：`critical/high>=0.98`、`medium>=0.95`、`low>=0.90`。
5. 引用充分（CitationAdequacy 下界）：`critical/high>=0.99`、`medium>=0.97`、`low>=0.93`。
6. 记忆危害（MemoryHarmRate 上界）：`critical<=0.2%`、`high<=0.5%`、`medium<=1.0%`、`low<=2.0%`。
7. 多 Agent 启用条件：`HandoffROI > 0` 且 `LoopHandoffRate <= 0.5%` 且 `InvalidHandoffRate <= 1.0%`。

## 18.12 fast_path_eval（仅低风险）

启用目标：降低低风险变更发布等待，不降低高风险安全标准。

准入条件（必须同时满足）：
1. `risk_tier=low`。
2. 变更不涉及：`policy bundle`、`tool side-effect contract`、`approval routing`、`context compiler major/minor`。
3. 变更范围在白名单：提示模板微调、UI/可观测字段、低风险检索参数微调。
4. 具备最近稳定窗口基线（过去 `7d` 无 critical gate fail）。

执行规则：
1. 仍必须通过 `policy_gate` 的关键检查（含 `critical_false_allow=0`）。
2. eval 仅跑 low-risk 快速套件（最小样本量与低风险阈值）。
3. 发布范围默认 `<=5%` canary，不允许直接全量。
4. 发布后 `24h` 内必须补跑 full shadow eval。
5. 若 full shadow eval 任一 critical/high 失败：自动回滚并冻结该 fast path 模板。

禁止条件：
1. 任一 high/critical 变更禁止使用 fast_path_eval。
2. 涉及 `external_write/irreversible` 节点的变更禁止使用 fast_path_eval。
3. 无 evidence pack 或样本不足时禁止使用 fast_path_eval。

## 18.13 Eval 组织能力基线（平台化要求）

说明：Eval 已是平台一级系统，落地前提是“组织能力 + 制度 + 工程”共同成立，而不只是代码上线。

组织能力最小集：
1. 数据治理：dataset 血缘、污染检测、标签漂移治理责任到人。
2. 样本质量：高风险样本覆盖、长尾样本补充、去偏策略持续执行。
3. grader 可靠性：自动 grader 监控与人工仲裁机制常态化。
4. 回放一致性：专门环境保障 snapshot 回放可复现。
5. oncall 制度：eval 事件有值班、升级、复盘与整改闭环。

发布要求：
1. 若组织能力缺项，Eval 仅可作为观测，不可作为强门禁真值。
2. 只有当组织能力最小集达标后，才允许将 Eval 结果提升为阻断级门禁。

## 18.14 发布证据融合裁决（Eval 非真值）

原则：
1. Eval 是“高价值证据”，不是“绝对真值”。
2. 发布最终结论由 `Release Decision Engine` 产出，而非单一 metric 直接裁决。

证据输入（最小）：
1. `eval_gate_result`（含置信区间与样本覆盖）。
2. `policy_regression_result`（critical false allow/deny）。
3. `replay_consistency_result`（关键链路回放一致性）。
4. `incident_trend`（近期事故与异常趋势）。
5. `human_signoff`（高风险变更必需）。

裁决规则：
1. `critical/high`：任一关键证据失败即阻断；证据缺失也阻断。
2. `medium`：允许 `review_required`，需签字后灰度。
3. `low`：可走 fast path，但必须补齐后置 full evidence。
4. 输出必须包含 `decision_confidence` 与 `evidence_completeness`，禁止只给 `pass/fail`。

---

## 19. 运营 API（Token / 成本 / 配额 / 调度）

## 19.1 采集字段

1. tenant/project/run/step
2. provider/model
3. prompt/completion/cached/total tokens
4. cost_usd
5. latency_ms
6. status/error_type

## 19.2 API

1. `GET /v1/usage/tokens`
2. `GET /v1/usage/tokens/breakdown`
3. `GET /v1/usage/costs`
4. `GET /v1/usage/anomalies`
5. `POST /v1/quotas`
6. `GET /v1/quotas/status`

## 19.3 治理动作

1. 80% 预算预警。
2. 超预算自动降级路由。
3. 超预算限流低优先级。
4. 异常爆量熔断。

## 19.4 对账

1. 平台计量与供应商账单周期对账。
2. 差异超阈值自动告警。
3. 差异明细写入审计仓。

## 19.5 Scheduler API

1. `POST /v1/schedules`（创建定时/重复任务）
2. `GET /v1/schedules/{schedule_id}`（查询详情）
3. `PATCH /v1/schedules/{schedule_id}`（更新规则）
4. `POST /v1/schedules/{schedule_id}/pause`
5. `POST /v1/schedules/{schedule_id}/resume`
6. `POST /v1/schedules/{schedule_id}/trigger`（手动触发一次）
7. `POST /v1/schedules/{schedule_id}/backfill`（按时间窗回填）
8. `GET /v1/schedules/{schedule_id}/runs`（执行历史）
9. `GET /v1/schedules/{schedule_id}/next-fire-times`（下次触发预览）

分阶段开放：
1. Phase 1：`create/get/pause/resume` + cron。
2. Phase 2：`trigger/backfill/runs/next-fire-times` + 高级并发策略。

## 19.6 调度配额与治理

1. 每租户 `max_schedules`。
2. 每租户 `max_trigger_rate_per_min`。
3. 单 schedule `max_concurrency` 与 `max_backfill_window_sec`。
4. 超配额策略：拒绝新建、自动限流或转人工审批。
5. tier 级预算：`vip_reserved_concurrency`、`pro/standard` 共享池上限。
6. 四级限流：global/tier/tenant/user，默认令牌桶 + 突发桶。

## 19.7 调度策略 API

1. `POST /v1/scheduler/policies`（创建或更新调度策略）
2. `GET /v1/scheduler/policies/{policy_id}`（查询策略）
3. `POST /v1/scheduler/policies/{policy_id}/dry-run`（回放模拟）
4. `GET /v1/scheduler/queues`（查看各队列积压与配额）
5. `POST /v1/scheduler/tenants/{tenant_id}/throttle`（临时限流）
6. `POST /v1/scheduler/tenants/{tenant_id}/priority-override`（临时优先级调整）

## 19.8 Knowledge Base / Retrieval API

1. `POST /v1/knowledge-bases`（创建知识库）
2. `POST /v1/knowledge-bases/{kb_id}/documents`（批量导入文档）
3. `POST /v1/knowledge-bases/{kb_id}/reindex`（增量重建索引）
4. `POST /v1/retrieval/search`（检索调试接口，返回召回与重排明细）
5. `GET /v1/knowledge-bases/{kb_id}/stats`（规模、新鲜度、命中率）
6. `GET /v1/knowledge-bases/{kb_id}/gaps`（知识缺口列表）

字段扩展（存储相关）：
1. `document_storage_mode`：`object_canonical`（默认且必选基线）|`object_plus_dfs_cache`（在基线上增加缓存层）。
2. `object_uri` / `version_id` / `content_hash`：文档入库必填元数据。
3. `dfs_cache_profile`：可选，声明缓存池与 TTL。
4. `rebuild_from_source`：`object_only|prefer_cache`（默认 `object_only`）。

新增接口（按需启用，不改变 canonical 规则）：
1. `POST /v1/knowledge-bases/{kb_id}/documents:presign-upload`（对象存储直传签名）。
2. `POST /v1/knowledge-bases/{kb_id}/documents:commit`（提交对象元数据并触发摄取）。
3. `POST /v1/knowledge-bases/{kb_id}/cache/rebuild`（重建或清理 DFS 缓存）。

## 19.9 Infra Health / Isolation API（稳定边界）

外部检测器接入：
1. `POST /internal/v1/health/signals`（上报健康信号）
2. `POST /internal/v1/health/signals/batch`（批量上报）

平台控制动作：
3. `POST /v1/infra/nodes/{node_id}/isolate`
4. `POST /v1/infra/nodes/{node_id}/drain`
5. `POST /v1/infra/nodes/{node_id}/recover`
6. `GET /v1/infra/nodes/{node_id}/status`
7. `GET /v1/infra/nodes`（按状态/机型/可用区筛选）

## 19.10 交换契约与版本兼容（解耦核心）

1. `HealthSignal` 必含：`schema_version`、`node_id`、`resource_type`、`fault_code`、`health_score`、`observed_at`、`ttl_sec`、`detector_id`、`detector_version`、`evidence_ref`。
2. 协议规则：`v1` 只允许向后兼容新增字段；删除字段需新主版本。
3. 适配层职责：不同检测器格式映射到统一 `HealthSignal`，调度器只依赖统一对象。
4. 幂等语义：`signal_id` 去重，过期信号自动失效。
5. 安全要求：检测器到平台使用 mTLS + 签名，防伪造故障信号。

## 19.11 开发者接入与调试 API（平台消费者视角）

1. `POST /v1/workflows/validate`：DSL 静态校验（schema、权限、补偿、审批绑定）。
2. `POST /v1/workflows/simulate`：离线仿真执行（不触发真实副作用）。
3. `POST /v1/context/compile/debug`：返回候选排序、裁剪理由、冲突仲裁轨迹。
4. `POST /v1/policies/evaluate/dry-run`：给定输入回放策略判定并展示命中规则。
5. `POST /v1/approvals/simulate`：模拟审批路由、SLA 超时升级、会签/或签结果。
6. `POST /v1/runs/{run_id}/replay`：按 execution snapshot 重放失败 run。
7. `POST /v1/retrieval/search/debug`：返回召回集合、重排分数、no-hit 原因、预算裁剪明细。
8. `GET /v1/dev/tool-contracts`：导出工具 I/O schema 与 side-effect 声明，供 CI 校验。

### 19.11.1 示例（请求/响应）

`POST /v1/workflows/validate`

```json
{
  "workflow_id": "wf_refund_v3",
  "dsl_version": "2.0",
  "workflow_spec": {
    "nodes": [],
    "edges": []
  }
}
```

```json
{
  "valid": false,
  "errors": [
    {
      "code": "SCHEMA_LINK_BROKEN",
      "path": "nodes[3].input_schema",
      "message": "input_schema 与上游输出不兼容",
      "next_action": "修复 schema 或新增映射节点"
    }
  ],
  "request_id": "req_01HV..."
}
```

`POST /v1/context/compile/debug`

```json
{
  "run_id": "run_123",
  "step_id": "step_4",
  "candidate_limit": 300,
  "include_trim_reason": true
}
```

```json
{
  "compile_fingerprint": "ccf_abc123",
  "selected": 42,
  "trimmed": 258,
  "top_reasons": ["low_confidence", "redundant", "stale"],
  "policy_blocks": 3,
  "request_id": "req_01HW..."
}
```

## 19.12 依赖服务继承与解析 API

1. `POST /v1/dependencies/profiles`：创建或更新 `tenant/project` 依赖基线。
2. `GET /v1/dependencies/profiles/{profile_id}`：查看继承后的有效配置。
3. `POST /v1/workflows/{workflow_id}/dependencies/resolve/dry-run`：返回 RDG、冲突与风险提示。
4. `POST /v1/workflows/{workflow_id}/dependencies/resolve`：生成 `dependency_bundle_id` 并签名固化。
5. `GET /v1/runs/{run_id}/dependencies/resolved`：查询该 run 实际使用的依赖快照。
6. `POST /v1/dependencies/bundles/{bundle_id}/replay`：按历史输入回放依赖解析结果。
7. `POST /v1/dependencies/bundles/{bundle_id}/promote`：将依赖 bundle 灰度或全量生效（仅新 run）。
8. `POST /v1/dependencies/bundles/{bundle_id}/rollback`：回滚到上一稳定 bundle。
9. `POST /v1/dependencies/profiles/{profile_id}/impact-analysis`：计算影响面并生成报告。
10. `POST /v1/dependencies/impact-analysis/batch`：批量分析多 profile 变更。
11. `GET /v1/dependencies/impact-analysis/{report_id}`：获取 blast radius 报告明细。
12. `POST /v1/dependencies/impact-analysis/{report_id}/ack`：治理负责人确认报告。

### 19.12.1 示例（请求/响应）

`POST /v1/workflows/{workflow_id}/dependencies/resolve/dry-run`

```json
{
  "workflow_id": "wf_refund_v3",
  "tenant_id": "t_001",
  "project_id": "p_cs",
  "declared_dependencies": [
    {
      "service_id": "svc_llm_primary",
      "version_pin": "^1.6"
    }
  ]
}
```

```json
{
  "resolved_dependency_graph": {
    "nodes": 6,
    "edges": 8
  },
  "conflicts": [],
  "warnings": [
    {
      "code": "VERSION_PIN_LOOSE",
      "service_id": "svc_llm_primary",
      "message": "建议将 ^1.6 收紧到 1.6.3"
    }
  ],
  "request_id": "req_01HX..."
}
```

`POST /v1/dependencies/profiles/{profile_id}/impact-analysis`

```json
{
  "profile_id": "tp_finance_v8",
  "change_set_id": "chg_20260403_01",
  "scope": "tenant:t_001"
}
```

```json
{
  "report_id": "imp_01HZ...",
  "affected_workflows_count": 137,
  "affected_nodes_count": 924,
  "affected_high_risk_nodes": 88,
  "affected_active_runs_count": 42,
  "requires_manual_review": true,
  "recommended_rollout": "canary",
  "request_id": "req_01HZ..."
}
```

## 19.13 异构调度接口预留（Heterogeneous Scheduling）

1. `GET /v1/scheduler/resource-classes`：查询异构资源类与容量（如 `gpu.h100.80g`）。
2. `POST /v1/scheduler/resource-classes`：创建/更新资源类（选择器、权重、保留并发、降级顺序）。
3. `POST /v1/scheduler/placement/dry-run`：基于 `resource_class + topology_hint` 做放置模拟。
4. `POST /v1/scheduler/jobs/{job_id}/rebind-class`：在 safe boundary 做资源类切换（受策略约束）。
5. `GET /v1/scheduler/hetero/decision-log`：查看异构选路决策与降级原因。
6. `POST /v1/scheduler/fault-domains/{domain_id}/quarantine`：隔离故障域并触发重分配。

交换字段约束：
1. `resource_class`、`accelerator_type`、`interconnect_pref` 必须与 `SchedulerPolicy.heterogeneous` 对齐。
2. 任何跨类迁移必须写 `hetero.rebind` 事件并记录前后类与原因。
3. `irreversible` 任务禁止在执行中跨类迁移。

### 19.13.1 示例（请求/响应）

`POST /v1/scheduler/placement/dry-run`

```json
{
  "job_id": "job_9001",
  "resource_class": "gpu.h100.80g",
  "accelerator_type": "nvidia_h100",
  "interconnect_pref": "nvlink",
  "topology_hint": "same_zone",
  "fault_tolerance_class": "strict"
}
```

```json
{
  "admitted": true,
  "candidate_nodes": 18,
  "selected_pool": "az-a/gpu-h100",
  "fallback_plan": ["gpu.l40s.48g", "cpu.highmem"],
  "reasons": ["class_capacity_ok", "fault_domain_clean"],
  "request_id": "req_01HY..."
}
```

## 19.14 易用性与可运营性约束

1. 任一失败响应必须包含：`error_code`、`human_hint`、`next_action`。
2. 调试接口结果必须可复制到回放接口（避免“只能看不能复现”）。
3. 所有 API 提供 `x-request-id` 贯穿链路，便于用户报障定位。
4. 提供“只读安全模式”开关，允许先验证观测再启写操作。

只读安全模式边界：
1. 允许：`GET` 查询、`/simulate`、`/dry-run`、`/debug`、`/replay`（无副作用模式）。
2. 禁止：`external_write/irreversible` 节点真实执行、`/promote`、`/rollback`、`/isolate` 等控制动作。
3. 所有被拒写请求返回统一错误码 `READ_ONLY_MODE_BLOCKED`。

## 19.15 外部 API 与内部 API 边界

1. `/v1/*`：租户可见外部 API，必须经过 AuthN/AuthZ、配额与审计。
2. `/internal/*`：平台内部 API，仅服务间 mTLS 调用，不对租户暴露。
3. 内部 API 不得直接承载业务放行动作；最终放行必须回到 `/v1` 治理链路。
4. 任何从 `/internal` 到 `/v1` 的影响链路必须写 `cross_boundary_call` 事件。
5. 接口版本策略：外部 API 以兼容优先；内部 API 可快迭代但必须通过适配层隔离。

## 19.16 开发者工作流（DX 基线）

1. 本地运行：提供 `local-runtime` 模式，支持 mock provider/tool/approval。
2. CI 校验：默认流水线顺序 `dsl-lint -> schema-link-check -> policy-dry-run -> simulate -> gate-dry-run`。
3. DSL IDE 支持：提供 schema 补全、字段提示、静态错误定位（行列级）。
4. 可视化调试：提供 run DAG、context compile trace、policy hit trace、merge report 视图。
5. 一键回放：失败 run 在 UI/API 可直接触发 `replay` 并对比差异。
6. Skill 调试：支持 `skill simulate + signature verify + policy boundary check` 一键联调。

## 19.17 Decision Causality Graph API

1. `GET /v1/runs/{run_id}/decision-graph`：获取 run 的完整因果图。
2. `GET /v1/runs/{run_id}/decision-path/final`：获取最终 allow/deny/approval 路径。
3. `POST /v1/runs/{run_id}/decision-graph/validate`：校验关键节点/关键边完整性。
4. `GET /v1/decision-graphs/{graph_id}/nodes/{node_id}`：查询单节点输入指纹、结论与证据引用。
5. `POST /v1/decision-graphs/query`：按 `tenant/workflow/risk/decision_type` 检索历史因果图。

强规则：
1. 高风险 run 查询结果若缺失 `policy.eval -> approval.decision -> run.resume` 路径，默认标记 `graph_incomplete`。
2. `graph_incomplete` 的 run 不得作为放行证据样本进入发布评审。

## 19.18 Skill Registry / Injection API

1. `POST /v1/skills/bundles`：创建或更新 skill bundle（含签名与元数据）。
2. `GET /v1/skills/bundles/{bundle_id}`：查询 skill 内容与适用范围。
3. `POST /v1/skills/bundles/{bundle_id}/validate`：校验签名、作用域、策略兼容性。
4. `POST /v1/skills/bundles/{bundle_id}/promote`：灰度启用 skill bundle（仅新 run）。
5. `POST /v1/skills/bundles/{bundle_id}/rollback`：回滚 skill bundle。
6. `POST /v1/runs/{run_id}/skills/simulate`：在不执行副作用下验证 skill 注入效果。

强规则：
1. Skill API 不提供绕过策略的“强制注入”模式。
2. Skill 注入失败返回 `SKILL_INJECTION_BLOCKED` 并附 `next_action`。
3. 所有 skill 注入请求必须写 `skill.bundle_resolved|skill.injected|skill.blocked_by_policy` 事件。

## 19.19 调度外包控制 API 与一致性校验

平台控制 API（对外部执行器）：
1. `POST /internal/v1/scheduler/dispatch-tickets`：签发执行票据。
2. `POST /internal/v1/scheduler/dispatch-tickets/{ticket_id}/cancel`：撤销票据。
3. `POST /internal/v1/scheduler/execution-receipts`：外部执行器回传执行结果。
4. `POST /internal/v1/scheduler/ticket-receipt/validate`：校验票据与回执一致性。

票据字段（最小）：
1. `ticket_id`、`run_id`、`step_id`、`tenant_id`。
2. `admission_decision_id`、`risk_tier`、`required_approval_state`。
3. `allowed_resource_classes[]`、`expires_at`、`signature`、`idempotency_key`。

一致性校验规则：
1. 回执无有效票据：`fail_closed`。
2. 回执超出票据授权资源类：`fail_closed + isolate_executor_if_repeated`。
3. 回执晚于 `expires_at`：标记 `stale_receipt`，不得直接恢复 run。
4. `approval_state` 与回执不一致：强制 `require_review`，禁止自动 resume。
5. 校验结果必须写入 `scheduler.ticket_receipt_validated` 事件与审计证据包。

## 19.20 调试复杂度控制（跨域回溯）

问题：多 decider + 回放 +审计导致单故障回溯路径过长，排障成本指数上升。

机制：
1. `root_cause_pack`：一键聚合 run 的 `decision_graph + decision_logs + audit_refs + snapshots + key_events`。
2. `first_bad_node` 检测：按因果图自动定位首个异常节点与上游输入差异。
3. 分层调试视图：`L1执行链路`、`L2策略与审批`、`L3证据与回放`。
4. `debug_replay_profile`：支持最小重放（仅关键节点）与全量重放两种模式。

SLO：
1. `root_cause_pack_ready_p95 <= 10s`。
2. `first_bad_node_identified_rate >= 95%`（关键故障集）。
3. 跨域故障平均定位时长 `MTTI` 持续下降。

---

## 20. 版本兼容与运行中恢复

## 20.1 Execution Snapshot

每个 run 固定：
1. `workflow_version`
2. `prompt_version`
3. `policy_version`
4. `tool_schema_version`
5. `model_profile_version`
6. `context_compiler_version`
7. `dependency_bundle_id`
8. `dependency_binding_hash`
9. `skill_bundle_set_hash`
10. `decision_graph_schema_version`

## 20.2 Compatibility Policy

1. `STRICT`：同版本恢复。
2. `FORWARD_SAFE`：通过兼容校验后升级。
3. `MIGRATABLE`：需 migration handler。

## 20.3 长运行流程策略

1. 默认 Worker Versioning。
2. 短流程可 pinned。
3. 长流程在 Continue-As-New 边界升级。
4. 无法版本化时使用 patching 兜底。

## 20.4 强规则

1. 新 run 仅使用新版本。
2. parked/pending run 默认按快照恢复。
3. 禁止隐式混配新旧配置。
4. 禁止恢复时替换依赖服务绑定（除非通过 `FORWARD_SAFE/MIGRATABLE` 且留痕审批）。

---

## 21. 高可用、高并发、低时延与模型热机

## 21.1 高可用

1. 关键服务 `>=3` 副本，跨 AZ 反亲和。
2. 存储层主从或多副本。
3. 目标：控制面 `RTO<=15min`，执行面 `RTO<=30min`，`RPO<=5min`。

## 21.2 扩缩容

信号：
1. `queue_lag`
2. `p95_step_latency`
3. `error_rate`
4. `cpu/memory`
5. `cost_burn_rate`

公式：

```text
desired_workers = ceil((queue_lag / target_drain_seconds) / worker_rps)
```

## 21.3 时延预算（交互链路示例）

1. 网关/鉴权：150ms
2. Context compile：600ms
3. Retrieval/rerank：900ms
4. 首轮模型：2200ms
5. 工具（A/B均值）：1400ms
6. 汇总输出：450ms
7. 预留：300ms

## 21.4 parked 规模估算

```text
parked_concurrency ≈ incoming_rps * avg_park_duration_sec * park_ratio
```

用途：
1. 评估状态存储容量。
2. 评估 resume 峰值处理能力。

## 21.5 模型热机（Warm Pool）

1. 每个模型配置 `min_warm_replicas`，按流量与 TTFT 目标动态调整。
2. 发布或扩容时执行 pre-warm（加载权重、JIT、连接池预建）。
3. 冷启动超阈值时降级到备用模型或备用 provider。
4. 结合 prompt caching 与请求路由提高命中率，降低预填充成本。

## 21.6 调度容量与精度模型

1. 触发吞吐估算：

```text
trigger_qps ≈ active_schedules * avg_fire_per_min / 60
```

2. 调度延迟指标：`schedule_fire_delay_p95 <= 3s`（到点到投递完成）。
3. 去重指标：`duplicate_trigger_rate <= 0.01%`。
4. 漏触发指标：`missed_trigger_rate <= 0.1%`。
5. 大规模回填时启用分片回填与令牌桶限速，避免挤占在线流量。

## 21.7 调度算法（VIP 独占 + 限流 + 公平）

1. 准入判定：

```text
allow = global_bucket.ok && tier_bucket.ok && tenant_bucket.ok && user_bucket.ok
```

2. 分池规则：

```text
if tier == VIP and vip_enabled then queue=vip_dedicated_pool else queue=shared_pool
```

3. 共享池调度（WDRR）：

```text
deficit[q] += quantum[q] * weight[q]
if deficit[q] >= cost(job): dispatch(job); deficit[q] -= cost(job)
```

4. `cost(job)` 估算：`predicted_tokens + alpha*predicted_tool_ms`。
5. aging 机制：等待超过 `promote_after_sec` 的任务提高有效权重，防止饿死。
6. 抢占规则：仅抢占低优先级 pending；运行中任务只在 `park/step boundary` 可抢占。
7. 保护规则：`irreversible` 或未知结果处理中任务禁止抢占。
8. 借还规则：VIP 空闲容量可借给共享池；VIP 压力恢复时按 `grace_sec` 回收。
9. 反馈控制：按 `tier_slo_violation_ratio` 动态调整权重，超阈值触发策略灰度回滚。
10. 异构预留：`resource_class` 进入对应子队列（如 `gpu.h100.*`），按类权重与保留并发调度，避免跨硬件互相挤占。

## 21.8 检索性能模型与调参

1. 检索预算：

```text
retrieval_budget_ms = retrieval_p95_target - (gateway + model_prefill + tool_avg + render)
```

2. 分层目标：`candidate_recall@100` 优先，`final_precision@k` 由 rerank 保证。
3. ANN 调参：`ef_search / num_candidates` 随查询复杂度与延迟预算动态变化。
4. 重排裁剪：只对 top-M 候选做高精度重排，避免全量 rerank。
5. 缓存命中策略：高频 query 命中缓存直接返回候选并做轻量 freshness 校验。
6. 版本一致性：索引切换采用双读灰度，避免切换窗口召回突降。

## 21.9 故障感知调度与机器隔离算法

1. 节点可调度判定：

```text
eligible(node, job) =
  node.state in {HEALTHY, DEGRADED} &&
  node.health_score(resource_type(job)) >= threshold(job.fault_tolerance_class) &&
  placement_policy_satisfied(node, job)
```

2. NIC 慢卡处理：`network_sensitive` 任务要求 `nic_score >= nic_threshold`。
3. NIC 降级节点仅允许 `best_effort` 非网络敏感任务。
4. GPU ECC/XID 等严重故障直接 `ISOLATED`。
5. GPU 轻微告警先 `DEGRADED`，限制新 GPU 任务分配。
6. 隔离动作优先级：`cordon -> drain -> isolate`。
7. `irreversible` 或未知结果处理中任务不强杀，等待安全边界再迁移。
8. `RECOVERING` 节点先灰度 5%-10% 流量后再全量回流。
9. 复发节点回退 `ISOLATED` 并升级告警等级。

## 21.10 容量闭环模型（从目标到资源）

1. Context Compiler CPU 模型：

```text
compile_cpu_ms ≈ a0 + a1*candidate_count + a2*conflict_count + a3*token_sum_k
```

2. 必测点：`candidate_count in {100, 500, 1000}` 三档压测，拟合系数每周更新。
3. Rerank 成本模型：

```text
rerank_ms ≈ b0 + b1*top_m + b2*query_len
```

4. 发布门禁要求给出 `top_m` 从 20->50->100 的质量/时延曲线，禁止“无曲线调参”。
5. Policy Eval 缓存模型：

```text
policy_eval_qps_capacity ≈ worker_count * per_worker_qps * cache_hit_ratio
```

6. 当 `cache_hit_ratio` 跌破阈值时自动启用短期预热与热 key 保护，防止雪崩。
7. Replay/Eval 回放压力模型：

```text
replay_read_iops ≈ replay_jobs * events_per_run / replay_window_sec
```

8. Eval Runner 与在线链路必须配额隔离，默认最多占用在线链路 `20%` 读带宽。
9. Approval 爆发退化边界：`pending_approval_count` 超阈值时，低风险审批自动转批处理；高风险保持实时队列且触发人工扩容。
10. 所有容量模型每月回归一次，回归失败不得扩流。
11. Candidate Budget Gate 模型：

```text
gate_cpu_ms ≈ g0 + g1*ingress_candidates + g2*source_count
```

12. 发布前必须提供 `ingress_candidates in {300, 800, 1200}` 下 `gate_ms + compile_ms` 组合曲线。

## 21.11 全链路 Capacity Model（QPS->CPU->IO->Storage->Cost）

1. 基础映射：

```text
total_cpu_cores ≈ qps * cpu_sec_per_run
total_io_mb_s ≈ qps * io_mb_per_run
total_storage_gb_day ≈ runs_per_day * artifact_gb_per_run * retention_factor
total_cost_day_usd ≈ runs_per_day * cost_per_run_usd
```

2. feature 叠加成本：

```text
cost_per_run_usd =
  base_model_cost
  + retrieval_cost
  + policy_eval_cost
  + tool_cost
  + eval_overhead_cost
  + decision_graph_cost
  + skill_injection_cost

## 21.12 关键路径时延预算与缩链策略

关键路径（交互）：
1. `CandidateGate -> ContextCompile -> SkillInject -> ModelPrefill/Decode -> PolicyEval -> ApprovalCheck -> Resume`。

时延预算（示例，P95）：
1. Candidate Gate：`<=40ms`
2. Context Compile：`<=180ms`
3. Skill Inject：`<=30ms`
4. Model 首 token：`<=1200ms`
5. Policy Eval（inline）：`<=10ms`
6. Approval Check（已批准路径）：`<=20ms`
7. Resume 组装：`<=60ms`

硬规则：
1. 关键路径上的日志/审计写入采用异步 outbox，不得同步阻塞主响应（高风险阻断判定除外）。
2. 任一 stage 超预算连续触发时，优先启用该 stage 的降级策略，不得跨域“放宽策略”换时延。
3. 发布前必须提交 `critical_path_latency_report`，包含单 stage 与端到端分位曲线。
```

3. replay 放大系数：

```text
replay_amplification = replay_runs / online_runs
effective_read_load = online_read_load * (1 + replay_amplification)
```

4. eval 放大系数：

```text
eval_amplification = eval_samples_executed / online_samples
effective_compute_load = online_compute_load * (1 + eval_amplification)
```

5. worst-case 预算：
6. 必须计算 `peak_online + replay_peak + eval_peak` 三者叠加上限，不允许仅按平均值扩容。
7. 当 `effective_read_load` 超过阈值，优先削减 replay/eval 并发，不得压垮在线主链路。
8. 容量评审必须输出：`per_run_cost`、`per_feature_cost`、`worst_case_amplification` 三项报告。

---

## 22. 安全、合规与风险控制

1. 身份权限：OIDC + RBAC + 可选 ABAC。
2. 密钥管理：Vault/KMS。
3. 工具沙箱：网络白名单、文件隔离、资源配额、超时。
4. 数据保护：分级、脱敏、加密、保留期、删除请求。
5. 供应链：镜像签名、漏洞扫描、依赖准入。
6. AI 风险：覆盖 Prompt Injection、Excessive Agency、Improper Output Handling、Unbounded Consumption 等风险面。
7. 风险治理框架：对齐 NIST AI RMF GenAI Profile。

---

## 23. 落地路线（三阶段强约束）

说明：
1. 交付以“三阶段可执行”作为强约束，未完成前一阶段不得进入下一阶段。
2. 阶段外能力统一放入 Extended/Experimental，不得混入当期上线范围。
3. 具体范围裁决与豁免流程以 `agent/doc/design/Agent-Infra-优先级与实施边界-强约束.md` 为准。

## Phase 1（可上线最小系统）

范围（必须）：
1. Workflow runtime（Temporal + park/resume + Execution Snapshot）。
2. Tool safety（幂等、unknown outcome、reconcile）。
3. Context Compiler（唯一裁决）+ RAG L1。
4. Policy minimal（deny/allow/require_approval 基线）。
5. Scheduler MVP（仅 `cron + dedupe + basic quota`）。

范围（明确不做）：
1. 多 Agent 调度层。
2. 高级检索（L2/L3）。
3. Scheduler 高级能力（WDRR/VIP/异构/跨类迁移）。

门禁：
1. 主链路成功率达标。
2. 高风险越权事故为 0。
3. 副作用重复执行事故为 0。

## Phase 2（治理与可运营）

范围（必须）：
1. Approval Domain 完整化。
2. Eval Control Plane + Gatekeeper。
3. Scheduler advanced（限流分层、回填、并发、公平）。
4. Token/成本治理与对账。
5. Developer workflow（CI 校验、回放、可视化调试）。

门禁：
1. 坏变更阻断能力稳定。
2. 调度漏触发/重复触发达标。
3. 审批与审计链路完整率达标。

## Phase 3（高级能力）

范围（必须）：
1. Multi-agent（在 Merge Contract 与 ROI 门槛下灰度）。
2. Self-heal advanced（模板化自动治理闭环）。
3. Retrieval L2/L3 按 feature gate 渐进启用。
4. 跨地域容灾演练与 DSAR 全链路。

门禁：
1. Multi-agent ROI > 0 且成本可控。
2. Replay/Eval 放大不压垮在线链路。
3. 容灾与合规时限达标。

## 阶段外扩展（持续演进）

1. 研究型策略仅在 Experimental 域验证。
2. 通过 Eval 证据后才可进入 Phase 3 灰度。

---

## 24. 验收标准（硬指标）

1. 任务成功率 `>=99.5%`。
2. 自愈成功率 `>=80%`。
3. 交互 `P95<=6s`，首 token `P95<=1.2s`。
4. park/resume 后 Worker 利用率提升 `>=30%`。
5. 无效上下文注入率下降 `>=40%`。
6. 错误记忆写入率 `<1%`。
7. 重复副作用事故 `0`。
8. 运行中实例混配恢复事故 `0`。
9. 成本可归因到租户/项目/模型/步骤。
10. 审批与变更审计完整率 `100%`。
11. 定时任务漏触发率 `<=0.1%`，重复触发率 `<=0.01%`。
12. 定时触发延迟 `P95<=3s`（到点到 run 创建）。
13. VIP 触发延迟 `P95<=1s`，VIP 抢占成功率 `>=95%`（仅在允许抢占场景）。
14. 标准租户饥饿率 `<=0.5%`（等待超阈值任务占比）。
15. 调度误杀率（不应被限流却被拒绝）`<=0.1%`。
16. 检索 `Recall@20` 与 `nDCG@10` 达到基线阈值，且 `No-hit Rate` 持续下降。
17. 检索链路延迟 `P95` 不超过预算，超时降级成功率 `>=99%`。
18. 节点故障发现到隔离执行 `P95<=30s`。
19. GPU 严重故障误放流率 `0`，NIC 慢卡对网络敏感任务影响率持续下降。
20. 外部检测器协议升级后，调度核心无需改动（仅适配层变更）通过回归验证。
21. `policy_bundle` 均具签名与回放证据，未签名 bundle 上线率 `0`。
22. 策略引擎故障时“放宽权限”事件 `0`。
23. `deterministic_diff_rate`（Context Compiler，且仅统计 deterministic 区）`<=0.1%`。
24. Eval 关键指标门禁基于置信区间下界执行，且作为证据输入参与融合裁决，违规放行事件 `0`。
25. 开发者自助调试链路（validate/simulate/dry-run/replay）成功率 `>=99%`。
26. 多 Agent 分支取消后资源回收延迟 `P95<=2s`。
27. 多 Agent Merge 可回放覆盖率 `100%`。
28. 依赖解析可复现率（同输入同输出）`>=99.9%`。
29. 依赖冲突漏检率 `0`（上线后发生的未检测冲突事件）。
30. run 恢复时依赖快照不一致事故 `0`。
31. 调试接口可复现率（debug 输出可直接 replay 成功）`>=99%`。
32. 知识库文档 canonical 追溯完整率（`object_uri+version_id+hash`）`=100%`。
33. DFS 缓存命中失败导致错误召回事故 `0`（仅允许性能退化，不允许正确性退化）。
34. `policy-spec.md` 字段契约覆盖率（输入/输出/时机/失败/幂等）`=100%`。
35. `eval-spec.md` 字段契约覆盖率（输入/输出/时机/失败/幂等）`=100%`。
36. Context Compiler 容灾演练通过率（回滚/降级/高风险阻断）`=100%`。
37. 依赖变更发布前 `blast_radius_report` 覆盖率 `=100%`。
38. 高风险依赖变更未评审直发事故 `0`。

## 24.1 Gate 配置附录（YAML，可机器执行）

用途：把 `11`（Policy）、`18.11`（Eval 阈值）、`4.11/skills-spec`（Skill 注入边界）、`23`（阶段门禁）统一落成 CI/CD 可执行配置。

```yaml
gate_config_version: v1
source_sections:
  policy_spec: "agent/doc/specs/policy-spec.md"
  eval_spec: "agent/doc/specs/eval-spec.md"
  skills_spec: "agent/doc/specs/skills-spec.md"
  eval_thresholds: "18.11"
  rollout_phases: "23"

global:
  evidence_pack_required: true
  decision_mode: "all_required_pass"
  default_fail_mode:
    critical: "fail_closed"
    high: "fail_closed"
    medium: "require_review"
    low: "fail_soft"
  on_gate_fail:
    action: "block_release"
    emit_event: "gate.blocked"

policy_gate:
  required: true
  checks:
    - id: "bundle_signed"
      expr: "policy.bundle.signature_valid == true"
    - id: "bundle_replayable"
      expr: "policy.bundle.replay_supported == true"
    - id: "shadow_sample_min"
      metric: "policy.shadow.sample_count"
      op: ">="
      value: 100000
    - id: "shadow_diff_rate_max"
      metric: "policy.shadow.diff_rate"
      op: "<="
      value: 0.02
    - id: "inline_eval_p95_ms"
      metric: "policy.eval.inline.p95_ms"
      op: "<="
      value: 10
    - id: "remote_eval_p95_ms"
      metric: "policy.eval.remote.p95_ms"
      op: "<="
      value: 50
    - id: "critical_false_allow_zero"
      metric: "policy.regression.critical_false_allow"
      op: "=="
      value: 0

eval_gate:
  required: true
  min_sample_size:
    critical: 2000
    high: 1000
    medium: 500
    low: 200
  thresholds:
    tsa_lower_bound:
      critical: 0.995
      high: 0.995
      medium: 0.98
      low: 0.95
    afs_lower_bound:
      critical: 0.99
      high: 0.99
      medium: 0.97
      low: 0.94
    eoc_lower_bound:
      critical: 0.98
      high: 0.98
      medium: 0.95
      low: 0.90
    citation_adequacy_lower_bound:
      critical: 0.99
      high: 0.99
      medium: 0.97
      low: 0.93
    memory_harm_rate_upper_bound:
      critical: 0.002
      high: 0.005
      medium: 0.01
      low: 0.02
    multi_agent_enablement:
      handoff_roi_op: ">"
      handoff_roi_value: 0
      invalid_handoff_rate_max: 0.01
      loop_handoff_rate_max: 0.005
  scoring:
    confidence_interval: "wilson_lower_bound"
    require_dataset_and_grader_hash: true
    missing_evidence_pack: "invalid_result"

fast_path_eval:
  enabled: true
  low_risk_only: true
  required: false
  eligibility:
    change_scope_allowlist:
      - "prompt_template_tuning"
      - "ui_observability_change"
      - "low_risk_retrieval_param_tuning"
    forbidden_changes:
      - "policy_bundle_change"
      - "tool_side_effect_contract_change"
      - "approval_routing_change"
      - "context_compiler_major_or_minor_change"
    requires_recent_stable_days: 7
  checks:
    - id: "risk_tier_low_only"
      expr: "release_manifest.risk_tier == 'low'"
    - id: "critical_false_allow_zero"
      metric: "policy.regression.critical_false_allow"
      op: "=="
      value: 0
    - id: "low_risk_sample_min"
      metric: "eval.low_risk.sample_count"
      op: ">="
      value: 200
  rollout:
    max_canary_traffic_ratio: 0.05
    full_shadow_eval_within_hours: 24
    on_shadow_fail:
      action: "auto_rollback_and_freeze_fast_path"
      emit_event: "gate.fast_path_rollback"

phase_gate:
  required: true
  target_phase_from: "release_manifest.phase_target"
  profiles:
    phase1:
      requires:
        - metric: "slo.task_success_rate"
          op: ">="
          value: 0.995
        - metric: "runtime.recovery_consistency_rate"
          op: ">="
          value: 0.999
        - metric: "audit.completeness_rate"
          op: "=="
          value: 1.0
        - metric: "incident.duplicate_side_effect_count"
          op: "=="
          value: 0
    phase2:
      requires:
        - metric: "schedule.missed_trigger_rate"
          op: "<="
          value: 0.001
        - metric: "schedule.duplicate_trigger_rate"
          op: "<="
          value: 0.0001
        - metric: "infra.isolation_p95_sec"
          op: "<="
          value: 30
        - metric: "gate.bad_change_block_rate"
          op: ">="
          value: 0.99
    phase3:
      requires:
        - metric: "policy.regression.false_block_rate"
          op: "<="
          value: 0.02
        - metric: "dev_debug_api.success_rate"
          op: ">="
          value: 0.99
        - metric: "multi_agent.handoff_roi"
          op: ">"
          value: 0
        - metric: "dr.drill_passed"
          op: "=="
          value: 1
        - metric: "dsar.sla_met_rate"
          op: ">="
          value: 0.99

release_decision:
  required: true
  engine: "release_decision_engine.v1"
  evidence_requirements:
    critical:
      - "eval_gate"
      - "policy_gate"
      - "replay_consistency"
      - "incident_trend"
      - "human_signoff"
    high:
      - "eval_gate"
      - "policy_gate"
      - "replay_consistency"
      - "incident_trend"
      - "human_signoff"
    medium:
      - "eval_gate"
      - "policy_gate"
      - "replay_consistency"
    low:
      - "eval_gate_or_fast_path_eval"
      - "policy_gate"
  outputs:
    - "decision_confidence"
    - "evidence_completeness"
    - "final_decision"

pipelines:
  - pipeline_id: "release_candidate"
    stages:
      - "policy_gate"
      - "eval_gate"
      - "phase_gate"
      - "release_decision"
    on_pass:
      action: "publish_rc"
      emit_event: "release.rc_ready"
  - pipeline_id: "prod_promote"
    depends_on: "release_candidate"
    extra_checks:
      - metric: "canary.error_rate_delta"
        op: "<="
        value: 0.01
      - metric: "canary.p95_latency_delta_ms"
        op: "<="
        value: 200
    on_fail:
      action: "auto_rollback"
      emit_event: "release.rollback"
  - pipeline_id: "prod_promote_fast_low_risk"
    depends_on: "release_candidate"
    when:
      expr: "release_manifest.fast_path_eval_enabled == true"
    stages:
      - "policy_gate"
      - "fast_path_eval"
      - "phase_gate"
      - "release_decision"
    on_pass:
      action: "publish_canary_5_percent"
      emit_event: "release.fast_path_canary"
    on_fail:
      action: "block_release"
      emit_event: "release.fast_path_blocked"
```

CI/CD 接入约定：
1. Gate 引擎输入：`release_manifest + metric_snapshot + evidence_pack`。
2. Gate 引擎输出：`pass|fail|review_required` 与失败明细；最终发布结论由 `release_decision` 产出。
3. `critical/high` 失败不可人工跳过；`medium/low` 仅允许带签字例外放行。
4. `fast_path_eval` 仅可在 `risk_tier=low` 且满足准入条件时启用；否则自动回退完整 `eval_gate`。

## 24.2 Gate 预置档位（用户易用）

1. `starter`：仅启用 `policy_bundle_signed + critical_false_allow_zero + 基础样本量`，用于低风险起步阶段。
2. `standard`：启用 `policy_gate + eval_gate + phase_gate(phase1/phase2) + release_decision`，用于常规生产。
3. `strict`：全量启用并要求 `phase_gate(phase1/phase2/phase3) + release_decision`，用于高风险业务。
4. `fast_low_risk`：启用 `policy_gate + fast_path_eval + phase_gate + release_decision`，仅允许低风险白名单改动。
5. 档位切换规则：只能从低到高升级，降级需治理签字与风险说明。

## 24.3 Gate 字段字典（对接观测与 CI）

1. `gate_config_version`：配置语义版本，变更需向后兼容或升级主版本。
2. `source_sections`：文档规范映射来源，便于审计追溯。
3. `policy_gate.checks[].expr`：布尔表达式，适用于存在/签名/状态类检查。
4. `policy_gate.checks[].metric`：指标键名，必须可在 `metric_snapshot` 解析到。
5. `op`：比较运算符，支持 `== != > >= < <=`。
6. `value`：阈值常量；浮点指标统一用小数（比例）而非百分数字符串。
7. `eval_gate.min_sample_size.*`：各风险等级样本量下限，低于下限自动标记 `insufficient_sample`。
8. `scoring.confidence_interval`：置信区间算法标识，当前固定 `wilson_lower_bound`。
9. `phase_gate.target_phase_from`：从发布清单读取目标阶段的字段路径。
10. `pipelines[].stages`：执行顺序；任一 stage fail 默认短路阻断。
11. `on_fail.action`：失败动作，当前支持 `block_release|auto_rollback|manual_review`。
12. 指标命名规范：`<domain>.<subdomain>.<metric_name>`，例如 `policy.eval.inline.p95_ms`。
13. `fast_path_eval.eligibility.*`：低风险快速路径准入条件；任一不满足即回退完整 eval。
14. `fast_path_eval.rollout.max_canary_traffic_ratio`：快速路径最大灰度比例，超出视为配置错误。
15. `fast_path_eval.rollout.full_shadow_eval_within_hours`：补跑 full eval 的时限；超时自动标记 `fast_path_expired`。
16. `release_decision.evidence_requirements.*`：各风险等级所需证据清单；缺失即 `fail/review_required`。
17. `release_decision.outputs.*`：裁决输出字段，至少包含 `final_decision/decision_confidence/evidence_completeness`。

对接约束：
1. `metric_snapshot` 必须携带 `metric_key/value/timestamp/source` 四元组。
2. 未知指标键默认按 fail 处理（禁止静默忽略）。
3. 同一指标多来源冲突时，按 `source_priority` 取值并写入 `gate.metric_conflict` 事件。

## 24.4 边界消歧清单（全书审计结论）

1. 上下文决策边界：仅 `Context Compiler` 可做最终注入裁决（见 7.1、4.6）。
2. 因果解释边界：高风险 run 必须具备完整 `Decision Causality Graph`，缺边不得放行（见 4.10、19.17）。
3. 权限与审批边界：`policy deny` 不可被审批覆盖（见 12.5、11.5）。
4. 调度与健康边界：检测器只上报，调度器不依赖检测器实现细节（见 4.5、19.10）。
5. 数据真值边界：KB 原文真值在对象存储，DFS 仅缓存（见 D26、9.10、14.6）。
6. 依赖配置边界：运行中 run 固定 `dependency_bundle_id`，恢复不得隐式替换（见 5.12、20.1、20.4）。
7. 发布裁决边界：发布最终结论由 `Release Decision Engine` 产出，`Eval Gatekeeper` 为必选证据输入（critical/high 不可跳过）（见 4.6、18.14、24.1）。
8. 自动化边界：Self-heal 仅执行模板化收紧动作，不得自动 promote（见 17.4）。
9. API 暴露边界：`/internal` 不对租户暴露，跨边界调用必须留痕（见 19.15）。
10. 可选能力边界：A2A/GraphDB/DFS cache/Multi-agent/异构调度均受 feature gate 约束（见 4.7）。
11. 异构调度边界：跨类迁移受 safe boundary 约束，`irreversible` 任务禁迁移（见 5.8、19.13）。
12. Skill 注入边界：skill 不得修改 `required_permissions/effect_type/approval_policy_ref`（见 5.10.1、4.11、19.18）。
13. Policy/Eval/Skill 执行细则边界：字段/时机/失败/幂等统一由 `policy-spec.md`、`eval-spec.md`、`skills-spec.md` 约束。
14. 正确性边界：Policy/Eval/Compiler 提供“可控性与可阻断性”，不等于“结论绝对正确”（见 D34、D35、D36）。
15. 耦合治理边界：跨域依赖必须登记到 Coupling Registry，禁止隐式耦合上线（见 4.14）。

## 24.5 评审问题闭环映射

1. Blocker-Policy/Eval/Skill 可执行规范 -> `policy-spec.md`、`eval-spec.md`、`skills-spec.md`。
2. Blocker-Compiler 单点风险 -> `7.7`（版本、回滚、失败策略、高风险阻断）。
3. Blocker-路线失真 -> `23`（三阶段强约束 + 阶段门禁）。
4. High-平台过载 -> `4.8`（Core/Extended/Experimental 分层）。
5. High-Merge 执行协议不足 -> `13.9`（Merge Contract）。
6. High-容量不闭环 -> `21.11`（QPS->CPU->IO->Storage->Cost）。
7. High-DX 偏弱 -> `19.16`（本地/CI/IDE/可视化调试）。
8. Medium-RAG 研究化 -> `9.9`（L1/L2/L3 强制门槛）。
9. Medium-Scheduler 过强 -> `5.5` + `23`（Phase1 scope lock）。
10. Medium-数据成本风险 -> `15.10`（hot/warm/cold retention policy）。
11. 新增-跨 Decider 隐式耦合 -> `4.10` + `19.17`（Decision Causality Graph）。
12. 新增-Context 候选爆发风险 -> `7.9`（Candidate Budget Gate，编译前闸门）。
13. 新增-Eval 瓶颈 -> `18.12` + `24.1`（fast_path_eval 低风险快速路径）。
14. 新增-确定性假设过强 -> `D35` + `policy-spec#11`（确定性/统计边界分离）。
15. 新增-Eval 被当真值 -> `D36` + `18.14` + `eval-spec#10.1`（证据融合裁决）。
16. 新增-错误传播缺失 -> `15.12`（Error Propagation & Containment）。
17. 新增-调试复杂度上升 -> `19.20`（root_cause_pack + first_bad_node）。
18. 新增-关键路径过长 -> `21.12`（关键路径时延预算与缩链策略）。

---

## 25. 参考资料（论文 / 官方工程实践 / 标准）

### 25.1 官方工程实践与标准

- [E1] OpenAI, *A practical guide to building agents*  
  https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/
- [E2] OpenAI, *Evaluation best practices*  
  https://developers.openai.com/api/docs/guides/evaluation-best-practices
- [E3] OpenAI, *Conversation state*  
  https://platform.openai.com/docs/guides/conversation-state
- [E4] OpenAI, *Prompt caching*  
  https://platform.openai.com/docs/guides/prompt-caching/prompt-caching
- [E5] OpenAI, *Migrate to Responses API*  
  https://platform.openai.com/docs/guides/migrate-to-responses
- [E6] Anthropic, *Building effective agents* (2024-12-19)  
  https://www.anthropic.com/engineering/building-effective-agents
- [E7] Anthropic, *How we built our multi-agent research system* (2025-06-13)  
  https://www.anthropic.com/engineering/built-multi-agent-research-system
- [E8] Anthropic, *Effective context engineering for AI agents* (2025-09-29)  
  https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
- [E9] Anthropic, *Demystifying evals for AI agents* (2026-01-09)  
  https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents
- [E10] Temporal, *Worker deployments*  
  https://docs.temporal.io/production-deployment/worker-deployments
- [E11] Temporal, *Worker Versioning*  
  https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning
- [E12] AWS, *Transactional Outbox pattern*  
  https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html
- [E13] MCP Specification (2025-06-18)  
  https://modelcontextprotocol.io/specification/2025-06-18
- [E14] OpenTelemetry, *GenAI agent spans semantic conventions*  
  https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-agent-spans/
- [E15] OPA, *Policy Language (Rego)*  
  https://www.openpolicyagent.org/docs/policy-language
- [E16] A2A Protocol Specification  
  https://a2a-protocol.org/dev/specification/
- [E17] Linux Foundation, *A2A project launch* (2025-06-23)  
  https://www.linuxfoundation.org/press/linux-foundation-launches-the-agent2agent-protocol-project-to-enable-secure-intelligent-communication-between-ai-agents
- [E18] OWASP, *Top 10 for LLM Applications 2025*  
  https://genai.owasp.org/llm-top-10/
- [E19] NIST, *AI RMF: Generative AI Profile (NIST AI 600-1)*  
  https://www.nist.gov/publications/artificial-intelligence-risk-management-framework-generative-artificial-intelligence
- [E20] Kubernetes, *CronJob*（并发策略、时区、missed schedules）  
  https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/
- [E21] Kubernetes, *Pod Priority and Preemption*  
  https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/
- [E22] Kubernetes, *API Priority and Fairness*  
  https://kubernetes.io/docs/concepts/cluster-administration/flow-control/
- [E23] OpenAI, *Retrieval guide*（vector store、chunking、attributes、batch ingestion）  
  https://developers.openai.com/api/docs/guides/retrieval
- [E24] OpenAI, *File search tool guide*  
  https://developers.openai.com/api/docs/guides/tools-file-search
- [E25] Elasticsearch, *Hybrid search / RRF overview*  
  https://elastic.aiops.work/guide/en/elasticsearch/reference/8.19/re-ranking-overview.html
- [E26] Pinecone, *Hybrid search*（dense+sparse 融合与权重）  
  https://docs.pinecone.io/guides/search/hybrid-search
- [E27] Kubernetes, *Monitor Node Health*  
  https://kubernetes.io/docs/tasks/debug/debug-cluster/monitor-node-health/
- [E28] Kubernetes, *Taints and Tolerations*  
  https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/
- [E29] Kubernetes, *Node Problem Detector*  
  https://github.com/kubernetes/node-problem-detector
- [E30] NVIDIA, *DCGM Feature Overview*  
  https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/feature-overview.html

### 25.2 论文与基准

- [P1] ReAct  
  https://arxiv.org/abs/2210.03629
- [P2] Toolformer  
  https://arxiv.org/abs/2302.04761
- [P3] AgentBench  
  https://arxiv.org/abs/2308.03688
- [P4] SWE-bench  
  https://arxiv.org/abs/2310.06770
- [P5] GAIA  
  https://arxiv.org/abs/2311.12983
- [P6] BFCL (ICML 2025)  
  https://proceedings.mlr.press/v267/patil25a.html
- [P7] ToolSandbox  
  https://arxiv.org/abs/2408.04682
- [P8] RAG  
  https://arxiv.org/abs/2005.11401
- [P9] Self-RAG  
  https://arxiv.org/abs/2310.11511
- [P10] CRAG  
  https://arxiv.org/abs/2401.15884
- [P11] RAPTOR  
  https://arxiv.org/abs/2401.18059
- [P12] GraphRAG  
  https://arxiv.org/abs/2404.16130
- [P13] Ragas  
  https://arxiv.org/abs/2309.15217
- [P14] τ-bench  
  https://arxiv.org/abs/2406.12045
- [P15] ARES  
  https://arxiv.org/abs/2311.09476
- [P16] FActScore  
  https://arxiv.org/abs/2305.14251
- [P17] HyDE: Precise Zero-Shot Dense Retrieval  
  https://arxiv.org/abs/2212.10496
- [P18] ColBERTv2: Effective and Efficient Retrieval via Lightweight Late Interaction  
  https://arxiv.org/abs/2112.01488
- [P19] REPLUG: Retrieval-Augmented Black-Box Language Models  
  https://arxiv.org/abs/2301.12652
- [P20] Lost in the Middle: How Language Models Use Long Contexts  
  https://arxiv.org/abs/2307.03172
- [P21] LightRAG: Simple and Fast Retrieval-Augmented Generation  
  https://arxiv.org/abs/2410.05779
