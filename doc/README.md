# Agent 文档索引（分层版）

更新时间：2026-04-08

---

## 1. 为什么要分层

文档建议按“**规范**”和“**说明**”分开，不混写：

1. 规范文档：用于裁决边界、门禁与上线规则，要求可执行、可审计。
2. 说明文档：用于帮助理解系统和上手，不作为最终裁决依据。

这样做的好处：

1. 读者不会把“讲解示例”误当“上线约束”。
2. 评审时可以快速定位唯一权威来源。
3. 迭代讲解内容时，不会影响规范稳定性。

---

## 2. 文档分组

## 2.1 设计与规范（权威裁决层）

1. `design/Agent系统设计-主文档.md`：架构与系统设计主规范。
2. `design/Agent-Infra-精简化落地方案-全问题覆盖.md`：精简架构实施方案（Kernel 边界、容量、治理、验收）。
3. `design/Agent-Infra-优先级与实施边界-强约束.md`：阶段边界与范围裁决。
4. `specs/policy-spec.md`：Policy 执行级规范（字段/时机/失败/幂等）。
5. `specs/eval-spec.md`：Eval 执行级规范（grader/数据集/回放/门禁）。
6. `specs/skills-spec.md`：Skill 注入执行级规范（边界/签名/幂等/失败矩阵）。

## 2.2 工程实现导读（代码落地层）

1. `guides/工程代码导读-从零到跑通.md`：逐文件职责、端到端流程、最小跑通操作。

## 2.3 项目说明与上手（说明层）

1. `guides/Agent-Infra-项目说明-全流程原理与运行机制.md`：从常见 Agent 场景解释触发、预算、取证与业务意义。

## 2.4 团队治理与协作（流程层）

1. `governance/团队开发守则.md`：研发协作、评审、发布、回滚、值班规则。

---

## 3. 冲突时以谁为准

1. 范围与阶段冲突：以 `design/Agent-Infra-优先级与实施边界-强约束.md` 为准。
2. Policy 语义冲突：以 `specs/policy-spec.md` 为准。
3. Eval 语义冲突：以 `specs/eval-spec.md` 为准。
4. Skill 注入语义冲突：以 `specs/skills-spec.md` 为准。
5. 架构边界冲突：以 `design/Agent系统设计-主文档.md` 为准。
6. 团队流程冲突：以 `governance/团队开发守则.md` 为准。
7. 说明文档与规范冲突：一律以“设计与规范层”文档为准。

---

## 4. 发布前一致性检查

1. 路径检查：索引必须覆盖全部现行文档，不得引用历史归档路径。
2. 术语检查：关键术语（如 Context Compiler、Policy Bundle、Eval Gate）必须单点定义。
3. 门禁检查：Policy 与 Eval 门禁必须同时通过。
4. 因果链检查：高风险 run 必须有完整 Decision Causality Graph 关键路径。
5. 语言边界检查：Core 运行域为 Go；离线/实验可用 Python；不得引入 Core 第二主语言。
6. 外包边界检查：执行层可外包，Decision/Evidence 真值语义不可外包。
7. 指标语义检查：区分 deterministic 与 stochastic 指标，禁止用单一 Eval 指标宣称“绝对正确”。
