// Package domain determines whether a task needs a sandbox for a repository or a shell or file tool.
// Provisioning during Preparing returns at once and keeps its own events.
// A warm pool keeps one or two idle base containers and refills in the background.
// A paused task releases its container, and an idle one is released after the time limit.
// A task may reach only listed host names, and a name or address that means the host is refused.
// A task allow-list starts empty, and a person grants or revokes a host name.
// A hardening tier is gVisor or Kata, and a platform that cannot meet it is refused.
// A sandbox has a fixed resource shape, and a command longer than 15 minutes is refused.
package domain

import (
	"errors"
	"strings"
)

const (
	// ClassShell identifies a shell tool.
	ClassShell Class = "shell"
	// ClassFile identifies a file tool.
	ClassFile Class = "file"
	// ClassConnector identifies a connector tool.
	ClassConnector Class = "connector"
	// ClassKnowledge identifies a knowledge tool.
	ClassKnowledge Class = "knowledge"
	// ClassWeb identifies a web tool.
	ClassWeb Class = "web"
)

// Class identifies a tool class.
type Class string

// ErrClass reports an empty or unknown class.
var ErrClass = errors.New("runtime: class")

// NeedsSandbox reports whether a task needs a sandbox.
func NeedsSandbox(repo string, classes []Class) (bool, error) {
	needsSandbox := strings.TrimSpace(repo) != ""

	for _, class := range classes {
		switch Class(strings.TrimSpace(string(class))) {
		case ClassShell, ClassFile:
			needsSandbox = true
		case ClassConnector, ClassKnowledge, ClassWeb:
		default:
			return false, ErrClass
		}
	}

	return needsSandbox, nil
}
