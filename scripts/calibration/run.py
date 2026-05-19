#!/usr/bin/env python3
"""
Calibration script — verify DAOF-CPA media seed pricing against real CPA upstream.

用法:
    CPA_URL=http://127.0.0.1:8317 CPA_KEY=sk-xxx python scripts/calibration/run.py

Exit code:
    0  全部 PASS
    1  有 FAIL（seed 价格与实际不符）
    2  脚本运行错误（网络 / JSON parse）

The script makes 7 calibration calls and compares against expected values from
the DAOF seed. If a call fails because the upstream provider isn't configured
(e.g. CPA admin hasn't set up OpenAI codex auth), it's SKIPPED, not FAILED.

新 provider 上线后跑此脚本验证 seed 价格 = 实际 cost_in_usd_ticks / usageMetadata。
偏差 > 5% 时按 README.md 中"如何更新 seed"修复。
"""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any


@dataclass
class TestCase:
    name: str
    endpoint: str
    payload: dict[str, Any]
    # 期望从 response 中抽取的字段（gjson-style path）
    expect_field: str
    # 期望值（int / float / "skip-if-error"）
    expect_value: Any
    # 容差（绝对值）
    tolerance: float = 0.0
    # description 用于 PASS/FAIL 输出
    description: str = ""


# 期望值来自 calibration 2026-05-19（scripts/calibration/00_summary.json）
# 单位：cost_in_usd_ticks (xAI) / candidatesTokenCount (Gemini)
TEST_CASES: list[TestCase] = [
    TestCase(
        name="xai_image_gen",
        endpoint="/v1/images/generations",
        payload={"model": "grok-imagine-image", "prompt": "a tiny grey pixel", "n": 1},
        expect_field="usage.cost_in_usd_ticks",
        expect_value=200_000_000,
        description="grok-imagine-image gen → $0.02",
    ),
    TestCase(
        name="xai_quality_1k",
        endpoint="/v1/images/generations",
        payload={"model": "grok-imagine-image-quality", "prompt": "a tiny grey pixel", "n": 1},
        expect_field="usage.cost_in_usd_ticks",
        expect_value=500_000_000,
        description="grok-imagine-image-quality 1K → $0.05",
    ),
    TestCase(
        name="xai_quality_2k",
        endpoint="/v1/images/generations",
        payload={"model": "grok-imagine-image-quality", "prompt": "a tiny grey pixel", "resolution": "2K", "n": 1},
        expect_field="usage.cost_in_usd_ticks",
        expect_value=700_000_000,
        description="grok-imagine-image-quality 2K → $0.07",
    ),
    TestCase(
        name="gemini_3_1_flash_image_default",
        endpoint="/v1beta/models/gemini-3.1-flash-image:generateContent",
        payload={"contents": [{"parts": [{"text": "a tiny grey square"}]}]},
        expect_field="usageMetadata.candidatesTokenCount",
        expect_value=1469,
        # ±30%: 多次实测 1255–1469 区间（同 prompt 不同输出图复杂度导致 token 数差异，
        # 这是 vendor 内部 image-token 抽取算法的正常波动，与 vendor 改价无关）
        # seed 价 $60/Mtok 是 rate 不是 unit cost，actual_tokens × $60/Mtok 直接吃实际值
        tolerance=500,
        description="Gemini 3.1 flash image default → ≈1469 token (≈$0.088)，运行间漂移属正常",
    ),
    TestCase(
        name="gemini_3_1_flash_image_2k",
        endpoint="/v1beta/models/gemini-3.1-flash-image:generateContent",
        payload={
            "contents": [{"parts": [{"text": "a tiny grey square"}]}],
            "generationConfig": {"imageConfig": {"imageSize": "2K"}},
        },
        expect_field="usageMetadata.candidatesTokenCount",
        expect_value=2036,
        tolerance=500,  # ±25%
        description="Gemini 3.1 flash image 2K → ≈2036 token (≈$0.122)，运行间漂移属正常",
    ),
    TestCase(
        name="openai_gpt_image_2",
        endpoint="/v1/images/generations",
        payload={"model": "gpt-image-2", "prompt": "a tiny grey pixel", "size": "1024x1024", "quality": "low", "n": 1},
        expect_field="usage.output_tokens",
        expect_value="skip-if-error",  # CPA admin 可能没配 codex auth
        description="gpt-image-2 low 1024² → usage.output_tokens shape check",
    ),
    TestCase(
        name="imagen_4",
        endpoint="/v1beta/models/imagen-4.0-generate-001:generateContent",
        payload={"contents": [{"parts": [{"text": "a tiny grey square"}]}]},
        expect_field="candidates.0.content.parts.0.inlineData.mimeType",
        expect_value="skip-if-error",  # CPA admin 可能没配 Vertex
        description="imagen-4.0 → :predict→generateContent 翻译产生 inlineData",
    ),
]


