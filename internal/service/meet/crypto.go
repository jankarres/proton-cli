package meet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	srp "github.com/ProtonMail/go-srp"
	"github.com/ProtonMail/gopenpgp/v3/constants"
	pgp "github.com/ProtonMail/gopenpgp/v3/crypto"
	pgphelper "github.com/roman-16/proton-cli/internal/crypto/pgp"
)

// The web client's names for what a meeting is made of.
const (
	// passwordLength is BASE_PASSWORD_LENGTH in @proton/shared/lib/meet/parseMeetingLink.
	passwordLength = 12
	// passwordAlphabet is what getRandomString draws from by default.
	passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	// saltLength is what generateKeySalt in @protontech/crypto/srp makes.
	saltLength = 16
	// sessionKeyAlgorithm is MEET_SESSION_KEY_ALGORITHM.
	sessionKeyAlgorithm = constants.AES256

	// The labels that keep one use of a key from being read as another.
	titleKeyInfo    = "aeskey.link.meet.proton" // deriveEncryptionKeyFromSessionKey
	titleData       = "metadata.meet.proton"    // encryptMetadataWithKey
	passwordContext = "pw.link.meet.proton"     // encryptMeetingPassword
)

// titleIVLength is IV_LENGTH_BYTES in @protontech/crypto/subtle/aesGcm, the
// nonce encryptData writes in front of what it encrypts.
const titleIVLength = 12

// Secret is what locks one meeting: the password its link carries, the salt
// that password is hashed with, and the session key both of them guard.
type Secret struct {
	Password   string
	Salt       []byte
	SessionKey *pgp.SessionKey
}

// newSecret makes the secret for a new meeting, as prepareMeetingCryptoData
// does: a password from getRandomString(BASE_PASSWORD_LENGTH), a salt from
// generateKeySalt, and a fresh AES-256 session key.
func newSecret() (Secret, error) {
	password := make([]byte, passwordLength)
	for i := range password {
		// rand.Text draws from another alphabet, and the web app's links all
		// look like this one, so each character is drawn by hand without bias.
		n, err := randIndex(len(passwordAlphabet))
		if err != nil {
			return Secret{}, fmt.Errorf("draw the meeting's password: %w", err)
		}
		password[i] = passwordAlphabet[n]
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return Secret{}, fmt.Errorf("draw the meeting's salt: %w", err)
	}
	sk, err := pgp.GenerateSessionKeyAlgo(sessionKeyAlgorithm)
	if err != nil {
		return Secret{}, fmt.Errorf("make the meeting's session key: %w", err)
	}
	return Secret{Password: string(password), Salt: salt, SessionKey: sk}, nil
}

// randIndex is a uniform draw from [0, n) for n up to 256, rejecting the bytes
// that would favour the start of the range.
func randIndex(n int) (int, error) {
	limit := 256 - 256%n
	var b [1]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < limit {
			return int(b[0]) % n, nil
		}
	}
}

// lockedSessionKey is the session key as a password-encrypted key packet under
// the link's password, binary, which is what encryptSessionKey sends.
func (s Secret) lockedSessionKey() ([]byte, error) {
	kp, err := keyPassword(s.Password, s.Salt)
	if err != nil {
		return nil, err
	}
	locked, err := pgphelper.EncryptSessionKeyWithPassword(kp, s.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("lock the meeting's session key: %w", err)
	}
	return locked, nil
}

// keyPassword is computeKeyPassword from @protontech/crypto/srp: the bcrypt
// hash of the password with its 29-character prefix of version, cost and salt
// cut off, which leaves the last 31 bytes. Drive's link passphrase is made the
// same way (unlockLinkShare in internal/service/drive).
func keyPassword(password string, salt []byte) ([]byte, error) {
	if password == "" || len(salt) != saltLength {
		return nil, errors.New("hash the meeting's password: a password and a 16-byte salt are required")
	}
	hashed, err := srp.MailboxPassword([]byte(password), salt)
	if err != nil {
		return nil, fmt.Errorf("hash the meeting's password: %w", err)
	}
	return hashed[len(hashed)-31:], nil
}

