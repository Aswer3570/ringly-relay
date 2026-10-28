package calls

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/turn/v4"
)

// makeCred builds valid REST credentials for a secret: username = "<expiry>", password = HMAC.
func makeCred(secret string, expiry time.Time) (string, string) {
	username := fmt.Sprintf("%d", expiry.Unix())
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return username, password
}

func TestRestAuthValidCred(t *testing.T) {
	const secret = "s3cr3t"
	now := time.Unix(1_000_000, 0)
	handler := restAuthHandler(secret, "ringly", func() time.Time { return now })

	username, password := makeCred(secret, now.Add(time.Hour))
	key, ok := handler(username, "ringly", &net.UDPAddr{})
	if !ok {
		t.Fatal("valid credentials rejected")
	}
	// the key must match what a client builds from the same username/realm/password
	want := turn.GenerateAuthKey(username, "ringly", password)
	if string(key) != string(want) {
		t.Fatal("the authentication key does not match (the client would fail the integrity check)")
	}
}

func TestRestAuthExpired(t *testing.T) {
	const secret = "s3cr3t"
	now := time.Unix(2_000_000, 0)
	handler := restAuthHandler(secret, "ringly", func() time.Time { return now })

	username, _ := makeCred(secret, now.Add(-time.Minute)) // expired a minute ago
	if _, ok := handler(username, "ringly", &net.UDPAddr{}); ok {
		t.Fatal("expired credentials accepted")
	}
}

func TestRestAuthMalformed(t *testing.T) {
	handler := restAuthHandler("s", "ringly", time.Now)
	for _, u := range []string{"", "notanumber", "abc:123"} {
		if _, ok := handler(u, "ringly", &net.UDPAddr{}); ok {
			t.Fatalf("invalid username %q accepted", u)
		}
	}
}

func TestUsernameWithUID(t *testing.T) {
	now := time.Unix(1_000, 0)
	if !usernameValid("2000:user-42", now) {
		t.Fatal("a username like <expiry>:<uid> must be valid until it expires")
	}
	if usernameValid("500:user-42", now) {
		t.Fatal("an expired <expiry>:<uid> must be rejected")
	}
}
