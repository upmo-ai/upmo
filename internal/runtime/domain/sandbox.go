// Package domain determines whether a task needs a sandbox for a repository or a shell or file tool.
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
