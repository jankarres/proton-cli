package meet

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"testing"

	pgp "github.com/ProtonMail/gopenpgp/v3/crypto"
	pgphelper "github.com/roman-16/proton-cli/internal/crypto/pgp"
)

// webPersonalRoom is a meeting the Meet web app made: the dashboard's personal
// room of the free test account, read back from the API on 2026-10-25 together
// with the password in its link.
//
// Opening it here is the proof that what this package writes is what the web
// app reads, because the vector was written by the web app and not by this
// package. The room itself is deleted by the end of the Meet work, so the
// password committed beside it opens nothing any more.
type webPersonalRoom struct {
	MeetingLinkName string
	Password        string
	Salt            string
	SessionKey      string
	MeetingName     string
	Title           string
}

func TestAWebMadeMeetingOpens(t *testing.T) {
	raw, err := os.ReadFile("testdata/web_personal_room.json")
	if err != nil {
		t.Fatal(err)
	}
	var room webPersonalRoom
	if err := json.Unmarshal(raw, &room); err != nil {
		t.Fatal(err)
	}
	salt, err := base64.StdEncoding.DecodeString(room.Salt)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := base64.StdEncoding.DecodeString(room.SessionKey)
	if err != nil {
		t.Fatal(err)
	}

	sk, err := unlockSessionKey(locked, room.Password, salt)
	if err != nil {
		t.Fatalf("unlock the web app's session key: %v", err)
	}
	title, err := openTitle(sk, room.MeetingName)
	if err != nil {
		t.Fatalf("open the web app's title: %v", err)
	}
	if title != room.Title {
		t.Errorf("title %q, want %q", title, room.Title)
	}

	// And a wrong password is refused rather than read as some other key.
	if _, err := unlockSessionKey(locked, "AAAAAAAAAAAA", salt); err == nil {
		t.Error("a wrong password unlocked the session key")
	}
}

func TestASecretRoundTrips(t *testing.T) {
	s, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Salt) != 16 {
		t.Errorf("salt of %d bytes, want 16", len(s.Salt))
	}
	if s.SessionKey.Algo != "aes256" || len(s.SessionKey.Key) != 32 {
		t.Errorf("session key %s of %d bytes, want aes256 of 32", s.SessionKey.Algo, len(s.SessionKey.Key))
	}
	locked, err := s.lockedSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	sk, err := unlockSessionKey(locked, s.Password, s.Salt)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	sealed, err := sealTitle(sk, "Weekly sync")
	if err != nil {
		t.Fatal(err)
	}
	if title, err := openTitle(s.SessionKey, sealed); err != nil || title != "Weekly sync" {
		t.Errorf("open: %q, %v; want %q", title, err, "Weekly sync")
	}
	if _, err := openTitle(s.SessionKey, sealed[:len(sealed)-4]+"AAAA"); err == nil {
		t.Error("a tampered title opened")
	}
}

func TestTheLinkPasswordUsesTheWebAlphabet(t *testing.T) {
	web := regexp.MustCompile(`^[A-Za-z0-9]{12}$`)
	seen := map[string]bool{}
	for range 50 {
		s, err := newSecret()
		if err != nil {
			t.Fatal(err)
		}
		if !web.MatchString(s.Password) {
			t.Fatalf("password %q is not 12 characters of [A-Za-z0-9]", s.Password)
		}
		seen[s.Password] = true
	}
	if len(seen) < 50 {
		t.Errorf("50 secrets gave %d distinct passwords", len(seen))
	}
}

func TestAPasswordCopyRoundTrips(t *testing.T) {
	kr := userKeyRing(t)
	armored, err := sealPassword(kr, "Synth3tic4Pw")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openPassword(kr, armored); err != nil || got != "Synth3tic4Pw" {
		t.Errorf("open: %q, %v", got, err)
	}
}

// The context is what tells the password copy apart from anything else the
// user's key has signed, so a copy signed without it must not be read as one.
func TestAPasswordCopyNeedsItsContext(t *testing.T) {
	kr := userKeyRing(t)
	msg, err := pgphelper.EncryptText(kr, kr, "Synth3tic4Pw")
	if err != nil {
		t.Fatal(err)
	}
	armored, err := msg.Armor()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openPassword(kr, armored); err == nil {
		t.Error("a copy signed without the context opened")
	}

	unsigned, err := pgphelper.EncryptText(kr, nil, "Synth3tic4Pw")
	if err != nil {
		t.Fatal(err)
	}
	armored, err = unsigned.Armor()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openPassword(kr, armored); err == nil {
		t.Error("an unsigned copy opened")
	}
}

func userKeyRing(t *testing.T) *pgp.KeyRing {
	t.Helper()
	key, err := pgphelper.GenerateKey("Test", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	kr, err := pgp.NewKeyRing(key)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}
