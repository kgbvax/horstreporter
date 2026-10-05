// Command vapidkeys prints a fresh Web Push VAPID key pair as env lines
// (PUSH_VAPID_PRIVATE_KEY / PUSH_VAPID_PUBLIC_KEY) in the base64url format
// the backend's webpush-go expects. Run via scripts/generate-vapid-keys.sh.
package main

import (
	"fmt"
	"os"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func main() {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate VAPID keys:", err)
		os.Exit(1)
	}
	fmt.Printf("PUSH_VAPID_PRIVATE_KEY=%s\nPUSH_VAPID_PUBLIC_KEY=%s\n", priv, pub)
}
