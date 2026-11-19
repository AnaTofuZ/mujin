package main

// workspace の作成・起動・転送。環境固有の値（テンプレート、bootstrap のパスなど）は config.yaml から読む。

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AnaTofuZ/mujin/internal/coder"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Config は ~/.config/mujin/config.yaml。どの項目も省略できる。
type Config struct {
	Create struct {
		Args    []string `yaml:"args"`    // coder create に足す引数（--template など）
		Param   string   `yaml:"param"`   // create の第 2 引数で値を渡すパラメータ名
		Choices []string `yaml:"choices"` // Param を対話で尋ねるときの選択肢
		Setup   string   `yaml:"setup"`   // 作成直後（bootstrap の前）に workspace で実行するシェル
	} `yaml:"create"`
	Bootstrap struct {
		When   string `yaml:"when"`   // 成功したときだけ bootstrap を待つシェルの条件
		Status string `yaml:"status"` // 最終行が "<時刻> done:..." / "<時刻> failed:..." になるファイル
		Log    string `yaml:"log"`
	} `yaml:"bootstrap"`
	CA      string `yaml:"ca"` // mujin ca で取得するファイルのリモートパス
	Forward struct {
		Ports []string `yaml:"ports"`
		Notes string   `yaml:"notes"` // 転送開始時に表示する案内
	} `yaml:"forward"`
}

func loadConfig() (Config, error) {
	var c Config
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".config", "mujin", "config.yaml")
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

func isTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func wsCommands() []*cobra.Command {
	var fwd bool
	create := &cobra.Command{
		Use:   "create [name] [value]",
		Short: "workspace を作成し、bootstrap の完了まで待って手元の herdr に登録する（value は config の create.param の値）",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, value := arg(args, 0), arg(args, 1)
			if name == "" {
				def := "dev-" + time.Now().Format("0102")
				if !isTTY() {
					name = def
				} else if err := survey.AskOne(&survey.Input{Message: "ワークスペース名", Default: def}, &name); err != nil {
					return err
				}
			}
			cargs := append([]string{"create", name}, cfg.Create.Args...)
			if p := cfg.Create.Param; p != "" {
				if value == "" {
					var q survey.Prompt = &survey.Input{Message: p}
					if len(cfg.Create.Choices) > 0 {
						q = &survey.Select{Message: p, Options: cfg.Create.Choices}
					}
					if !isTTY() {
						return fmt.Errorf("%s の値を指定してください", p)
					}
					if err := survey.AskOne(q, &value); err != nil {
						return err
					}
				}
				cargs = append(cargs, "--parameter", p+"="+value)
			}
			if err := coder.Run(append(cargs, "--yes")...); err != nil {
				return err
			}
			if err := provision(cfg, name); err != nil {
				return err
			}
			herdrAdd(name)
			if fwd {
				return forward(cfg, name, nil)
			}
			return nil
		},
	}
	create.Flags().BoolVar(&fwd, "forward", false, "完了後にポート転送を続ける")

	up := &cobra.Command{
		Use:   "up [name]",
		Short: "停止中なら起動し、使える状態になるまで待つ",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, err := pickWorkspace(args)
			if err != nil {
				return err
			}
			if err := start(name); err != nil {
				return err
			}
			if err := waitBootstrap(cfg, name); err != nil {
				fmt.Fprintln(os.Stderr, "warning:", err)
			}
			if fwd {
				return forward(cfg, name, nil)
			}
			return nil
		},
	}
	up.Flags().BoolVar(&fwd, "forward", false, "完了後にポート転送を続ける")

	var ports []string
	fw := &cobra.Command{
		Use:   "forward [name]",
		Short: "ポートを手元に転送する（Ctrl-C で終了）",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, err := pickWorkspace(args)
			if err != nil {
				return err
			}
			return forward(cfg, name, ports)
		},
	}
	fw.Flags().StringSliceVarP(&ports, "port", "p", nil, "転送するポート（既定は config の forward.ports）")

	status := &cobra.Command{
		Use:   "status [name]",
		Short: "bootstrap の状態とログの末尾を表示する",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Bootstrap.Status == "" {
				return errors.New("config の bootstrap.status が設定されていません")
			}
			name, err := pickWorkspace(args)
			if err != nil {
				return err
			}
			script := fmt.Sprintf(`echo "== status"; cat %s 2>/dev/null || echo "(not started)"`, cfg.Bootstrap.Status)
			if cfg.Bootstrap.Log != "" {
				script += fmt.Sprintf(`; echo; echo "== log (tail)"; tail -n 30 %s 2>/dev/null || true`, cfg.Bootstrap.Log)
			}
			cmd := coder.SSH(name, script)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			return cmd.Run()
		},
	}

	ca := &cobra.Command{
		Use:   "ca [name] [out]",
		Short: "ルート CA 証明書を手元に保存する（既定 rootCA.pem）",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.CA == "" {
				return errors.New("config の ca が設定されていません")
			}
			name, err := pickWorkspace(args)
			if err != nil {
				return err
			}
			out := arg(args, 1)
			if out == "" {
				out = "rootCA.pem"
			}
			pem, err := coder.Output(name, "cat "+cfg.CA)
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, []byte(pem), 0o644); err != nil {
				return err
			}
			fmt.Println("保存しました:", out)
			fmt.Println("macOS: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain", out)
			return nil
		},
	}
	setupCmd := &cobra.Command{
		Use:   "setup [name...]",
		Short: "config の create.setup を既存の workspace で実行する（同じ内容を実行済みなら飛ばす）",
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Create.Setup == "" {
				return errors.New("config の create.setup が設定されていません")
			}
			if len(args) == 0 {
				name, err := pickWorkspace(args)
				if err != nil {
					return err
				}
				args = []string{name}
			}
			// 1 台が失敗しても残りには流す
			var errs []error
			for _, name := range args {
				fmt.Fprintln(os.Stderr, "==", name)
				if err := setup(cfg, name); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
				}
			}
			return errors.Join(errs...)
		},
	}
	var yes bool
	rm := &cobra.Command{
		Use:   "rm <name...>",
		Short: "workspace を削除し、手元の herdr の machine 登録も消す",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := confirm("削除", args, yes); err != nil {
				return err
			}
			var errs []error
			for _, name := range args {
				if err := coder.Run("delete", name, "--yes"); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					continue
				}
				if err := herdrRemove(name); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
				}
			}
			return errors.Join(errs...)
		},
	}
	rm.Flags().BoolVarP(&yes, "yes", "y", false, "確認せずに削除する")

	var all bool
	recreate := &cobra.Command{
		Use:   "recreate [name...] | recreate --all",
		Short: "workspace を削除し、同じ名前・同じパラメータで作り直して herdr に登録し直す（テンプレートは最新版になる）",
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			ws, err := coder.List()
			if err != nil {
				return err
			}
			if all {
				args = nil
				for _, w := range ws {
					args = append(args, w.Name)
				}
			}
			if len(args) == 0 {
				return errors.New("ワークスペース名か --all を指定してください")
			}
			// 消してからでは読めないので、先に全台のパラメータを集めて表示する。
			cargs := map[string][]string{}
			for _, name := range args {
				w, ok := coder.Find(ws, name)
				if !ok {
					return fmt.Errorf("ワークスペース %s がありません", name)
				}
				ps, err := coder.Parameters(w)
				if err != nil {
					return err
				}
				cargs[name] = recreateArgs(w, ps)
				fmt.Fprintln(os.Stderr, "coder", strings.Join(cargs[name], " "))
			}
			if err := confirm("削除して作り直し", args, yes); err != nil {
				return err
			}
			// bootstrap は workspace 側で並行に進むので、全台を作ってから順に待つ。
			var errs []error
			var made []string
			for _, name := range args {
				fmt.Fprintln(os.Stderr, "==", name)
				if err := coder.Run("delete", name, "--yes"); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					continue
				}
				if err := herdrRemove(name); err != nil {
					fmt.Fprintln(os.Stderr, "warning:", err)
				}
				if err := coder.Run(cargs[name]...); err != nil {
					errs = append(errs, fmt.Errorf("%s: 削除後の作成に失敗しました（coder %s）: %w", name, strings.Join(cargs[name], " "), err))
					continue
				}
				made = append(made, name)
			}
			for _, name := range made {
				fmt.Fprintln(os.Stderr, "==", name)
				if err := provision(cfg, name); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					continue
				}
				herdrAdd(name)
			}
			return errors.Join(errs...)
		},
	}
	recreate.Flags().BoolVar(&all, "all", false, "自分の workspace をすべて作り直す")
	recreate.Flags().BoolVarP(&yes, "yes", "y", false, "確認せずに作り直す")

	return []*cobra.Command{create, up, fw, status, ca, setupCmd, rm, recreate}
}

// recreateArgs は w と同じテンプレート・パラメータで作る coder create の引数を返す。
func recreateArgs(w coder.Workspace, ps []coder.Parameter) []string {
	args := []string{"create", w.Name, "--template", w.TemplateName}
	for _, p := range ps {
		args = append(args, "--parameter", p.Name+"="+p.Value)
	}
	return append(args, "--yes")
}

