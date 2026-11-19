// Package coder は coder CLI を呼び出す。Coder の API は CLI で取れないもの（ビルドパラメータ）だけ直接叩く。
package coder

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Workspace struct {
	Name         string `json:"name"`
	TemplateName string `json:"template_name"`
	LatestBuild  struct {
		ID          string    `json:"id"`
		Status      string    `json:"status"`
		Deadline    time.Time `json:"deadline"`     // 停止予定。ゼロなら autostop なし。人の操作やアクセスでも動く
		MaxDeadline time.Time `json:"max_deadline"` // テンプレートが許す停止予定の上限。ゼロなら上限なし
	} `json:"latest_build"`
}

// Extension は停止予定を now+ttl 以降にするために coder schedule extend へ渡す時間を返す。
// 今の停止予定で足りるなら 0（他の人が延ばした予定を縮めない）。上限で切ったときは capped が true。
func (w Workspace) Extension(now time.Time, ttl time.Duration) (d time.Duration, capped bool) {
	b := w.LatestBuild
	if b.Deadline.IsZero() {
		return 0, false
	}
	want := now.Add(ttl)
	if !b.MaxDeadline.IsZero() && want.After(b.MaxDeadline) {
		want, capped = b.MaxDeadline, true
	}
	if !b.Deadline.Before(want) {
		return 0, capped
	}
	// extend は分単位で、30 分未満を受け付けない
	return max(want.Sub(now).Truncate(time.Minute), 30*time.Minute), capped
}

// Find は名前で workspace を探す。
func Find(ws []Workspace, name string) (Workspace, bool) {
	for _, w := range ws {
		if w.Name == name {
			return w, true
		}
	}
	return Workspace{}, false
}

// Quote は s を POSIX シェルの 1 語にする。
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// SSH は workspace で bash -lc script を実行するコマンドを返す。
// coder ssh は残りの引数を連結してリモートのシェルで再分割するため、script を 1 語にクオートしておく。
// Stdin は nil（/dev/null）のままにする。端末が繋がっていると coder ssh が入力を待って戻らない。
func SSH(ws, script string) *exec.Cmd {
	return exec.Command("coder", "ssh", ws, "--", "bash", "-lc", Quote(script))
}

// Output は workspace で script を実行し、標準出力を返す。標準エラーはそのまま流す。
func Output(ws, script string) (string, error) {
	cmd := SSH(ws, script)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

// Run は coder CLI を端末に繋いだまま実行する。
func Run(args ...string) error {
	cmd := exec.Command("coder", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func List() ([]Workspace, error) {
	out, err := exec.Command("coder", "list", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("coder list に失敗しました（coder login 済みか確認してください）: %w", err)
	}
	var ws []Workspace
	return ws, json.Unmarshal(out, &ws)
}

type Parameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Parameters は workspace の最新ビルドのパラメータを返す。coder CLI では読めないので、CLI のトークンで API を叩く。
func Parameters(w Workspace) ([]Parameter, error) {
	out, err := exec.Command("coder", "whoami", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("coder whoami に失敗しました: %w", err)
	}
	var me []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(out, &me); err != nil || len(me) == 0 {
		return nil, fmt.Errorf("coder whoami の出力を読めません: %v", err)
	}
	token, err := exec.Command("coder", "login", "token").Output()
	if err != nil {
		return nil, fmt.Errorf("coder login token に失敗しました: %w", err)
	}
	req, err := http.NewRequest("GET", strings.TrimRight(me[0].URL, "/")+"/api/v2/workspacebuilds/"+w.LatestBuild.ID+"/parameters", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Coder-Session-Token", strings.TrimSpace(string(token)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s のパラメータを読めません: %s", w.Name, res.Status)
	}
	var ps []Parameter
	return ps, json.NewDecoder(res.Body).Decode(&ps)
}
