# policy-spec.md（执行级规范）

## 0. 规范地位

1. 本文为 Policy 子规范，和主文档同级约束。
2. 主文档中的策略章节描述原则，本文定义实施级规则。
3. 若主文档与本文冲突，以本文字段契约与失败语义为准。

## 1. 术语与对象

1. `PolicyDSL`：业务可审查规则定义。
2. `PolicyIR`：编译后中间表示（engine-agnostic）。
3. `EnginePolicy`：Rego/CEL 等执行引擎可执行规则。
4. `PolicyBundle`：一组规则与签名、元数据、编译产物。
5. `Decision`：单次策略判定结果（allow/deny/require_approval/require_review）。
6. `Obligation`：策略判定后必须执行的附加动作（tag/limit/audit/template）。

## 2. 输入输出契约（字段级）

强规则：
1. 任何新增字段必须补充：`input type`、`output type`、`execution time`、`failure behavior`、`idempotence`。
2. 未满足字段契约的变更禁止进入 bundle 发布流程。

### 2.1 PolicyDSL 字段契约

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `policy_id` | string | string | policy create/update | reject on empty/duplicate in same scope | idempotent by key |
| `version` | int | int | policy create/update | reject on non-monotonic increment | monotonic only |
| `scope` | enum(global/tenant/project/workflow/step) | enum | compile + eval | reject unknown scope | idempotent |
| `effect` | enum(allow/deny/require_approval/require_review) | enum | compile + eval | reject unknown effect | idempotent |
| `priority` | int(0-1000) | int | conflict resolution | clamp not allowed; reject out of range | idempotent |
| `conditions` | expr[] | normalized expr[] | compile | reject parse/type error | idempotent |
| `obligations` | obligation[] | obligation[] | post-decision | reject unknown obligation type | idempotent per obligation_key |
| `exceptions` | expr[] | expr[] | eval | reject parse/type error | idempotent |
| `effective_at` | RFC3339 timestamp | timestamp | eval | reject invalid format | idempotent |
| `expires_at` | RFC3339 timestamp | timestamp | eval | reject if <= effective_at | idempotent |
| `owner` | string | string | governance | reject missing | idempotent |
| `reviewer_group` | string | string | governance | reject missing | idempotent |
| `ticket_ref` | string | string | governance | reject missing | idempotent |

### 2.2 Decision 字段契约

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `decision_id` | generated uuid | uuid | eval return | fail request if cannot generate | globally unique |
| `decision` | computed enum | enum | eval return | fallback by fail matrix | idempotent with request hash |
| `matched_rule_ids` | rule set | string[] | eval return | empty allowed | deterministic |
| `obligations` | matched obligations | obligation[] | eval return | if obligation build fails, apply fail matrix | deterministic |
| `trace_id` | request trace | string | eval return | reject if missing trace context | idempotent |
| `policy_bundle_id` | bound bundle | string | eval return | reject if unbound | idempotent |
| `decision_graph_id` | run graph id | string | eval return | reject missing for high risk | idempotent |
| `decision_node_id` | generated node id | string | eval return | reject if generation fails | globally unique in graph |
| `parent_decision_node_ids` | string[] | string[] | eval return | empty allowed for root | deterministic |

## 3. DSL -> IR -> Engine 编译规范

## 3.1 编译流水线

1. Parse: DSL -> AST。
2. Normalize: AST -> canonical AST（常量折叠、字段名标准化、scope 展开）。
3. Type-check: 字段类型与操作符一致性检查。
4. IR generate: canonical AST -> PolicyIR。
5. Engine lower: PolicyIR -> Rego/CEL 目标规则。
6. Semantic diff: DSL 语义样本集与 EnginePolicy 结果逐条对比。
7. Sign: 产出 `PolicyBundle` 并签名。

## 3.2 语义保持（必过）

1. `semantic_equivalence_rate` 必须 `=100%`（在 policy parity test set）。
2. parity set 最小样本：每条规则至少 `20` 正例 + `20` 反例。
3. 任一规则 parity 失败禁止发布。

说明：
1. `semantic_equivalence_rate=100%` 仅约束“DSL -> IR -> Engine 编译语义保持”，不声明业务运行结果绝对正确。
2. 业务正确性仍受上游输入质量、外部系统行为和统计误差影响，需结合 Eval/Replay/审计证据判定。

## 3.3 编译失败语义

1. 单条规则编译失败：该规则标记 `invalid`，bundle 构建失败。
2. 引擎降级失败：禁止 fallback 到“宽松解释器”。
3. 签名失败：bundle 不可发布。

## 4. Obligation 执行模型

## 4.1 Obligation 类型

1. `attach_tag`：为 run/step 添加治理标签。
2. `limit_param`：收紧参数范围。
3. `require_template`：绑定审批模板。
4. `emit_audit`：强制审计落盘。

## 4.2 执行顺序（固定）

1. `limit_param`
2. `attach_tag`
3. `require_template`
4. `emit_audit`

## 4.3 顺序冲突处理

1. 同一字段多个 `limit_param`：取“最严格交集”。
2. 若交集为空：判定 `deny` 并记录 `obligation_conflict`。
3. `attach_tag` 冲突：并集去重。
4. `require_template` 冲突：取高风险模板（按模板风险等级排序）。
5. `emit_audit` 不可被覆盖或删除。

