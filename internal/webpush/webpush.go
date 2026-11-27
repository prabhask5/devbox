// Package webpush sends Web Push notifications: the payload is encrypted for one browser
// (RFC 8291, aes128gcm content coding of RFC 8188) and the request is signed with the
// application server's VAPID key (RFC 8292). Only the standard library's cryptography is used.
package webpush

import (
	"bytes"
	"context"
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
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var b64 = base64.RawURLEncoding

// Subscription is where and how to reach one browser, as its PushManager reports it.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"` // the browser's public key, base64url
		Auth   string `json:"auth"`   // the shared authentication secret, base64url
	} `json:"keys"`
}

// Keys is the application server's VAPID key pair, base64url: a P-256 private scalar and the
// uncompressed public point that browsers subscribe with.
type Keys struct {
	Public, Private string
}

// GenerateKeys creates a new VAPID key pair.
func GenerateKeys() (Keys, error) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, err
	}
	return Keys{Public: b64.EncodeToString(key.PublicKey().Bytes()), Private: b64.EncodeToString(key.Bytes())}, nil
}

// ErrGone reports a subscription the push service no longer knows: it must be forgotten.
var ErrGone = errors.New("push subscription is gone")

const (
	recordSize = 4096
	// MaxPayload is the longest message that fits one record of the size push services accept.
	MaxPayload = recordSize - 16 - 1 - 86
)

// Encrypt encrypts message for the browser behind a subscription (RFC 8291 section 3). The
// ephemeral key and the salt must be fresh for every message; [Send] makes them.
func Encrypt(message []byte, browserKey, authSecret []byte, ephemeral *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(message) > MaxPayload {
		return nil, fmt.Errorf("push message of %d bytes is over the limit of %d", len(message), MaxPayload)
	}
	if len(salt) != 16 || len(authSecret) != 16 {
		return nil, errors.New("push: salt and authentication secret must be 16 bytes")
	}
	browser, err := ecdh.P256().NewPublicKey(browserKey)
	if err != nil {
		return nil, fmt.Errorf("push: the subscription's public key: %w", err)
	}
	shared, err := ephemeral.ECDH(browser)
	if err != nil {
		return nil, err
	}
	server := ephemeral.PublicKey().Bytes()

	// The key that only this browser and this message's ephemeral key can derive.
	info := append(append([]byte("WebPush: info\x00"), browserKey...), server...)
	keying, err := hkdf.Key(sha256.New, shared, authSecret, string(info), 32)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, keying, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, keying, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// One record: salt, record size, the server's key as key id, then the sealed message, which
	// ends in the delimiter of a last record.
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(recordSize))
	out.WriteByte(byte(len(server)))
	out.Write(server)
	out.Write(gcm.Seal(nil, nonce, append(append([]byte{}, message...), 0x02), nil))
	return out.Bytes(), nil
}

// Authorization returns the VAPID Authorization header for a request to endpoint: a token
// signed with the application server's key, valid for twelve hours, naming who to contact.
func Authorization(keys Keys, endpoint, contact string, now time.Time) (string, error) {
	target, err := url.Parse(endpoint)
	if err != nil || target.Host == "" {
		return "", fmt.Errorf("push: endpoint %q is not a URL", endpoint)
	}
	raw, err := b64.DecodeString(keys.Private)
	if err != nil {
		return "", errors.New("push: the VAPID private key is not base64url")
	}
	private, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return "", errors.New("push: the VAPID private key is not a P-256 key")
	}
	claims, _ := json.Marshal(map[string]any{ //nolint:errchkjson // strings and a number
		"aud": target.Scheme + "://" + target.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": contact,
	})
	unsigned := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, private, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return "vapid t=" + unsigned + "." + b64.EncodeToString(signature) + ", k=" + keys.Public, nil
}

// Message is one notification to deliver.
type Message struct {
	Payload []byte
	TTL     time.Duration // how long the push service keeps it for a device that is offline
	Urgent  bool          // deliver at once even to a device saving power
}

// Sender delivers messages.
type Sender struct {
	Keys    Keys
	Contact string // "mailto:…" or an https URL the push service may contact about this server
	Client  *http.Client
}

// Send delivers one message to one subscription. [ErrGone] means the subscription is dead.
func (s *Sender) Send(ctx context.Context, subscription Subscription, message Message, now time.Time) error {
	browserKey, err := b64.DecodeString(subscription.Keys.P256dh)
	if err != nil {
		return errors.New("push: the subscription's public key is not base64url")
	}
	auth, err := b64.DecodeString(subscription.Keys.Auth)
	if err != nil {
		return errors.New("push: the subscription's authentication secret is not base64url")
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	body, err := Encrypt(message.Payload, browserKey, auth, ephemeral, salt)
	if err != nil {
		return err
	}
	authorization, err := Authorization(s.Keys, subscription.Endpoint, s.Contact, now)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, subscription.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(message.TTL.Seconds())))
	req.Header.Set("Urgency", map[bool]string{true: "high", false: "normal"}[message.Urgent])
	req.Header.Set("Authorization", authorization)
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do about it
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("push: the push service answered %s", resp.Status)
	}
	return nil
}
