//go:build !servicebus

package main

import (
	"context"
	"errors"
)

func runServiceBus(_ context.Context, _ *apiClient, _, _ string, _ *stateStore) error {
	return errors.New("Service Bus build requires -tags servicebus and the Azure SDK dependency")
}
