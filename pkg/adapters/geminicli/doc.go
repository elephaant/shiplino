// Package geminicli is the Gemini CLI adapter: hook registration, hook
// payload parsing and chat recording (transcript) tailing.
//
// Facts checked on 2026-10-10 against the official hooks docs
// (docs/hooks/index.md and reference.md in google-gemini/gemini-cli,
// published at geminicli.com) and the source of release v0.63.0. Tested
// with Gemini CLI 0.63. Recorded fixtures live in testdata/<version>/.
package geminicli
