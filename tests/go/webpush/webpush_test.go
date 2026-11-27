package webpush_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/webpush"
	pushfake "github.com/prabhask5/devbox/tests/fakes/push"
)

var b64 = base64.RawURLEncoding

func decode(t *testing.T, text string) []byte {
	t.Helper()
	data, err := b64.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The example of RFC 8291, appendix A.
func TestEncryptMatchesTheRFC8291Example(t *testing.T) {
	const (
		plaintext       = "When I grow up, I want to be a watermelon"
		serverPrivate   = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
		serverPublic    = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
		browserPublic   = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
		authSecret      = "BTBZMqHH6r4Tts7J_aSIgg"
		salt            = "DGv6ra1nlYgDCS1FRnbzlw"
		expectedMessage = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	)
	ephemeral, err := ecdh.P256().NewPrivateKey(decode(t, serverPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(ephemeral.PublicKey().Bytes()); got != serverPublic {
		t.Fatalf("the example's server public key: %s", got)
	}
	message, err := webpush.Encrypt([]byte(plaintext), decode(t, browserPublic), decode(t, authSecret), ephemeral, decode(t, salt))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(message); got != expectedMessage {
		t.Errorf("encrypted message:\n got %s\nwant %s", got, expectedMessage)
	}
}

func sender(t *testing.T) (*webpush.Sender, *pushfake.Service) {
	t.Helper()
	keys, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	service := pushfake.Start()
	t.Cleanup(service.Close)
	return &webpush.Sender{Keys: keys, Contact: "https://agents.example.com", Client: http.DefaultClient}, service
}

func TestSendReachesTheBrowserThroughThePushService(t *testing.T) {
	s, service := sender(t)
	phone, laptop := service.Subscribe("phone"), service.Subscribe("laptop")
	payload := `{"v":1,"title":"🔐 claude-devbox — Fix the flaky sync test","body":"Bash: cargo test","url":"/#/s/a1b2c3d4","tag":"s-a1b2c3d4","badge":1}`
	now := time.Now()
	if err := s.Send(context.Background(), phone, webpush.Message{Payload: []byte(payload), TTL: time.Hour, Urgent: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), laptop, webpush.Message{Payload: []byte("plain"), TTL: time.Minute}, now); err != nil {
		t.Fatal(err)
	}
	got := service.Deliveries()
	if len(got) != 2 || len(service.Rejected()) != 0 {
		t.Fatalf("deliveries %+v, rejected %v", got, service.Rejected())
	}
	if d := got[0]; d.Device != "phone" || string(d.Payload) != payload || d.TTL != "3600" || d.Urgency != "high" || d.Subject != "https://agents.example.com" {
		t.Errorf("phone: %+v", d)
	}
	if d := got[1]; d.Device != "laptop" || string(d.Payload) != "plain" || d.TTL != "60" || d.Urgency != "normal" {
		t.Errorf("laptop: %+v", d)
	}

	// The longest message that fits, and one byte more.
	if err := s.Send(context.Background(), phone, webpush.Message{Payload: bytes.Repeat([]byte("x"), webpush.MaxPayload), TTL: time.Hour}, now); err != nil {
		t.Errorf("a message of MaxPayload bytes: %v (rejected: %v)", err, service.Rejected())
	}
	if err := s.Send(context.Background(), phone, webpush.Message{Payload: bytes.Repeat([]byte("x"), webpush.MaxPayload+1), TTL: time.Hour}, now); err == nil {
		t.Error("a message over MaxPayload was sent")
	}
}

func TestSendReportsDeadSubscriptionsAndRefusals(t *testing.T) {
	s, service := sender(t)
	phone := service.Subscribe("phone")
	service.Expire("phone")
	message := webpush.Message{Payload: []byte("hi"), TTL: time.Hour}
	if err := s.Send(context.Background(), phone, message, time.Now()); !errors.Is(err, webpush.ErrGone) {
		t.Errorf("an expired subscription: %v", err)
	}
	unknown := phone
	unknown.Endpoint += "-nope"
	if err := s.Send(context.Background(), unknown, message, time.Now()); !errors.Is(err, webpush.ErrGone) {
		t.Errorf("an unknown subscription (404): %v", err)
	}

	// A token signed by another key, an expired token, a bad subscription key.
	laptop := service.Subscribe("laptop")
	other, _ := webpush.GenerateKeys()
	forged := &webpush.Sender{Keys: webpush.Keys{Public: s.Keys.Public, Private: other.Private}, Contact: s.Contact, Client: http.DefaultClient}
	if err := forged.Send(context.Background(), laptop, message, time.Now()); err == nil || errors.Is(err, webpush.ErrGone) {
		t.Errorf("a forged VAPID token: %v", err)
	}
	if err := s.Send(context.Background(), laptop, message, time.Now().Add(-13*time.Hour)); err == nil {
		t.Error("an expired VAPID token was accepted")
	}
	if got := service.Rejected(); len(got) != 3 || got[0] != "no such subscription" || !strings.Contains(got[1], "signature does not verify") || !strings.Contains(got[2], "exp") {
		t.Errorf("rejected: %v", got)
	}
	broken := laptop
	broken.Keys.P256dh = "not a key"
	if err := s.Send(context.Background(), broken, message, time.Now()); err == nil {
		t.Error("a subscription with a broken key was sent to")
	}
	if len(service.Deliveries()) != 0 {
		t.Errorf("deliveries: %+v", service.Deliveries())
	}
}

func TestAuthorizationNamesTheEndpointsOrigin(t *testing.T) {
	keys, _ := webpush.GenerateKeys()
	now := time.Unix(1760000000, 0)
	header, err := webpush.Authorization(keys, "https://push.example.net:8443/send/abc?x=1", "mailto:you@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	token, key, _ := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	parts := strings.Split(token, ".")
	if !strings.HasPrefix(header, "vapid t=") || key != keys.Public || len(parts) != 3 {
		t.Fatalf("header: %s", header)
	}
	if got := string(decode(t, parts[0])); got != `{"typ":"JWT","alg":"ES256"}` {
		t.Errorf("JWT header: %s", got)
	}
	if got := string(decode(t, parts[1])); got != `{"aud":"https://push.example.net:8443","exp":1760043200,"sub":"mailto:you@example.com"}` {
		t.Errorf("claims: %s", got)
	}
	if len(decode(t, parts[2])) != 64 {
		t.Errorf("signature of %d bytes", len(decode(t, parts[2])))
	}
	for _, endpoint := range []string{"", "not a url", "/relative"} {
		if _, err := webpush.Authorization(keys, endpoint, "mailto:you@example.com", now); err == nil {
			t.Errorf("endpoint %q accepted", endpoint)
		}
	}
	if _, err := webpush.Authorization(webpush.Keys{Public: keys.Public, Private: "AAAA"}, "https://push.example.net/x", "mailto:you@example.com", now); err == nil {
		t.Error("a private key that is no key was accepted")
	}
	// Generated keys have the shapes browsers and push services expect.
	if len(decode(t, keys.Public)) != 65 || decode(t, keys.Public)[0] != 4 || len(decode(t, keys.Private)) != 32 {
		t.Errorf("generated keys: %d and %d bytes", len(decode(t, keys.Public)), len(decode(t, keys.Private)))
	}
}
