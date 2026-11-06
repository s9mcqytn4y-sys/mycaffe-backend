//go:build !windows

package main

// runAsService is a no-op outside Windows: systemd runs the agent as a normal process.
func runAsService(*agent) bool { return false }
