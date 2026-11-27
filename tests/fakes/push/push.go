// Package pushfake is a push service and a browser in one, for tests: it creates subscriptions
// the way a browser's PushManager does, accepts Web Push requests for them, verifies the VAPID
// signature the way a push service does, and decrypts the message the way the browser does
// (RFC 8291, 8188, 8292). A subscription can be made to answer 410 Gone.
package pushfake

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/prabhask5/devbox/internal/webpush"
)

var b64 = base64.RawURLEncoding

// Delivery is one message the service accepted and the browser decrypted.
type Delivery struct {
	Device  string // the name the subscription was created with
	Payload []byte // the decrypted message
	TTL     string // the TTL header
	Urgency string // the Urgency header
	Subject string // the "sub" claim of the VAPID token
}

type device struct {
	name    string
	private *ecdh.PrivateKey
	auth    []byte
	gone    bool
}

// Service is a fake push service.
type Service struct {
	server *httptest.Server

	mu         sync.Mutex
	devices    map[string]*device // by path
	deliveries []Delivery
	rejected   []string // why requests were refused
}

// Start runs the service on a loopback port.
func Start() *Service {
	s := &Service{devices: map[string]*device{}}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Close stops the service.
func (s *Service) Close() { s.server.Close() }

// Subscribe creates a subscription as a browser would for the application server key.
func (s *Service) Subscribe(name string) webpush.Subscription {
	private, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	path := "/push/" + b64.EncodeToString(id)
	s.mu.Lock()
	s.devices[path] = &device{name: name, private: private, auth: auth}
	s.mu.Unlock()
	var subscription webpush.Subscription
	subscription.Endpoint = s.server.URL + path
	subscription.Keys.P256dh = b64.EncodeToString(private.PublicKey().Bytes())
	subscription.Keys.Auth = b64.EncodeToString(auth)
	return subscription
}

// Expire makes the named device's subscription answer 410 Gone from now on.
func (s *Service) Expire(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.name == name {
			d.gone = true
		}
	}
}

// Deliveries returns the messages delivered so far.
func (s *Service) Deliveries() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delivery(nil), s.deliveries...)
}

// Rejected returns why requests were refused so far.
func (s *Service) Rejected() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.rejected...)
}

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	d := s.devices[r.URL.Path]
	s.mu.Unlock()
	refuse := func(code int, why string) {
		s.mu.Lock()
		s.rejected = append(s.rejected, why)
		s.mu.Unlock()
		http.Error(w, why, code)
	}
	switch {
	case d == nil:
		refuse(http.StatusNotFound, "no such subscription")
		return
	case d.gone:
		w.WriteHeader(http.StatusGone)
		return
	case r.Method != http.MethodPost:
		refuse(http.StatusMethodNotAllowed, "not a POST")
		return
	case r.Header.Get("Content-Encoding") != "aes128gcm":
		refuse(http.StatusBadRequest, "Content-Encoding is not aes128gcm")
		return
	case r.Header.Get("TTL") == "":
		refuse(http.StatusBadRequest, "no TTL")
		return
	}
	subject, err := verifyVAPID(r.Header.Get("Authorization"), s.server.URL)
	if err != nil {
		refuse(http.StatusForbidden, "VAPID: "+err.Error())
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
	if len(body) > 4096 {
		refuse(http.StatusRequestEntityTooLarge, "message over 4096 bytes")
		return
	}
	payload, err := decrypt(body, d.private, d.auth)
	if err != nil {
		refuse(http.StatusBadRequest, "decrypt: "+err.Error())
		return
	}
	s.mu.Lock()
	s.deliveries = append(s.deliveries, Delivery{d.name, payload, r.Header.Get("TTL"), r.Header.Get("Urgency"), subject})
	s.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

// verifyVAPID checks an RFC 8292 Authorization header: an ES256 token signed by the key in the
// header, for this service, not expired and not valid for more than a day. It returns the
// token's subject.
func verifyVAPID(header, audience string) (string, error) {
	fields := map[string]string{}
	rest, ok := strings.CutPrefix(header, "vapid ")
	if !ok {
		return "", errors.New("not a vapid Authorization header")
	}
	for _, part := range strings.Split(rest, ",") {
		key, value, _ := strings.Cut(strings.TrimSpace(part), "=")
		fields[key] = value
	}
	public, err := b64.DecodeString(fields["k"])
	if err != nil || len(public) != 65 || public[0] != 4 {
		return "", errors.New("k is not an uncompressed P-256 point")
	}
	parts := strings.Split(fields["t"], ".")
	if len(parts) != 3 {
		return "", errors.New("t is not a JWT")
	}
	var head struct{ Typ, Alg string }
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	rawHead, _ := b64.DecodeString(parts[0])
	rawClaims, _ := b64.DecodeString(parts[1])
	signature, _ := b64.DecodeString(parts[2])
	if json.Unmarshal(rawHead, &head) != nil || head.Alg != "ES256" || head.Typ != "JWT" {
		return "", fmt.Errorf("header %s", rawHead)
	}
	if json.Unmarshal(rawClaims, &claims) != nil {
		return "", errors.New("claims are not JSON")
	}
	now := time.Now()
	switch {
	case claims.Aud != audience:
		return "", fmt.Errorf("aud %q, want %q", claims.Aud, audience)
	case claims.Exp <= now.Unix() || claims.Exp > now.Add(24*time.Hour).Unix():
		return "", fmt.Errorf("exp %d is not within the next 24 hours", claims.Exp)
	case !strings.HasPrefix(claims.Sub, "mailto:") && !strings.HasPrefix(claims.Sub, "https://"):
		return "", fmt.Errorf("sub %q is neither a mailto: nor an https: URL", claims.Sub)
	case len(signature) != 64:
		return "", errors.New("signature is not 64 bytes")
	}
	key := ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(public[1:33]), Y: new(big.Int).SetBytes(public[33:])}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return "", errors.New("signature does not verify")
	}
	return claims.Sub, nil
}

// decrypt opens an aes128gcm message with the browser's private key and authentication secret.
func decrypt(body []byte, private *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("shorter than a header")
	}
	salt, recordSize, keyLen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if len(body) < 21+keyLen || keyLen != 65 {
		return nil, errors.New("no sender key in the header")
	}
	senderKey, sealed := body[21:21+keyLen], body[21+keyLen:]
	if uint32(len(sealed)) > recordSize {
		return nil, errors.New("more than one record")
	}
	sender, err := ecdh.P256().NewPublicKey(senderKey)
	if err != nil {
		return nil, err
	}
	shared, err := private.ECDH(sender)
	if err != nil {
		return nil, err
	}
	info := append(append([]byte("WebPush: info\x00"), private.PublicKey().Bytes()...), senderKey...)
	keying, _ := hkdf.Key(sha256.New, shared, auth, string(info), 32)
	key, _ := hkdf.Key(sha256.New, keying, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, keying, salt, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, err
	}
	// The record ends in its delimiter, 0x02 for the last one, then padding of zeros.
	end := len(plain) - 1
	for end >= 0 && plain[end] == 0 {
		end--
	}
	if end < 0 || plain[end] != 0x02 {
		return nil, errors.New("no last-record delimiter")
	}
	return plain[:end], nil
}
