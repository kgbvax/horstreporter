package main

import "testing"

func TestValidateConfigUltrabeam(t *testing.T) {
	t.Run("rejects enabled with empty broker", func(t *testing.T) {
		err := validateConfig(map[string]string{"UB_ENABLED": "true", "UB_BROKER_URL": ""})
		if err == nil {
			t.Fatal("expected error when UB_ENABLED=true and UB_BROKER_URL empty")
		}
	})

	t.Run("rejects enabled with invalid broker scheme", func(t *testing.T) {
		err := validateConfig(map[string]string{"UB_ENABLED": "true", "UB_BROKER_URL": "http://localhost:1883"})
		if err == nil {
			t.Fatal("expected error for non-MQTT broker scheme")
		}
	})

	t.Run("accepts enabled with valid tcp broker", func(t *testing.T) {
		if err := validateConfig(map[string]string{"UB_ENABLED": "true", "UB_BROKER_URL": "tcp://127.0.0.1:1883"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("accepts tls broker", func(t *testing.T) {
		if err := validateConfig(map[string]string{"UB_ENABLED": "true", "UB_BROKER_URL": "tls://broker.example.org:8883"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("accepts disabled regardless of broker fields", func(t *testing.T) {
		if err := validateConfig(map[string]string{"UB_ENABLED": "false", "UB_BROKER_URL": ""}); err != nil {
			t.Fatalf("unexpected error when disabled: %v", err)
		}
		if err := validateConfig(map[string]string{"UB_ENABLED": "false", "UB_BROKER_URL": "garbage"}); err != nil {
			t.Fatalf("unexpected error when disabled with junk broker: %v", err)
		}
	})

	t.Run("rejects non-bool enabled", func(t *testing.T) {
		if err := validateConfig(map[string]string{"UB_ENABLED": "yes"}); err == nil {
			t.Fatal("expected error for non-bool UB_ENABLED")
		}
	})
}

func TestUltrabeamTopicPrefixDefault(t *testing.T) {
	// An empty prefix in config falls back to the ubctrl default in the client.
	uc := newUltrabeamClient(serviceConfig{UBBrokerURL: "tcp://127.0.0.1:1883", UBTopicPrefix: ""})
	if uc.prefix != defaultUltrabeamTopicPrefix {
		t.Fatalf("prefix = %q, want %q", uc.prefix, defaultUltrabeamTopicPrefix)
	}
}

func TestUltrabeamPasswordNotInConfigFields(t *testing.T) {
	// UB_PASSWORD is a Secret field; confirm it is marked Secret so it is never
	// echoed back in the Settings page payload (sourced from env only).
	for _, f := range configFields {
		if f.Key == "UB_PASSWORD" {
			if !f.Secret {
				t.Fatal("UB_PASSWORD must be marked Secret")
			}
			return
		}
	}
	t.Fatal("UB_PASSWORD field not found in configFields")
}
