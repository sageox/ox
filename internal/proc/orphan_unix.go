//go:build darwin || linux || freebsd

package proc

import "os/exec"

// orphanScript runs the command in a background subshell and exits at once, so
// the subshell (and the command, its child) are re-parented to init or the
// nearest subreaper. The subshell stays behind only to record the exit status;
// writing it under a temporary name and renaming makes the read atomic.
const orphanScript = `status=$1; shift
( "$@"; echo $? > "$status.tmp" && mv "$status.tmp" "$status" ) &`

func startOrphaned(cmd *exec.Cmd, statusPath string) error {
	args := []string{"-c", orphanScript, "sh", statusPath, cmd.Path}
	// exec.Cmd allows empty Args (it then runs Path alone)
	if len(cmd.Args) > 1 {
		args = append(args, cmd.Args[1:]...)
	}
	sh := exec.Command("/bin/sh", args...)
	sh.Dir, sh.Env = cmd.Dir, cmd.Env
	sh.Stdin, sh.Stdout, sh.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	// its own session as well: a terminal hangup or a process-group kill
	// must not reach the command either
	Detach(sh)
	// returns once the shell has forked the subshell and exited
	return sh.Run()
}
