package ui

import (
	"os"
	"testing"

	"ramus/internal/askpass"
)

// The test binary is the askpass helper when git or ssh starts it as one.
func TestMain(m *testing.M) {
	if askpass.IsHelper() {
		os.Exit(askpass.Helper(os.Args[1:], os.Stdout))
	}
	os.Exit(m.Run())
}
