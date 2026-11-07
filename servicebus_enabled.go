//go:build servicebus

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"log"
	"strings"
	"time"
)

type connectionDetails struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	UseSSL       bool   `json:"useSSL"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Topic        string `json:"topic"`
	Subscription string `json:"subscription"`
}

func runServiceBus(ctx context.Context, api *apiClient, userGroup, chargerID string, store *stateStore) error {
	for {
		if err := consume(ctx, api, userGroup, chargerID, store); err != nil {
			log.Printf("servicebus reconnect: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Second):
		}
	}
}
func consume(ctx context.Context, api *apiClient, userGroup, chargerID string, store *stateStore) error {
	var details connectionDetails
	if err := api.get(ctx, "/api/userGroups/"+userGroup+"/messagingConnectionDetails", &details); err != nil {
		return err
	}
	if !details.UseSSL {
		return fmt.Errorf("refusing non-TLS Service Bus")
	}
	if details.Host == "" || details.Topic == "" || details.Subscription == "" {
		return fmt.Errorf("incomplete Service Bus connection details")
	}
	// Azure SDK authentication uses SAS key name + key from Zaptec-provided connection details.
	conn := fmt.Sprintf("Endpoint=sb://%s/;SharedAccessKeyName=%s;SharedAccessKey=%s", strings.TrimSuffix(details.Host, "/"), details.Username, details.Password)
	client, err := azservicebus.NewClientFromConnectionString(conn, nil)
	if err != nil {
		return err
	}
	defer client.Close(ctx)
	receiver, err := client.NewReceiverForSubscription(details.Topic, details.Subscription, nil)
	if err != nil {
		return err
	}
	defer receiver.Close(ctx)
	for {
		msgs, err := receiver.ReceiveMessages(ctx, 10, nil)
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			raw := msg.Body
			// Zaptec sends a JSON string containing JSON, rather than always a JSON object.
			var nested string
			if json.Unmarshal(raw, &nested) == nil {
				raw = []byte(nested)
			}
			var obs observation
			if err := json.Unmarshal(raw, &obs); err != nil {
				log.Printf("invalid event: %v", err)
			} else if strings.EqualFold(obs.ChargerID, chargerID) {
				store.apply([]observation{obs})
			}
			if err := receiver.CompleteMessage(ctx, msg, nil); err != nil {
				return err
			}
		}
	}
}
