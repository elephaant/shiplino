// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package notify

import "testing"

func TestGVariantQuoting(t *testing.T) {
	if got := gvariantString("it's a \\ test\nline"); got != `'it\'s a \\ test\nline'` {
		t.Errorf("gvariant: %s", got)
	}
}
