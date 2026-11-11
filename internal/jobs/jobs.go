// Package jobs は司令塔の ~/.mujin/jobs/ に置く、1 ジョブ 1 ファイルの Markdown 記録を扱う。
// 状態はファイルに保存せず、そのつど herdr と結果ファイルから求める。
package jobs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Job struct {
	ID        string `yaml:"id" json:"id"`
	WS        string `yaml:"ws" json:"ws"`
	Agent     string `yaml:"agent" json:"agent"`
	Cwd       string `yaml:"cwd" json:"cwd"`
	Branch    string `yaml:"branch,omitempty" json:"branch,omitempty"` // 空なら worker が決めて ~/.mujin/out/<ID>.branch に書く
	Model     string `yaml:"model,omitempty" json:"model,omitempty"`
	CreatedAt string `yaml:"created_at" json:"created_at"`
	Extend    string `yaml:"extend,omitempty" json:"extend,omitempty"`
	Prompt    string `yaml:"-" json:"prompt"`
}

const (
	Done    = "done"    // 結果を回収済み
	Running = "running" // agent が動いている
	Blocked = "blocked" // 承認待ち。人間に返す
	Check   = "check"   // agent が落ち着いたのに結果がない。途中で止まった可能性
	Stopped = "stopped" // workspace が止まっている。autostop の可能性
)

// Decide はジョブの状態を求める。agentStatus は herdr の agent_status で、agent がいなければ空。
func Decide(hasLocalOut, wsRunning bool, agentStatus string, hasRemoteOut bool) string {
	switch {
	case hasLocalOut:
		return Done
	case !wsRunning:
		return Stopped
	case agentStatus == "working" || agentStatus == "unknown":
		return Running
	case agentStatus == "blocked":
		return Blocked
	case hasRemoteOut:
		return Done
	}
	return Check
}

func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mujin", "jobs")
}

func path(id string) string    { return filepath.Join(Dir(), id+".md") }
func OutPath(id string) string { return filepath.Join(Dir(), id+".out.md") }
func Remove(id string) error   { return os.Remove(path(id)) }

func HasOut(id string) bool {
	_, err := os.Stat(OutPath(id))
	return err == nil
}

// Create は次の ID を割り当てて j を書き込む。ID の衝突はファイルの排他作成で防ぐ。
func Create(j *Job) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	all, err := Load()
	if err != nil {
		return err
	}
	n := 0
	for _, x := range all {
		n = max(n, num(x.ID))
	}
	for {
		n++
		j.ID = fmt.Sprintf("J%d", n)
		j.Agent = fmt.Sprintf("mujin-j%d", n)
		f, err := os.OpenFile(path(j.ID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		fm, _ := yaml.Marshal(j)
		_, err = fmt.Fprintf(f, "---\n%s---\n%s\n", fm, j.Prompt)
		return errors.Join(err, f.Close())
	}
}

func Get(id string) (Job, error) {
	b, err := os.ReadFile(path(strings.ToUpper(id)))
	if err != nil {
		return Job{}, fmt.Errorf("ジョブ %s が見つかりません", id)
	}
	return parse(b)
}

// Load は全ジョブを ID 順に返す。
func Load() ([]Job, error) {
	files, _ := filepath.Glob(filepath.Join(Dir(), "J*.md"))
	var all []Job
	for _, f := range files {
		if strings.HasSuffix(f, ".out.md") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		j, err := parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		all = append(all, j)
	}
	sort.Slice(all, func(a, b int) bool { return num(all[a].ID) < num(all[b].ID) })
	return all, nil
}

func parse(b []byte) (Job, error) {
	var j Job
	parts := bytes.SplitN(b, []byte("---\n"), 3)
	if len(parts) != 3 {
		return j, errors.New("frontmatter がありません")
	}
	j.Prompt = strings.TrimSpace(string(parts[2]))
	return j, yaml.Unmarshal(parts[1], &j)
}

func num(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "J"))
	return n
}
