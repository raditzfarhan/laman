//go:build !windows

package fsx

func transient(error) bool { return false }
