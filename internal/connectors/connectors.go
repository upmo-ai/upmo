// Package connectors derives tools from a declared spec operation.
// A sandbox receives the catalog without credential references.
// A tool carries a credential reference, and the verb chooses read, change, or delete.
// A paused connector call resumes only by its id.
package connectors
