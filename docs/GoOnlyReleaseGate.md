# Go Only Release Gate

## 判定

通常製品RuntimeについてGo Only Release Gateは成立しています。正本の配布物と運用入口は`bin/workspace-run`、loopback HTTP入口は`bin/workspace-daemon`です。公開v0.1 Python packageは別のcompatibility surfaceであり、Go製品Runtimeの依存ではありません。

## Capability Matrix

| Capability | Go製品入口／実装 | Python interpreterなし | 検証 |
|---|---|---|---|
| 通常運用 | `workspace-run` plan／execute／migration | OK | CLI、Runtime、temporary Vault E2E |
| HTTP／daemon | `workspace-command.v1`、必須Command ID、Ledger status | OK | loopback handler、同一command replay、graceful shutdown |
| CEO plan | `ceo-plan-generate`、`ceo-plan-apply-*` | OK | Mock Provider、typed plan、Go writer E2E |
| Project／Task管理 | `project-bootstrap-*`、`task-create-*`、`project-dependencies-*` | OK | temporary Vault、TaskService Event／Audit |
| Organization／Identity | `organization-inspect`、`identity-validate`、`employee-*`、`organization-sync-*` | OK | shared fixture、temporary Vault、partial failure tests |
| Task execution | `plan`、`execute` | OK | Go Runtime、Claude Adapter mock、Task lifecycle、Command ID replay tests |
| Review | `review-plan`、`review-execute` | OK | canonical JSON、Markdown projection、Review Event／Audit、Command replay |
| Revision | `revision-plan`、`revision-execute` | OK | immutable intent、TaskService.Create、Revision Event／Audit |
| Multi-task Workflow | `workflow-plan`、`workflow-execute` | OK | readiness再plan、決定的child Command、順次Task E2E、replay／conflict |
| Deliverable／Audit | Execution／Review／Revision composition | OK | immutable Store、Event subscriber、partial failure tests |
| Recovery | `recovery-inspect`、`recovery-plan`、`recovery-apply` | OK | read-only inventory、evidence digest、Task CAS、temporary Vault E2E |

## Automated Gate

```bash
make go-only-release-gate
```

このtargetはGo binaryをbuildし、全Go test、race test、vetだけを実行します。Python、`.venv`、Python Provider SDK、python-dotenvを起動・importしません。

`go_only_release_gate_test.go`は、上表の製品operationがGo CLIに存在することと、production／testを含む全Go sourceが`os/exec`または`os.StartProcess`を使ってPythonを含む外部interpreterを起動できないことを検査します。さらに、Domain／Service／Kernelからedge layerへの逆向き依存、Task lifecycle EventのTaskService外生成、全書込みcommandの承認前Vault／Provider I/Oを拒否します。Provider APIはGo Claude AdapterのMock HTTP testだけで検証し、実APIを呼びません。

明示`--command-id`付き通常Task／Review executionは、temporary Vault E2Eで同一requestの2回目がProviderを呼ばず保存済みresultを返すこと、異なるrequestでのID再利用を拒否することを検証します。これは現在まだ全mutating commandのidempotency保証ではありません。

## v1.0 Candidate Gate

```bash
make v1-release-gate
```

これはGo Only製品Gateをそのまま実行した後、公開Python compatibility surfaceについて次も確認します。

- `uv.lock`が`pyproject.toml`と一致すること（offline check）
- `PYTHON_DOTENV_DISABLED=1`で全Python compatibility／migration testが成功すること
- Python source／testがrepository外のtemporary cacheだけを使ってcompileできること
- `git diff --check`が成功すること

Pythonを実行するのは公開互換distributionのRelease確認だけです。Go製品artifactがPythonに依存することを意味しません。実Vault、`.env`、実APIはどちらのGateでも使用しません。

## Remaining Separation Work

- Python compatibility packageは公開API維持のためrepositoryとPython dependency lockに残りますが、Go製品配布物には含めません。
- `workspace-ai` console scriptはv0.1 compatibility placeholderです。通常運用手順とrelease artifactでは`workspace-run`とloopback `workspace-daemon`を案内します。
- Python packageの物理削除は公開caller移行後の別Release Gateです。現在の削除対象と条件は`PythonRuntimeInventory.md`を正とします。
- daemon、自動retry／reconciliationはGo Only成立の前提ではありません。明示Recoveryと主要な副作用commandのLedger foundationは追加済みですが、Event replayやartifact adoptionは行いません。
