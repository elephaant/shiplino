package cline

import (
	"context"

	"golang.org/x/sys/windows"
)

// documentsDir is the Documents known folder, which the extension asks
// Windows for (it moves into OneDrive when folder backup is on).
func documentsDir(context.Context, string) string {
	p, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
	if err != nil {
		return ""
	}
	return p
}
