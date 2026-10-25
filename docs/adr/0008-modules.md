# ADR 0008: アダプタは別モジュールにする

- 状態: 採用
- 日付: 2026-10-25

## 決定

- リポジトリを 4 つのモジュールに分ける。
  - `github.com/skrtk98/ethan`: 本体と `ethantest`。標準ライブラリだけに依存する。
  - `github.com/skrtk98/ethan/anthropic`: Claude のアダプタ。`anthropic-sdk-go` に依存する。
  - `github.com/skrtk98/ethan/openai`: OpenAI と Responses API 互換サーバーのアダプタ。`openai-go` に依存する。
  - `github.com/skrtk98/ethan/examples`: デモ。公開 API ではない。
- 開発中は、各サブモジュールの `go.mod` の `replace` でリポジトリ内の本体を参照する。リリースの手順は [RELEASING.md](../RELEASING.md) に書く。

## 理由

ADR 0003 で本体のパッケージは標準ライブラリだけに依存させたが、モジュールが一つだと、本体だけを使う利用者の依存グラフにも両方の SDK とその間接依存（OpenTelemetry など）が入る。`go mod download` や脆弱性の検査、依存の監査の対象も増える。アダプタを別モジュールにすれば、利用者は使うプロバイダの SDK だけを持ち込む。

分割は最初のタグより前に行う。タグを打った後に分けると、利用者の `go.mod` の書き換えが必要になる。

## 結果

- テストはモジュールごとに実行する（`go test ./...` はモジュールの境界を越えない）。CI はモジュールの一覧を順に回す。
- アダプタは本体と別にバージョンを付けられる。SDK の更新だけならアダプタのタグだけを打てばよい。
- 本体の API を変えたときは、本体のタグを打ってから、アダプタの `go.mod` を更新してタグを打つ。