// unlockSessionKey is decryptSessionKey: the session key out of its key packet,
// opened with the link's password and the meeting's salt.
func unlockSessionKey(locked []byte, password string, salt []byte) (*pgp.SessionKey, error) {
	kp, err := keyPassword(password, salt)
	if err != nil {
		return nil, err
	}
	sk, err := pgphelper.DecryptSessionKeyWithPassword(kp, locked)
	if err != nil {
		return nil, fmt.Errorf("unlock the meeting's session key: %w", err)
	}
	return sk, nil
}

// titleCipher is deriveEncryptionKeyFromSessionKey: an AES-256-GCM key drawn
// from the session key by HKDF-SHA256 with 32 zero bytes of salt. The web app
// refuses any session key but an AES-256 one, and so does this.
func titleCipher(sk *pgp.SessionKey) (cipher.AEAD, error) {
	if sk == nil || sk.Algo != sessionKeyAlgorithm {
		return nil, errors.New("the meeting's session key is not an AES-256 key")
	}
	key, err := hkdf.Key(sha256.New, sk.Key, make([]byte, 32), titleKeyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("derive the meeting's title key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealTitle is encryptMeetingName: the title under the session key's title
// key, with the nonce in front and the whole of it in base64.
func sealTitle(sk *pgp.SessionKey, title string) (string, error) {
	aead, err := titleCipher(sk)
	if err != nil {
		return "", err
	}
	iv := make([]byte, titleIVLength)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("seal the meeting's title: %w", err)
	}
	sealed := aead.Seal(iv, iv, []byte(title), []byte(titleData))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// openTitle is decryptMetadataWithKey under the title key, the other half of
// decryptMeetingName.
func openTitle(sk *pgp.SessionKey, sealed string) (string, error) {
	aead, err := titleCipher(sk)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("read the meeting's title: %w", err)
	}
	if len(raw) < titleIVLength {
		return "", errors.New("read the meeting's title: it is too short to hold one")
	}
	title, err := aead.Open(nil, raw[:titleIVLength], raw[titleIVLength:], []byte(titleData))
	if err != nil {
		return "", fmt.Errorf("open the meeting's title: %w", err)
	}
	return string(title), nil
}

// sealPassword is encryptMeetingPassword: the link's password encrypted to the
// user's key and signed by it in the critical context pw.link.meet.proton,
// armoured. It is the account's own copy, which is how a meeting's link is
// rebuilt later.
func sealPassword(userKR *pgp.KeyRing, password string) (string, error) {
	enc, err := pgphelper.PGP.Encryption().Recipients(userKR).SigningKeys(userKR).
		SigningContext(pgp.NewSigningContext(passwordContext, true)).Utf8().New()
	if err != nil {
		return "", fmt.Errorf("seal the meeting's password: %w", err)
	}
	msg, err := enc.Encrypt([]byte(password))
	if err != nil {
		return "", fmt.Errorf("seal the meeting's password: %w", err)
	}
	return msg.Armor()
}

// openPassword is decryptMeetingPassword: the account's copy of the link's
// password, read only if the user's key signed it in the password's context,
// as the web app's expectSigned and required context demand.
func openPassword(userKR *pgp.KeyRing, armored string) (string, error) {
	dec, err := pgphelper.PGP.Decryption().DecryptionKeys(userKR).VerificationKeys(userKR).
		VerificationContext(pgp.NewVerificationContext(passwordContext, true, 0)).Utf8().New()
	if err != nil {
		return "", fmt.Errorf("open the meeting's password: %w", err)
	}
	res, err := dec.Decrypt([]byte(armored), pgp.Armor)
	if err != nil {
		return "", fmt.Errorf("open the meeting's password: %w", err)
	}
	if err := pgphelper.SignatureError(res, userKR); err != nil {
		return "", fmt.Errorf("verify the meeting's password: %w", err)
	}
	return res.String(), nil
}
