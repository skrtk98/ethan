# リリース手順

モジュールの構成は [ADR 0008](adr/0008-modules.md) を参照。

1. すべてのモジュールでテストを通す（CI と同じ）。
2. 本体にタグを打つ: `git tag vX.Y.Z && git push origin vX.Y.Z`
3. `anthropic`、`openai`、`examples` の `go.mod` で、`github.com/skrtk98/ethan` の要求を `vX.Y.Z` にし、`replace` を外す（`examples` はアダプタの `replace` も、手順 5 の後に外す）。`go mod tidy` をかけてコミットする。
4. アダプタにタグを打つ: `git tag anthropic/vX.Y.Z openai/vX.Y.Z && git push origin anthropic/vX.Y.Z openai/vX.Y.Z`
5. `examples/go.mod` でアダプタの要求を手順 4 のタグにし、`go mod tidy` をかけてコミットする。
6. 開発を続けるときは、必要に応じて `replace` を戻す。

`CHANGELOG.md` の該当バージョンの節は、手順 2 の前に書く。
