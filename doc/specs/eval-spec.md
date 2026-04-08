# eval-spec.md（执行级规范）

## 0. 规范地位

1. 本文为 Eval 子规范，和主文档同级约束。
2. 本文定义评测输入、执行、统计、门禁与回放的一致实施规则。
3. 若主文档与本文冲突，以本文评测协议为准。

## 1. 核心对象

1. `EvalDataset`：评测样本集合。
2. `EvalRun`：一次评测任务执行。
3. `Grader`：样本评分实现（auto/human）。
4. `MetricPipeline`：指标计算管线（streaming/batch）。
5. `GateDecision`：门禁判定结果。

## 2. 字段契约（字段级）

强规则：
1. 任何新增字段必须补充：`input type`、`output type`、`execution time`、`failure behavior`、`idempotence`。
2. 未满足字段契约的变更禁止进入 gate 判定链路。

### 2.1 EvalDataset

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `dataset_id` | string | string | create | reject duplicate id | idempotent by id |
| `dataset_version` | semver | semver | publish | reject non-semver | monotonic only |
| `schema_version` | int | int | publish | reject incompatible downgrade | monotonic only |
| `sample_count` | int | int | publish | reject below suite minimum | deterministic |
| `risk_tier_distribution` | object | object | publish | reject missing critical bucket | deterministic |
| `source_lineage` | object | object | publish | reject missing lineage | deterministic |
| `checksum` | sha256 | sha256 | publish | reject mismatch | deterministic |

### 2.2 EvalRun

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `eval_run_id` | generated uuid | uuid | run start | fail on generate error | globally unique |
| `suite_id` | string | string | run start | reject unknown suite | idempotent by request hash |
| `dataset_ref` | dataset_id@version | resolved dataset | run start | reject if dataset not immutable | idempotent |
| `grader_refs` | grader ids | grader handles | run start | fallback to alternate grader | deterministic selection |
| `traffic_snapshot_ref` | snapshot id | snapshot handle | replay mode | block if snapshot incomplete | idempotent |
| `status` | enum | enum | run lifecycle | terminal on unrecoverable error | idempotent state transitions |

### 2.3 GateDecision

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `gate_id` | string | string | gate eval | reject unknown gate config | idempotent |
| `result` | computed enum(pass/fail/review_required) | enum | gate eval | fail-closed on missing critical metric | idempotent |
| `failed_checks` | check results | check[] | gate eval | empty allowed | deterministic |
| `evidence_pack_ref` | artifact ref | artifact ref | gate eval | reject missing pack | deterministic |
| `decision_trace_id` | trace id | trace id | gate eval | reject missing trace | idempotent |

## 3. Grader 实现协议

## 3.1 统一接口

### Request

```json
{
  "sample_id": "s_001",
  "task_type": "tool_call",
  "prediction": {},
  "ground_truth": {},
  "context": {
    "risk_tier": "high",
    "dataset_version": "1.4.2"
  }
}
```

### Response

```json
{
  "grader_id": "grader.tool.v2",
  "score": 0.94,
  "label": "pass",
  "explanations": ["tool match", "args exact"],
  "confidence": 0.88,
  "artifact_refs": ["art://..."],
  "latency_ms": 12
}
```

## 3.2 fallback 规则

1. primary grader 超时/故障 -> fallback grader（同任务类型）接管。
2. fallback 仍失败：标记 `grading_failed`，该样本进入人工池。
3. critical/high 套件中 `grading_failed` 占比超阈值时整套件判定无效。

## 3.3 幂等语义

1. grader 幂等键：`hash(sample_id + grader_id + prediction_hash + gt_hash)`。
2. 重试必须返回相同 `score/label` 或显式 `non_deterministic=true` 标记。

## 4. 数据集版本化与 schema evolution

## 4.1 版本规则

1. `major`：schema 不兼容变更。
2. `minor`：兼容字段新增。
3. `patch`：样本修订或标签修订。

## 4.2 schema 演进规则

1. 新增字段必须给默认值和回填策略。
2. 删除字段必须升 major。
3. 评测运行必须固定 `dataset_version + schema_version`。

## 4.3 回放兼容

1. replay 默认按原始 `dataset_version`。
2. 迁移 replay 必须声明 `migration_plan_ref`。
3. 无迁移计划不得跨 major replay。

## 5. Replay 与线上一致性保证

1. Replay 输入必须使用 `execution snapshot`（workflow/policy/model/context/dependency）。
2. Replay 环境需锁定 feature flags，禁止额外能力注入。
3. 时间相关逻辑使用 recorded clock 或固定时间窗。
4. 外部依赖调用默认 mock/reconcile，避免污染线上系统。
5. 一致性指标：`replay_behavior_match_rate`（按分层统计与置信区间评估），低于阈值则该 replay 证据无效。

## 6. Metric 计算 pipeline（streaming + batch）

## 6.1 双管线职责

1. streaming：近实时监控、预警、趋势。
2. batch：门禁最终判定、财务级对账、统计校准。

## 6.2 一致性规则

1. 门禁以 batch 结果为准。
2. streaming 与 batch 差异超阈值时触发 `metric_drift_in_pipeline`。
3. batch 回写后可修正 streaming 看板，但需保留修正轨迹。

## 6.3 计算契约

| metric | primary pipeline | fallback | failure behavior | idempotence |
|---|---|---|---|---|
| TSA/AFS/EOC | batch | streaming provisional | gate waits batch for critical/high | idempotent over eval_run_id |
| latency/cost trend | streaming | batch backfill | alert if both unavailable | idempotent over time bucket |
| MemoryHarmRate | batch | none | gate fail-closed on missing high-risk metric | idempotent over eval_run_id |

