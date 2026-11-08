//go:build !windows

package main

// listDisks is only implemented for WinPE.
func listDisks() []disk { return nil }
