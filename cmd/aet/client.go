package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// signedPost issues a POST request signed with the AETHERNET-REQUEST-V1 envelope.
func signedPost(nodeURL, path string, body any, agentID string, pk ed25519.PrivateKey, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	headers := signRequest("POST", path, data, agentID, pk)

	req, err := http.NewRequest("POST", nodeURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return apiError(resp.StatusCode, respBody)
	}
	if out != nil {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

// apiGet issues a GET request and decodes the JSON response into out.
func apiGet(nodeURL, path string, out any) error {
	resp, err := http.Get(nodeURL + path)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return apiError(resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// apiPost issues a POST request with a JSON body (unsigned, uses API key fallback).
func apiPost(nodeURL, path string, reqBody any, out any) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequest("POST", nodeURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "aethernet-testnet-arena-key-v1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return apiError(resp.StatusCode, body)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func apiError(code int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Error != "" {
		return fmt.Errorf("api error %d: %s", code, e.Error)
	}
	return fmt.Errorf("api error %d: %s", code, strings.TrimSpace(string(body)))
}
