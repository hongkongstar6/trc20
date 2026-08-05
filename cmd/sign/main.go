// Command sign runs sign-service: the only process that holds key material.
// It must run in its own network segment, reachable only by the workers, and in
// production it should read the mnemonic from a KMS/HSM/Vault backed secret.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/signer"
)

func main() {
	app, err := bootstrap.Init("sign-service")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	policy := signer.PolicyFromConfig(app.Cfg)
	svc, err := signer.New(app.Cfg.Sign, policy, signer.NewDBAudit(app.Store, app.Log))
	if err != nil {
		app.Log.Error("sign service init failed", "err", err)
		return
	}
	srv := &http.Server{
		Addr:              app.Cfg.Sign.Listen,
		Handler:           signer.NewHTTPServer(svc, app.Cfg.Sign.Token, app.Log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if app.Cfg.Sign.TLS.Enabled {
		tlsCfg, err := serverTLS(app.Cfg.Sign.TLS.CAFile)
		if err != nil {
			app.Log.Error("mTLS setup failed", "err", err)
			return
		}
		srv.TLSConfig = tlsCfg
	}
	go func() {
		app.Log.Info("sign-service listening", "addr", srv.Addr, "mtls", app.Cfg.Sign.TLS.Enabled)
		var err error
		if app.Cfg.Sign.TLS.Enabled {
			err = srv.ListenAndServeTLS(app.Cfg.Sign.TLS.CertFile, app.Cfg.Sign.TLS.KeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.Log.Error("sign server stopped", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	if err := srv.Close(); err != nil {
		app.Log.Error("sign server close failed", "err", err)
	}
}

// serverTLS requires client certificates: only the workers may sign.
func serverTLS(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert}
	if caFile == "" {
		return nil, errors.New("sign.tls.ca_file is required for mTLS")
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("sign.tls.ca_file contains no certificates")
	}
	cfg.ClientCAs = pool
	return cfg, nil
}
