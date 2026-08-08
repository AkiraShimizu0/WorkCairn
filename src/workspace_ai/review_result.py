"""Frozen v0.1 review-result reference contract."""

import json
import re


REVIEW_RESULT_JSON_START = "REVIEW_RESULT_JSON_START"
REVIEW_RESULT_JSON_END = "REVIEW_RESULT_JSON_END"
VERDICTS = ("Approve", "Request Changes")
ISSUE_CATEGORIES = ("date", "format", "requirements", "context", "todo", "other")
ISSUE_SEVERITIES = ("high", "medium", "low")


def parse_review_output(output):
    """Runner出力から人間向けMarkdownと検証済みJSONを分離する"""
    if not isinstance(output, str) or not output.strip():
        raise ValueError("Runnerは空でないレビュー文字列を返す必要があります。")
    if output.count(REVIEW_RESULT_JSON_START) != 1:
        raise ValueError("レビュー結果JSONの開始マーカーが1つ必要です。")
    if output.count(REVIEW_RESULT_JSON_END) != 1:
        raise ValueError("レビュー結果JSONの終了マーカーが1つ必要です。")

    pattern = re.compile(
        rf"{re.escape(REVIEW_RESULT_JSON_START)}\s*(.*?)\s*"
        rf"{re.escape(REVIEW_RESULT_JSON_END)}",
        re.DOTALL,
    )
    match = pattern.search(output)
    if match is None:
        raise ValueError("レビュー結果JSONのマーカー順序が不正です。")
    try:
        result = json.loads(match.group(1))
    except json.JSONDecodeError as error:
        raise ValueError(f"レビュー結果JSONが不正です: {error.msg}") from error

    human_markdown = (output[:match.start()] + output[match.end():]).strip()
    if not human_markdown:
        raise ValueError("人間向けレビュー本文がありません。")
    return human_markdown, validate_review_result(result)


def validate_review_result(result):
    """レビューJSONを許可リストで検証し、正規化した値を返す"""
    if not isinstance(result, dict):
        raise ValueError("レビュー結果JSONはオブジェクトである必要があります。")
    verdict = result.get("verdict")
    if verdict not in VERDICTS:
        raise ValueError("verdictはApproveまたはRequest Changesのみ使用できます。")
    issues = result.get("issues")
    if not isinstance(issues, list):
        raise ValueError("issuesは配列である必要があります。")
    if verdict == "Request Changes" and not issues:
        raise ValueError("Request Changesには1件以上のissuesが必要です。")

    normalized_issues = []
    for index, issue in enumerate(issues, 1):
        if not isinstance(issue, dict):
            raise ValueError(f"issues[{index}]はオブジェクトである必要があります。")
        category = issue.get("category")
        severity = issue.get("severity")
        if category not in ISSUE_CATEGORIES:
            raise ValueError(f"issues[{index}].categoryが不正です: {category}")
        if severity not in ISSUE_SEVERITIES:
            raise ValueError(f"issues[{index}].severityが不正です: {severity}")
        description = _required_text(issue.get("description"), f"issues[{index}].description")
        suggested_action = _required_text(
            issue.get("suggested_action"),
            f"issues[{index}].suggested_action",
        )
        normalized_issues.append({
            "category": category,
            "severity": severity,
            "description": description,
            "suggested_action": suggested_action,
        })
    return {"verdict": verdict, "issues": normalized_issues}


def _required_text(value, field_name):
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{field_name}は空でない文字列である必要があります。")
    return value.strip()
