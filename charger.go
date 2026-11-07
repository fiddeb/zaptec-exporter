package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func (c *apiClient) installationID(ctx context.Context, chargerID string) (string, error) {
	var charger struct {
		InstallationID string `json:"installationId"`
	}
	if err := c.get(ctx, "/api/chargers/"+url.PathEscape(chargerID), &charger); err != nil {
		return "", err
	}
	if !isUUID(charger.InstallationID) {
		return "", errors.New("charger details did not include a valid installation UUID")
	}
	return charger.InstallationID, nil
}

func isUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}

// Resolve the configured UUID or hardware serial once, before any source starts.
// Compare deviceId exactly: a display name or partial match can select another charger.
func (c *apiClient) resolveChargerID(ctx context.Context, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("charger_id must be a UUID or charger serial number")
	}
	if isUUID(value) {
		return strings.ToLower(value), nil
	}
	var matchedID string
	for page := 0; page < 1000; page++ {
		query := url.Values{"PageIndex": {strconv.Itoa(page)}, "PageSize": {"100"}}
		var response struct {
			Pages *int `json:"pages"`
			Data  []struct {
				ID       string `json:"id"`
				DeviceID string `json:"deviceId"`
			} `json:"data"`
		}
		if err := c.get(ctx, "/api/chargers?"+query.Encode(), &response); err != nil {
			return "", fmt.Errorf("resolve charger serial via charger list: %w", err)
		}
		if response.Pages == nil || *response.Pages < 0 || (*response.Pages == 0 && len(response.Data) != 0) {
			return "", errors.New("charger list returned invalid pagination")
		}
		for _, charger := range response.Data {
			if !strings.EqualFold(charger.DeviceID, value) {
				continue
			}
			if !isUUID(charger.ID) {
				return "", errors.New("charger list returned an invalid UUID for the matching serial")
			}
			id := strings.ToLower(charger.ID)
			if matchedID != "" && matchedID != id {
				return "", fmt.Errorf("multiple chargers have serial %q; configure the charger UUID instead", value)
			}
			matchedID = id
		}
		if page+1 >= *response.Pages {
			if matchedID == "" {
				return "", fmt.Errorf("no accessible charger has serial %q; check account permissions or copy the charger UUID from Zaptec Portal", value)
			}
			return matchedID, nil
		}
	}
	return "", errors.New("charger lookup exceeded 1000 pages; configure the charger UUID instead")
}