// confirm は消す前に人間に確かめる。対話できないときは --yes を求める。
func confirm(what string, names []string, yes bool) error {
	if yes {
		return nil
	}
	if !isTTY() {
		return fmt.Errorf("対話できない環境です。%s してよければ --yes を付けてください", what)
	}
	ok := false
	if err := survey.AskOne(&survey.Confirm{Message: fmt.Sprintf("%s を%sしますか（中のデータは消えます）", strings.Join(names, ", "), what)}, &ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("中止しました")
	}
	return nil
}

// herdrHost は workspace の SSH ホスト名（coder config-ssh の既定の接尾辞）。
func herdrHost(name string) string { return name + ".coder" }

// herdrRemove は手元の herdr から name の machine 登録を ID 指定で消す。herdr がなければ何もしない。
func herdrRemove(name string) error {
	if _, err := exec.LookPath("herdr"); err != nil {
		return nil
	}
	out, err := exec.Command("herdr", "machine", "list").Output()
	if err != nil {
		return fmt.Errorf("herdr machine list に失敗しました: %w", err)
	}
	for _, id := range herdrIDs(string(out), herdrHost(name)) {
		if err := exec.Command("herdr", "machine", "remove", id).Run(); err != nil {
			return fmt.Errorf("herdr machine remove %s に失敗しました: %w", id, err)
		}
		fmt.Fprintf(os.Stderr, "herdr の machine 登録を消しました: %s (%s)\n", herdrHost(name), id)
	}
	return nil
}

// herdrIDs は herdr machine list（ID, ラベル, SSH ターゲット, ... のタブ区切り）から host に向く ID を返す。
func herdrIDs(list, host string) []string {
	var ids []string
	for _, line := range strings.Split(list, "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 3 && f[2] == host {
			ids = append(ids, f[0])
		}
	}
	return ids
}

// herdrAdd は workspace に手元と同じ版の herdr を入れてから、手元の herdr に machine として登録する。
// herdr machine add はリモートに同じ版がないと対話で導入の承認を求めるので、先に入れておく。
// 手元に herdr がなければ何もしない。失敗したら手で登録するコマンドを案内する。
func herdrAdd(name string) {
	if _, err := exec.LookPath("herdr"); err != nil {
		return
	}
	err := func() error {
		out, err := exec.Command("herdr", "--version").Output()
		if err != nil {
			return fmt.Errorf("herdr --version に失敗しました: %w", err)
		}
		f := strings.Fields(string(out))
		if len(f) < 2 {
			return fmt.Errorf("herdr の版を読めません: %q", out)
		}
		cmd := coder.SSH(name, herdrInstallScript(f[1]))
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("herdr %s の導入に失敗しました: %w", f[1], err)
		}
		add := exec.Command("herdr", "machine", "add", herdrHost(name), "--label", name)
		add.Stdout, add.Stderr = os.Stderr, os.Stderr
		return add.Run()
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
		fmt.Fprintf(os.Stderr, "herdr には端末で手で登録してください: herdr machine add %s --label %s\n", herdrHost(name), name)
	}
}

