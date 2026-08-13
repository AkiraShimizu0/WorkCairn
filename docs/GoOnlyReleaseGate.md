# Go Only Release Gate

## 判定

WorkCairnのrepository、build、test、release、distributionはGo Onlyです。正式な製品surfaceは`workcairn`、`workcairn-daemon`、JSON Contract v1の`workcairn-core`であり、clone後の検証にGo toolchain以外の言語runtimeやpackage managerを必要としません。Public Beta候補versionは`VERSION`の`v1.0.0-beta.1`です。

## Capability matrix

| Capability | Go製品入口／実装 | 主な検証 |
|---|---|---|
| CEO依頼／Plan／Apply | `workcairn ceo-plan-*`、Interaction Session | typed validation、approval、temporary Vault、Mock Provider |
| Project／Task／Organization／Identity | Go process／Service／Vault Adapter | plan/execute分離、CAS、atomic write、partial failure |
| Task execution／Deliverable／Audit | ExecutionService、WorkerService、TaskService | commit ordering、Event ownership、Fake Runner |
| Review／Revision／Reviewed Workflow | Go Review／Revision／Workflow services | canonical evidence、child Command Ledger、branch E2E |
| Recovery／Command Ledger | `recovery-*`、durable Ledger | evidence-bound recovery、replay、ID conflict rejection |
| HTTP／Local Web UI | `workcairn-daemon` | loopback default、pairing、graceful shutdown、mobile flow |
| Scheduler／Notification／Metrics／External Action | Go Kernel／Adapter | approved command dispatch、redaction、Mock transport |
| JSON Contract v1 | `workcairn-core` | golden fixtures、strict request／response envelope |

## Official gate

```bash
make v1-release-gate
```

この単一targetは次を実行します。

1. native向け3つのGo binaryをbuildする。
2. macOS／Linuxの4 targetで3 binaryをCGOなしcross-buildする。
3. 全Go testsをcacheなしで実行する。
4. 全Go race testsをcacheなしで実行する。
5. `go vet ./...`を実行する。
6. `gofmt`差分がないことを検査する。
7. release packaging scriptの実行権限とshell syntaxを検査する。
8. `git diff --check`で差分衛生を検査する。

Go test内のRelease Gateはさらに次を拒否します。

- Domain／Service／KernelからAdapter／Runtime／Processへの逆向き依存
- Task lifecycle EventのTaskService外生成
- 書込みcommandの承認前Vault／Provider I/O
- Go製品sourceからの外部process起動。ただしADR-0038のmacOS Local OS Adapterだけはnative picker、Keychain、Finder／browser openの固定absolute OS tool allow-listを許可し、専用testでshell／interpreter、動的command、secret argv混入を拒否する
- `.py`、`.python-version`、`.venv`、`pyproject.toml`、`uv.lock`等、撤去済みruntime資産のrepository再混入

Provider APIはMock HTTP serverだけ、Vaultはtemporary directoryだけで検証し、実API、実Vault、`.env`を使用しません。

## Release artifact

```bash
make release-package \
  RELEASE_VERSION=v1.0.0-beta.1 \
  BUILD_DATE=2026-08-09T12:00:00Z
```

packaging scriptはallow-listで次だけをarchiveへ含めます。

- `workcairn`
- `workcairn-daemon`
- `workcairn-core`
- `VERSION`、`LICENSE`、`README.md`、`CHANGELOG.md`、`SECURITY.md`、`CONTRIBUTING.md`
- `docs/`

`.env`、Vault、source tree、test data、cache、local build directory、他言語runtime資産は含めません。archiveとSHA-256 checksumは既存同名fileを上書きしません。

生成後は隣接checksum、必須asset、allow-list外file、archive名と`VERSION`の一致を検証します。

```bash
make verify-release-package ARCHIVE=/absolute/path/to/workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz
```

## Historical migration

Public Beta前に旧compatibility distributionを撤去した経緯と、意図的に残したlanguage-neutral fixtureは[MigrationHistory.md](MigrationHistory.md)および[ADR-0033](adr/ADR-0033-public-beta-go-only-repository.md)を参照してください。過去ADRの移行記述は当時の判断記録であり、現在利用可能なruntime surfaceではありません。
