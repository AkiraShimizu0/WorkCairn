# Public Beta Product Naming Review

Reviewed: 2026-08-10

この文書は名称変更の実施ではなく、Public Beta前の人間判断材料です。検索結果は商標clearance、法人名調査、domain取得可能性を保証しません。

## Current name: Workspace OS

`Workspace OS`は内部Architectureを正確に表しますが、一般語の組合せで検索識別性が弱く、既に同名・近似名の異なる製品があります。

- [OpenVerb Workspace OS](https://studio.openverb.org/)はAI支援付きcustom application platformとして`Workspace OS`を使用しています。
- [AI Workspace OS](https://chromewebstore.google.com/detail/ai-workspace-os/nncnlkjobfplmemecopinmckgncmdjhe)はprompt管理Chrome extensionです。
- [Links Workspace OS](https://linkspowered.com/en)はphysical workspace infrastructure platformです。

このため、一般公開名としてそのまま採用することは推奨しません。Public Betaまで内部codename／repository名として維持し、公開前に人間が最終判断します。

## Avoided candidates

- `Agent Workspace`: [agent-workspace.ai](https://agent-workspace.ai/)と[AgentWorkspace](https://agentworkspace.dev/)が既にlocal AI agent製品で使用し、NiCEやAWSも一般名称として使用しています。
- `Workloom`: [AI Sales OS](https://useworkloom.com/)や複数のworkspace製品が存在します。
- `Workstead`: [business automation service](https://workstead.app/)とHR／workspace関連製品が存在します。
- `LedgerFlow`: 複数の会計／金融製品が使用しています。
- `TaskWard`: local-first task CLIと同名domainが存在します。

## Recommended candidates for human review

| Candidate | Fit | Preliminary risk |
|---|---|---|
| Work Steward | 人間の依頼、承認、安全な実行、証拠保持という中心価値を最も表す | 一般的な役割語。exact trademark、repository、domain調査が必要 |
| Plan Steward | Plan検証と承認境界を明確に伝える | 実行・Review・Recoveryまで扱う製品範囲を狭く見せる可能性 |
| Task Harbor | local-first、安全停止、Recoveryの比喩が分かりやすい | `Harbor`を使うagentic frameworkや業務製品が多く、近似調査が必要 |

現時点の第一候補は`Work Steward`です。tag、binary、Go module、Vault metadata、HTTP contractを同時にrenameせず、公開表示名と技術識別子を分ける案も比較してください。

## Required human checks

1. 日本、米国、EU等、配布予定地域の商標databaseを専門家または正式手順で検索する。
2. GitHub organization／repository slug、主要domain、package registry、App Store／browser extensionの近似名を確認する。
3. 日本語・英語で発音、綴り、検索性、製品説明との一致をuser interviewで確認する。
4. 公開表示名だけを変更するか、repository、Go module、binary、Vault markerまで変更するかmigration scopeを決定する。
5. renameする場合は独立ADR、contract影響、release migration、redirect方針を用意する。
