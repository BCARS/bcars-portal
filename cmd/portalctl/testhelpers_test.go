package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
// bootstrapAdmin prints the invitation URL rather than returning it, and the
// database stores only the token hash, so the printed output is the only way
// a test can obtain the raw token — which is also the only way a real operator
// obtains it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)

	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	require.NoError(t, w.Close())
	return <-done
}
