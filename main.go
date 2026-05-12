package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"golang.org/x/crypto/acme/autocert"
)

//go:embed static
var staticFiles embed.FS

var logLevel = "INFO"

func init() {
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		logLevel = strings.ToUpper(lvl)
	}
}

func logDebug(format string, v ...interface{}) {
	if logLevel == "DEBUG" {
		log.Printf("[DEBUG] "+format, v...)
	}
}

func logInfo(format string, v ...interface{}) {
	if logLevel == "DEBUG" || logLevel == "INFO" {
		log.Printf("[INFO] "+format, v...)
	}
}

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

type Client struct {
	target string
	send   chan Spot
}

type Hub struct {
	sync.RWMutex
	clients map[*Client]bool
	history []MQTTMessage
}

var hub = &Hub{
	clients: make(map[*Client]bool),
	history: make([]MQTTMessage, 0),
}

func (h *Hub) broadcastMsg(m MQTTMessage) {
	h.Lock()
	defer h.Unlock()

	h.history = append(h.history, m)

	now := time.Now().Unix()
	for client := range h.clients {
		if spot, ok := matchAndCreateSpot(client, m, now); ok {
			select {
			case client.send <- spot:
			default:
				// Client buffer full, spot dropped for this client
			}
		}
	}
}

// matchCall checks for an exact callsign match, or a match with common prefix/suffix modifiers (e.g., W1AW/P, DL/W1AW)
func matchCall(spotCall, target string) bool {
	if spotCall == target {
		return true
	}
	if strings.HasPrefix(spotCall, target+"/") {
		return true
	}
	if strings.HasSuffix(spotCall, "/"+target) {
		return true
	}
	if strings.Contains(spotCall, "/"+target+"/") {
		return true
	}
	return false
}

func matchAndCreateSpot(client *Client, m MQTTMessage, now int64) (Spot, bool) {
	if client.target == "" {
		return Spot{}, false
	}

	sc, rc := strings.ToUpper(m.SC), strings.ToUpper(m.RC)
	sl, rl := strings.ToUpper(m.SL), strings.ToUpper(m.RL)

	isSender := matchCall(sc, client.target) || (sl != "" && strings.HasPrefix(sl, client.target))
	isReceiver := matchCall(rc, client.target) || (rl != "" && strings.HasPrefix(rl, client.target))

	if logLevel == "DEBUG" {
		logDebug("Target '%s' | Evaluating Spot -> SC:%s RC:%s SL:%s RL:%s | isSender:%v isReceiver:%v", client.target, sc, rc, sl, rl, isSender, isReceiver)
	}

	if !isSender && !isReceiver {
		return Spot{}, false
	}

	var remoteLocator string
	var relation string
	if isSender {
		remoteLocator = rl
		relation = "Sender"
	} else {
		remoteLocator = sl
		relation = "Receiver"
	}

	if remoteLocator == "" {
		if logLevel == "DEBUG" {
			logDebug("Target '%s' matched as %s, but remote locator is empty. Dropping spot.", client.target, relation)
		}
		return Spot{}, false
	}

	lat, lng := locatorToLatLng(remoteLocator)
	age := now - m.T
	if age < 0 {
		age = 0
	}

	if logLevel == "DEBUG" {
		logDebug("Target '%s' matched successfully! Mapped to Remote Locator: %s", client.target, remoteLocator)
	}

	return Spot{
		Lat:        lat,
		Lng:        lng,
		SNR:        m.RP,
		AgeSeconds: age,
		Locator:    remoteLocator,
		Band:       m.B,
	}, true
}

