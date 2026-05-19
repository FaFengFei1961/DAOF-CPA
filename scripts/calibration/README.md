# DAOF-CPA Media Calibration

把媒体 seed 价格（precheck 估算价）与真实上游 cost_in_usd_ticks / usageMetadata 对齐的工具。

## 为什么需要 calibration

DAOF 的 seed 价格表来自厂商官方文档（OpenAI / xAI / Google），但文档常常：

- **过时**：vendor 改价后文档滞后几天到几周
- **模糊**：xAI doc "edits billed for both input + output" 实际 input 是 output 的 10-20%（[`8dd2712`](https://github.com/FaFengFei1961/DAOF-CPA/commit/8dd2712) 教训）
- **不全**：B3 报告 Gemini 3.1 image 1K = 1120 token，antigravity 实际 1469 ([`9aa225e`](https://github.com/FaFengFei1961/DAOF-CPA/commit/9aa225e) calibration 数据)

`calibration/run.py` 直接调本地 CPA 上游，比对真实响应与 seed 假设，把"文档 + 想象"换成"实测数据"。

## 用法

```bash
# 启动本地 CPA（参考 CLIProxyAPI README），假设监听 127.0.0.1:8317
# 在 CPA 管理界面创建临时 API key（最低权限即可）

CPA_URL=http://127.0.0.1:8317 \
CPA_KEY=sk-xxxxxxxxxxxxxxxx \
python scripts/calibration/run.py
```

Exit code:

- `0` — 全部 PASS 或合理 SKIP
- `1` — 某项 DRIFT（actual 与 seed 差 > 容差）
- `2` — 脚本运行错误（CPA 离线 / 密钥无效）

## 输出解读

```
  ✅ [ 1/7] xai_image_gen: PASS — actual=2e+08 expect=2e+08 (0.0%)
  ⏭️  [ 6/7] openai_gpt_image_2: SKIP — auth_not_found
  ⚠️  [ 4/7] gemini_3_1_flash_image_default: DRIFT — actual=1406 expect=1469 (4.3%)
```

| 标记 | 含义 | 处理 |
|---|---|---|
| ✅ PASS | actual 在 ±tolerance 内 | 无需动作 |
| ⏭️ SKIP | upstream 未配置（OpenAI codex / Vertex 等）或网络抖动 | 等 admin 配齐再跑；不阻塞 |
| ⚠️ DRIFT | actual 超 tolerance | **看下面"如何更新 seed"** |
| ❌ ERROR | 上游 4xx/5xx 或字段 shape 变 | 看 error 详情，可能需要改 handler 解析逻辑 |

## 如何更新 seed（处理 DRIFT 的标准流程）

假设脚本报：

```
⚠️ gemini_3_1_flash_image_default: DRIFT — actual=1800 expect=1469 (22.5%)
```

意思是 Gemini 3.1 flash image 默认尺寸现在返回 1800 token（不再 1469）。

### 1. 判定漂移真伪

跑 2-3 次同样的 prompt，看 token 数是否稳定在 1800±50：

```bash
for i in 1 2 3; do python scripts/calibration/run.py; sleep 5; done
```

- 稳定 → 是真实 vendor 改动 / antigravity 政策变了 → 进入步骤 2
- 飘忽不定 → 增大 tolerance（在 `run.py` 中改 `tolerance=`）即可，不动 seed

### 2. 更新 seed 价格

**对 token-billed 模型**：seed 中改 `Token` 字段不需要改（rate 来自 vendor 文档，actual 是 vendor 实际计费）。但要更新 `expect_value` 让脚本不再报 DRIFT。

**对 image-billed (cost_in_usd_ticks) 模型**：actual ticks / 10^10 = 真实美元价。改 seed `Media[].Price`：

```go
// 旧：
Media: []defaultMediaPrice{
  {Unit: "image", Direction: "input", Price: "0.005"},
}
// 新（按 actual 数据）：
Media: []defaultMediaPrice{
  {Unit: "image", Direction: "input", Price: "0.002"},
}
```

### 3. 同步更新脚本期望值

打开 `run.py`，找到对应 TestCase 改 `expect_value`：

```python
TestCase(
    name="gemini_3_1_flash_image_default",
    ...
    expect_value=1800,  # was 1469
    ...
)
```

### 4. 跑测试 + commit

```bash
go test ./...
git add scripts/calibration/run.py database/model_runtime_seeds.go
git commit -m "fix(seed): re-calibrate xxx after vendor change"
```

## 添加新 provider 的 calibration

CPA admin 配通新 provider（如 OpenAI codex auth）后，编辑 `run.py` 加 TestCase：

```python
TestCase(
    name="openai_gpt_image_2_low",
    endpoint="/v1/images/generations",
    payload={"model": "gpt-image-2", "prompt": "a tiny grey pixel", "quality": "low", "n": 1},
    expect_field="usage.output_tokens",
    expect_value=420,  # 实测填入
    tolerance=20,
    description="gpt-image-2 low → low-tier output token count",
),
```

跑 `python scripts/calibration/run.py`，记录初次实测值，更新 `expect_value`，提交。

## 计费链路口径速查

| Provider | 字段 | 单位 | 公式 |
|---|---|---|---|
| xAI (grok-imagine-image*) | `usage.cost_in_usd_ticks` | 10⁻¹⁰ USD | ticks / 10^10 |
| Google Gemini image | `usageMetadata.candidatesTokenCount` | image-modality token | tokens × ($60/$120/$30 per M) |
| Google Imagen (translated) | `candidates[].content.parts[].inlineData` | image count | count × flat (e.g. $0.04/img) |
| OpenAI gpt-image-2 | `usage.output_tokens` + `output_tokens_details.image_tokens` | image-modality token | tokens × $30 per M (主流复述，待 calibration 确认) |

DAOF 在 [proxy/image_generation.go:1020](../../proxy/image_generation.go) `costTicksFromImageResponse` 和
[proxy/gemini_native.go:575](../../proxy/gemini_native.go) `resolveGeminiActualPrice` 中实现这些公式。

## Gemini token 数运行间漂移（不是 bug）

实测同样的 prompt `"a tiny grey square"` 在 Gemini 3.1 flash image 上多次生成：

| 时刻 | default size | 2K size |
|---|---|---|
| run 1 | 1469 | 2036 |
| run 2 | 1406 | 1957 |
| run 3 | 1255 | 1838 |

差异 ~15-20%。这是 Google **image-token 抽取算法**的正常波动——同一 prompt 因生成图的细节
复杂度不同，会编码成不同数量的 image token。

**对 DAOF 计费的影响：无**。Gemini image 是 token-billed (`$60/Mtok × actual_tokens`)，
DAOF 按 `usageMetadata.candidatesTokenCount` 实扣，自动跟踪实际值。seed 价是 **rate** 不是 **unit cost**。

**对 calibration 的影响**：判定 vendor 改价时，"actual 偏离 expect 20% 内"算正常波动，不算 DRIFT。
脚本 tolerance 设为 ±500 token（约 ±30%）容纳这个噪声。如果连续 5 次都偏离 50%+，那才是真的 vendor 改了。

## 历史 calibration 数据

`00_summary.json` 是 2026-05-19 首次 calibration 的留档。其他 `*.json` 是各次跑的原始响应（不含 base64
内容，仅 usage / metadata）。新跑覆盖旧文件——重要发现请单独 commit 留 git 历史。

## 已知未覆盖

- **CPA OpenAI codex auth** 未配 → gpt-image-2 calibration SKIP
- **CPA Vertex provider** 未配 → Imagen 4 calibration SKIP
- **CPA Google AI Studio** 未配 → gemini-2.5-flash-image (Google direct) calibration SKIP
- **Codex Responses WebSocket** 未涵盖（需要 ws client + 多帧协议处理，未自动化）
- **视频生成** 未涵盖（单次 ~$0.20-0.50，留待手动需要时跑）

补完这些 calibration 需要 CPA admin 配好相应 provider auth + 在 `run.py` 加 TestCase。
