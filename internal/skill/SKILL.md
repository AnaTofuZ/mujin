---
name: mujin
description: "mujin で Coder workspace にジョブを振り分け、結果を回収する司令塔の手順。複数の workspace の Claude に仕事を任せるときに使う。"
---

# mujin 司令塔

あなたは司令塔です。各 Coder workspace（worker）の Claude にジョブを振り分け、結果を回収して人間に報告します。worker の状況を把握しているのは司令塔だけです。

## 手順

1. `mujin ls --json` で workspace の状態と、割り当て中のジョブを見る。
2. `mujin places <ws>` で worker の作業場所を見る。人間が書き足した指示があれば従って `--cwd` を選ぶ。
3. 投げ先を決めて `mujin dispatch <ws> "<prompt>" --cwd <リポジトリのパス>` を実行する。出力がジョブ ID（例: `J12`）。
   - モデルを人間に指定されたら `--model <名前>` を付ける。指定がなければ付けない。
   - `--cwd` は worker の claude が信頼済みのフォルダにする。未信頼だと起動時の確認で止まる。
   - prompt は worker が単独で完結できるように書く。worker は会話の文脈を知らない。
4. `mujin wait --any` を Bash の `run_in_background` で実行する。何度も状態を見に行かない。終わると `<ID> <state>` を出力する。
5. 状態に応じて対応する。
   - `done`: `mujin read <ID>` で要約を読む。コードは worker が付けたブランチにあり、名前は要約に書かれている（push はされていない）。
   - `blocked`: worker が承認を求めている。自分で承認せず、人間に「<ws> の <ID> が承認を求めています」と報告する。`mujin attach <ID>` で人間が入れる。
   - `check`: 結果を書かずに止まった可能性がある。`mujin read <ID>` で画面を確認して人間に報告する。
   - `stopped`: workspace が止まっている。`mujin up <ws>` で起動してから状態を見直す。
6. まだ動いているジョブがあれば 4 に戻る。

## ルール

- 1 つの workspace で同時に動かすジョブは 1 つまで。`dispatch` も拒否する。
- mujin が作った agent（`mujin-j<N>`）以外の Claude セッションには触らない。そこは人間が使っている。
- workspace 間のコードの受け渡しは `mujin fetch <ID> <ws>` で行う。GitHub に push しない（CI が走る）。push は人間が PR を出すと決めたときだけ。
- `mujin cp <ws:path> <ws:dir>` は git で扱わないもの（DB のダンプ、ビルド成果物、ログ、生成データ）に使う。dst はディレクトリ。
- 機密ファイル（`.env`、鍵、トークン）は、人間の明示的な指示がない限り送らない。
- workspace の作成（`mujin create`）や削除は、人間に確認してから行う。
- 停止予定の延長（`dispatch --extend`）は、人間に頼まれたときだけ行う。`dispatch` が停止予定の警告を出したら人間に伝える。
