// Package report builds a read-only usage report straight from the agents'
// own transcript files: no daemon, hooks, database or Shiplino home. It
// uses the same adapters, pricing, redaction and engine as the daemon,
// keeps everything in memory and writes nothing to disk.
package report
