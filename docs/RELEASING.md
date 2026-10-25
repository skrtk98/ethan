# リリース手順

モジュールの構成は [ADR 0008](adr/0008-modules.md) を参照。

1. すべてのモジュールでテストを通す（CI と同じ）。`CHANGELOG.md` に該当バージョンの節を書く。
2. 本体にタグを打つ。

   ```sh
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

3. `anthropic` と `openai` の `go.mod` で、`github.com/skrtk98/ethan` の要求を `vX.Y.Z` にし、`replace` を外す。`go mod tidy` をかけ、テストを通してから `go.mod` と `go.sum` をコミットし、プッシュする。
4. アダプタにタグを打つ。`git tag` は 1 回に 1 つのタグしか作らないので、別々に実行する。

   ```sh
   git tag anthropic/vX.Y.Z
   git tag openai/vX.Y.Z
   git push origin anthropic/vX.Y.Z openai/vX.Y.Z
   ```

5. `examples/go.mod` で、本体とアダプタの要求を手順 2 と 4 のタグにし、すべての `replace` を外す。そのうえで `go mod tidy` をかける（順序を逆にすると、公開版のチェックサムが `go.sum` に入らない）。ビルドを確かめてから `go.mod` と `go.sum` をコミットする。
6. 開発を続けるときは、必要に応じて `replace` を戻す。

## 開発中の要求

開発中のサブモジュールは、`replace` でリポジトリ内の本体を読む。ただし `replace` は利用者の側では無視されるので、`require` には本体の実在するコミットの擬似バージョンを書いておく。そうすれば、タグの前でも `go get github.com/skrtk98/ethan/anthropic@<commit>` が成り立つ。本体の API を変え、アダプタがそれに依存するようになったら、その本体のコミットをプッシュしてから擬似バージョンを更新する。
