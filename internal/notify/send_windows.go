package notify

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
)

// Windows: a toast through the WinRT notification API, run by PowerShell
// (built in) under PowerShell's registered app id, so no installer
// registration is needed.

const powershellAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

func osAvailable() (bool, string) {
	if _, err := exec.LookPath("powershell.exe"); err == nil {
		return true, "Windows notifications"
	}
	return false, ""
}

func osSend(ctx context.Context, n Note) error {
	xml := `<toast><visual><binding template="ToastGeneric"><text>` + xmlEscape(n.Title) + `</text><text>` +
		xmlEscape(n.Body) + `</text></binding></visual></toast>`
	script := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml('` + strings.ReplaceAll(xml, "'", "''") + `')
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + powershellAppID + `').Show([Windows.UI.Notifications.ToastNotification]::new($x))`
	// -EncodedCommand (UTF-16LE base64) avoids every quoting problem.
	enc := utf16.Encode([]rune(script))
	b := make([]byte, len(enc)*2)
	for i, c := range enc {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// hideWindow keeps a console window from flashing up.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