## 7. 指标定义与聚合

1. 单样本评分先标准化为 `[0,1]`。
2. 关键门禁指标使用 Wilson 下界。
3. 聚合分桶至少包含：风险等级、租户 tier、任务类型、模型版本。
4. 任何跨桶聚合必须保留加权方法与权重来源。

## 8. 失败行为矩阵

1. critical metric 缺失 -> gate fail。
2. high metric 缺失 -> gate fail 或 review_required（仅在预定义例外）。
3. medium/low metric 缺失 -> review_required。
4. grader 大面积失败 -> 自动降级人工复核并阻断自动放行。

## 9. 观测与审计

1. 事件：`eval.run.started|sample.graded|metric.computed|gate.decided|replay.mismatch`。
2. 必含字段：`eval_run_id`、`dataset_version`、`grader_version`、`pipeline_type`、`trace_id`。
3. 审计包必须含：数据集哈希、grader 哈希、脚本哈希、配置哈希。

## 10. 发布门禁（Eval 专项）

1. 数据集版本固定且可追溯。
2. critical/high 套件的 batch 结果必须完整。
3. `critical_false_allow=0`。
4. `insufficient_sample=0`（critical/high）。
5. 未满足上述任一条即阻断发布。

## 10.1 证据属性声明（Eval 不是绝对真值）

1. Eval 结果属于“统计证据”，不是业务真值本身。
2. 任一 gate 结论必须携带：`confidence_interval + sample_coverage + bias_note`。
3. 对 `critical/high` 变更，Eval 必须与 replay/policy regression/human signoff 联合裁决。
4. 仅凭单一 proxy metric 禁止直接宣告“系统正确”。

## 11. Eval 运营制度（Runbook 级）

## 11.1 角色与职责

1. `Eval Owner`：审批 grader 上线、维护阈值与套件配置。
2. `Data Steward`：负责 dataset 质量、污染检测与版本冻结。
3. `Risk Owner`：对 critical/high 套件门禁结果签字。
4. `Oncall`：处理 replay mismatch、pipeline drift 与 gate 故障。

## 11.2 新 grader 进入 gate 流程

1. 提交：`grader_spec + benchmark + 风险评估`。
2. 阶段：`shadow -> canary -> gated`。
3. 审批：必须由 `Eval Owner + Risk Owner` 双签。
4. 未完成双签不得参与 `critical/high` gate 判定。

## 11.3 dataset 污染/标签漂移紧急冻结

1. 触发条件：污染证据、标签漂移超阈值、来源完整性异常。
2. 冻结权限：`Data Steward` 可紧急冻结，`Eval Owner` 在 SLA 内复核。
3. 冻结行为：停止该 dataset 参与 gate，回退到上一个稳定版本。
4. 冻结事件必须写 `dataset.frozen` 并生成影响范围报告。

## 11.4 human grader 不一致仲裁

1. 若 `CohenKappa < 0.75` 或分歧率超阈值，触发仲裁。
2. 仲裁主体：`Eval Owner` 指派独立仲裁组复核样本。
3. 仲裁结果可修订标签，但必须升 dataset patch 版本并留痕。
4. 仲裁未完成前，该套件仅可 `review_required`，不得自动放行。

## 11.5 replay mismatch 值班流程

1. `replay.mismatch` 按严重度进入 oncall 队列。
2. `critical` mismatch 触发即时发布冻结与回滚评估。
3. oncall 必须在 SLA 内给出：根因分类、影响范围、临时缓解。
4. 根因归档到 `eval_incident` 并自动生成回归样本。

## 12. fast_path_eval（低风险快速路径）实施规范

## 12.1 准入字段契约

| field | input type | output type | execution time | failure behavior | idempotence |
|---|---|---|---|---|---|
| `fast_path_enabled` | bool | bool | gate pre-check | reject if missing | idempotent |
| `risk_tier` | enum(low/medium/high/critical) | enum | gate pre-check | non-low -> fallback full eval | deterministic |
| `change_scope_tags` | string[] | string[] | gate pre-check | unknown tag -> fallback full eval | deterministic |
| `forbidden_change_detected` | bool | bool | gate pre-check | true -> block fast path | deterministic |
| `recent_stable_days` | int | int | gate pre-check | below threshold -> fallback full eval | deterministic |
| `low_risk_sample_count` | int | int | eval run | below threshold -> insufficient_sample | deterministic |

## 12.2 执行规则

1. fast path 仅允许 `risk_tier=low`。
2. fast path 必须保留 policy 关键门禁，不得绕过 `critical_false_allow=0`。
3. fast path 通过后只允许小流量 canary 发布（由主文档 gate 配置限制）。
4. 发布后必须在 `full_shadow_eval_within_hours` 时限内完成 full eval。
5. full eval 失败时必须 `auto_rollback_and_freeze_fast_path`。

## 12.3 幂等与审计

1. fast path 幂等键：`hash(release_id + gate_profile + risk_tier + change_scope_hash)`。
2. 同一 release 重试 fast path 必须返回相同初判，除非输入快照变化。
3. 审计包必须额外包含：`fast_path_eligibility_report` 与 `shadow_eval_deadline`。

## 13. 统计稳健性与漂移治理

1. 关键指标必须同时报告点估计与区间估计，门禁以区间下界/上界执行。
2. 若 `dataset drift` 或 `grader drift` 超阈值，Eval 结果降级为 `review_required`。
3. `replay_behavior_match_rate` 使用分层统计，不得只报全局均值。
4. 漂移期间禁止放宽阈值；仅允许冻结套件或回退到更保守门禁。