func startMQTT() {
	opts := mqtt.NewClientOptions().AddBroker("tcp://mqtt.pskreporter.info:1883")
	opts.SetClientID(fmt.Sprintf("horstreporter.kgbvax.net-%d", time.Now().UnixNano()))
	opts.SetAutoReconnect(true)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("MQTT connect error: %v", token.Error())
	}

	msgHandler := func(client mqtt.Client, msg mqtt.Message) {
		var m MQTTMessage
		if err := json.Unmarshal(msg.Payload(), &m); err != nil {
			logDebug("Failed to unmarshal payload: %s", string(msg.Payload()))
			return
		}

		// Extract missing fields from the topic string (PSKReporter omits them in JSON to save bandwidth)
		// Topic format: pskr/filter/v2/{band}/{mode}/{senderCall}/{receiverCall}/{senderLocator}/{receiverLocator}
		topicParts := strings.Split(msg.Topic(), "/")
		if len(topicParts) >= 9 {
			if m.B == "" && topicParts[3] != "unknown" {
				m.B = topicParts[3]
			}
			if m.MD == "" && topicParts[4] != "unknown" {
				m.MD = topicParts[4]
			}
			if m.SC == "" && topicParts[5] != "unknown" {
				m.SC = strings.ReplaceAll(topicParts[5], ".", "/") // Slashes in callsigns are replaced by dots in the topic
			}
			if m.RC == "" && topicParts[6] != "unknown" {
				m.RC = strings.ReplaceAll(topicParts[6], ".", "/")
			}
			if m.SL == "" && topicParts[7] != "unknown" {
				m.SL = topicParts[7]
			}
			if m.RL == "" && topicParts[8] != "unknown" {
				m.RL = topicParts[8]
			}
		}

		mode := strings.ToUpper(m.MD)
		if mode == "FT8" || mode == "FT4" {
			if m.T == 0 {
				m.T = time.Now().Unix()
			}
			hub.broadcastMsg(m)
		}
	}

	if token := client.Subscribe("pskr/filter/v2/#", 0, msgHandler); token.Wait() && token.Error() != nil {
		log.Fatalf("MQTT subscribe error: %v", token.Error())
	}
	logInfo("Subscribed to global PSKReporter MQTT feed")
}

func locatorToLatLng(locator string) (float64, float64) {
	locator = strings.ToUpper(locator)
	if len(locator) < 2 {
		return 0, 0
	}
	lng := float64(locator[0]-'A')*20 - 180
	lat := float64(locator[1]-'A')*10 - 90

	if len(locator) >= 4 {
		lng += float64(locator[2]-'0') * 2
		lat += float64(locator[3]-'0') * 1
		if len(locator) >= 6 {
			lng += float64(locator[4]-'A')*(5.0/60.0) + (5.0 / 120.0)
			lat += float64(locator[5]-'A')*(2.5/60.0) + (2.5 / 120.0)
		} else {
			lng += 1.0
			lat += 0.5
		}
	} else {
		lng += 10.0 // Center of field
		lat += 5.0
	}
	return lat, lng
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	target := strings.ToUpper(r.URL.Query().Get("target"))
	minutesStr := r.URL.Query().Get("minutes")

	// Fallback for cached frontend clients that still send callsign/locator params
	if target == "" {
		if call := r.URL.Query().Get("callsign"); call != "" {
			target = strings.ToUpper(call)
		} else if loc := r.URL.Query().Get("locator"); loc != "" {
			target = strings.ToUpper(loc)
		}
	}

	if target == "" {
		http.Error(w, "target required", http.StatusBadRequest)
		return
	}

	minutes, err := strconv.Atoi(minutesStr)
	if err != nil || minutes <= 0 {
		minutes = 15
	}
	if minutes > 60 {
		minutes = 60
	}
	historySeconds := int64(minutes * 60)

	client := &Client{
		target: target,
		send:   make(chan Spot, 10000), // Buffer to handle initial history dump
	}

	hub.Lock()
	hub.clients[client] = true
	logInfo("New client stream started for target: %s (History: %d mins)", target, minutes)

	now := time.Now().Unix()
	var historySpots []Spot
	for _, m := range hub.history {
		// Dump spots from the requested history length
		if now-m.T <= historySeconds {
			if spot, ok := matchAndCreateSpot(client, m, now); ok {
				historySpots = append(historySpots, spot)
			}
		}
	}
	hub.Unlock()

	defer func() {
		hub.Lock()
		if _, ok := hub.clients[client]; ok {
			delete(hub.clients, client)
			close(client.send)
		}
		hub.Unlock()
		logInfo("Client stream closed for target: %s", target)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	for _, spot := range historySpots {
		b, _ := json.Marshal(spot)
		fmt.Fprintf(w, "data: %s\n\n", string(b))
	}
	fmt.Fprintf(w, "event: history_end\ndata: {}\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case spot, ok := <-client.send:
			if !ok {
				return
			}
			b, _ := json.Marshal(spot)
			fmt.Fprintf(w, "data: %s\n\n", string(b))
			flusher.Flush()
		}
	}
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	hub.RLock()
	numClients := len(hub.clients)
	historySize := len(hub.history)
	var historyMinutes int
	if historySize > 0 {
		oldest := hub.history[0].T
		now := time.Now().Unix()
		historyMinutes = int((now - oldest) / 60)
	}
	hub.RUnlock()

	stats := struct {
		ActiveConnections int `json:"active_connections"`
		HistorySize       int `json:"history_size"`
		HistoryMinutes    int `json:"history_minutes"`
	}{
		ActiveConnections: numClients,
		HistorySize:       historySize,
		HistoryMinutes:    historyMinutes,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// noCache is a middleware that sets headers to prevent caching of static files.
// This is useful for development to ensure the latest files are always served.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate") // HTTP 1.1.
		w.Header().Set("Pragma", "no-cache")                                   // HTTP 1.0.
		w.Header().Set("Expires", "0")                                         // Proxies.
		h.ServeHTTP(w, r)
	})
}

