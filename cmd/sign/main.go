// Command sign runs sign-service: the only process that holds key material.
// It must run in its own network segment, reachable only by the workers, and in
// production it should read the mnemonic from a KMS/HSM/Vault backed secret.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/sirupsen/logrus"
)

func main() {
	pwd, _ := os.Getwd()
	fmt.Println("sign 当前工作目录:", pwd)
	app, err := bootstrap.Init("sign-service")
	if err != nil {
		panic(err)
	}
	policy := signer.PolicyFromConfig()
	svc, err := signer.New(bootstrap.Cfg.Sign, policy, signer.NewDBAudit(app.Store))
	if err != nil {
		logrus.Error("sign service init failed", "err", err)
		return
	}
	r := signer.NewHTTPServer(svc, bootstrap.Cfg.Sign.Token)
	logrus.Info("sign-service listening", "addr", bootstrap.Cfg.Sign.Listen, "mtls", bootstrap.Cfg.Sign.TLS.Enabled)
	if !bootstrap.Cfg.Sign.TLS.Enabled {
		if err := r.Run(bootstrap.Cfg.Sign.Listen); err != nil {
			logrus.Error("sign server stopped", "err", err)
		}
		return
	}

	// mTLS needs a client CA pool, which gin's RunTLS cannot express, so the
	// engine serves a TLS listener built here instead.
	tlsCfg, err := serverTLS(bootstrap.Cfg.Sign.TLS.CAFile, bootstrap.Cfg.Sign.TLS.CertFile, bootstrap.Cfg.Sign.TLS.KeyFile)
	if err != nil {
		logrus.Error("mTLS setup failed", "err", err)
		return
	}
	ln, err := net.Listen("tcp", bootstrap.Cfg.Sign.Listen)
	if err != nil {
		logrus.Error("sign listen failed", "err", err)
		return
	}
	if err := r.RunListener(tls.NewListener(ln, tlsCfg)); err != nil {
		logrus.Error("sign server stopped", "err", err)
	}
}

// serverTLS requires client certificates: only the workers may sign.
func serverTLS(caFile, certFile, keyFile string) (*tls.Config, error) {
	if caFile == "" {
		return nil, errors.New("sign.tls.ca_file is required for mTLS")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		Certificates: []tls.Certificate{cert},
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
