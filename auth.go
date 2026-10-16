package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type credential struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Server   string `json:"server,omitempty"`
	OrgID    string `json:"org_id,omitempty"`
	Label    string `json:"label,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	ProxyURL string `json:"proxy_url,omitempty"`
}

func decodeCredential(raw []byte, metadata map[string]any) (credential, error) {
	var c credential
	if len(raw) == 0 && metadata != nil {
		raw, _ = json.Marshal(metadata)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fail(400, "invalid OpenCode service-key auth file")
	}
	if c.Type != providerID {
		return c, fail(400, "credential type must be opencode")
	}
	if c.Key == "" || strings.TrimSpace(c.Key) != c.Key || strings.ContainsAny(c.Key, "\r\n\t ") {
		return c, fail(401, "a nonempty service-account key is required")
	}
	if c.Server == "" {
		c.Server = "https://opencode.ai/console"
	}
	c.Server = strings.TrimRight(c.Server, "/")
	u, err := url.Parse(c.Server)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return c, fail(400, "invalid Console server URL")
	}
	if strings.ContainsAny(c.OrgID, "\r\n") {
		return c, fail(400, "invalid organization ID")
	}
	if c.Disabled {
		return c, fail(403, "credential is disabled")
	}
	if err := allowedURL(loadedConfig(), c.Server+"/api/v2/config"); err != nil {
		return c, err
	}
	return c, nil
}
func (c credential) fingerprint() string {
	sum := sha256.Sum256([]byte(c.Key + "\x00" + c.Server + "\x00" + c.OrgID + "\x00" + c.ProxyURL))
	return hex.EncodeToString(sum[:])
}
func authData(c credential, id, fileName string) pluginapi.AuthData {
	raw, _ := json.Marshal(c)
	var metadata map[string]any
	_ = json.Unmarshal(raw, &metadata)
	if id == "" {
		id = providerID + "-" + c.fingerprint()[:16]
	}
	if fileName == "" {
		fileName = id + ".json"
	}
	return pluginapi.AuthData{Provider: providerID, ID: id, FileName: fileName, Label: c.Label, ProxyURL: c.ProxyURL, StorageJSON: raw, Metadata: metadata, Attributes: map[string]string{"auth_kind": "api_key"}, NextRefreshAfter: now().Add(time.Minute)}
}
func parseAuth(raw []byte) (any, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fail(400, "invalid auth parse request")
	}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(req.RawJSON, &header) != nil {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	if header.Type != providerID {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	// Disabled files are recognized without making network calls.
	var c credential
	_ = json.Unmarshal(req.RawJSON, &c)
	disabled := c.Disabled
	c.Disabled = false
	checkRaw, _ := json.Marshal(c)
	c, err := decodeCredential(checkRaw, nil)
	if err != nil {
		return nil, err
	}
	c.Disabled = disabled
	data := authData(c, "", filepath.Base(req.FileName))
	if req.FileName == "" {
		data.FileName = data.ID + ".json"
	}
	data.Disabled = disabled
	return pluginapi.AuthParseResponse{Handled: true, Auth: data}, nil
}
func reloadAuth(raw []byte) (any, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fail(400, "invalid reload request")
	}
	c, err := decodeCredential(req.StorageJSON, req.Metadata)
	if err != nil {
		return nil, err
	}
	rememberHostProxy(c, req.Host.ProxyURL)
	ctx, cancel := discoveryContext()
	defer cancel()
	_, err = discover(ctx, c, req.Host.ProxyURL, true)
	if err != nil {
		return nil, err
	}
	data := authData(c, req.AuthID, "")
	return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: now().Add(time.Minute)}, nil
}
func modelsForAuth(raw []byte) (any, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fail(400, "invalid model request")
	}
	c, err := decodeCredential(req.StorageJSON, req.Metadata)
	if err != nil {
		return nil, err
	}
	rememberHostProxy(c, req.Host.ProxyURL)
	ctx, cancel := discoveryContext()
	defer cancel()
	routes, err := discover(ctx, c, req.Host.ProxyURL, false)
	if err != nil {
		return nil, err
	}
	models := modelInfos(routes)
	log.Printf("OpenCode discovery: stage=models_for_auth returned=%d", len(models))
	return pluginapi.ModelResponse{Provider: providerID, Models: models}, nil
}
