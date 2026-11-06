//go:build windows

package proxy

import (
	"errors"
	"testing"
)

func TestWithTrustedSudoCallsFnDirectlyOnWindows(t *testing.T) {
	want := errors.New("from fn")
	ran := false
	err := withTrustedSudo(func() error { ran = true; return want })
	if !ran || err != want {
		t.Fatalf("ran %v, err %v", ran, err)
	}
}
