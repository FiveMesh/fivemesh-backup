//go:build !windows

package main

func ownsConsoleAlone() bool { return false }

func waitForEnter() {}
