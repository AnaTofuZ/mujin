# mujin チュートリアル

手元の Mac を司令塔にして、2 台の workspace（`ws-a` と `ws-b`）にジョブを振り分ける流れを順に試します。最初は人間がコマンドを叩き、最後に同じことを司令塔の Claude に任せます。

## 1. 準備

手元に必要なもの:

- `coder` CLI（`coder login` 済み）
- Go 1.26 以上（インストール用）

worker（各 workspace）に必要なもの:

- `herdr` 0.9 系と `jq`
- ログイン済みの `claude`
- ジョブを動かすリポジトリを、一度 `claude` で開いて信頼済みにしておく（未信頼だと起動時の確認で止まる）

インストール:

```sh
go install github.com/AnaTofuZ/mujin/cmd/mujin@latest
mujin --help
```

## 2. 設定を書く（任意）

`create` / `status` / `ca` / `forward` の既定値は `~/.config/mujin/config.yaml` に書きます。ジョブの振り分けだけなら設定は要りません。

```yaml
create:
  args: [--template, my-template]
  param: stack
  choices: [app, none]
bootstrap:
  status: ~/src/.bootstrap-status
  log: ~/src/.bootstrap.log
forward:
  ports: ["3000"]
```

各項目の意味は [README](../README.md#設定) を参照してください。

## 3. workspace を用意する

```sh
mujin create ws-b app     # 作成して、bootstrap の完了まで待つ
mujin up ws-a             # 既存の workspace を起動して待つ
mujin ls
```

```
WORKSPACE  STATUS   DEADLINE              JOBS
ws-a       running  2026-11-11T03:42:35Z
ws-b       running  2026-11-11T03:43:10Z
```

手元のブラウザで確認したいときは、転送を開いておきます（Ctrl-C で終了）。

```sh
mujin forward ws-a -p 3000
```

## 4. ジョブを投げる

```sh
mujin dispatch ws-a "認証まわりのバグを直して。テストも通して。" --cwd ~/src/app
# → J1
```

`dispatch` は次の順に動きます。

1. ws-a が止まっていれば起動する
2. 停止期限を `--ttl`（既定 8h）だけ延ばす
3. worker の herdr に `mujin` という workspace を作り、新しい tab で `claude` を起動する（agent 名は `mujin-j1`）
4. prompt の先頭に「要約を `~/.mujin/out/J1.md` に書き、コードはブランチ `mujin/J1` に push する」という指示を付けて送る

人間が使っている既存の Claude のセッションには触りません。1 つの workspace で同時に動かすジョブは 1 つまでです。

もう 1 台にも投げます。

```sh
mujin dispatch ws-b "決済処理のテストを追加して。" --cwd ~/src/app
# → J2
```

## 5. 待って、結果を読む

```sh
mujin wait --any          # 実行中のどれかが終わるか、対応が必要になるまで待つ
# → J1 done
mujin read J1             # 要約を表示する
```

結果は完了を検知した時点で `~/.mujin/jobs/J1.out.md` にコピーされるので、ws-a が止まった後でも読めます。コードはブランチ `mujin/J1` にあります。

状態の一覧:

```sh
mujin jobs
```

| 状態 | 意味 | 次にやること |
| --- | --- | --- |
| `running` | 動いている | 待つ |
| `done` | 結果を回収した | `mujin read <ID>` |
| `blocked` | 権限の確認などで止まっている | `mujin attach <ID>` で端末に入って答える |
| `check` | 結果を書かずに止まった | `mujin read <ID>` で画面の出力を確認する |
| `stopped` | workspace が止まっている | `mujin up <ws>` で起動して見直す |

`mujin attach J2` で入った端末からは、herdr の操作で抜けられます。

## 6. レビューを別の workspace に頼む

コードの受け渡しは git のブランチで行います。

```sh
mujin dispatch ws-b "ブランチ mujin/J1 をレビューして、問題点を要約に書いて。" --cwd ~/src/app
```

## 7. ファイルを渡す

git で扱わないもの（DB のダンプ、ログ、生成データ）は `cp` で送ります。手元を経由してストリームで流すので、一時ファイルは作りません。転送先は常にディレクトリです。

```sh
mujin cp ws-a:~/dump.sql ws-b:~/tmp/     # workspace から workspace へ
mujin cp ws-a:~/logs/ ./logs/            # workspace から手元へ
mujin cp ./seed.json ws-b:~/             # 手元から workspace へ
```

## 8. 司令塔の Claude に任せる

ここまでの操作は、司令塔の Claude にそのまま任せられます。

```sh
mujin
```

司令塔用の skill を読み込んだ `claude` が、`mujin` コマンドを許可された状態で起動します。たとえば次のように頼みます。

> ws-a で認証のバグを直して、終わったら ws-b でレビューさせて。

司令塔は `mujin ls --json` で様子を見て `dispatch` し、`mujin wait --any` をバックグラウンドで待ちます。`blocked` になったジョブは自分で承認せず、人間に返します。skill の中身は `mujin --skill` で確認できます。

## 困ったとき

- **dispatch が「ジョブを開始できませんでした」で失敗する**: `--cwd` が worker の claude で信頼済みか確認してください。worker で一度 `cd <dir> && claude` を実行して信頼すれば通ります。
- **ジョブが `stopped` になる**: autostop で止まった可能性があります。長いジョブは `--ttl` を長めにしてください。延ばせる上限はテンプレートの設定で決まります。
- **worker の herdr に tab が溜まる**: 終わったジョブの tab は自動では閉じません。worker の herdr で `mujin` workspace の tab を閉じてください。
- **ジョブの記録を消したい**: `~/.mujin/jobs/` のファイルを消すだけです。
