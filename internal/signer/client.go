package signer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/sirupsen/logrus"
)

// Client talks to sign-service. Workers hold this, never a private key.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func NewClient(cfg config.SignConfig) (*Client, error) {
	c := &Client{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		token:    cfg.Token,
		http:     &http.Client{Timeout: defaultClientTimeout},
	}
	if cfg.TLS.Enabled {
		tlsCfg, err := buildTLS(cfg)
		if err != nil {
			return nil, err
		}
		c.http.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}
	if c.endpoint == "" {
		return nil, fmt.Errorf("signer: sign.endpoint is required")
	}
	return c, nil
}

func buildTLS(cfg config.SignConfig) (*tls.Config, error) {
	dir, _ := os.Getwd()
	logrus.Println("当前工作目录:", dir)

	out := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.TLS.ServerName}
	if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("signer: load client cert: %w", err)
		}
		out.Certificates = []tls.Certificate{cert}
	}
	if cfg.TLS.CAFile != "" {
		pem, err := os.ReadFile(cfg.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("signer: read ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("signer: ca file contains no certificates")
		}
		out.RootCAs = pool
	}
	return out, nil
}

func (c *Client) Sign(ctx context.Context, req *SignRequest) (*SignResponse, error) {
	var out SignResponse
	if err := c.post(ctx, "/v1/sign", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type deriveRequest struct {
	Path string `json:"path"`
}

type deriveResponse struct {
	Address string `json:"address"`
	Path    string `json:"path"`
}

func (c *Client) DeriveAddress(ctx context.Context, path string) (string, error) {
	var out deriveResponse
	if err := c.post(ctx, "/v1/derive", deriveRequest{Path: path}, &out); err != nil {
		return "", err
	}
	return out.Address, nil
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("signer: http %d: %s", resp.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, out)
}
