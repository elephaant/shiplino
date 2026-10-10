// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package opencodeplugin holds the OpenCode plugin (shiplino.js) that
// `shiplino setup` installs; see pkg/adapters/opencode.
package opencodeplugin

import _ "embed"

// Source is shiplino.js with the binary path still a placeholder.
//
//go:embed shiplino.js
var Source []byte

// BinPlaceholder is the string literal content Install replaces with the
// absolute path of the shiplino binary.
const BinPlaceholder = "__SHIPLINO_BIN__"

// Marker is in the header of every copy Shiplino writes, so Install
// and Uninstall only ever touch their own file.
const Marker = "shiplino-opencode-plugin"
