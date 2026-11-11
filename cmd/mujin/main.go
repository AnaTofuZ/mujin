// mujin は Coder workspace と、その中の herdr を束ねて Claude にジョブを振り分ける CLI。
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/AnaTofuZ/mujin/internal/skill"
	"github.com/spf13/cobra"
)

func main() {
	var printSkill bool
	root := &cobra.Command{
		Use:           "mujin",
		Short:         "Coder workspace の艦隊運用 CLI。引数なしで司令塔の Claude を起動する",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printSkill {
				fmt.Print(skill.Markdown)
				return nil
			}
			c := exec.Command("claude", "--append-system-prompt", skill.Markdown, "--allowedTools", "Bash(mujin:*)")
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
	}
	root.Flags().BoolVar(&printSkill, "skill", false, "司令塔用の SKILL.md を出力する")
	root.AddCommand(wsCommands()...)
	root.AddCommand(jobCommands()...)
	root.AddCommand(cpCommand())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
