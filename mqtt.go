package main

import (
	"encoding/json"
	"fmt"
	"log"
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

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		logInfo("Connected to MQTT broker. Subscribing...")
		if token := c.Subscribe("pskr/filter/v2/#", 0, msgHandler); token.Wait() && token.Error() != nil {
			log.Printf("MQTT subscribe error: %v", token.Error())
		} else {
			logInfo("Subscribed to global PSKReporter MQTT feed")
		}
	})

	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		logInfo("MQTT connection lost: %v", err)
	})

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("MQTT connect error: %v", token.Error())
	}
}
