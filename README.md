# mujin

Coder workspace と、その中の [herdr](https://herdr.dev) を束ねて、複数の workspace の Claude Code にジョブを振り分ける CLI。

- 人間が叩くコマンドと、司令塔の Claude が Bash から叩くコマンドは同じ
- `mujin`（引数なし）で、司令塔用の skill を読み込んだ `claude` が起動する
- ジョブは worker 側の herdr の中で動くので、司令塔が止まっても続く

前提: 手元に `coder`（`coder login` 済み）。worker に `herdr` 0.9 系、`jq`、ログイン済みの `claude`。

```sh
go install github.com/AnaTofuZ/mujin/cmd/mujin@latest
```

## コマンド

| コマンド | やること |
| --- | --- |
| `mujin create [name] [value]` | workspace を作成し、bootstrap の完了まで待つ。`--forward` で続けて転送 |
| `mujin up [name]` | 停止中なら起動して待つ。`--forward` で続けて転送 |
| `mujin forward [name] [-p 3000 ...]` | ポートを手元に転送する（Ctrl-C で終了） |
| `mujin status [name]` / `mujin ca [name] [out]` | bootstrap の状態とログ / ルート CA 証明書の取得 |
| `mujin ls [--json]` | workspace の状態と、割り当て中のジョブ |
| `mujin dispatch <ws> "prompt" --cwd <dir> [--ttl 8h]` | 停止期限を延長し、新しい claude にジョブを投げる。ジョブ ID を出力 |
| `mujin wait <job>` / `mujin wait --any` | 終わるか、人間の対応が必要になるまで待つ |
| `mujin read <job>` / `mujin attach <job>` | 結果を読む / その claude の端末に入る |
| `mujin jobs [--json]` | ジョブの一覧と状態（`done` `running` `blocked` `check` `stopped`） |
| `mujin cp <src> <dst>` | `ws:path` 形式で workspace 間・手元との間を転送する |
| `mujin --skill` | 司令塔用の SKILL.md を出力する |

ジョブの記録は `~/.mujin/jobs/<ID>.md`、回収した結果は `~/.mujin/jobs/<ID>.out.md`。

## 設定

`~/.config/mujin/config.yaml`（任意）。環境ごとの値はここに書く。

```yaml
create:
  args: [--template, my-template, --parameter, arch=arm64]  # coder create に足す引数
  param: stack                     # create の第 2 引数で渡すパラメータ
  choices: [app, none]             # 省略時に対話で選ぶ選択肢
bootstrap:
  when: '[ "$APP_STACK" != none ]' # 成功したときだけ bootstrap を待つ
  status: ~/src/.bootstrap-status  # 最終行が "<時刻> done:..." か "<時刻> failed:..."
  log: ~/src/.bootstrap.log
ca: ~/src/proxy/certs/rootCA.pem
forward:
  ports: ["443", "80"]
  notes: |
    https://app.example.test
```

`MUJIN_BIND` で転送の待ち受けアドレスを変えられる（既定 `0.0.0.0`）。
