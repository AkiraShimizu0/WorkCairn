"""Frozen v0.1 Python ClaudeRunner compatibility implementation."""

from math import ceil
import os
from time import perf_counter

from anthropic import Anthropic


class ClaudeRunner:
    """Anthropic SDKでClaudeを呼び出すRunner"""

    name = "ClaudeRunner"
    DEFAULT_MODEL_OPTIONAL_PARAMETERS = {
        "claude-sonnet-5": frozenset(),
    }
    SUPPORTED_OPTIONAL_PARAMETERS = frozenset({"temperature"})

    def __init__(
        self,
        *,
        client=None,
        model="claude-sonnet-5",
        temperature=0.2,
        max_tokens=3000,
        model_optional_parameters=None,
    ):
        self.model = model
        self.temperature = temperature
        self.max_tokens = max_tokens
        self.model_optional_parameters = dict(
            self.DEFAULT_MODEL_OPTIONAL_PARAMETERS
        )
        if model_optional_parameters:
            self._register_model_optional_parameters(model_optional_parameters)
        self.last_execution_log = None

        if client is not None:
            self.client = client
            return

        api_key = os.getenv("ANTHROPIC_API_KEY")
        if not api_key:
            raise RuntimeError("ANTHROPIC_API_KEYが設定されていません。")
        self.client = Anthropic(api_key=api_key)

    def run(
        self,
        *,
        system_prompt,
        user_prompt,
        project_name=None,
        task=None,
        employee=None,
    ):
        """Workerのプロンプトを使い、Markdown文字列だけを返す"""
        system_prompt = self._validate_prompt(system_prompt, "system_prompt")
        user_prompt = self._validate_prompt(user_prompt, "user_prompt")
        started_at = perf_counter()

        try:
            response = self.client.messages.create(
                **self._request_arguments(system_prompt, user_prompt)
            )
            markdown = self._extract_markdown(response)
            (
                estimated_tokens,
                token_source,
                input_tokens,
                output_tokens,
            ) = self._token_count(
                response,
                system_prompt,
                user_prompt,
                markdown,
            )
        except Exception:
            self.last_execution_log = {
                "runner": self.name,
                "model": self.model,
                "estimated_tokens": None,
                "input_tokens": None,
                "output_tokens": None,
                "token_source": None,
                "duration_seconds": round(perf_counter() - started_at, 6),
                "status": "failed",
            }
            raise

        self.last_execution_log = {
            "runner": self.name,
            "model": self.model,
            "estimated_tokens": estimated_tokens,
            "input_tokens": input_tokens,
            "output_tokens": output_tokens,
            "token_source": token_source,
            "duration_seconds": round(perf_counter() - started_at, 6),
            "status": "success",
        }
        return markdown

    def _request_arguments(self, system_prompt, user_prompt):
        """モデルで許可されたオプションだけをAPI引数へ追加する"""
        arguments = {
            "model": self.model,
            "max_tokens": self.max_tokens,
            "system": system_prompt,
            "messages": [{"role": "user", "content": user_prompt}],
        }
        optional_parameters = self.model_optional_parameters.get(
            self.model,
            frozenset(),
        )
        if "temperature" in optional_parameters and self.temperature is not None:
            arguments["temperature"] = self.temperature
        return arguments

    def _register_model_optional_parameters(self, model_parameters):
        for model, parameters in model_parameters.items():
            parameters = frozenset(parameters)
            unsupported = parameters - self.SUPPORTED_OPTIONAL_PARAMETERS
            if unsupported:
                raise ValueError(
                    f"未対応のClaudeオプションです: {', '.join(sorted(unsupported))}"
                )
            self.model_optional_parameters[str(model)] = parameters

    def get_last_execution_log(self):
        """直近実行の統計を呼び出し側が記録できる形で返す"""
        if self.last_execution_log is None:
            return None
        return dict(self.last_execution_log)

    @staticmethod
    def _validate_prompt(value, field_name):
        if not isinstance(value, str) or not value.strip():
            raise ValueError(f"{field_name}は空でない文字列である必要があります。")
        return value.strip()

    @staticmethod
    def _extract_markdown(response):
        texts = [
            block.text
            for block in getattr(response, "content", [])
            if getattr(block, "type", None) == "text"
            and isinstance(getattr(block, "text", None), str)
        ]
        markdown = "\n".join(text.strip() for text in texts if text.strip()).strip()
        if not markdown:
            raise ValueError("Claudeレスポンスにtextブロックがありません。")
        return markdown

    @staticmethod
    def _token_count(response, system_prompt, user_prompt, markdown):
        usage = getattr(response, "usage", None)
        input_tokens = getattr(usage, "input_tokens", None)
        output_tokens = getattr(usage, "output_tokens", None)
        if isinstance(input_tokens, int) and isinstance(output_tokens, int):
            return (
                input_tokens + output_tokens,
                "api_usage",
                input_tokens,
                output_tokens,
            )

        characters = len(system_prompt) + len(user_prompt) + len(markdown)
        return max(1, ceil(characters / 4)), "character_estimate", None, None
