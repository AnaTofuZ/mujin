package main

// workspace の作成・起動・転送。環境固有の値（テンプレート、bootstrap のパスなど）は config.yaml から読む。

import (
	"errors"
	"fmt"
	"os"
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
		Short: "workspace を作成し、bootstrap の完了まで待つ（value は config の create.param の値）",
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
			if err := waitAgent(name); err != nil {
				return err
			}
			if err := waitBootstrap(cfg, name); err != nil {
				return err
			}
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
	return []*cobra.Command{create, up, fw, status, ca}
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
