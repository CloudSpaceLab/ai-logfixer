//go:build windows

package agentfix

import "os/exec"

func prepareAgentCommand(cmd *exec.Cmd) {
}

func killAgentCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
