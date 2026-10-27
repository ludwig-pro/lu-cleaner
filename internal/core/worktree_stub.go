//go:build !darwin || !cgo

package core

import "context"

func outputWorktreeRemoval(ctx context.Context, dir string, req WorktreeRemoval) ([]byte, error) {
	return (ExecRunner{}).Output(ctx, dir, "git", req.Args()...)
}

func runPrivateWorktreeHelper([]string) int { return 125 }
