package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// HookResult captures the outcome of a pre/post deploy hook script.
type HookResult struct {
	ExitCode   int    `json:"exit_code"`
	OutputTail string `json:"output_tail"`
	DurationMs int64  `json:"duration_ms"`
	Err        error  `json:"-"`
}

func runHook(script, dir string, env map[string]string, timeout time.Duration) HookResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), mapToEnv(env)...)
	out, err := cmd.CombinedOutput()

	res := HookResult{
		OutputTail: tail(string(out), 4000),
		DurationMs: time.Since(start).Milliseconds(),
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.Err = fmt.Errorf("hook timed out after %s", timeout)
		return res
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
		res.Err = fmt.Errorf("hook exited %d: %s", res.ExitCode, res.OutputTail)
		return res
	}
	res.ExitCode = 0
	return res
}
