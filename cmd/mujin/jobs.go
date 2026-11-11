package main

// 司令塔からのジョブの振り分けと回収。ジョブは各 workspace の herdr の中で、mujin 専用の herdr workspace の tab として動く。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/AnaTofuZ/mujin/internal/coder"
	"github.com/AnaTofuZ/mujin/internal/jobs"
	"github.com/spf13/cobra"
)

type jobState struct {
	jobs.Job
	State string `json:"state"`
}

// dispatchScript は worker 側で mujin 用の herdr workspace に tab を作り、claude を起動して prompt を送る。
// 失敗したら作った pane を閉じる。残すと agent 名が埋まったままになり、次のジョブが起動できない。
// %[1]s=cwd %[2]s=agent 名 %[3]s=prompt（いずれもクオート済み） %[4]s=claude に渡す引数（" -- --model 'x'" か空）
// 関数の中から exit すると、ログインシェルの環境によっては bash が pop_var_context の警告を出すので、その場で exit する。
const dispatchScript = `set -euo pipefail
command -v jq >/dev/null || { echo "worker に jq がありません" >&2; exit 1; }
herdr --version | grep -q '^herdr 0\.9\.' || { echo "herdr 0.9 系が必要です（$(herdr --version 2>&1)）" >&2; exit 1; }
cwd=%[1]s; cwd="${cwd/#\~/$HOME}"
[ -d "$cwd" ] || { echo "$cwd がありません" >&2; exit 1; }
if herdr agent get %[2]s >/dev/null 2>&1; then echo "agent %[2]s が既に worker にいます。herdr の mujin workspace から閉じてください" >&2; exit 1; fi
mkdir -p ~/.mujin/out
w=$(herdr workspace list | jq -r '.result.workspaces[] | select(.label=="mujin") | .workspace_id' | head -n1)
if [ -z "$w" ]; then
  p=$(herdr workspace create --label mujin --cwd "$cwd" --no-focus | jq -r .result.root_pane.pane_id)
else
  p=$(herdr tab create --workspace "$w" --cwd "$cwd" --label %[2]s --no-focus | jq -r .result.root_pane.pane_id)
fi
trap '[ $? -eq 0 ] || herdr pane close "$p" >/dev/null 2>&1 || true' EXIT
sleep 2 # shell のプロンプトが出るのを待つ。agent start は対話プロンプトの pane にしか起動できない
if ! err=$(herdr agent start %[2]s --kind claude --pane "$p" --timeout 60000%[4]s 2>&1 >/dev/null); then
  case "$err" in
    *agent_not_ready*) echo "claude が起動時の確認で止まりました。worker で $cwd を一度 claude で開いて信頼してください" >&2 ;;
    *) echo "$err" >&2 ;;
  esac
  exit 1
fi
herdr agent prompt %[2]s %[3]s --wait --until working --until blocked --timeout 60000 >/dev/null
`

