// Package sync is the opt-in cloud sync client. It is open source so
// users can audit exactly what is sent; the wire protocol is documented
// in docs/sync-protocol.md.
//
// Nothing leaves the machine unless sync is enabled, the device is
// signed in (device flow, tokens in the OS keychain) and a project is
// allowed. Every event is stripped to metadata (redact.ForSync: never
// prompts, replies, commands, tool output or diffs) and redacted again
// right before it is sent.
package sync