// herdrInstallScript は herdr machine add が見る ~/.local/bin/herdr に version を入れるスクリプト。
// 入れる版の URL と sha256 は herdr の配布マニフェストから引く。同じ版が入っていれば何もしない。
func herdrInstallScript(version string) string {
	return fmt.Sprintf(`set -eo pipefail
b="$HOME/.local/bin/herdr" v=%[1]s
[ "$("$b" --version 2>/dev/null)" = "herdr $v" ] && exit 0
a="linux-$(uname -m)"
m=$(curl -fsSL --retry 3 https://herdr.dev/latest.json)
url=$(jq -r --arg v "$v" --arg a "$a" '.releases[$v].assets[$a] // empty' <<<"$m")
sum=$(jq -r --arg v "$v" --arg a "$a" '.releases[$v].sha256[$a] // empty' <<<"$m")
[ -n "$url" ] && [ -n "$sum" ] || { echo "herdr $v ($a) が配布マニフェストにありません" >&2; exit 1; }
mkdir -p "$(dirname "$b")"
curl -fsSL --retry 3 -o "$b.tmp" "$url"
echo "$sum  $b.tmp" | sha256sum -c --quiet
chmod +x "$b.tmp" && mv "$b.tmp" "$b"
echo "herdr $v を $b に入れました" >&2`, coder.Quote(version))
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// pickWorkspace は args[0] を返す。なければ自分の workspace から選ばせる（1 つならそれ）。
func pickWorkspace(args []string) (string, error) {
	if name := arg(args, 0); name != "" {
		return name, nil
	}
	ws, err := coder.List()
	if err != nil {
		return "", err
	}
	switch {
	case len(ws) == 0:
		return "", errors.New("ワークスペースがありません")
	case len(ws) == 1:
		fmt.Fprintf(os.Stderr, "ワークスペース: %s (%s)\n", ws[0].Name, ws[0].LatestBuild.Status)
		return ws[0].Name, nil
	case !isTTY():
		return "", errors.New("ワークスペース名を指定してください（対話できない環境です）")
	}
	var labels []string
	for _, w := range ws {
		labels = append(labels, fmt.Sprintf("%s (%s)", w.Name, w.LatestBuild.Status))
	}
	var i int
	if err := survey.AskOne(&survey.Select{Message: "ワークスペース", Options: labels}, &i); err != nil {
		return "", err
	}
	return ws[i].Name, nil
}

// start は停止中なら起動し、Coder agent に繋がるまで待つ。
func start(name string) error {
	_ = coder.Run("start", name, "--yes") // 起動済みなら失敗するが問題ない
	return waitAgent(name)
}

func waitAgent(name string) error {
	fmt.Fprintln(os.Stderr, "ワークスペースのエージェント（Coder agent）の接続を待っています...")
	for range 120 {
		if coder.SSH(name, "true").Run() == nil {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("エージェントに接続できません（coder show %s で状態を確認してください）", name)
}

// provision は作成直後の workspace で agent を待ち、create.setup を流して bootstrap を待つ。
func provision(cfg Config, name string) error {
	if err := waitAgent(name); err != nil {
		return err
	}
	// 失敗しても workspace は使えるので、警告に留めて bootstrap を待つ。
	if err := setup(cfg, name); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	return waitBootstrap(cfg, name)
}

// setup は config の create.setup を workspace で実行する。
func setup(cfg Config, name string) error {
	if cfg.Create.Setup == "" {
		return nil
	}
	fmt.Fprintln(os.Stderr, "create.setup を実行しています...")
	cmd := coder.SSH(name, setupScript(cfg.Create.Setup))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create.setup が失敗しました: %w", err)
	}
	return nil
}

// setupScript は、成功した setup の内容のハッシュを workspace に残し、同じ内容なら実行しないスクリプトにする。
// setup を書き換えると、次の実行で流し直す。
func setupScript(setup string) string {
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(setup)))
	return fmt.Sprintf(`m="$HOME/.mujin/setup.sha256"
if [ "$(cat "$m" 2>/dev/null)" = %[1]s ]; then echo "create.setup は実行済みです（$m）" >&2; exit 0; fi
(
%[2]s
) || exit
mkdir -p "$HOME/.mujin" && echo %[1]s >"$m"`, sum, setup)
}

func waitBootstrap(cfg Config, name string) error {
	b := cfg.Bootstrap
	if b.Status == "" || (b.When != "" && coder.SSH(name, b.When).Run() != nil) {
		return nil
	}
	fmt.Fprintln(os.Stderr, "初期化の完了を待っています（中断しても初期化はワークスペース側で続きます）")
	last := ""
	for range 180 {
		out, _ := coder.Output(name, fmt.Sprintf(`tail -n 1 %s 2>/dev/null | cut -d" " -f2- || true`, b.Status))
		st := strings.TrimSpace(out)
		if st != last {
			fmt.Fprintf(os.Stderr, "  %s %s\n", time.Now().Format("15:04:05"), st)
			last = st
		}
		switch {
		case strings.HasPrefix(st, "done:"):
			return nil
		case strings.HasPrefix(st, "failed:"):
			return fmt.Errorf("初期化に失敗しました。mujin status %s でログを確認してください", name)
		}
		time.Sleep(20 * time.Second)
	}
	return fmt.Errorf("60 分待っても初期化が完了しません。mujin status %s で状態を確認してください", name)
}

func forward(cfg Config, name string, ports []string) error {
	if len(ports) == 0 {
		ports = cfg.Forward.Ports
	}
	if len(ports) == 0 {
		return errors.New("転送するポートを -p か config の forward.ports で指定してください")
	}
	// macOS は 1024 番未満への bind を特定アドレス指定のときだけ root に限るため、既定はワイルドカードにする。
	bind := os.Getenv("MUJIN_BIND")
	if bind == "" {
		bind = "0.0.0.0"
	}
	args := []string{"port-forward", name}
	for _, p := range ports {
		args = append(args, "--tcp", fmt.Sprintf("%s:%s:%s", bind, p, p))
	}
	fmt.Fprintf(os.Stderr, "ポート転送を開始します（Ctrl-C で終了）。待ち受けアドレス: %s（MUJIN_BIND で変更可能）\n", bind)
	if cfg.Forward.Notes != "" {
		fmt.Fprintln(os.Stderr, strings.TrimRight(cfg.Forward.Notes, "\n"))
	}
	return coder.Run(args...)
}
