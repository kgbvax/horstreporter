package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type MQTTMessage struct {
	RP int    `json:"rp"`
	T  int64  `json:"t"`
	SC string `json:"sc"`
	SL string `json:"sl"`
	RC string `json:"rc"`
	RL string `json:"rl"`
	B  string `json:"b"`
	MD string `json:"md"`

	// Set programmatically for DX-cluster spots only (never from the MQTT
	// payload — hence json:"-"); carried in hub.history for /api/dxspots.
	F          float64 `json:"-"` // spot frequency in kHz
	CM         string  `json:"-"` // spot comment
	OpName     string  `json:"-"` // DX operator name (QRZ)
	Country    string  `json:"-"` // DX country / DXCC entity (cty.dat, else QRZ)
	CountryISO string  `json:"-"` // ISO-3166 alpha-2 for the flag (cty.dat)

	// Source identifies which ingest produced this spot: "mqtt" (PSKReporter),
	// "dxcluster", or "rbn". Set by each ingest and by the startup backfill (from
	// dx_raw_spots.source_type) so sourceTypeForMessage can tag the stream without
	// abusing MD (RBN's real modes CW/RTTY are not source markers). json:"-" —
	// surfaces via toStreamSpot.sourceType, never the raw MQTT payload.
	Source string `json:"-"`

	// TXPower is the reported transmitter power in milliwatts, populated only
	// by the WSPR ingest path (wspr.go handleWSPRSpot). Zero for all other
	// sources. Used by the WSPR propagation-intelligence nowcast to compute
	// SSB/CW viability from a SNR+Power budget model. json:"-" — never in the
	// raw MQTT payload; carried in hub.history for the nowcast.
	TXPower int `json:"-"`
}

func startMQTT() {
	opts := mqtt.NewClientOptions().AddBroker("tcp://mqtt.pskreporter.info:1883")
	opts.SetClientID(fmt.Sprintf("horstreporter.kgbvax.net-%d", time.Now().UnixNano()))
	opts.SetAutoReconnect(true)

	msgHandler := func(client mqtt.Client, msg mqtt.Message) {
		var m MQTTMessage
		if err := json.Unmarshal(msg.Payload(), &m); err != nil {
			logDebug("Failed to unmarshal payload: %s", string(msg.Payload()))
			return
		}

		if m.B == "" || m.MD == "" || m.SC == "" || m.RC == "" || m.SL == "" || m.RL == "" {
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
		}

		m.B = strings.TrimSpace(m.B)
		m.MD = strings.ToUpper(strings.TrimSpace(m.MD))
		m.SC = strings.ToUpper(strings.TrimSpace(m.SC))
		m.RC = strings.ToUpper(strings.TrimSpace(m.RC))
		m.SL = strings.ToUpper(strings.TrimSpace(m.SL))
		m.RL = strings.ToUpper(strings.TrimSpace(m.RL))

		mode := m.MD
		if mode == "FT8" || mode == "FT4" {
			if m.T == 0 {
				m.T = time.Now().Unix()
			}

			if dxBaseline != nil {
				dxBaseline.Observe(m)
			}
			hub.broadcastMsg(m)
		}
	}

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		logInfo("Connected to MQTT broker. Subscribing...")
		if token := c.Subscribe("pskr/filter/v2/#", 0, msgHandler); token.Wait() && token.Error() != nil {
			logError("MQTT subscribe error: %v", token.Error())
		} else {
			logInfo("Subscribed to global PSKReporter MQTT feed")
		}
	})

	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		logInfo("MQTT connection lost: %v", err)
	})

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		logFatal("MQTT connect error: %v", token.Error())
	}
}
