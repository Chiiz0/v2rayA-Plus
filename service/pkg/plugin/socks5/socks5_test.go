package socks5

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/v2rayA/v2rayA/pkg/plugin/socks"
)

func TestHandshakeAuthenticationFailureDoesNotExposeCredentials(t *testing.T) {
	const expectedUser = "configured-user"
	const expectedPassword = "configured-password"
	for _, tc := range []struct {
		name     string
		user     string
		password string
	}{
		{"wrong user", "supplied-user", expectedPassword},
		{"wrong password", expectedUser, "supplied-password%secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := []byte{Version, 1, socks.AuthPassword, 1, byte(len(tc.user))}
			request = append(request, tc.user...)
			request = append(request, byte(len(tc.password)))
			request = append(request, tc.password...)
			var response bytes.Buffer
			rw := struct {
				io.Reader
				io.Writer
			}{bytes.NewReader(request), &response}
			server := Socks5{user: expectedUser, password: expectedPassword}
			_, err := server.handshake(rw)
			if err == nil {
				t.Fatal("authentication with invalid credentials succeeded")
			}
			for _, secret := range []string{tc.user, tc.password, expectedUser, expectedPassword} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("authentication error exposes credentials")
				}
			}
			if err.Error() != "auth failed: invalid username or password" {
				t.Fatalf("unexpected authentication error: %v", err)
			}
			if want := []byte{Version, socks.AuthPassword, 1, 1}; !bytes.Equal(response.Bytes(), want) {
				t.Fatalf("authentication response = %v, want %v", response.Bytes(), want)
			}
		})
	}
}
