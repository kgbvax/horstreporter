#!/bin/bash
#
# Build + deploy the horstoperator-agent (tray build) to your Windows shack PC
# over SSH, in ONE command. On first run it bootstraps the box (creates the
# install folder, a starter .env, and the "at logon" scheduled task that launches
# the tray app); on every run it ships a fresh exe and restarts the task.
#
# The operator agent is LOCAL: the browser opmode UI talks to it at
# 127.0.0.1:9955 and it bridges to PSTrotator over UDP, so it must run ON the
# shack machine you operate from. The scheduled task is just the autostart that
# launches the tray app in your interactive desktop session at logon.
#
# One-time prerequisite on the Windows box: enable OpenSSH Server
#   (PowerShell as Admin):
#     Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
#     Start-Service sshd; Set-Service sshd -StartupType Automatic
#
# Usage:
#   HORSTOP_HOST=shack.lan ./deploy_operator_agent_win.sh
#   ./deploy_operator_agent_win.sh <host> [user]
#
# Overridable settings (env vars):
#   HORSTOP_HOST    target hostname/IP                    (required)
#   HORSTOP_USER    Windows SSH user                      (default: current $USER)
#   HORSTOP_DIR     install dir on the box (Windows path) (default: C:\horstoperator)
#   HORSTOP_TASK    scheduled task name                   (default: HorstOperatorAgent)
#   HORSTOP_EXE     exe filename in HORSTOP_DIR           (default: horstoperator-agent.exe)
#   HORSTOP_LISTEN  agent listen address                  (default: 127.0.0.1:9955)
#
# There is nothing to configure on disk: after the first deploy, open the tray
# (once logged in) -> Settings and set your locator / PSTrotator host / Wavelog
# key interactively. The agent persists that for you.

set -euo pipefail

# Run from repo root so the build script + module resolve regardless of CWD.
cd "$(dirname "$0")"

HOST="${HORSTOP_HOST:-${1:-}}"
USER="${HORSTOP_USER:-${2:-$USER}}"
DIR="${HORSTOP_DIR:-C:\\horstoperator}"
TASK="${HORSTOP_TASK:-HorstOperatorAgent}"
EXE="${HORSTOP_EXE:-horstoperator-agent.exe}"
LISTEN="${HORSTOP_LISTEN:-127.0.0.1:9955}"

if [ -z "$HOST" ]; then
  echo "No target host. Set HORSTOP_HOST or pass it as the first argument." >&2
  echo "Usage: HORSTOP_HOST=shack.lan $0   (or: $0 <host> [user])" >&2
  exit 1
fi

TARGET="${USER}@${HOST}"
LOCAL_EXE="dist/horstoperator-agent-windows-amd64.exe"
# scp wants forward slashes; schtasks/taskkill want the native backslash path.
DEST_FWD="$(printf '%s' "$DIR" | tr '\\' '/')/$EXE"

echo "==> Building tray (windowsgui) exe..."
GUI=1 scripts/build-operator-agent-win64.sh >/dev/null
echo "    $LOCAL_EXE"

# --- Bootstrap the box (idempotent): folder + scheduled task -----------------
# Built as a PowerShell script and shipped via -EncodedCommand (base64 of
# UTF-16LE) so there's zero quoting trouble over the cmd-default SSH shell.
# No .env is seeded: all configuration is done interactively in the tray
# Settings page, which the agent persists itself.
echo "==> Ensuring install dir and scheduled task exist on ${HOST}..."
PSBODY=$(cat <<'PS'
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$exe = Join-Path $InstallDir 'horstoperator-agent.exe'
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
  Write-Host "  scheduled task '$TaskName' already exists"
} else {
  $a = New-ScheduledTaskAction -Execute $exe -Argument "-tray -listen $Listen -task-name $TaskName" -WorkingDirectory $InstallDir
  $t = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
  $p = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
  $s = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
  Register-ScheduledTask -TaskName $TaskName -Action $a -Trigger $t -Principal $p -Settings $s -Force | Out-Null
  Write-Host "  created scheduled task '$TaskName' (At logon, -tray)"
}
PS
)
# Prepend the bash-supplied values as PowerShell variables, then encode.
PSFULL="\$InstallDir='${DIR}'; \$TaskName='${TASK}'; \$Listen='${LISTEN}';
${PSBODY}"
ENC=$(printf '%s' "$PSFULL" | iconv -f UTF-8 -t UTF-16LE | base64 | tr -d '\n')
ssh "$TARGET" "powershell -NoProfile -ExecutionPolicy Bypass -EncodedCommand $ENC"

# --- Stop any running instance so the exe is unlocked ------------------------
echo "==> Stopping '$TASK' on ${TARGET}..."
ssh "$TARGET" "schtasks /End /TN \"$TASK\" & taskkill /IM \"$EXE\" /F & exit 0" || true
sleep 1

# --- Ship the exe ------------------------------------------------------------
echo "==> Uploading exe to ${HOST}:${DEST_FWD}..."
scp "$LOCAL_EXE" "${TARGET}:${DEST_FWD}"

# --- Start it ----------------------------------------------------------------
echo "==> Starting '$TASK'..."
ssh "$TARGET" "schtasks /Run /TN \"$TASK\"" || {
  echo "    NOTE: could not start the task now (are you logged in to the desktop?)."
  echo "    The tray app will start automatically at your next logon."
}

echo "Deployment complete. Look for the tray icon on ${HOST} (green = PSTrotator OK)."
echo "First time? Open the tray -> Settings to configure locator / PSTrotator host / Wavelog — all interactive, nothing to edit on disk."
