# ADR 0003: コアは標準ライブラリのみで書き、プロバイダはサブパッケージに分ける

- 状態: 採用
- 日付: 2026-10-17

## 決定

- `ethan` パッケージは標準ライブラリだけに依存する。LLM は `Model` インターフェース越しに呼ぶ。
- Claude 用のアダプタは `ethan/anthropic` に置き、公式 SDK（`anthropic-sdk-go`）に依存する。
- テスト用のスクリプト化モデルは `ethan/ethantest` に置く。リクエストを `Tag` で振り分けるので、並列実行でも決定的にテストできる。

## Claude アダプタの方針

- 構造化出力は `output_config.format`（JSON スキーマ）で得る。Claude Sonnet 5.5 などの現行モデルは、強制 `tool_choice` を受け付けないためである。スキーマには構造化出力の制約（全オブジェクトに `additionalProperties: false`、数値範囲の制約なし）を適用する。
- 拒否に備えて、サーバー側のフォールバック（`fallbacks: "default"`、beta `server-side-fallback-2026-07-01`）を既定で有効にする。Claude API 以外では `WithoutFallbacks` で無効にする。
- 思考ブロックやサーバー側ツールのブロックは `Part.Raw` に元の形で保持し、ツールループで変更せずに送り返す。
- `pause_turn` はアダプタ内で再送して再開する。
- Web 検索はアダプタのオプションとし、構造化出力の呼び出しには付けない。

## 結果

コアを使うだけなら SDK を持ち込まずに済む。他のプロバイダも `Model` を実装すれば追加できる。