func main() {
	port := flag.String("port", "8080", "HTTP/HTTPS server port")
	certFile := flag.String("cert", "", "Path to TLS certificate file")
	keyFile := flag.String("key", "", "Path to TLS key file")
	domain := flag.String("domain", "", "Domain for Let's Encrypt (enables automatic TLS)")
	dev := flag.Bool("dev", false, "Enable development mode (disables caching of static files)")
	flag.Parse()

	go startMQTT()

	go func() {
		for range time.Tick(1 * time.Minute) {
			hub.Lock()
			cutoff := time.Now().Unix() - 3600 // Prune history older than 60 minutes
			var newHistory []MQTTMessage
			for _, m := range hub.history {
				if m.T >= cutoff {
					newHistory = append(newHistory, m)
				}
			}
			hub.history = newHistory
			hub.Unlock()
		}
	}()

	var fileServer http.Handler
	if *dev {
		logInfo("Development mode enabled: Serving static files directly from the disk.")
		fileServer = http.FileServer(http.Dir("static"))
		http.Handle("/", noCache(fileServer))
	} else {
		staticFS, err := fs.Sub(staticFiles, "static")
		if err != nil {
			log.Fatal("Failed to load embedded static files:", err)
		}
		fileServer = http.FileServer(http.FS(staticFS))
		http.Handle("/", fileServer)
	}
	http.HandleFunc("/api/stream", streamHandler)
	http.HandleFunc("/api/stats", statsHandler)

	if *domain != "" {
		logInfo("HorstReporter starting HTTPS server with Let's Encrypt for domain %s on port %s...", *domain, *port)
		m := &autocert.Manager{
			Cache:      autocert.DirCache("certs"), // Stores certificates in a local "certs" folder
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*domain),
		}
		server := &http.Server{
			Addr:      ":" + *port,
			TLSConfig: m.TLSConfig(),
		}
		log.Fatal(server.ListenAndServeTLS("", ""))
	} else if *certFile != "" && *keyFile != "" {
		logInfo("HorstReporter starting HTTPS server with provided certs on port %s...", *port)
		log.Fatal(http.ListenAndServeTLS(":"+*port, *certFile, *keyFile, nil))
	} else {
		logInfo("HorstReporter starting HTTP server on port %s...", *port)
		log.Fatal(http.ListenAndServe(":"+*port, nil))
	}
}
