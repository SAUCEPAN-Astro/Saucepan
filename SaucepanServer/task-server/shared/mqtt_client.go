package shared

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTClientOptionsFromEnv builds paho client options from MQTT_* env vars.
//
//	MQTT_BROKER       — tcp:// | mqtt:// | ssl:// | mqtts:// (defaultBroker if empty)
//	MQTT_CLIENT_ID    — client id (defaultClientID if empty)
//	MQTT_USERNAME     — required unless DEV_MODE=1
//	MQTT_PASSWORD     — password for MQTT_USERNAME
//	MQTT_TLS_CA       — optional PEM CA path for server verify
//	MQTT_TLS_INSECURE — set to 1 to skip TLS verify (self-signed / local only)
func MQTTClientOptionsFromEnv(defaultBroker, defaultClientID string) (*mqtt.ClientOptions, error) {
	broker := strings.TrimSpace(os.Getenv("MQTT_BROKER"))
	if broker == "" {
		broker = defaultBroker
	}
	clientID := strings.TrimSpace(os.Getenv("MQTT_CLIENT_ID"))
	if clientID == "" {
		clientID = defaultClientID
	}

	user := strings.TrimSpace(os.Getenv("MQTT_USERNAME"))
	pass := os.Getenv("MQTT_PASSWORD")
	if user == "" && os.Getenv("DEV_MODE") != "1" {
		return nil, fmt.Errorf("MQTT_USERNAME is required when DEV_MODE is not 1")
	}

	opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID(clientID)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetAutoReconnect(true)
	if user != "" {
		opts.SetUsername(user)
		opts.SetPassword(pass)
	}

	needTLS := brokerUsesTLS(broker) ||
		os.Getenv("MQTT_TLS_CA") != "" ||
		os.Getenv("MQTT_TLS_INSECURE") == "1"
	if needTLS {
		tlsCfg, err := mqttTLSConfigFromEnv()
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsCfg)
	}

	return opts, nil
}

func brokerUsesTLS(broker string) bool {
	b := strings.ToLower(broker)
	return strings.HasPrefix(b, "ssl://") ||
		strings.HasPrefix(b, "mqtts://") ||
		strings.HasPrefix(b, "tls://")
}

func mqttTLSConfigFromEnv() (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if os.Getenv("MQTT_TLS_INSECURE") == "1" {
		cfg.InsecureSkipVerify = true
		return cfg, nil
	}
	caPath := strings.TrimSpace(os.Getenv("MQTT_TLS_CA"))
	if caPath == "" {
		// System roots (public CA or already-trusted).
		return cfg, nil
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("MQTT_TLS_CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("MQTT_TLS_CA: no certificates parsed from %s", caPath)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
