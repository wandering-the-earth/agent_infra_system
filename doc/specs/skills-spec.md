# skills-spec.md（执行级规范）

## 0. 规范地位

1. 本文为 Skill 注入子规范，和主文档同级约束。
2. 主文档定义架构边界，本文定义字段契约、执行时机、失败行为与幂等语义。
3. 若主文档与本文冲突，以本文执行规则为准。

## 1. 核心对象

1. `SkillBundle`：可发布、可签名、可回滚的技能包。
2. `SkillProfile`：workflow/node 绑定的技能配置视图。
3. `SkillInjectionRequest`：一次注入请求上下文。
4. `SkillInjectionResult`：注入后产物及约束检查结果。

## 2. 字段契约（字段级）

强规则：
1. 任何新增字段必须补充：`input type`、`output type`、`execution time`、`failure behavior`、`idempotence`。
2. 未满足字段契约的变更禁止进入 skill bundle 发布流程。

### 2.1 SkillBundle

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `bundle_id` | string | string | create/update | reject duplicate id | idempotent by id |
| `version` | semver | semver | publish | reject invalid/non-monotonic | monotonic only |
| `scope` | enum(tenant/project/workflow/node) | enum | publish + resolve | reject unknown scope | idempotent |
| `prompt_patch` | object | normalized object | inject | reject invalid schema | deterministic |
| `tool_allowlist` | string[] | string[] | inject | reject unknown tool id | deterministic |
| `retrieval_profile_ref` | string | string | inject | fallback by policy matrix | idempotent |
| `memory_policy_ref` | string | string | inject | fallback by policy matrix | idempotent |
| `eval_tags` | string[] | string[] | inject | empty allowed | deterministic |
| `owner` | string | string | governance | reject missing | idempotent |
| `signature` | bytes | bytes | publish + verify | reject invalid signature | deterministic |

### 2.2 SkillInjectionRequest

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `run_id` | string | string | inject start | reject missing | idempotent |
| `step_id` | string | string | inject start | reject missing | idempotent |
| `skill_profile_ref` | string | resolved profile | inject start | reject unknown profile | idempotent |
| `phase` | enum(PRE_CONTEXT_COMPILE/PRE_MODEL/PRE_TOOL/PRE_RESUME) | enum | inject start | reject unknown phase | deterministic |
| `risk_tier` | enum | enum | inject start | reject missing | deterministic |
| `trace_id` | string | string | inject start | reject missing | idempotent |
| `decision_graph_id` | string | string | inject start | reject missing for high risk | idempotent |

### 2.3 SkillInjectionResult

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `injection_id` | generated uuid | uuid | inject return | fail if generation fails | globally unique |
| `status` | computed enum(applied/blocked/degraded) | enum | inject return | fail by risk matrix | deterministic |
| `applied_bundle_id` | resolved bundle | string | inject return | empty when blocked | deterministic |
| `blocked_reasons` | rule results | string[] | inject return | empty allowed | deterministic |
| `decision_node_id` | generated id | string | inject return | reject missing for high risk | idempotent |

## 3. 执行时机与顺序

1. `PRE_CONTEXT_COMPILE`：影响候选筛选偏好与检索参数，不得写权限字段。
2. `PRE_MODEL`：注入模型层 skill 片段（模板、风格、约束提示）。
3. `PRE_TOOL`：仅允许收紧 tool allowlist，不允许扩权。
4. `PRE_RESUME`：恢复后可追加低风险提示，不得放宽前序 obligation。

顺序规则：
1. 同一 phase 多 skill 冲突时，按 `scope_specificity > version > updated_at`。
2. 与 Policy obligation 冲突时，Policy 优先。
3. 审批返回的限制高于 skill 注入限制。

## 4. 与 Policy/Approval/Context 的边界

1. Skill 不得修改：`required_permissions`、`approval_policy_ref`、`effect_type`。
2. Skill 不得绕过审批触发条件，不得覆盖 `deny` 决策。
3. Skill 注入只影响“执行建议与参数收敛”，不改变 final decider 归属。
4. Skill 注入产物必须进入 `Decision Causality Graph`。

## 5. 失败矩阵

1. high/critical + 注入失败：`fail_closed` 或 `require_review`（按风险模板）。
2. medium：`require_review`。
3. low：允许 `degraded`（无 skill 继续）但必须审计留痕。
4. 签名校验失败一律 `blocked`。

## 6. 幂等语义

1. 注入幂等键：`hash(run_id + step_id + phase + skill_profile_ref + bundle_version)`。
2. 重试必须返回同一 `status/applied_bundle_id/decision_node_id`。
3. `blocked` 结果重复请求不得转为 `applied`，除非 bundle 或策略版本变化。

## 7. 观测与审计

1. 事件：`skill.bundle_resolved|skill.injected|skill.blocked_by_policy|skill.injection_degraded`。
2. 必含字段：`bundle_id`、`bundle_version`、`phase`、`risk_tier`、`trace_id`、`decision_graph_id`。
3. 审计包必须包含：bundle 哈希、签名、作用域、阻断原因、注入结果摘要。

## 8. 发布门禁（Skill 专项）

1. `signature_valid=100%`。
2. `forbidden_field_override_count=0`。
3. `blocked_by_policy` 比例异常升高必须阻断发布并复盘。
4. high/critical 场景的 skill 注入回放通过率必须达标。
