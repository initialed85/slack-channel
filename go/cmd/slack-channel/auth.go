package main

// Auth as "you" using the Slack web client's own credentials:
//
//   - the `d` cookie (xoxd-...), HttpOnly, read straight out of the browser's / Slack desktop app's
//     on-disk cookie DB and decrypted with the "<X> Safe Storage" key from the macOS Keychain
//   - an `xoxc-` token, scraped from the HTML Slack serves to a logged-in browser (the `d` cookie is
//     what makes it logged-in), so it never needs copying around by hand
//
// The `d` cookie lasts about a year, so in practice this just works until you log out of Slack.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type cookieSource struct {
	DBPath          string // e.g. ~/Library/Application Support/Google/Chrome/Default/Cookies
	KeychainService string // e.g. "Chrome Safe Storage" / "Slack Safe Storage"
}

func defaultCookieSource() cookieSource {
	home, _ := os.UserHomeDir()
	s := cookieSource{
		DBPath:          filepath.Join(home, "Library/Application Support/Google/Chrome/Default/Cookies"),
		KeychainService: "Chrome Safe Storage",
	}
	if v := os.Getenv("SLACK_COOKIE_DB"); v != "" {
		s.DBPath = v
	}
	if v := os.Getenv("SLACK_COOKIE_KEYCHAIN_SERVICE"); v != "" {
		s.KeychainService = v
	}
	return s
}

// readDCookie returns the raw (still URL-encoded, as the browser sends it) value of the `d` cookie.
func readDCookie(src cookieSource) (string, error) {
	if v := os.Getenv("SLACK_D_COOKIE"); v != "" {
		return v, nil
	}

	// the live DB is locked by the browser; work on a copy
	tmp, err := os.CreateTemp("", "slack-channel-cookies-*.db")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	in, err := os.Open(src.DBPath)
	if err != nil {
		return "", fmt.Errorf("open cookie db: %w", err)
	}
	_, err = io.Copy(tmp, in)
	in.Close()
	tmp.Close()
	if err != nil {
		return "", err
	}

	q := func(sql string) (string, error) {
		out, err := exec.Command("sqlite3", "-readonly", tmp.Name(), sql).Output()
		return strings.TrimSpace(string(out)), err
	}
	encHex, err := q("select hex(encrypted_value) from cookies where host_key = '.slack.com' and name = 'd' order by last_access_utc desc limit 1")
	if err != nil {
		return "", fmt.Errorf("query cookie db: %w", err)
	}
	if encHex == "" {
		return "", fmt.Errorf("no .slack.com `d` cookie in %s (logged in to Slack there?)", src.DBPath)
	}
	verStr, _ := q("select value from meta where key = 'version'")
	ver, _ := strconv.Atoi(verStr)

	enc, err := hex.DecodeString(encHex)
	if err != nil {
		return "", err
	}

	pw, err := exec.Command("security", "find-generic-password", "-w", "-s", src.KeychainService).Output()
	if err != nil {
		return "", fmt.Errorf("read %q from keychain: %w", src.KeychainService, err)
	}

	plain, err := decryptChromiumCookie(enc, strings.TrimSpace(string(pw)), ver)
	if err != nil {
		return "", err
	}
	return plain, nil
}

// decryptChromiumCookie implements Chromium's macOS cookie encryption (v10): AES-128-CBC, key from PBKDF2-SHA1
// over the Keychain password, salt "saltysalt", 1003 rounds, IV of 16 spaces. From DB version 24 the plaintext
// is prefixed with SHA256(host_key), which we drop.
func decryptChromiumCookie(enc []byte, password string, dbVersion int) (string, error) {
	if len(enc) < 3 || string(enc[:3]) != "v10" {
		return "", fmt.Errorf("unexpected cookie encryption prefix %q", enc[:min(3, len(enc))])
	}
	enc = enc[3:]
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	if len(enc) == 0 || len(enc)%aes.BlockSize != 0 {
		return "", fmt.Errorf("bad ciphertext length %d", len(enc))
	}
	iv := []byte(strings.Repeat(" ", aes.BlockSize))
	out := make([]byte, len(enc))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, enc)

	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) {
		return "", fmt.Errorf("bad padding (wrong keychain key?)")
	}
	out = out[:len(out)-pad]
	if dbVersion >= 24 {
		if len(out) < 32 {
			return "", fmt.Errorf("plaintext too short for v24 host hash")
		}
		out = out[32:]
	}
	return string(out), nil
}

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

var apiTokenRe = regexp.MustCompile(`"api_token":"(xoxc-[^"]+)"`)

// fetchXoxc loads a page of the Slack web client as the logged-in user and pulls the xoxc token out of the boot data.
func fetchXoxc(workspaceDomain, dCookie string) (string, error) {
	u := fmt.Sprintf("https://%s.slack.com/ssb/redirect", workspaceDomain)
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Cookie", "d="+dCookie)
	// without browser-ish headers Slack answers 403
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-AU,en;q=0.9")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	m := apiTokenRe.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("no api_token in %s (HTTP %d, landed on %s) — is the d cookie stale?", u, resp.StatusCode, resp.Request.URL)
	}
	return string(m[1]), nil
}

// cookieTransport adds the d cookie to every Slack API call; xoxc tokens are useless without it.
type cookieTransport struct {
	d    string
	base http.RoundTripper
}

func (t *cookieTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Add("Cookie", "d="+t.d)
	return t.base.RoundTrip(r)
}
