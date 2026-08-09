# Contributing to Workspace OS

Workspace OSはPublic Beta前のGo Only projectです。変更前に`AGENTS.md`、`docs/CONSTITUTION.md`、`docs/Architecture.md`、関連ADRを読んでください。

## Development environment

- Go 1.23以上
- POSIX shell、`make`、`tar`
- 実Vault、`.env`、実Provider APIは不要

```bash
make v1-release-gate
```

testはFake Runner、Mock HTTP server、temporary directoryだけを使用します。別言語runtime、SDK、compatibility packageを追加しないでください。

## Change rules

- Kernel／Domain／Service／Adapter／Runtime境界を維持する。
- Task状態変更とTask lifecycle Event生成はTaskServiceだけに置く。
- 副作用前の明示承認、Version/CAS、Command Ledger、partial failureを壊さない。
- JSON Contract v1の破壊的変更は新version、migration、fixture、ADRを伴う。
- Provider credential、実Vault、個人データ、machine固有pathをfixtureやlogへ入れない。
- 大きな設計判断はADR、利用手順の変更はREADME／Operator Guideへ反映する。

## Pull request checklist

1. 変更理由と対象scopeを説明する。
2. 追加・変更した契約とfailure semanticsを明示する。
3. relevant test、race test、vet、gofmt、`git diff --check`を成功させる。
4. 実API／実Vaultを使用していないことを記録する。
5. generated artifact、secret、local pathを差分へ含めない。

Security問題は公開Issueへ投稿せず、`SECURITY.md`の手順を使用してください。
