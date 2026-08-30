# Public Beta macOS First-run Acceptance

この手順が実Keychain、実Providerを使う人間確認です。自動testはtemporary directoryとFakeだけを使い、既存の個人Obsidian Vaultには触れません。

## A. 必須 — macOS／arm64 Acceptance

必須手順はMacだけで完結します。iPhone、`--local-network`、iCloud Drive、Obsidianはいずれも不要です。

1. final tag対象commitから作ったdarwin／arm64 packaged binaryのarchiveと隣接checksumを確認し、clean directoryへ展開する。
2. 3 binaryの`version`出力でversion metadataとcommit metadataを確認する。
3. `workcairn-daemon`を既定loopbackで起動する（`--local-network`は使用しない）。
4. 新規install相当の状態で、native folder pickerが自動表示されることを確認する。
5. 空の`WorkCairn`専用folderを新規作成して選ぶ。ローカルfolderだけで完結し、iCloud Driveを要求しない。iCloud rootや既存の個人Vaultは選ばない。
6. credential登録前に、Provider必須の操作が安全に拒否されることを確認する。
7. Macに自動表示されたFirst-run Wizardで「最初のAIチームを作ります」を確認し、明示承認する。
8. Product Manager、Content Writer、QA EngineerがAI社員一覧へ表示されることを確認する。
9. `AI Connections`で`MacでClaudeを接続`を押し、Macのnative hidden-inputへtest用credentialを入力し、Keychainへ登録する。
10. Claudeが`Connected`、Routingが`Automatic`となり、Model ID入力がないことを確認する。
11. `会社を始める`を押し、最初の自然言語依頼画面へ移ることを確認する。
12. 依頼を1件送り、「このように進めます」でPlanを確認する。通常表示に`PROPOSED-*`、Role ID、digestが出ないことを確認する。
13. Maker実行、別のQA ReviewerによるReviewまで進める。必要ならRevisionはそのまま確認してよいが、Plan 1回、Task 1件、Review 1回という最小構成を意図的に増やさない。
14. 自動retryや別Provider fallbackが起きないことを確認する。
15. 実行中は小さい「会社が働いています」だけ、質問／承認／failure時だけ依頼詳細が前面に出ることを確認する。
16. Timeline、Proof of Work、Deliverable、Reviewを確認する。failureを起こした場合は画面移動／reload後も消えず、technical detailsをcopyできることを確認する。
17. daemonをgraceful shutdownして再起動する。folder pickerを再表示せず、同じ専用folderと過去のSession、Timeline、Plan、Task、Deliverable、Review、Revision、failure／Recovery evidenceを表示することを確認する。
18. credentialがbrowser、HTTP payload、Vault、Command Ledger、Audit、log、shell history、screenshotへ出ていないことを確認する。
19. test後にcredentialを失効またはrotationする。
20. 同一Vaultへ複数daemonをwriterとして起動しない。

上記の必須手順だけでPublic Beta GOと判断できます。

## B. 任意確認

次はいずれもPublic Beta GOの前提ではありません。実施しなくてもPublic BetaをGOにできます。既存の個人Vaultは必須Acceptanceに使用しません。

- iPhone／iPad、`--local-network`、pairing
- iCloud Drive
- Obsidian（Macで`Obsidianで見る準備`を押し、`Open folder as vault`で専用folderを開く）
- 複数Mac／VM
- 実Vaultのcopy、migration
- native filesystemでのCAS conflict追加stress
- 実Vaultのbackup／restore演習
