package calls

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pion/turn/v4"
)

// restAuthHandler returns an AuthHandler for the coturn-compatible REST scheme of temporary credentials.
//
// The client gets from the control server (or from a link): username = "<expiry-unix>[:uid]",
// password = base64(HMAC-SHA1(secret, username)). The server computes the password from the username
// with the same secret and builds the long-term authentication key — it matches only if the client knew
// the same secret. An expired username is rejected.
func restAuthHandler(secret, realm string, now func() time.Time) turn.AuthHandler {
	return func(username, _ string, _ net.Addr) ([]byte, bool) {
		if !usernameValid(username, now()) {
			return nil, false
		}
		mac := hmac.New(sha1.New, []byte(secret))
		mac.Write([]byte(username))
		password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		return turn.GenerateAuthKey(username, realm, password), true
	}
}

// usernameValid checks the expiry of a temporary username (the first field before ':' is the unix expiry
// time). An empty or malformed expiry counts as invalid.
func usernameValid(username string, now time.Time) bool {
	if username == "" {
		return false
	}
	head := username
	if i := strings.IndexByte(username, ':'); i >= 0 {
		head = username[:i]
	}
	expiry, err := strconv.ParseInt(head, 10, 64)
	if err != nil {
		return false
	}
	return now.Unix() <= expiry
}
