package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Spot struct {
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	SNR        int     `json:"snr"`
	AgeSeconds int64   `json:"ageSeconds"`
	Locator    string  `json:"locator"`
	Band       string  `json:"band"`
}

type MQTTMessage struct {
	RP int    `json:"rp"`
	T  int64  `json:"t"`
	SC string `json:"sc"`
	SL string `json:"sl"`
	RC string `json:"rc"`
	RL string `json:"rl"`
	B  string `json:"b"`
	MD string `json:"md"`
}

func locatorToLatLng(locator string) (float64, float64) {
	locator = strings.ToUpper(locator)
	if len(locator) < 4 {
		return 0, 0
	}
	lng := float64(locator[0]-'A')*20 - 180
	lat := float64(locator[1]-'A')*10 - 90
	lng += float64(locator[2]-'0') * 2
	lat += float64(locator[3]-'0') * 1
	if len(locator) >= 6 {
		lng += float64(locator[4]-'A')*(5.0/60.0) + (5.0 / 120.0)
		lat += float64(locator[5]-'A')*(2.5/60.0) + (2.5 / 120.0)
	} else {
		lng += 1.0
		lat += 0.5
	}
	return lat, lng
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	callsign := strings.ToUpper(r.URL.Query().Get("callsign"))
	locator := strings.ToUpper(r.URL.Query().Get("locator"))

	if callsign == "" && locator == "" {
		http.Error(w, "callsign or locator required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	opts := mqtt.NewClientOptions().AddBroker("tcp://mqtt.pskreporter.info:1883")
	opts.SetClientID(fmt.Sprintf("horstreporter-%d", time.Now().UnixNano()))
	opts.SetAutoReconnect(true)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Printf("MQTT connect error: %v", token.Error())
		return
	}
	defer client.Disconnect(250)

	topics := make(map[string]byte)
	if callsign != "" {
		safeCall := strings.ReplaceAll(callsign, "/", ".")
		topics[fmt.Sprintf("pskr/filter/v2/+/+/%s/#", safeCall)] = 0
		topics[fmt.Sprintf("pskr/filter/v2/+/+/+/%s/#", safeCall)] = 0
	} else if locator != "" {
		loc := locator
		if len(loc) > 4 {
			loc = loc[:4]
		}
		if len(loc) == 4 {
			topics[fmt.Sprintf("pskr/filter/v2/+/+/+/+/%s/#", loc)] = 0
			topics[fmt.Sprintf("pskr/filter/v2/+/+/+/+/+/%s/#", loc)] = 0
		} else {
			topics["pskr/filter/v2/#"] = 0
		}
	} else {
		topics["pskr/filter/v2/#"] = 0
	}

	spotChan := make(chan Spot, 100)

	msgHandler := func(client mqtt.Client, msg mqtt.Message) {
		var m MQTTMessage
		if err := json.Unmarshal(msg.Payload(), &m); err != nil {
			return
		}

		mode := strings.ToUpper(m.MD)
		if mode != "FT8" && mode != "FT4" {
			return
		}

		isSender := (callsign != "" && strings.ToUpper(m.SC) == callsign) || (locator != "" && strings.HasPrefix(strings.ToUpper(m.SL), locator))
		isReceiver := (callsign != "" && strings.ToUpper(m.RC) == callsign) || (locator != "" && strings.HasPrefix(strings.ToUpper(m.RL), locator))

		if !isSender && !isReceiver {
			return
		}

		var remoteLocator string
		if isSender {
			remoteLocator = m.RL
		} else {
			remoteLocator = m.SL
		}

		if remoteLocator == "" {
			return
		}

		lat, lng := locatorToLatLng(remoteLocator)
		age := time.Now().Unix() - m.T
		if age < 0 || m.T == 0 {
			age = 0
		}

		//log.Printf("Matched spot: %s -> %s | Band: %s, SNR: %ddB, Mode: %s", m.SC, m.RC, m.B, m.RP, m.MD)
		spotChan <- Spot{Lat: lat, Lng: lng, SNR: m.RP, AgeSeconds: age, Locator: strings.ToUpper(remoteLocator), Band: m.B}
	}

	var topicList []string
	for t := range topics {
		topicList = append(topicList, t)
	}
	log.Printf("Connecting to MQTT live stream. Subscribed to paths: %s", strings.Join(topicList, ", "))

	if token := client.SubscribeMultiple(topics, msgHandler); token.Wait() && token.Error() != nil {
		log.Printf("MQTT subscribe error: %v", token.Error())
		return
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case spot := <-spotChan:
			b, _ := json.Marshal(spot)
			fmt.Fprintf(w, "data: %s\n\n", string(b))
			flusher.Flush()
		}
	}
}

func main() {
	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/api/stream", streamHandler)
	fmt.Println("HorstReporter streaming API starting on http://localhost:8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
