package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/AnaTofuZ/mujin/internal/coder"
	"github.com/spf13/cobra"
)

func cpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cp <src> <dst>",
		Short: "workspace 間、または手元と workspace の間でファイルを転送する（ws:path の形で指定）",
		Long: `司令塔を経由して tar をストリームで流す。一時ファイルは作らない。
dst は常にディレクトリとして扱い、src をその中に置く。上書きは cp と同じく黙って行う。

  mujin cp ws-a:~/dump.sql ws-b:~/tmp/
  mujin cp ws-a:~/out/ ./out/
  mujin cp ./seed.json ws-b:~/`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			sws, src := splitEndpoint(args[0])
			dws, dst := splitEndpoint(args[1])
			if sws == "" && dws == "" {
				return errors.New("どちらかを ws:path の形で指定してください")
			}
			return pipe(side(sws, sendScript(src)), side(dws, recvScript(dst)))
		},
	}
}

// ~ はクオートすると展開されないので、実行する側のシェルで $HOME に置き換える。
func sendScript(src string) string {
	return fmt.Sprintf(`src=%s; src="${src/#\~/$HOME}"; cd "$(dirname "$src")" && tar -cf - "$(basename "$src")"`, coder.Quote(src))
}

func recvScript(dst string) string {
	return fmt.Sprintf(`dst=%s; dst="${dst/#\~/$HOME}"; mkdir -p "$dst" && tar -C "$dst" -xf -`, coder.Quote(dst))
}

// splitEndpoint は "ws:path" を分ける。":" より前に "/" があればローカルのパスとみなす。
func splitEndpoint(s string) (ws, path string) {
	i := strings.Index(s, ":")
	if i <= 0 || strings.Contains(s[:i], "/") {
		return "", s
	}
	return s[:i], s[i+1:]
}

func side(ws, script string) *exec.Cmd {
	if ws == "" {
		return exec.Command("bash", "-c", script)
	}
	return coder.SSH(ws, script)
}

// pipe は send の出力を recv に流す。送る側が途中で失敗しても受ける側は成功を返しうるので、両方の終了コードを見る。
func pipe(send, recv *exec.Cmd) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	send.Stdout, send.Stderr = w, os.Stderr
	recv.Stdin, recv.Stdout, recv.Stderr = r, os.Stdout, os.Stderr
	if err := send.Start(); err != nil {
		return err
	}
	if err := recv.Start(); err != nil {
		_ = send.Process.Kill()
		_ = send.Wait()
		return err
	}
	w.Close()
	r.Close()
	errS, errR := send.Wait(), recv.Wait()
	if errS != nil || errR != nil {
		return fmt.Errorf("転送に失敗しました（送信: %v, 受信: %v）", errS, errR)
	}
	return nil
}
