# Go Only Migration History

## Status

PythonからGoへの移行はPublic Beta前に完了しました。ADR-0033に基づき、旧Python compatibility package、console entry point、tests、package metadata、lockfile、Provider SDK依存、Python専用build／release toolingをrepositoryから削除しています。

現在の正式な製品surfaceは次の3つです。

- `workcairn`: local CLIと全process composition
- `workcairn-daemon`: HTTP Command APIとmobile-first Local Web UI
- `workcairn-core`: JSON Contract v1の外部process boundary

repositoryのbuild、test、release、distributionにはGo toolchain以外の言語runtimeを必要としません。

## Retained contract assets

移行元の言語ではなく、現在のGo製品に必要かどうかで資産を判断しました。次はGo testsが直接使用するlanguage-neutralな契約資産として残しています。

- JSON Contract v1 fixture
- Task／Review Prompt golden fixture
- Vault Markdown、Identity、Project、Workflow migration fixture
- Provider-neutral E2E fixture

既存Vaultを安全に読むための5列`Tasks.md`、managed metadata、artifact naming、明示migrationも維持します。これは削除済みruntimeのAPI互換ではなく、ユーザーデータ互換です。形式を変更する場合は新schema、明示migration、fixture、ADRを用意します。

## Historical references

ADR-0001からADR-0019には、移行時の判断根拠としてPython実装、parity、cutoverに関する記述が残ります。これらは当時の設計記録であり、現在利用できるruntimeやpackageを表しません。現在のrepository方針はADR-0033、Architecture、System Overview、Release Gateを正とします。

## Deleted distribution

- `src/workspace_ai`
- Python compatibility／migration tests
- `workspace-ai` console entry point
- `pyproject.toml`、`uv.lock`、`.python-version`
- Python Anthropic SDK、`python-dotenv`、Python build backend
- Python test／compile／lockfile release steps

外部公開前で利用者がいないことを確認したうえで削除したため、compatibility packageやfallbackは設けていません。