func jobCommands() []*cobra.Command {
	var asJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "各 workspace の状態と、そこで動く mujin のジョブを表示する",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			ws, err := coder.List()
			if err != nil {
				return err
			}
			all, err := jobs.Load()
			if err != nil {
				return err
			}
			st := states(ws, all)
			type row struct {
				Name     string     `json:"name"`
				Status   string     `json:"status"`
				Deadline string     `json:"deadline"`
				Jobs     []jobState `json:"jobs"`
			}
			rows := []row{}
			for _, w := range ws {
				r := row{Name: w.Name, Status: w.LatestBuild.Status, Jobs: []jobState{}}
				if d := w.LatestBuild.Deadline; !d.IsZero() {
					r.Deadline = d.Local().Format(time.RFC3339)
				}
				for _, j := range st {
					// 終わったジョブは jobs で見る。ls は今の割り当てだけを出す
					if j.WS == w.Name && j.State != jobs.Done {
						r.Jobs = append(r.Jobs, j)
					}
				}
				rows = append(rows, r)
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WORKSPACE\tSTATUS\tDEADLINE\tJOBS")
			for _, r := range rows {
				var js []string
				for _, j := range r.Jobs {
					js = append(js, j.ID+":"+j.State)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, r.Status, r.Deadline, strings.Join(js, " "))
			}
			return tw.Flush()
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "JSON で出力する")

	var cwd, model string
	var extend time.Duration
	dispatch := &cobra.Command{
		Use:   "dispatch <ws> <prompt>",
		Short: "workspace に新しい claude を起動してジョブを投げ、ジョブ ID を出力する",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			name, prompt := args[0], args[1]
			ws, err := coder.List()
			if err != nil {
				return err
			}
			all, err := jobs.Load()
			if err != nil {
				return err
			}
			for _, j := range states(ws, all) {
				// 同じリポジトリを 2 つのジョブが触ると衝突するので、1 workspace 1 ジョブにする
				if j.WS == name && (j.State == jobs.Running || j.State == jobs.Blocked) {
					return fmt.Errorf("%s ではジョブ %s が %s です", name, j.ID, j.State)
				}
			}
			if w, ok := coder.Find(ws, name); !ok || w.LatestBuild.Status != "running" {
				if err := start(name); err != nil {
					return err
				}
				if ws, err = coder.List(); err != nil { // 起動で停止予定が変わるので読み直す
					return err
				}
			}
			if err := checkDeadline(ws, name, extend); err != nil {
				fmt.Fprintln(os.Stderr, "warning:", err)
			}
			j := jobs.Job{WS: name, Cwd: cwd, Model: model, CreatedAt: time.Now().Format(time.RFC3339), Prompt: prompt}
			if extend > 0 {
				j.Extend = extend.String()
			}
			if err := jobs.Create(&j); err != nil {
				return err
			}
			// ブランチ名は worker に決めさせる。リポジトリの慣習を見られるのは worker で、mujin/J1 のような連番は司令塔をまたぐと衝突する
			full := fmt.Sprintf("あなたは mujin のジョブ %[1]s です。終わったら要約を ~/.mujin/out/%[1]s.md に書いてください。コードを変更したら、このリポジトリのブランチ名の慣習（git branch -a で確かめる）に沿い、内容が分かって既存のブランチと重ならない名前のブランチを作って commit してください（push はしない）。そのブランチ名を ~/.mujin/out/%[1]s.branch に 1 行で書き、要約にも書いてください。\n\n%[2]s", j.ID, prompt)
			var agentArgs string
			if model != "" {
				agentArgs = " -- --model " + coder.Quote(model)
			}
			if _, err := coder.Output(name, fmt.Sprintf(dispatchScript, coder.Quote(cwd), j.Agent, coder.Quote(full), agentArgs)); err != nil {
				_ = jobs.Remove(j.ID)
				return fmt.Errorf("ジョブを開始できませんでした: %w", err)
			}
			fmt.Println(j.ID)
			return nil
		},
	}
	dispatch.Flags().StringVar(&cwd, "cwd", "", "worker で claude を起動するディレクトリ（claude で信頼済みのもの）")
	dispatch.Flags().DurationVar(&extend, "extend", 0, "停止予定が今からこの時間より前なら、coder schedule extend でここまで延ばす（既定は延ばさない）")
	dispatch.Flags().StringVar(&model, "model", "", "worker の claude に渡す --model（sonnet、opus、モデル ID など。既定は worker の設定）")
	_ = dispatch.MarkFlagRequired("cwd")

	var anyJob bool
	wait := &cobra.Command{
		Use:   "wait <job> | wait --any",
		Short: "ジョブが終わるか、人間の対応が必要になるまで待つ",
		Args: func(_ *cobra.Command, args []string) error {
			if anyJob == (len(args) == 1) {
				return errors.New("ジョブ ID か --any のどちらかを指定してください")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			targets := map[string]bool{}
			if len(args) == 1 {
				j, err := jobs.Get(args[0])
				if err != nil {
					return err
				}
				targets[j.ID] = true
			}
			for first := true; ; first = false {
				if !first {
					time.Sleep(30 * time.Second)
				}
				ws, err := coder.List()
				if err != nil {
					fmt.Fprintln(os.Stderr, "warning:", err) // 一時的な失敗では待つのをやめない
					continue
				}
				all, err := jobs.Load()
				if err != nil {
					return err
				}
				st := states(ws, all)
				if anyJob && first {
					for _, j := range st {
						if j.State == jobs.Running {
							targets[j.ID] = true
						}
					}
					if len(targets) == 0 {
						return errors.New("実行中のジョブがありません")
					}
				}
				for _, j := range st {
					if targets[j.ID] && j.State != jobs.Running {
						fmt.Println(j.ID, j.State)
						return nil
					}
				}
			}
		},
	}
	wait.Flags().BoolVar(&anyJob, "any", false, "実行中のジョブのどれかを待つ")

	read := &cobra.Command{
		Use:   "read <job>",
		Short: "ジョブの結果を表示する（結果ファイルがなければ画面の出力）",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			j, err := jobs.Get(args[0])
			if err != nil {
				return err
			}
			if b, err := os.ReadFile(jobs.OutPath(j.ID)); err == nil {
				_, err = os.Stdout.Write(b)
				return err
			}
			out, err := coder.Output(j.WS, fmt.Sprintf("cat ~/.mujin/out/%s.md 2>/dev/null || herdr agent read %s --source recent-unwrapped --lines 200", j.ID, j.Agent))
			fmt.Print(out)
			return err
		},
	}

	attach := &cobra.Command{
		Use:   "attach <job>",
		Short: "ジョブの claude の端末に入る",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			j, err := jobs.Get(args[0])
			if err != nil {
				return err
			}
			return coder.Run("ssh", "-t", j.WS, "--", "herdr", "agent", "attach", j.Agent)
		},
	}

	list := &cobra.Command{
		Use:   "jobs",
		Short: "ジョブの一覧と、いまの状態を表示する",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			ws, err := coder.List()
			if err != nil {
				return err
			}
			all, err := jobs.Load()
			if err != nil {
				return err
			}
			st := states(ws, all)
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(st)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tWS\tSTATE\tCREATED\tPROMPT")
			for _, j := range st {
				p, _, _ := strings.Cut(j.Prompt, "\n")
				if r := []rune(p); len(r) > 40 {
					p = string(r[:40]) + "…"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", j.ID, j.WS, j.State, j.CreatedAt, p)
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "JSON で出力する")

	var to string
	fetch := &cobra.Command{
		Use:   "fetch <job> <ws>",
		Short: "ジョブのブランチを別の workspace のリポジトリに渡す（GitHub を経由しない）",
		Long: `ジョブの workspace で git bundle を作り、司令塔を経由して渡し先のリポジトリに fetch する。
送るのは origin/HEAD から分かれた後のコミットだけ。渡し先のブランチは上書きする。`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			j, err := jobs.Get(args[0])
			if err != nil {
				return err
			}
			if to == "" {
				to = j.Cwd
			}
			if j.Branch == "" {
				out, err := coder.Output(j.WS, fmt.Sprintf("cat ~/.mujin/out/%s.branch 2>/dev/null || true", j.ID))
				if err != nil {
					return err
				}
				if j.Branch = strings.TrimSpace(out); j.Branch == "" {
					return fmt.Errorf("%s はブランチを残していません（コードを変更していない可能性があります。mujin read %s で確認してください）", j.ID, j.ID)
				}
			}
			if err := pipe(coder.SSH(j.WS, bundleScript(j.Cwd, j.Branch)), coder.SSH(args[1], fetchScript(to, j.Branch))); err != nil {
				return err
			}
			fmt.Printf("%s:%s のブランチ %s を %s:%s に渡しました\n", j.WS, j.Cwd, j.Branch, args[1], to)
			return nil
		},
	}
	fetch.Flags().StringVar(&to, "cwd", "", "渡し先のリポジトリ（既定はジョブの --cwd と同じパス）")

	var rescan bool
	places := &cobra.Command{
		Use:   "places <ws>",
		Short: "workspace の作業場所（git リポジトリ）の一覧 md を表示する。なければ調べて作る",
		Long: `~/.mujin/places/<ws>.md を表示する。なければ worker のホーム以下の git リポジトリを調べて書く。
この md に各リポジトリの用途や「〜の作業はここで」といった指示を書き足しておくと、司令塔が --cwd を選ぶときに読む。
--rescan で調べ直して上書きする（書き足した内容は消える）。`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			home, _ := os.UserHomeDir()
			p := filepath.Join(home, ".mujin", "places", name+".md")
			if b, err := os.ReadFile(p); err == nil && !rescan {
				_, err = os.Stdout.Write(b)
				return err
			}
			out, err := coder.Output(name, placesScript)
			if err != nil {
				return fmt.Errorf("%s を調べられませんでした: %w", name, err)
			}
			md := fmt.Sprintf("# %s の作業場所\n\nmujin dispatch の --cwd の候補。用途や作業の指示を書き足すと司令塔が読む。\n\n%s", name, out)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(md), 0o644); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "保存しました:", p)
			fmt.Print(md)
			return nil
		},
	}
	places.Flags().BoolVar(&rescan, "rescan", false, "調べ直して上書きする")

	return []*cobra.Command{ls, dispatch, wait, read, attach, list, fetch, places}
}

