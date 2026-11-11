package main

// 司令塔からのジョブの振り分けと回収。ジョブは各 workspace の herdr の中で、mujin 専用の herdr workspace の tab として動く。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
// %[1]s=cwd %[2]s=agent 名 %[3]s=prompt（いずれもクオート済み）
const dispatchScript = `set -euo pipefail
command -v jq >/dev/null || { echo "jq がありません" >&2; exit 1; }
herdr --version | grep -q '^herdr 0\.9\.' || { echo "herdr 0.9 系が必要です（$(herdr --version 2>&1)）" >&2; exit 1; }
mkdir -p ~/.mujin/out
cwd=%[1]s; cwd="${cwd/#\~/$HOME}"
w=$(herdr workspace list | jq -r '.result.workspaces[] | select(.label=="mujin") | .workspace_id' | head -n1)
if [ -z "$w" ]; then
  p=$(herdr workspace create --label mujin --cwd "$cwd" --no-focus | jq -r .result.root_pane.pane_id)
else
  p=$(herdr tab create --workspace "$w" --cwd "$cwd" --label %[2]s --no-focus | jq -r .result.root_pane.pane_id)
fi
sleep 2 # shell のプロンプトが出るのを待つ。agent start は対話プロンプトの pane にしか起動できない
herdr agent start %[2]s --kind claude --pane "$p" --timeout 60000 >/dev/null
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
				r := row{Name: w.Name, Status: w.LatestBuild.Status, Deadline: w.LatestBuild.Deadline, Jobs: []jobState{}}
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

	var cwd, ttl string
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
			if err := start(name); err != nil {
				return err
			}
			// 誰も接続していない herdr の処理は autostop の延長に数えられない可能性があるので、先に延ばしておく
			if err := coder.Run("schedule", "extend", name, ttl); err != nil {
				fmt.Fprintln(os.Stderr, "warning: 停止期限を延長できませんでした:", err)
			}
			j := jobs.Job{WS: name, Cwd: cwd, TTL: ttl, CreatedAt: time.Now().Format(time.RFC3339), Prompt: prompt}
			if err := jobs.Create(&j); err != nil {
				return err
			}
			full := fmt.Sprintf("あなたは mujin のジョブ %[1]s です。終わったら要約を ~/.mujin/out/%[1]s.md に書き、コードの変更はブランチ %[2]s に push してください。\n\n%[3]s", j.ID, j.Branch, prompt)
			if _, err := coder.Output(name, fmt.Sprintf(dispatchScript, coder.Quote(cwd), j.Agent, coder.Quote(full))); err != nil {
				_ = jobs.Remove(j.ID)
				return fmt.Errorf("ジョブを開始できませんでした（--cwd が claude で信頼済みのフォルダか確認してください）: %w", err)
			}
			fmt.Println(j.ID)
			return nil
		},
	}
	dispatch.Flags().StringVar(&cwd, "cwd", "", "worker で claude を起動するディレクトリ（claude で信頼済みのもの）")
	dispatch.Flags().StringVar(&ttl, "ttl", "8h", "workspace の停止期限を今からこの時間だけ延ばす")
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

	return []*cobra.Command{ls, dispatch, wait, read, attach, list}
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
