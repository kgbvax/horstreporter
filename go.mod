module horstreporter

go 1.24.0

require (
	dxlens v0.0.0-00010101000000-000000000000
	fyne.io/systray v1.11.0
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/jackc/pgx/v5 v5.7.6
	golang.org/x/crypto v0.42.0
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
)

require (
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/net v0.44.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/sys v0.36.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace dxlens => ../dxlens