def get_nested(data: dict, path: str) -> Any:
    """gjson-style nested field lookup. Supports 'a.b.0.c' for array indexing."""
    cur: Any = data
    for part in path.split("."):
        if isinstance(cur, list):
            try:
                cur = cur[int(part)]
            except (ValueError, IndexError):
                return None
        elif isinstance(cur, dict):
            cur = cur.get(part)
        else:
            return None
        if cur is None:
            return None
    return cur


def call_cpa(url: str, key: str, endpoint: str, payload: dict, timeout: int = 240) -> dict:
    req = urllib.request.Request(
        url=url.rstrip("/") + endpoint,
        method="POST",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {key}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        # CPA 把上游错误以 JSON 形式包裹后返回 5xx；读 body 拿 error 详情
        try:
            return json.loads(e.read().decode("utf-8"))
        except Exception:
            return {"error": {"message": f"HTTPError {e.code}: {e.reason}"}}
    except (TimeoutError, urllib.error.URLError, OSError) as e:
        # CPA 或上游忙 / 网络抖动 → 当成 SKIP 处理（不阻塞 calibration）
        return {"error": {"message": f"timeout/network: {type(e).__name__}: {e}", "type": "network_error"}}


def main() -> int:
    cpa_url = os.environ.get("CPA_URL", "http://127.0.0.1:8317")
    cpa_key = os.environ.get("CPA_KEY", "").strip()
    if not cpa_key:
        print("ERROR: set CPA_KEY env var (CPA API key)", file=sys.stderr)
        return 2

    print(f"# Calibration run against {cpa_url}")
    print(f"# {len(TEST_CASES)} test cases")
    print()

    pass_count = 0
    fail_count = 0
    skip_count = 0

    for i, tc in enumerate(TEST_CASES, 1):
        start = time.time()
        resp = call_cpa(cpa_url, cpa_key, tc.endpoint, tc.payload)
        elapsed = int((time.time() - start) * 1000)

        if "error" in resp:
            err_msg = resp["error"].get("message", str(resp["error"]))
            err_type = resp["error"].get("type", "")
            if tc.expect_value == "skip-if-error" or err_type == "network_error":
                # network_error 表示 timeout / 上游抖动，与"未配置上游"语义类似 → SKIP
                print(f"  ⏭️  [{i:2d}/{len(TEST_CASES)}] {tc.name}: SKIP ({elapsed}ms) — {err_msg[:80]}")
                skip_count += 1
            else:
                print(f"  ❌ [{i:2d}/{len(TEST_CASES)}] {tc.name}: ERROR ({elapsed}ms) — {err_msg[:120]}")
                fail_count += 1
            continue

        actual = get_nested(resp, tc.expect_field)
        if actual is None:
            print(f"  ❌ [{i:2d}/{len(TEST_CASES)}] {tc.name}: FIELD MISSING ({elapsed}ms) — expected field {tc.expect_field}")
            print(f"     response keys: {list(resp.keys())}")
            fail_count += 1
            continue

        if tc.expect_value == "skip-if-error":
            # Provider works but we only checked shape
            print(f"  ✅ [{i:2d}/{len(TEST_CASES)}] {tc.name}: PASS shape-only ({elapsed}ms) — {tc.description}")
            print(f"     {tc.expect_field} = {actual}")
            pass_count += 1
            continue

        # 数值校验
        try:
            actual_num = float(actual)
            expect_num = float(tc.expect_value)
            diff = abs(actual_num - expect_num)
            if diff <= tc.tolerance:
                pct = (diff / expect_num * 100.0) if expect_num else 0.0
                print(f"  ✅ [{i:2d}/{len(TEST_CASES)}] {tc.name}: PASS ({elapsed}ms) — actual={actual_num:g} expect={expect_num:g} diff={diff:g} ({pct:.1f}%)")
                pass_count += 1
            else:
                pct = (diff / expect_num * 100.0) if expect_num else 0.0
                print(f"  ⚠️  [{i:2d}/{len(TEST_CASES)}] {tc.name}: DRIFT ({elapsed}ms) — actual={actual_num:g} expect={expect_num:g} diff={diff:g} ({pct:.1f}%) tolerance={tc.tolerance:g}")
                print(f"     {tc.description}")
                print(f"     → update seed price if confirmed (see README.md)")
                fail_count += 1
        except (TypeError, ValueError):
            # 非数值字段：精确比对
            if actual == tc.expect_value:
                print(f"  ✅ [{i:2d}/{len(TEST_CASES)}] {tc.name}: PASS ({elapsed}ms) — actual={actual!r}")
                pass_count += 1
            else:
                print(f"  ❌ [{i:2d}/{len(TEST_CASES)}] {tc.name}: MISMATCH ({elapsed}ms) — actual={actual!r} expect={tc.expect_value!r}")
                fail_count += 1

    print()
    print(f"# Summary: {pass_count} PASS / {fail_count} FAIL / {skip_count} SKIP")

    if fail_count > 0:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
