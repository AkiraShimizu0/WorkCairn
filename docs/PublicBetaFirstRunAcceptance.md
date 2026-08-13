# Public Beta macOS First-run Acceptance

この手順だけが実iCloud、実Keychain、実Providerを使う人間確認です。自動testはtemporary directoryとFakeだけを使い、既存の個人Obsidian Vaultには触れません。

1. 新規install状態のmacOSで`workcairn-daemon --mobile`を起動する。
2. native pickerでiCloud Driveを開き、空の`WorkCairn`専用folderを新規作成して選ぶ。iCloud rootや既存の個人Vaultは選ばない。
3. Macに自動表示されたFirst-run Wizardで「最初のAIチームを作ります」を確認し、明示承認する。
4. Product Manager、Content Writer、QA EngineerがCompany Viewへ表示されることを確認する。
5. `AI Connections`で`MacでClaudeを接続`を押し、Macのnative hidden-inputへtest用credentialを入力する。iPhoneに入力欄や値が出ないことを確認する。
6. Claudeが`Connected`、Routingが`Automatic`となり、Model ID入力がないことを確認する。
7. `会社を始める`を押し、最初の自然言語依頼画面へ移ることを確認する。
8. iPhone Safariでdaemon表示のlocal URLを開き、pairing codeで接続する。
9. 依頼を1件送り、「このように進めます」でPlanを確認する。通常表示に`PROPOSED-*`、Role ID、digestが出ないことを確認する。
10. Maker実行、別のQA ReviewerによるReview、必要ならRevision、再Review、完了まで進める。
11. 実行中は小さい「会社が働いています」だけ、質問／承認／failure時だけMy Actionsが前面に出ることを確認する。
12. Timelineで依頼、Plan、担当、Deliverable、Review、Revision、承認、完了を確認する。failureを起こした場合は画面移動／reload後も消えず、technical detailsをcopyできることを確認する。
13. Macで`Obsidianで見る準備`を押し、Finderに専用folderが表示されることを確認する。Obsidianの`Open folder as vault`で同じfolderを開き、成果物と人間可読履歴を閲覧する。
14. daemonをgraceful shutdownして再起動する。folder pickerを再表示せず同じ専用Vaultを利用することを確認する。
15. 過去Session、Timeline、Plan、Task、Deliverable、Review、Revision、failure／Recovery evidenceへ再度到達できることを確認する。

同一Vaultで2つのdaemonを同時起動しません。実Provider確認はPlan 1回、Task 1件、Review 1回に制限し、自動retryや別Provider fallbackが起きないことを確認します。
