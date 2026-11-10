// 本文件验证客户端证书过期后，既有 TLS 连接也不能绕过逐请求身份有效期检查。
//
// @author Andersen
package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestExpiredAgentOnExistingTLSConnection 使用真实连接跨越证书有效期，并确认请求被拒绝。
func TestExpiredAgentOnExistingTLSConnection(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "expiry-agent"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(3 * time.Second), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.CreateEnrollmentToken(ctx, "expiry-token", "expiry", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 注册回调把合成客户端证书序列号绑定到测试节点。
	if _, err := database.ConsumeEnrollmentToken(ctx, "expiry-token", time.Now(), func(name string) (store.Agent, error) {
		return store.Agent{ID: "expiry-agent", Name: name, CertificateSerial: "2a", CertificateExpiresAt: cert.NotAfter}, nil
	}); err != nil {
		t.Fatal(err)
	}
	service := New(&app.Services{Store: database}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// 受保护资源仅在逐请求认证通过后返回成功。
	server := httptest.NewUnstartedServer(service.requireAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })))
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	var handshakes atomic.Int32
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots,
		// 握手计数验证第二次请求复用了已建立连接。
		VerifyConnection: func(tls.ConnectionState) error { handshakes.Add(1); return nil },
	}
	server.StartTLS()
	defer server.Close()
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{pair}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("有效证书未通过")
	}
	time.Sleep(time.Until(cert.NotAfter.Add(100 * time.Millisecond)))
	response, err = client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 403 || handshakes.Load() != 1 {
		t.Fatalf("过期证书必须在同一连接被拒绝：status=%d handshakes=%d", response.StatusCode, handshakes.Load())
	}
}