## 4.4 Obligation 字段契约

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `obligation_id` | string | string | obligation build | reject duplicate in same rule | idempotent |
| `type` | enum | enum | obligation build | reject unknown type | idempotent |
| `target` | field path | field path | runtime apply | reject invalid path | idempotent |
| `value` | scalar/object | scalar/object | runtime apply | fail by risk matrix | idempotent with `(decision_id, obligation_id)` |
| `phase` | enum(PRE_DISPATCH/PRE_TOOL/POST_TOOL/PRE_RESUME) | enum | runtime schedule | reject invalid phase | idempotent |
| `fail_mode` | enum(fail_closed/fail_soft/require_review) | enum | runtime apply | default from risk tier | deterministic |

## 4.5 跨相位冲突与合并规则

## 4.5.1 PRE_TOOL vs PRE_RESUME 重复 obligation

1. 冲突键：`(type, target)`。
2. 若同键 obligation 在 `PRE_TOOL` 与 `PRE_RESUME` 同时存在：
3. `PRE_RESUME` 仅允许“同等或更严格”约束。
4. 若 `PRE_RESUME` 试图放宽约束，判定 `obligation_phase_conflict`，保留 `PRE_TOOL` 结果并触发 `require_review`（中高风险）或 `fail_closed`（高风险写）。

## 4.5.2 审批后新增 obligation 合并

1. 审批返回 obligation 记为 `approval_obligations`，与原策略 obligation 执行并集。
2. 合并优先级：`policy_obligation >= approval_obligation`（审批不得放宽策略）。
3. 同键冲突取“最严格交集”；交集为空则 `deny`。
4. 合并后写 `obligation_merged_set` 到审计证据包。

## 4.5.3 obligation 与 workflow fallback 优先级

1. obligation 执行优先于业务节点执行与 workflow fallback 分支选择。
2. 若 obligation `fail_mode=fail_closed`，禁止进入 workflow fallback，直接阻断。
3. 若 obligation `fail_mode=require_review`，允许转审批/人工，不允许自动 fallback 到更高权限路径。
4. 仅 `fail_soft` 且 fallback 分支 `effect_type` 不高于原路径时，允许进入 fallback。

## 5. 多 bundle 并存策略

## 5.1 并存维度

1. `tenant` 维度并存（不同租户不同 bundle）。
2. `workflow` 维度并存（同租户不同工作流可绑定不同 bundle）。
3. `run` 维度冻结（同 run 生命周期内 bundle 不变）。

## 5.2 选择规则

1. 先按 `tenant/project/workflow` 绑定查找。
2. 未命中则回退到 `tenant default`。
3. 再未命中回退到 `platform baseline`。
4. 命中多个候选时按 `scope specificity > bundle_priority > updated_at`。

## 5.3 版本冲突

1. 同一绑定位只允许一个 `active bundle`。
2. 多版本并存仅允许在 `canary scope`。
3. canary 结束必须显式 promote 或 rollback，不允许长期悬挂。

## 6. Policy Eval 请求协议

### 6.1 Request

```json
{
  "request_id": "req_...",
  "tenant_id": "t_001",
  "project_id": "p_001",
  "workflow_id": "wf_abc",
  "step_id": "step_3",
  "action": "refund",
  "resource": {
    "type": "order",
    "id": "o_123"
  },
  "risk_tier": "high",
  "effect_type": "external_write",
  "decision_graph_id": "dg_01",
  "parent_decision_node_ids": ["node_model_09"],
  "inputs": {
    "amount": 600,
    "currency": "USD"
  },
  "trace_id": "tr_..."
}
```

### 6.2 Response

```json
{
  "decision_id": "dec_...",
  "decision": "require_approval",
  "matched_rule_ids": ["policy.refund.high_risk"],
  "obligations": [
    {"type": "require_template", "value": "finance_l2", "phase": "PRE_TOOL"}
  ],
  "policy_bundle_id": "pb_20260402_01",
  "trace_id": "tr_...",
  "decision_graph_id": "dg_01",
  "decision_node_id": "node_policy_10",
  "parent_decision_node_ids": ["node_model_09"]
}
```

## 7. 失败矩阵（执行期）

1. critical/high + write/external_write/irreversible：`fail_closed`。
2. medium：`require_review`。
3. low + pure/read：`fail_soft`（降级）+ 强审计。
4. obligation 失败默认沿用 decision 风险等级的失败矩阵。
5. eval 超时不得放宽权限。

## 8. 幂等语义

1. Eval 幂等键：`hash(tenant+workflow+step+action+resource+inputs_normalized+bundle_id)`。
2. Obligation 幂等键：`hash(decision_id+obligation_id+phase)`。
3. 重试返回同一 `decision_id` 与同一 obligation 集合。

## 9. 观测与审计

1. 事件：`policy.eval.started|completed|failed|timed_out|obligation_conflict`。
2. 必含字段：`bundle_id`、`rule_ids`、`risk_tier`、`fail_mode`、`latency_ms`、`decision_graph_id`、`decision_node_id`。
3. 审计真值：`Policy Decision Log + Audit Evidence Store`。

## 10. 发布门禁（Policy 专项）

1. `semantic_equivalence_rate=100%`。
2. `critical_false_allow=0`。
3. `shadow_diff_rate<=threshold`。
4. `inline_p95<=10ms`、`remote_p95<=50ms`。
5. 任一失败阻断发布。

## 11. 确定性边界声明（Policy）

确定性约束（必须）：
1. 规则解析、类型检查、冲突求解、obligation 合并、幂等键计算。
2. 编译语义保持（parity set）。

统计性约束（需概率治理）：
1. 请求分布变化引起的误伤率波动。
2. 上游输入漂移导致的判定效果变化。
3. 与审批/技能/上下文联动后的全链路行为。
