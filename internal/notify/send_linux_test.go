package notify

import "testing"

func TestGVariantQuoting(t *testing.T) {
	if got := gvariantString("it's a \\ test\nline"); got != `'it\'s a \\ test\nline'` {
		t.Errorf("gvariant: %s", got)
	}
}
