package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/wire"
)

// campaignBoardPublisher is deliberately small so the HTTP handler can be
// tested without a broker. The API stores the researcher's opaque message in
// Postgres first, then publishes the same envelope to the campaign stream.
type campaignBoardPublisher interface {
	Publish(wire.BoardNote) error
}

type mqttCampaignBoardPublisher struct {
	client mqtt.Client
}

func (p *mqttCampaignBoardPublisher) Publish(note wire.BoardNote) error {
	raw, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("marshal campaign board note: %w", err)
	}
	topic := fmt.Sprintf(wire.TopicCampaignBoard, note.CampaignID, "researcher")
	token := p.client.Publish(topic, 1, true, raw)
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("publish campaign board note to %s: %w", topic, err)
	}
	return nil
}

func (p *mqttCampaignBoardPublisher) Close() {
	if p != nil && p.client.IsConnected() {
		p.client.Disconnect(250)
	}
}

// boardPublisher is nil when MQTT_BROKER is unset. That keeps a
// local database-only API useful, while every compose deployment explicitly
// enables the broker fan-out path.
var boardPublisher campaignBoardPublisher

// initCampaignBoardPublisher enables the researcher-to-pier half of the
// campaign board. It is called before the HTTP server starts, so a configured
// but unreachable broker fails startup instead of silently losing board posts.
func initCampaignBoardPublisher() (*mqttCampaignBoardPublisher, error) {
	broker := strings.TrimSpace(os.Getenv("MQTT_BROKER"))
	if broker == "" {
		return nil, nil
	}
	opts, err := shared.MQTTClientOptionsFromEnv(broker, "apiserver-board")
	if err != nil {
		return nil, err
	}
	client := mqtt.NewClient(opts)
	token := client.Connect()
	token.Wait()
	if token.Error() != nil {
		return nil, fmt.Errorf("connect campaign board MQTT: %w", token.Error())
	}
	return &mqttCampaignBoardPublisher{client: client}, nil
}
