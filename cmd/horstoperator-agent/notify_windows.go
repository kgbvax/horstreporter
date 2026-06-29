//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// notify raises a native Windows toast. It shells out to PowerShell using the
// Windows.UI.Notifications runtime (present on Win10/11, no extra modules), so
// it stays pure-Go / cgo-free for cross-compilation. Title and body are passed
// via the environment to avoid any quoting/injection in the inline script.
//
// Notifications are rare (only on readiness transitions), so the per-call
// PowerShell cost is irrelevant. Failures are swallowed: a missing toast must
// never disturb the agent.
func notify(title, body string) {
	go func() {
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", toastScript)
		cmd.Env = append(os.Environ(),
			"HORSTOP_TOAST_TITLE="+title,
			"HORSTOP_TOAST_BODY="+body,
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		_ = cmd.Run()
	}()
}

const createNoWindow = 0x08000000

const toastScript = `
$ErrorActionPreference = 'SilentlyContinue'
try {
  $AppId = 'HorstOperator.Agent'
  [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
  [Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType = WindowsRuntime]        | Out-Null
  [Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime]          | Out-Null
  $tmpl = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
  $texts = $tmpl.GetElementsByTagName('text')
  $texts.Item(0).AppendChild($tmpl.CreateTextNode($env:HORSTOP_TOAST_TITLE)) | Out-Null
  $texts.Item(1).AppendChild($tmpl.CreateTextNode($env:HORSTOP_TOAST_BODY))  | Out-Null
  $toast = [Windows.UI.Notifications.ToastNotification]::new($tmpl)
  [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($AppId).Show($toast)
} catch { }
`