// checkDeadline は Coder から読んだ今の停止予定を確かめる。停止予定は人の操作やアクセスでも動く。
// 延ばすのは extend を指定したときだけで、そのときも今の予定より縮めない（勝手に延ばすと管理者の運用とぶつかる）。
func checkDeadline(ws []coder.Workspace, name string, extend time.Duration) error {
	w, ok := coder.Find(ws, name)
	if !ok {
		return fmt.Errorf("%s が見つかりません", name)
	}
	if extend == 0 {
		// shortcut: 1 時間は固定。ジョブの見込み時間が分かるようになったらそれと比べる
		if dl := w.LatestBuild.Deadline; !dl.IsZero() && time.Until(dl) < time.Hour {
			fmt.Fprintf(os.Stderr, "warning: %s は %s に停止予定です。ジョブの途中で止まるかもしれません（延ばすなら --extend）\n", name, dl.Local().Format("01/02 15:04"))
		}
		return nil
	}
	d, capped := w.Extension(time.Now(), extend)
	if capped {
		fmt.Fprintf(os.Stderr, "warning: %s はテンプレートの上限で %s に止まります\n", name, w.LatestBuild.MaxDeadline.Local().Format("01/02 15:04"))
	}
	if d == 0 {
		return nil
	}
	if out, err := exec.Command("coder", "schedule", "extend", name, d.String()).CombinedOutput(); err != nil {
		return fmt.Errorf("停止予定を延ばせませんでした: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// placesScript はホーム以下の git リポジトリを "- ~/path (origin の URL)" の形で出力する。
// 隠しディレクトリと node_modules は見ない。
const placesScript = `find ~ -maxdepth 5 \( -name node_modules -o \( -name '.*' ! -name .git \) \) -prune -o -name .git -print 2>/dev/null | sort | while read -r g; do
  d=${g%/.git}
  echo "- ~${d#$HOME} ($(git -C "$d" remote get-url origin 2>/dev/null || echo "origin なし"))"
done`

// bundleScript は dir のリポジトリで branch の bundle を標準出力に書く。
// 渡し先も origin を fetch している前提で、origin/HEAD との分岐点より前は送らない。
func bundleScript(dir, branch string) string {
	return fmt.Sprintf(`set -euo pipefail
d=%s; cd "${d/#\~/$HOME}"
if base=$(git merge-base origin/HEAD %[2]s 2>/dev/null); then
  git bundle create -q - %[2]s "^$base"
else
  git bundle create -q - %[2]s
fi`, coder.Quote(dir), coder.Quote(branch))
}

// fetchScript は標準入力の bundle を dir のリポジトリの branch に取り込む。
func fetchScript(dir, branch string) string {
	return fmt.Sprintf(`set -euo pipefail
d=%s; cd "${d/#\~/$HOME}"
f=$(mktemp); trap 'rm -f "$f"' EXIT
cat > "$f"
git fetch -q "$f" %s || { echo "取り込めませんでした。前提のコミットがないと言われたら、渡し先で git fetch origin してから再実行してください" >&2; exit 1; }`,
		coder.Quote(dir), coder.Quote("+"+branch+":"+branch))
}

// states は各ジョブの状態を求める。worker への問い合わせは workspace ごとに 1 回で、
// 完了を見つけたら結果を司令塔にコピーする（以後その worker が止まっても読める）。
func states(ws []coder.Workspace, all []jobs.Job) []jobState {
	running := map[string]bool{}
	for _, w := range ws {
		running[w.Name] = w.LatestBuild.Status == "running"
	}
	type remote struct {
		agents map[string]string // agent 名 → agent_status
		outs   map[string]bool   // ~/.mujin/out にあるファイル名
	}
	seen := map[string]*remote{}
	// shortcut: workspace を 1 つずつ順に問い合わせる。台数が増えて遅くなったら並列にする
	query := func(name string) *remote {
		if r, ok := seen[name]; ok {
			return r
		}
		r := &remote{map[string]string{}, map[string]bool{}}
		seen[name] = r
		out, err := coder.Output(name, `herdr agent list; ls ~/.mujin/out 2>/dev/null || true`)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s に問い合わせできませんでした: %v\n", name, err)
			return r
		}
		first, rest, _ := strings.Cut(out, "\n")
		var list struct {
			Result struct {
				Agents []struct {
					Name   string `json:"name"`
					Status string `json:"agent_status"`
				} `json:"agents"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(first), &list); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s の herdr agent list を読めませんでした: %v\n", name, err)
		}
		for _, a := range list.Result.Agents {
			if a.Name != "" {
				r.agents[a.Name] = a.Status
			}
		}
		for _, f := range strings.Fields(rest) {
			r.outs[f] = true
		}
		return r
	}

	var res []jobState
	for _, j := range all {
		local := jobs.HasOut(j.ID)
		var agent string
		var remoteOut bool
		if !local && running[j.WS] {
			r := query(j.WS)
			agent, remoteOut = r.agents[j.Agent], r.outs[j.ID+".md"]
		}
		s := jobs.Decide(local, running[j.WS], agent, remoteOut)
		if s == jobs.Done && !local {
			out, err := coder.Output(j.WS, fmt.Sprintf("cat ~/.mujin/out/%s.md", j.ID))
			if err == nil {
				err = os.WriteFile(jobs.OutPath(j.ID), []byte(out), 0o644)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s の結果を回収できませんでした: %v\n", j.ID, err)
				s = jobs.Check
			}
		}
		res = append(res, jobState{j, s})
	}
	return res
}
